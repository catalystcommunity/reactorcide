"""CLI interface for runnerlib."""

import os
import subprocess
import shlex
import sys
import getpass
import typer
from typing import List, Optional, Dict

from src.logging import log_stdout, log_stderr
from src.git_ops import get_files_changed
from src.container import run_container
from src.source_prep import checkout_git_repo, copy_directory, cleanup_job_directory
from src.config import get_config, get_secrets_to_mask, get_environment_vars
from src.secrets_resolver import has_secret_refs, resolve_secrets_in_dict
from src.validation import validate_config, format_validation_result
from src.secrets import SecretMasker
from src.plugins import plugin_manager, PluginContext, PluginPhase, initialize_plugins

app = typer.Typer()


def resolve_job_secrets(env_vars: Dict[str, str]) -> Dict[str, str]:
    """Resolve any ${secret:path:key} references in environment variables.

    Args:
        env_vars: Dictionary of environment variables that may contain secret refs

    Returns:
        Dictionary with secret references resolved to actual values
    """
    from src import secrets_local as secrets
    from src.secrets import register_secret

    # Quick check: any secret refs present?
    needs_resolution = any(has_secret_refs(str(v)) for v in env_vars.values())
    if not needs_resolution:
        return env_vars

    # Get password
    password = os.environ.get("REACTORCIDE_SECRETS_PASSWORD")
    if not password:
        password = getpass.getpass("Secrets password: ")

    # Create getter function that uses local provider
    def get_secret(path: str, key: str) -> Optional[str]:
        return secrets.secret_get(path, key, password)

    # Resolve all references
    resolved = resolve_secrets_in_dict(env_vars, get_secret)

    # Register resolved values with secret masker for log redaction
    for orig_key in env_vars:
        orig_val = env_vars[orig_key]
        new_val = resolved.get(orig_key)
        if orig_val != new_val and new_val:  # A substitution happened
            register_secret(new_val)

    return resolved


def _run_local(config, command_args):
    """
    Execute a job command locally on the host (no container).

    This is the default execution mode for runnerlib, enabling:
    - Emergency job execution when infrastructure is down
    - Local development and testing
    - Deployment scenarios

    Args:
        config: RunnerConfig with job details
        command_args: Additional command line arguments

    Returns:
        Exit code from the executed command
    """
    from src.secrets import SecretMasker

    log_stdout("Executing job locally (no container)")
    log_stdout(f"Command: {config.job_command}")

    # Get environment variables for the job
    env = os.environ.copy()
    job_env_vars = get_environment_vars(config)

    # Resolve secret references in environment variables
    job_env_vars = resolve_job_secrets(job_env_vars)

    env.update(job_env_vars)

    # Initialize secret masker
    masker = SecretMasker()
    secrets = get_secrets_to_mask(config, job_env_vars)
    for secret in secrets:
        masker.register_secret(secret)

    # Execute the command using shell
    try:
        # Run command with shell to support complex commands (pipes, redirects, etc.)
        # Use subprocess.run for simpler cross-platform streaming
        process = subprocess.Popen(
            config.job_command,
            shell=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.STDOUT,  # Merge stderr into stdout for simpler handling
            env=env,
            text=True,
            bufsize=1  # Line buffered
        )

        # Stream output in real-time with secret masking
        for line in iter(process.stdout.readline, ''):
            if line:
                # Mask secrets and output
                masked_line = masker.mask_string(line.rstrip())
                log_stdout(masked_line)

        # Wait for process to complete
        exit_code = process.wait()

        if exit_code == 0:
            log_stdout(f"✓ Job completed successfully (exit code: {exit_code})")
        else:
            log_stderr(f"✗ Job failed with exit code: {exit_code}")

        return exit_code

    except Exception as e:
        log_stderr(f"Error executing job: {e}")
        return 1


@app.command()
def run(
    ctx: typer.Context,
    args: Optional[List[str]] = typer.Argument(None),
    # Configuration overrides
    code_dir: Optional[str] = typer.Option(None, "--code-dir", help="Code directory path (default: /job/src)"),
    job_dir: Optional[str] = typer.Option(None, "--job-dir", help="Job directory path (default: same as code-dir)"),
    job_command: Optional[str] = typer.Option(None, "--job-command", help="Command to run in the container"),
    runner_image: Optional[str] = typer.Option(None, "--runner-image", help="Container image to use (default: quay.io/catalystcommunity/reactorcide_runner)"),
    job_env: Optional[str] = typer.Option(None, "--job-env", help="Environment variables as key=value pairs or file path (must start with ./job/)"),
    secrets_list: Optional[str] = typer.Option(None, "--secrets-list", help="Comma-separated list of secret values to mask in logs, or path to secrets file"),
    secrets_file: Optional[str] = typer.Option(None, "--secrets-file", help="Path to secrets file to mount into container at /run/secrets/env"),
    work_dir: Optional[str] = typer.Option(None, "--work-dir", help="Working directory for job execution (default: current directory)"),
    plugin_dir: Optional[str] = typer.Option(None, "--plugin-dir", help="Directory containing custom plugins"),
    # Source preparation options
    source_type: Optional[str] = typer.Option(None, "--source-type", help="Source type: git, copy, tarball, hg, svn, none (default: none)"),
    source_url: Optional[str] = typer.Option(None, "--source-url", help="Source URL or path (required for git, copy, tarball, hg, svn)"),
    source_ref: Optional[str] = typer.Option(None, "--source-ref", help="Source ref: branch, tag, commit, or version"),
    # CI source preparation options
    ci_source_type: Optional[str] = typer.Option(None, "--ci-source-type", help="CI source type: git, copy, tarball, hg, svn, none (default: none)"),
    ci_source_url: Optional[str] = typer.Option(None, "--ci-source-url", help="CI source URL or path (required for git, copy, tarball, hg, svn)"),
    ci_source_ref: Optional[str] = typer.Option(None, "--ci-source-ref", help="CI source ref: branch, tag, commit, or version"),
    # Execution mode flags
    dry_run: bool = typer.Option(False, "--dry-run", help="Validate configuration without executing"),
    container: bool = typer.Option(False, "--container", help="Run job in a container (for integration testing). Default is local execution.")
):
    """Run a job command, passing through all arguments."""
    # Get all arguments passed after 'run'
    command_args = ctx.args if ctx.args else (args or [])

    # Build configuration overrides from CLI arguments
    cli_overrides = {}
    if code_dir is not None:
        cli_overrides['code_dir'] = code_dir
    if job_dir is not None:
        cli_overrides['job_dir'] = job_dir
    if job_command is not None:
        cli_overrides['job_command'] = job_command
    if runner_image is not None:
        cli_overrides['runner_image'] = runner_image
    if job_env is not None:
        cli_overrides['job_env'] = job_env
    if secrets_list is not None:
        cli_overrides['secrets_list'] = secrets_list
    if secrets_file is not None:
        cli_overrides['secrets_file'] = secrets_file
    if source_type is not None:
        cli_overrides['source_type'] = source_type
    if source_url is not None:
        cli_overrides['source_url'] = source_url
    if source_ref is not None:
        cli_overrides['source_ref'] = source_ref
    if ci_source_type is not None:
        cli_overrides['ci_source_type'] = ci_source_type
    if ci_source_url is not None:
        cli_overrides['ci_source_url'] = ci_source_url
    if ci_source_ref is not None:
        cli_overrides['ci_source_ref'] = ci_source_ref

    try:
        # Initialize plugins
        initialize_plugins(plugin_dir)
        if plugin_manager.plugins:
            log_stdout(f"Loaded {len(plugin_manager.plugins)} plugins: {', '.join(plugin_manager.list_plugins())}")

        # Get configuration with CLI overrides
        config = get_config(**cli_overrides)

        # If work_dir is provided, change to it before running
        if work_dir:
            os.chdir(work_dir)
            log_stdout(f"Changed working directory to: {work_dir}")

        # Create plugin context for validation
        plugin_context = PluginContext(
            config=config,
            phase=PluginPhase.PRE_VALIDATION,
            metadata={}
        )

        # Execute pre-validation plugins
        plugin_manager.execute_phase(PluginPhase.PRE_VALIDATION, plugin_context)

        # Determine execution mode before validation
        # Container mode is used if --container flag is set OR --runner-image is specified
        use_container = container or runner_image is not None

        # Validate configuration (only require docker if using container mode)
        validation_result = validate_config(config, check_files=True, require_container_runtime=use_container)

        if not validation_result.is_valid:
            log_stderr("Configuration validation failed:")
            log_stderr(format_validation_result(validation_result))
            raise typer.Exit(1)

        # Execute post-validation plugins
        plugin_context.phase = PluginPhase.POST_VALIDATION
        plugin_manager.execute_phase(PluginPhase.POST_VALIDATION, plugin_context)

        # Show warnings if any
        if validation_result.has_warnings:
            log_stderr(format_validation_result(validation_result))

        if dry_run:
            # Dry-run mode: show configuration and what would be executed
            _perform_dry_run(config, command_args)
            raise typer.Exit(0)

        # Prepare source code (if configured)
        plugin_context.phase = PluginPhase.PRE_SOURCE_PREP
        plugin_manager.execute_phase(PluginPhase.PRE_SOURCE_PREP, plugin_context)

        from src.source_prep import prepare_source, prepare_ci_source

        # Prepare CI source first (trusted code)
        ci_source_path = prepare_ci_source(config)
        if ci_source_path:
            plugin_context.metadata['ci_source_path'] = str(ci_source_path)

        # Prepare regular source (potentially untrusted)
        source_path = prepare_source(config)
        if source_path:
            plugin_context.metadata['source_path'] = str(source_path)

        plugin_context.phase = PluginPhase.POST_SOURCE_PREP
        plugin_manager.execute_phase(PluginPhase.POST_SOURCE_PREP, plugin_context)

        # Execute the job (use_container already determined before validation)
        if use_container:
            # Container execution mode - for integration testing
            log_stdout("Running job in CONTAINER mode")
            exit_code = run_container(config=config, additional_args=command_args)
        else:
            # Local execution mode (default) - for deployment and emergency use
            exit_code = _run_local(config, command_args)

        raise typer.Exit(exit_code)
    except typer.Exit:
        # Re-raise typer.Exit to avoid catching it as a generic exception
        raise
    except (ValueError, FileNotFoundError) as e:
        log_stderr(f"Configuration error: {e}")
        raise typer.Exit(1)
    except Exception as e:
        log_stderr(f"Unexpected error: {e}")
        raise typer.Exit(1)


@app.command()
def checkout(
    git_url: str,
    git_ref: Optional[str] = typer.Option(None, "--ref", "-r", help="Git reference to checkout"),
    # Configuration overrides
    code_dir: Optional[str] = typer.Option(None, "--code-dir", help="Code directory path (default: /job/src)"),
    job_dir: Optional[str] = typer.Option(None, "--job-dir", help="Job directory path (default: same as code-dir)"),
    runner_image: Optional[str] = typer.Option(None, "--runner-image", help="Container image to use (default: quay.io/catalystcommunity/reactorcide_runner)"),
    work_dir: Optional[str] = typer.Option(None, "--work-dir", help="Working directory for job execution (default: current directory)")
):
    """Checkout a git repository to the configured code directory."""
    # Build configuration overrides from CLI arguments
    cli_overrides = {}
    if code_dir is not None:
        cli_overrides['code_dir'] = code_dir
    if job_dir is not None:
        cli_overrides['job_dir'] = job_dir
    if runner_image is not None:
        cli_overrides['runner_image'] = runner_image

    try:
        # Get configuration with CLI overrides (dummy job_command for directory setup)
        config = get_config(job_command="dummy", **cli_overrides)

        # If work_dir is provided, change to it before checkout
        if work_dir:
            os.chdir(work_dir)
            log_stdout(f"Changed working directory to: {work_dir}")

        # Validate configuration for directory operations
        validation_result = validate_config(config, check_files=False)  # Don't check files for checkout
        if not validation_result.is_valid:
            log_stderr("Configuration validation failed:")
            log_stderr(format_validation_result(validation_result))
            raise typer.Exit(1)
        
        # Show warnings if any
        if validation_result.has_warnings:
            log_stderr(format_validation_result(validation_result))
        
        log_stdout(f"Checking out {git_url} to {config.code_dir}")
        if git_ref:
            log_stdout(f"Using git reference: {git_ref}")
        
        checkout_git_repo(git_url, git_ref, config)
        log_stdout("✅ Repository checkout complete")
        
        # Show what was created
        from src.source_prep import get_code_directory_path
        code_path = get_code_directory_path(config)
        try:
            item_count = len(list(code_path.iterdir()))
            log_stdout(f"📂 Created {item_count} items in {code_path}")
        except Exception:
            pass
            
    except (ValueError, FileNotFoundError) as e:
        log_stderr(f"Configuration error: {e}")
        raise typer.Exit(1)
    except Exception as e:
        log_stderr(f"Checkout failed: {e}")
        raise typer.Exit(1)


@app.command()
def run_job(
    job_file: str = typer.Argument(..., help="Path to job definition file (JSON or YAML)"),
    secrets_file: Optional[str] = typer.Option(None, "--secrets-file", help="Path to secrets file to mount into container"),
    work_dir: Optional[str] = typer.Option(None, "--work-dir", help="Working directory for job execution"),
    dry_run: bool = typer.Option(False, "--dry-run", help="Validate configuration without executing")
):
    """Run a job from a JSON/YAML definition file."""
    import json
    import yaml
    from pathlib import Path

    # Read job file
    job_file_path = Path(job_file)
    if not job_file_path.exists():
        log_stderr(f"Job file not found: {job_file}")
        raise typer.Exit(1)

    try:
        with open(job_file_path, 'r') as f:
            if job_file_path.suffix in ['.yaml', '.yml']:
                job_spec = yaml.safe_load(f)
            else:
                job_spec = json.load(f)
    except Exception as e:
        log_stderr(f"Failed to parse job file: {e}")
        raise typer.Exit(1)

    # Extract job configuration
    cli_overrides = {
        'runner_image': job_spec.get('image', 'alpine:latest'),
        'job_command': job_spec.get('command', 'echo "No command specified"'),
    }

    # Add secrets file if provided
    if secrets_file:
        cli_overrides['secrets_file'] = secrets_file

    # Handle environment variables
    if 'environment' in job_spec:
        env_pairs = [f"{k}={v}" for k, v in job_spec['environment'].items()]
        cli_overrides['job_env'] = ','.join(env_pairs)

    # Handle source configuration
    source = job_spec.get('source', {})
    source_type = source.get('type', 'local')

    if source_type == 'git':
        # For git sources, checkout first
        from src.source_prep import checkout_git_repo
        git_url = source.get('url')
        git_ref = source.get('ref', 'main')

        if git_url:
            log_stdout(f"Checking out {git_url} (ref: {git_ref})")
            config = get_config(job_command="dummy")
            checkout_git_repo(git_url, git_ref, config)

    # Get configuration
    try:
        config = get_config(**cli_overrides)

        if work_dir:
            os.chdir(work_dir)
            log_stdout(f"Changed working directory to: {work_dir}")

        # Validate
        validation_result = validate_config(config, check_files=True)
        if not validation_result.is_valid:
            log_stderr("Configuration validation failed:")
            log_stderr(format_validation_result(validation_result))
            raise typer.Exit(1)

        if validation_result.has_warnings:
            log_stderr(format_validation_result(validation_result))

        # Log job info
        log_stdout(f"Running job: {job_spec.get('name', 'unnamed')}")
        log_stdout(f"Image: {config.runner_image}")
        log_stdout(f"Command: {config.job_command}")

        if dry_run:
            log_stdout("🔍 Dry-run mode - skipping execution")
            return

        # Run the container
        exit_code = run_container(config=config)
        if exit_code != 0:
            raise typer.Exit(exit_code)

    except (ValueError, FileNotFoundError) as e:
        log_stderr(f"Configuration error: {e}")
        raise typer.Exit(1)
    except typer.Exit:
        raise  # Re-raise Exit exceptions
    except Exception as e:
        log_stderr(f"Job execution failed: {e}")
        raise typer.Exit(1)


@app.command()
def copy(
    source_dir: str,
    # Configuration overrides
    code_dir: Optional[str] = typer.Option(None, "--code-dir", help="Code directory path (default: /job/src)"),
    job_dir: Optional[str] = typer.Option(None, "--job-dir", help="Job directory path (default: same as code-dir)"),
    runner_image: Optional[str] = typer.Option(None, "--runner-image", help="Container image to use (default: quay.io/catalystcommunity/reactorcide_runner)"),
    work_dir: Optional[str] = typer.Option(None, "--work-dir", help="Working directory for job execution (default: current directory)")
):
    """Copy a directory to the configured code directory."""
    # Build configuration overrides from CLI arguments
    cli_overrides = {}
    if code_dir is not None:
        cli_overrides['code_dir'] = code_dir
    if job_dir is not None:
        cli_overrides['job_dir'] = job_dir
    if runner_image is not None:
        cli_overrides['runner_image'] = runner_image

    try:
        # Get configuration with CLI overrides (dummy job_command for directory setup)
        config = get_config(job_command="dummy", **cli_overrides)

        # If work_dir is provided, change to it before copy
        if work_dir:
            os.chdir(work_dir)
            log_stdout(f"Changed working directory to: {work_dir}")

        # Validate configuration for directory operations
        validation_result = validate_config(config, check_files=False)  # Don't check files for copy
        if not validation_result.is_valid:
            log_stderr("Configuration validation failed:")
            log_stderr(format_validation_result(validation_result))
            raise typer.Exit(1)
        
        # Show warnings if any
        if validation_result.has_warnings:
            log_stderr(format_validation_result(validation_result))
        
        log_stdout(f"Copying {source_dir} to {config.code_dir}")
        
        copy_directory(source_dir, config)
        log_stdout("✅ Directory copy complete")
        
        # Show what was created
        from src.source_prep import get_code_directory_path
        code_path = get_code_directory_path(config)
        try:
            item_count = len(list(code_path.iterdir()))
            log_stdout(f"📂 Copied {item_count} items to {code_path}")
        except Exception:
            pass
            
    except (ValueError, FileNotFoundError) as e:
        log_stderr(f"Configuration error: {e}")
        raise typer.Exit(1)
    except Exception as e:
        log_stderr(f"Copy failed: {e}")
        raise typer.Exit(1)


@app.command()
def cleanup(
    verbose: bool = typer.Option(False, "--verbose", "-v", help="Show detailed cleanup information"),
    work_dir: Optional[str] = typer.Option(None, "--work-dir", help="Working directory for job execution (default: current directory)")
):
    """Clean up the job directory."""
    try:
        from pathlib import Path

        # If work_dir is provided, change to it before cleanup
        if work_dir:
            os.chdir(work_dir)
            if verbose:
                log_stdout(f"Changed working directory to: {work_dir}")

        job_path = Path("./job")
        
        if verbose and job_path.exists():
            log_stdout("🗂️  Analyzing job directory before cleanup...")
            try:
                all_items = list(job_path.rglob("*"))
                file_count = len([item for item in all_items if item.is_file()])
                dir_count = len([item for item in all_items if item.is_dir()])
                
                log_stdout(f"📊 Found {file_count} files and {dir_count} directories")
                
                # Show top-level contents
                top_level = list(job_path.iterdir())
                if top_level:
                    log_stdout("📂 Top-level contents:")
                    for item in top_level[:10]:  # Show first 10 items
                        item_type = "📁" if item.is_dir() else "📄"
                        log_stdout(f"  {item_type} {item.name}")
                    if len(top_level) > 10:
                        log_stdout(f"  ... and {len(top_level) - 10} more items")
            except Exception as e:
                log_stdout(f"⚠️  Could not analyze directory: {e}")
        
        if job_path.exists():
            log_stdout(f"🗑️  Cleaning up job directory: {job_path}")
        else:
            log_stdout(f"📭 Job directory does not exist: {job_path}")
        
        cleanup_job_directory()
        log_stdout("✅ Cleanup complete")
        
    except Exception as e:
        log_stderr(f"Cleanup failed: {e}")
        raise typer.Exit(1)


@app.command()
def config(
    # Configuration overrides
    code_dir: Optional[str] = typer.Option(None, "--code-dir", help="Code directory path (default: /job/src)"),
    job_dir: Optional[str] = typer.Option(None, "--job-dir", help="Job directory path (default: same as code-dir)"),
    job_command: Optional[str] = typer.Option(None, "--job-command", help="Command to run in the container"),
    runner_image: Optional[str] = typer.Option(None, "--runner-image", help="Container image to use (default: quay.io/catalystcommunity/reactorcide_runner)"),
    job_env: Optional[str] = typer.Option(None, "--job-env", help="Environment variables as key=value pairs or file path (must start with ./job/)"),
    secrets_list: Optional[str] = typer.Option(None, "--secrets-list", help="Comma-separated list of secret values to mask in logs, or path to secrets file"),
):
    """Display the resolved configuration."""
    # Build configuration overrides from CLI arguments
    cli_overrides = {}
    if code_dir is not None:
        cli_overrides['code_dir'] = code_dir
    if job_dir is not None:
        cli_overrides['job_dir'] = job_dir
    if job_command is not None:
        cli_overrides['job_command'] = job_command
    if runner_image is not None:
        cli_overrides['runner_image'] = runner_image
    if job_env is not None:
        cli_overrides['job_env'] = job_env
    if secrets_list is not None:
        cli_overrides['secrets_list'] = secrets_list
    
    try:
        # Get configuration with CLI overrides
        from src.config import get_environment_vars
        config = get_config(**cli_overrides)
        
        log_stdout("Resolved Configuration:")
        log_stdout(f"  Code Directory: {config.code_dir}")
        log_stdout(f"  Job Directory: {config.job_dir}")
        log_stdout(f"  Job Command: {config.job_command}")
        log_stdout(f"  Runner Image: {config.runner_image}")
        log_stdout(f"  Job Environment: {config.job_env or 'None'}")
        
        # Show environment variables that would be passed to container
        env_vars = get_environment_vars(config)
        log_stdout("\nEnvironment Variables:")
        for key, value in sorted(env_vars.items()):
            log_stdout(f"  {key}={value}")
            
    except (ValueError, FileNotFoundError) as e:
        log_stderr(f"Configuration error: {e}")
        raise typer.Exit(1)
    except Exception as e:
        log_stderr(f"Unexpected error: {e}")
        raise typer.Exit(1)


@app.command()
def validate(
    # Configuration overrides
    code_dir: Optional[str] = typer.Option(None, "--code-dir", help="Code directory path (default: /job/src)"),
    job_dir: Optional[str] = typer.Option(None, "--job-dir", help="Job directory path (default: same as code-dir)"),
    job_command: Optional[str] = typer.Option(None, "--job-command", help="Command to run in the container"),
    runner_image: Optional[str] = typer.Option(None, "--runner-image", help="Container image to use (default: quay.io/catalystcommunity/reactorcide_runner)"),
    job_env: Optional[str] = typer.Option(None, "--job-env", help="Environment variables as key=value pairs or file path (must start with ./job/)"),
    secrets_list: Optional[str] = typer.Option(None, "--secrets-list", help="Comma-separated list of secret values to mask in logs, or path to secrets file"),
    # Validation options
    check_files: bool = typer.Option(True, "--check-files/--no-check-files", help="Check file and directory existence"),
):
    """Validate the configuration without executing."""
    # Build configuration overrides from CLI arguments
    cli_overrides = {}
    if code_dir is not None:
        cli_overrides['code_dir'] = code_dir
    if job_dir is not None:
        cli_overrides['job_dir'] = job_dir
    if job_command is not None:
        cli_overrides['job_command'] = job_command
    if runner_image is not None:
        cli_overrides['runner_image'] = runner_image
    if job_env is not None:
        cli_overrides['job_env'] = job_env
    if secrets_list is not None:
        cli_overrides['secrets_list'] = secrets_list
    
    try:
        # Get configuration with CLI overrides
        config = get_config(**cli_overrides)
        
        # Validate configuration
        validation_result = validate_config(config, check_files=check_files)
        
        # Display validation results
        result_text = format_validation_result(validation_result)
        if validation_result.is_valid:
            log_stdout(result_text)
        else:
            log_stderr(result_text)
        
        # Exit with appropriate code
        if validation_result.is_valid:
            raise typer.Exit(0)
        else:
            raise typer.Exit(1)
            
    except (ValueError, FileNotFoundError) as e:
        log_stderr(f"Configuration error: {e}")
        raise typer.Exit(1)
    except Exception as e:
        log_stderr(f"Unexpected error: {e}")
        raise typer.Exit(1)


git_app = typer.Typer()
app.add_typer(git_app, name="git")

# Note: Secrets CLI commands have been moved to the Go CLI (reactorcide secrets *).
# The resolve_job_secrets function above still uses secrets_local.py for reading
# secrets during job execution.


@git_app.command("files-changed")
def git_files_changed(
    gitref: str,
    # Configuration overrides
    code_dir: Optional[str] = typer.Option(None, "--code-dir", help="Code directory path (default: /job/src)"),
    job_dir: Optional[str] = typer.Option(None, "--job-dir", help="Job directory path (default: same as code-dir)"),
    runner_image: Optional[str] = typer.Option(None, "--runner-image", help="Container image to use (default: quay.io/catalystcommunity/reactorcide_runner)")
):
    """Get list of files changed from the given git reference."""
    # Build configuration overrides from CLI arguments
    cli_overrides = {}
    if code_dir is not None:
        cli_overrides['code_dir'] = code_dir
    if job_dir is not None:
        cli_overrides['job_dir'] = job_dir
    if runner_image is not None:
        cli_overrides['runner_image'] = runner_image
    
    try:
        # Get configuration with CLI overrides
        config = get_config(job_command="dummy", **cli_overrides)
        
        # Basic validation (mainly for directory paths)
        validation_result = validate_config(config, check_files=False)
        if not validation_result.is_valid:
            log_stderr("Configuration validation failed:")
            log_stderr(format_validation_result(validation_result))
            raise typer.Exit(1)
        
        # Get the host path for the code directory
        from src.source_prep import get_code_directory_path
        repo_path = get_code_directory_path(config)
        
        # Check if repository exists
        if not repo_path.exists():
            log_stderr(f"Repository directory does not exist: {repo_path}")
            log_stderr("💡 Use 'reactorcide checkout' or 'reactorcide copy' to set up the code directory first")
            raise typer.Exit(1)
        
        if not (repo_path / ".git").exists():
            log_stderr(f"Not a git repository: {repo_path}")
            log_stderr("💡 The code directory must contain a git repository")
            raise typer.Exit(1)
        
        log_stderr(f"🔍 Checking for changes from {gitref} in {repo_path}")
        
        changed_files = get_files_changed(gitref, str(repo_path))
        
        if changed_files:
            log_stderr(f"📝 Found {len(changed_files)} changed files:")
            for file_path in changed_files:
                print(file_path)  # Use print for clean output that can be piped
        else:
            log_stderr(f"✅ No files changed from {gitref}")
            
    except (ValueError, FileNotFoundError) as e:
        log_stderr(f"Configuration error: {e}")
        raise typer.Exit(1)
    except Exception as e:
        log_stderr(f"Error getting changed files: {e}")
        raise typer.Exit(1)


@git_app.command("info")
def git_info(
    # Configuration overrides
    code_dir: Optional[str] = typer.Option(None, "--code-dir", help="Code directory path (default: /job/src)"),
    job_dir: Optional[str] = typer.Option(None, "--job-dir", help="Job directory path (default: same as code-dir)"),
    runner_image: Optional[str] = typer.Option(None, "--runner-image", help="Container image to use (default: quay.io/catalystcommunity/reactorcide_runner)")
):
    """Show information about the git repository in the code directory."""
    # Build configuration overrides from CLI arguments
    cli_overrides = {}
    if code_dir is not None:
        cli_overrides['code_dir'] = code_dir
    if job_dir is not None:
        cli_overrides['job_dir'] = job_dir
    if runner_image is not None:
        cli_overrides['runner_image'] = runner_image
    
    try:
        # Get configuration with CLI overrides
        config = get_config(job_command="dummy", **cli_overrides)
        
        # Get the host path for the code directory
        from src.source_prep import get_code_directory_path
        from src.git_ops import get_repository_info, validate_git_repository
        
        repo_path = get_code_directory_path(config)
        
        log_stdout(f"📂 Code directory: {repo_path}")
        log_stdout(f"🔗 Container path: {config.code_dir}")
        
        # Validate repository
        is_valid, message = validate_git_repository(str(repo_path))
        
        if not is_valid:
            log_stderr(f"❌ {message}")
            raise typer.Exit(1)
        
        # Get repository information
        repo_info = get_repository_info(str(repo_path))
        
        if repo_info["error"]:
            log_stderr(f"❌ Repository error: {repo_info['error']}")
            raise typer.Exit(1)
        
        log_stdout("\n📋 Repository Information:")
        log_stdout(f"  Branch: {repo_info['current_branch']}")
        log_stdout(f"  Commit: {repo_info['current_commit']}")
        log_stdout(f"  Status: {'🔴 Dirty' if repo_info['is_dirty'] else '✅ Clean'}")
        
        if repo_info['remotes']:
            log_stdout(f"  Remotes: {', '.join(repo_info['remotes'])}")
        else:
            log_stdout("  Remotes: None")
            
    except (ValueError, FileNotFoundError) as e:
        log_stderr(f"Configuration error: {e}")
        raise typer.Exit(1)
    except Exception as e:
        log_stderr(f"Error getting repository info: {e}")
        raise typer.Exit(1)


def _perform_dry_run(config, additional_args: Optional[List[str]] = None) -> None:
    """Perform a dry-run showing what would be executed without running it.
    
    Args:
        config: Runner configuration
        additional_args: Additional arguments that would be passed to the job
    """
    from src.source_prep import get_code_directory_path, get_job_directory_path
    from src.container_validation import (
        check_container_image_availability, 
        validate_container_runtime
    )
    from pathlib import Path
    
    log_stdout("🔍 DRY RUN MODE - No execution will occur")
    log_stdout("=" * 50)
    
    # Show resolved configuration
    log_stdout("\n📋 Resolved Configuration:")
    log_stdout(f"  Code Directory: {config.code_dir}")
    log_stdout(f"  Job Directory: {config.job_dir}")
    log_stdout(f"  Job Command: {config.job_command}")
    log_stdout(f"  Runner Image: {config.runner_image}")
    log_stdout(f"  Job Environment: {config.job_env or 'None'}")
    
    # Show additional arguments if any
    if additional_args:
        log_stdout(f"  Additional Args: {' '.join(additional_args)}")
    else:
        log_stdout("  Additional Args: None")
    
    # Show environment variables
    env_vars = get_environment_vars(config)
    log_stdout(f"\n🌍 Environment Variables ({len(env_vars)} total):")

    # Group environment variables for better display
    reactorcide_vars = {k: v for k, v in env_vars.items() if k.startswith('REACTORCIDE_')}
    job_vars = {k: v for k, v in env_vars.items() if not k.startswith('REACTORCIDE_')}

    # Create masker with proper secrets list
    masker = SecretMasker()
    secrets_list = get_secrets_to_mask(config, env_vars)
    masker.register_secrets(secrets_list)

    if reactorcide_vars:
        log_stdout("  REACTORCIDE Configuration:")
        for key, value in sorted(reactorcide_vars.items()):
            # REACTORCIDE vars are config, not secrets - show them unmasked
            log_stdout(f"    {key}={value}")

    if job_vars:
        log_stdout("  Job-specific Variables:")
        for key, value in sorted(job_vars.items()):
            # Job vars may contain secrets, mask them
            masked_value = masker.mask_string(str(value))
            log_stdout(f"    {key}={masked_value}")
    
    # Show detailed directory structure
    log_stdout("\n📁 Directory Structure Validation:")
    job_path = Path("./job").resolve()
    log_stdout(f"  Host Job Directory: {job_path}")
    log_stdout(f"  Container Mount: {job_path} → /job")
    
    # Get specific directory paths
    try:
        code_path = get_code_directory_path(config)
        job_dir_path = get_job_directory_path(config)
        
        log_stdout(f"  Code Directory: {code_path} → {config.code_dir}")
        if config.code_dir != config.job_dir:
            log_stdout(f"  Job Directory: {job_dir_path} → {config.job_dir}")
        else:
            log_stdout("  Job Directory: Same as code directory")
    except Exception as e:
        log_stdout(f"  ⚠️  Error resolving directory paths: {e}")
        code_path = job_path / "src"
        job_dir_path = job_path
    
    # Check base job directory
    if job_path.exists():
        if job_path.is_dir():
            log_stdout("  ✅ Base job directory exists and is accessible")
            
            # Show directory contents with more detail
            try:
                all_contents = list(job_path.iterdir())
                if all_contents:
                    log_stdout(f"  📂 Contents ({len(all_contents)} items):")
                    # Show first 8 items with more detail
                    for item in all_contents[:8]:
                        if item.is_dir():
                            try:
                                sub_count = len(list(item.iterdir()))
                                log_stdout(f"    📁 {item.name}/ ({sub_count} items)")
                            except PermissionError:
                                log_stdout(f"    📁 {item.name}/ (permission denied)")
                        else:
                            size_kb = item.stat().st_size // 1024
                            log_stdout(f"    📄 {item.name} ({size_kb}KB)")
                    
                    if len(all_contents) > 8:
                        log_stdout(f"    ... and {len(all_contents) - 8} more items")
                else:
                    log_stdout("  📂 Directory is empty")
            except PermissionError:
                log_stdout("  ⚠️  Cannot read directory contents (permission denied)")
        else:
            log_stdout("  ❌ Path exists but is not a directory")
    else:
        log_stdout("  ⚠️  Job directory does not exist (will be created automatically)")
    
    # Check specific code directory
    if code_path.exists():
        if code_path.is_dir():
            try:
                code_contents = list(code_path.iterdir())
                log_stdout(f"  ✅ Code directory exists ({len(code_contents)} items)")
                # Check for common files that indicate a valid code repository
                common_files = ['.git', 'package.json', 'Cargo.toml', 'go.mod', 'requirements.txt', 'Makefile']
                found_indicators = [f for f in common_files if (code_path / f).exists()]
                if found_indicators:
                    log_stdout(f"    📋 Detected: {', '.join(found_indicators)}")
            except PermissionError:
                log_stdout("  ⚠️  Code directory exists but cannot be read")
        else:
            log_stdout("  ❌ Code path exists but is not a directory")
    else:
        log_stdout("  ⚠️  Code directory does not exist")
    
    # Check job directory if different
    if config.code_dir != config.job_dir and job_dir_path != code_path:
        if job_dir_path.exists():
            if job_dir_path.is_dir():
                try:
                    job_contents = list(job_dir_path.iterdir())
                    log_stdout(f"  ✅ Job directory exists ({len(job_contents)} items)")
                except PermissionError:
                    log_stdout("  ⚠️  Job directory exists but cannot be read")
            else:
                log_stdout("  ❌ Job path exists but is not a directory")
        else:
            log_stdout("  ⚠️  Job directory does not exist")
    
    # Validate container runtime and image
    log_stdout("\n🔧 Container Runtime & Image Validation:")
    
    # Check runtime
    runtime_valid, runtime_message = validate_container_runtime()
    log_stdout(f"  {runtime_message}")
    
    # Check container image availability
    if runtime_valid:
        log_stdout(f"  🔍 Checking image availability: {config.runner_image}")
        image_available, image_message = check_container_image_availability(config.runner_image)
        
        if image_available:
            log_stdout("  ✅ Container image is available")
            if image_message:
                log_stdout(f"    💡 {image_message}")
        else:
            log_stdout("  ❌ Container image is NOT available")
            if image_message:
                log_stdout(f"    ⚠️  {image_message}")
    else:
        log_stdout("  ⏭️  Skipping image check (runtime not available)")
    
    # Show container execution details
    log_stdout("\n🐳 Container Execution Plan:")
    log_stdout(f"  Image: {config.runner_image}")
    log_stdout(f"  Working Directory: {config.job_dir}")
    log_stdout(f"  Command: {config.job_command}")
    if additional_args:
        log_stdout(f"  Arguments: {' '.join(additional_args)}")
    else:
        log_stdout("  Arguments: None")
    
    # Build the actual command that would be executed (for reference)
    cmd_parts = ["docker", "run", "--rm"]
    
    # Add environment variables
    for key, value in env_vars.items():
        if any(sensitive in key.lower() for sensitive in ['token', 'secret', 'key', 'password', 'auth']):
            cmd_parts.extend(["-e", f"{key}=***"])
        else:
            cmd_parts.extend(["-e", f"{key}={value}"])
    
    # Add mount and other options
    cmd_parts.extend(["-v", f"{job_path}:/job"])
    cmd_parts.extend(["-w", config.job_dir])
    cmd_parts.append(config.runner_image)
    cmd_parts.append(config.job_command)
    if additional_args:
        cmd_parts.extend(additional_args)
    
    log_stdout("\n💻 Equivalent Command:")
    # Split long commands for readability
    cmd_str = " ".join(cmd_parts)
    if len(cmd_str) > 80:
        log_stdout(f"  {cmd_parts[0]} {cmd_parts[1]} {cmd_parts[2]} \\")
        current_line = "    "
        for part in cmd_parts[3:]:
            if len(current_line + part + " ") > 76:
                log_stdout(f"{current_line}\\")
                current_line = f"    {part} "
            else:
                current_line += f"{part} "
        log_stdout(current_line.rstrip())
    else:
        log_stdout(f"  {cmd_str}")
    
    # Provide overall assessment
    log_stdout("\n📊 Execution Readiness Assessment:")
    
    issues = []
    warnings = []
    
    # Check for blocking issues
    if not runtime_valid:
        issues.append("Container runtime is not available")
    
    if runtime_valid and not image_available:
        issues.append("Container image is not available")
    
    if not job_path.exists() and not code_path.exists():
        warnings.append("No job or code directories exist yet")
    
    if config.job_env:
        try:
            # Re-validate job environment during dry-run
            from src.config import config_manager
            config_manager.parse_job_environment(config.job_env)
        except Exception as e:
            issues.append(f"Job environment configuration error: {e}")
    
    # Display assessment
    if issues:
        log_stdout(f"  ❌ Execution would FAIL ({len(issues)} blocking issues):")
        for issue in issues:
            log_stdout(f"    • {issue}")
    elif warnings:
        log_stdout(f"  ⚠️  Execution might succeed ({len(warnings)} warnings):")
        for warning in warnings:
            log_stdout(f"    • {warning}")
        log_stdout("  💡 Consider addressing warnings before execution")
    else:
        log_stdout("  ✅ Execution should succeed - all checks passed")
    
    log_stdout("\n🔍 Dry-run completed")
    if not issues:
        log_stdout("💡 Run without --dry-run to execute the job")
    else:
        log_stdout("🛠️  Fix the issues above before executing")


if __name__ == "__main__":
    app()