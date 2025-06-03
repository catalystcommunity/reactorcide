"""Container execution utilities for runnerlib."""

import os
import subprocess
import shutil
from pathlib import Path
from typing import List, Optional, Dict
from runnerlib.logging import log_stdout, log_stderr
from runnerlib.config import RunnerConfig, get_environment_vars
from runnerlib.source_prep import prepare_job_directory


def run_container(
    config: RunnerConfig,
    additional_args: Optional[List[str]] = None
) -> int:
    """Run the job container using nerdctl with full configuration support.
    
    Args:
        config: Runner configuration
        additional_args: Additional arguments to pass to the job command
        
    Returns:
        Exit code of the container process
        
    Raises:
        ValueError: If configuration is invalid
        FileNotFoundError: If nerdctl is not available
    """
    # Basic validation is handled by CLI layer
    
    # Check if nerdctl is available
    if not shutil.which("nerdctl"):
        raise FileNotFoundError("nerdctl is not available in PATH")
    
    # Prepare job directory structure
    job_path = prepare_job_directory(config)
    
    # Get all environment variables to pass to container
    env_vars = get_environment_vars(config)
    
    # Build nerdctl command
    cmd = ["nerdctl", "run", "--rm"]
    
    # Add environment variables
    for key, value in env_vars.items():
        cmd.extend(["-e", f"{key}={value}"])
    
    # Mount job directory
    cmd.extend(["-v", f"{job_path}:/job"])
    
    # Set working directory to job directory inside container
    cmd.extend(["-w", config.job_dir])
    
    # Add container image
    cmd.append(config.runner_image)
    
    # Add job command and additional arguments
    cmd.append(config.job_command)
    if additional_args:
        cmd.extend(additional_args)
    
    log_stdout(f"Running container: {' '.join(_mask_sensitive_args(cmd))}")
    
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
        
        # Stream output line by line
        while True:
            stdout_line = process.stdout.readline() if process.stdout else None
            stderr_line = process.stderr.readline() if process.stderr else None
            
            if stdout_line:
                log_stdout(stdout_line.rstrip())
            if stderr_line:
                log_stderr(stderr_line.rstrip())
                
            # Check if process has finished
            if process.poll() is not None:
                break
        
        # Get any remaining output
        remaining_stdout, remaining_stderr = process.communicate()
        if remaining_stdout:
            for line in remaining_stdout.splitlines():
                log_stdout(line)
        if remaining_stderr:
            for line in remaining_stderr.splitlines():
                log_stderr(line)
        
        return process.returncode
        
    except subprocess.SubprocessError as e:
        log_stderr(f"Container execution failed: {e}")
        return 1
    except KeyboardInterrupt:
        log_stderr("Container execution interrupted")
        if 'process' in locals():
            process.terminate()
        return 130


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


def _mask_sensitive_args(cmd: List[str]) -> List[str]:
    """Mask potentially sensitive environment variable values in command for logging.
    
    Args:
        cmd: Command arguments list
        
    Returns:
        Command list with sensitive values masked
    """
    masked_cmd = []
    i = 0
    while i < len(cmd):
        arg = cmd[i]
        if arg == "-e" and i + 1 < len(cmd):
            # Mask environment variable values that might contain secrets
            env_arg = cmd[i + 1]
            if '=' in env_arg:
                key, _ = env_arg.split('=', 1)
                # Mask values for potentially sensitive keys
                if any(sensitive in key.lower() for sensitive in ['token', 'secret', 'key', 'password', 'auth']):
                    masked_cmd.extend([arg, f"{key}=***"])
                else:
                    masked_cmd.extend([arg, env_arg])
            else:
                masked_cmd.extend([arg, env_arg])
            i += 2
        else:
            masked_cmd.append(arg)
            i += 1
    
    return masked_cmd