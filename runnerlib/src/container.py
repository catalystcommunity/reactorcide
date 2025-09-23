"""Container execution utilities for runnerlib."""

import subprocess
import shutil
from pathlib import Path
from typing import List, Optional, Dict
from src.logging import log_stdout, log_stderr, logger
from src.config import RunnerConfig, get_environment_vars, get_secrets_to_mask
from src.source_prep import prepare_job_directory
from src.secrets import SecretMasker
from src.secrets_server import SecretRegistrationServer
from src.plugins import plugin_manager, PluginContext, PluginPhase


def build_docker_command(
    config: RunnerConfig,
    job_path: Path,
    env_vars: Dict[str, str],
    additional_args: Optional[List[str]] = None,
    resource_limits: Optional[Dict[str, str]] = None,
    secrets_file_path: Optional[Path] = None
) -> List[str]:
    """Build the docker command for container execution.

    Args:
        config: Runner configuration
        job_path: Path to the job directory to mount
        env_vars: Environment variables to pass to container
        additional_args: Additional arguments to pass to the job command
        resource_limits: Optional resource limits from plugins

    Returns:
        List of command arguments for docker run
    """
    import shlex

    cmd = ["docker", "run", "--rm"]

    # Add resource limits if provided by plugins
    if resource_limits:
        if "memory" in resource_limits:
            cmd.extend(["--memory", resource_limits["memory"]])
        if "cpus" in resource_limits:
            cmd.extend(["--cpus", resource_limits["cpus"]])

    # Add environment variables
    for key, value in env_vars.items():
        cmd.extend(["-e", f"{key}={value}"])

    # Mount job directory
    cmd.extend(["-v", f"{job_path}:/job"])

    # Mount and load secrets file if provided
    if secrets_file_path and secrets_file_path.exists():
        # Use --env-file to load the secrets from host path
        cmd.extend(["--env-file", str(secrets_file_path.absolute())])
        # Also mount as read-only file at /run/secrets/env for reference
        cmd.extend(["-v", f"{secrets_file_path.absolute()}:/run/secrets/env:ro"])

    # Mount /tmp if we have a socket for secret registration
    if 'REACTORCIDE_SECRETS_SOCKET' in env_vars:
        socket_path = env_vars['REACTORCIDE_SECRETS_SOCKET']
        if Path(socket_path).exists():
            cmd.extend(["-v", "/tmp:/tmp"])

    # Set working directory to job directory inside container
    cmd.extend(["-w", config.job_dir])

    # Add container image
    cmd.append(config.runner_image)

    # Add job command and additional arguments
    job_cmd_parts = shlex.split(config.job_command)
    cmd.extend(job_cmd_parts)
    if additional_args:
        cmd.extend(additional_args)

    return cmd


def run_container(
    config: RunnerConfig,
    additional_args: Optional[List[str]] = None
) -> int:
    """Run the job container using docker with full configuration support.

    Args:
        config: Runner configuration
        additional_args: Additional arguments to pass to the job command

    Returns:
        Exit code of the container process

    Raises:
        ValueError: If configuration is invalid
        FileNotFoundError: If docker is not available
    """
    # Create plugin context for the execution
    plugin_context = PluginContext(
        config=config,
        phase=PluginPhase.PRE_SOURCE_PREP,
        metadata={}
    )

    try:
        # Execute pre-source-prep plugins
        plugin_manager.execute_phase(PluginPhase.PRE_SOURCE_PREP, plugin_context)

        # Basic validation is handled by CLI layer

        # Check if docker is available
        if not shutil.which("docker"):
            logger.error("Docker is not available in PATH")
            raise FileNotFoundError("docker is not available in PATH")

        # Prepare job directory structure
        job_path = prepare_job_directory(config)
        plugin_context.job_path = job_path

        # Execute post-source-prep plugins
        plugin_context.phase = PluginPhase.POST_SOURCE_PREP
        plugin_manager.execute_phase(PluginPhase.POST_SOURCE_PREP, plugin_context)

        # Get all environment variables to pass to container
        env_vars = get_environment_vars(config)
        plugin_context.env_vars = env_vars

        # Create masker with proper secrets list
        secrets_list = get_secrets_to_mask(config, env_vars)
        output_masker = SecretMasker()
        output_masker.register_secrets(secrets_list)

        # Start secret registration server for dynamic secret registration
        secret_server = SecretRegistrationServer(output_masker)
        secret_server.start()

        # Add socket path to environment so jobs know where to connect
        socket_path = secret_server.get_socket_path()
        env_vars['REACTORCIDE_SECRETS_SOCKET'] = socket_path

        # Execute pre-container plugins
        plugin_context.phase = PluginPhase.PRE_CONTAINER
        plugin_manager.execute_phase(PluginPhase.PRE_CONTAINER, plugin_context)

        # Get resource limits from plugin metadata if available
        resource_limits = plugin_context.metadata.get("resource_limits")

        # Prepare secrets file path if provided
        secrets_file_path = None
        if config.secrets_file:
            secrets_file_path = Path(config.secrets_file).absolute()
            if not secrets_file_path.exists():
                logger.warning(f"Secrets file does not exist: {secrets_file_path}")
                secrets_file_path = None

        # Build the docker command
        cmd = build_docker_command(config, job_path, env_vars, additional_args, resource_limits, secrets_file_path)

        # Log the command with secrets masked
        masked_cmd = output_masker.mask_command_args(cmd)
        logger.info(
            "Starting container execution",
            fields={
                "image": config.runner_image,
                "work_dir": config.job_dir,
                "command": ' '.join(masked_cmd)
            }
        )
        log_stdout(f"Running container: {' '.join(masked_cmd)}")

        try:
            # Run the container and stream output
            process = subprocess.Popen(
                cmd,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
                text=True,
                bufsize=1,
                universal_newlines=True
            )

            # Stream output line by line with secret masking
            while True:
                stdout_line = process.stdout.readline() if process.stdout else None
                stderr_line = process.stderr.readline() if process.stderr else None

                if stdout_line:
                    # Mask secrets in output before logging
                    masked_line = output_masker.mask_string(stdout_line.rstrip())
                    log_stdout(masked_line)
                if stderr_line:
                    # Mask secrets in error output before logging
                    masked_line = output_masker.mask_string(stderr_line.rstrip())
                    log_stderr(masked_line)

                # Check if process has finished and no more output
                if process.poll() is not None and not stdout_line and not stderr_line:
                    break

            # Get any remaining output
            remaining_stdout, remaining_stderr = process.communicate()
            if remaining_stdout:
                for line in remaining_stdout.splitlines():
                    masked_line = output_masker.mask_string(line)
                    log_stdout(masked_line)
            if remaining_stderr:
                for line in remaining_stderr.splitlines():
                    masked_line = output_masker.mask_string(line)
                    log_stderr(masked_line)

            # Stop the secret registration server
            secret_server.stop()

            # Execute post-container plugins
            plugin_context.phase = PluginPhase.POST_CONTAINER
            plugin_context.exit_code = process.returncode
            plugin_manager.execute_phase(PluginPhase.POST_CONTAINER, plugin_context)

            logger.info(
                "Container execution completed",
                fields={"exit_code": process.returncode, "image": config.runner_image}
            )
            return process.returncode

        except subprocess.SubprocessError as e:
            logger.error("Container execution failed", error=e)
            log_stderr(f"Container execution failed: {e}")
            # Execute error plugins
            plugin_context.phase = PluginPhase.ON_ERROR
            plugin_context.error = e
            try:
                plugin_manager.execute_phase(PluginPhase.ON_ERROR, plugin_context)
            except Exception:
                pass  # Don't let plugin errors mask the original error
            secret_server.stop()
            return 1
        except KeyboardInterrupt:
            logger.warning("Container execution interrupted by user")
            log_stderr("Container execution interrupted")
            if 'process' in locals():
                process.terminate()
            secret_server.stop()
            return 130
        except Exception as e:
            # Execute error plugins for any unexpected errors
            if 'plugin_context' in locals():
                plugin_context.phase = PluginPhase.ON_ERROR
                plugin_context.error = e
                try:
                    plugin_manager.execute_phase(PluginPhase.ON_ERROR, plugin_context)
                except Exception:
                    pass
            raise
        finally:
            # Execute cleanup plugins
            if 'plugin_context' in locals():
                plugin_context.phase = PluginPhase.CLEANUP
                try:
                    plugin_manager.execute_phase(PluginPhase.CLEANUP, plugin_context)
                except Exception:
                    pass  # Don't let plugin errors interfere with cleanup
            # Ensure server is always stopped
            if 'secret_server' in locals():
                secret_server.stop()
    except Exception as e:
        # Handle any exceptions from the outer try block
        if 'plugin_context' in locals():
            plugin_context.phase = PluginPhase.ON_ERROR
            plugin_context.error = e
            try:
                plugin_manager.execute_phase(PluginPhase.ON_ERROR, plugin_context)
            except Exception:
                pass
        raise


def validate_container_config(config: RunnerConfig) -> None:
    """Validate container configuration before execution.
    
    Args:
        config: Runner configuration to validate
        
    Raises:
        ValueError: If configuration is invalid
        FileNotFoundError: If required files don't exist
    """
    # Validate required fields
    if not config.job_command:
        raise ValueError("job_command is required")
    
    if not config.runner_image:
        raise ValueError("runner_image is required")
    
    # Validate job environment if specified
    if config.job_env:
        try:
            from runnerlib.config import config_manager
            config_manager.parse_job_environment(config.job_env)
        except (FileNotFoundError, ValueError) as e:
            raise ValueError(f"Invalid job_env configuration: {e}")
    
    # Validate directory paths
    if not config.code_dir:
        raise ValueError("code_dir is required")
    
    if not config.job_dir:
        raise ValueError("job_dir is required")
    
    # Ensure paths are absolute container paths
    if not config.code_dir.startswith('/'):
        raise ValueError(f"code_dir must be an absolute path: {config.code_dir}")
    
    if not config.job_dir.startswith('/'):
        raise ValueError(f"job_dir must be an absolute path: {config.job_dir}")


