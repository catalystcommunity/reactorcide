"""CLI interface for runnerlib."""

import os
import json
import signal
import subprocess
import shlex
import sys
import threading
import getpass
import typer
from pathlib import Path
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
from src.signals import (
    TermRequested,
    TERM_EXIT_CODE,
    install_sigterm_handler,
    register_sigterm_cleanup,
    unregister_sigterm_cleanup,
)

app = typer.Typer()


def discover_run_plugin_directories(
    config,
    ci_source_path=None,
    source_path=None,
    explicit_plugin_dir=None,
):
    """Find plugin directories for an in-container runnerlib job.

    Trusted CI plugins take precedence over application-source plugins. This
    prevents a pull request from adding a plugin when the coordinator supplied
    a separate trusted CI checkout.
    """
    plugin_dirs = []

    def add_plugins_from(base_path):
        if not base_path:
            return False
        candidate = Path(base_path) / ".reactorcide" / "plugins"
        if not candidate.is_dir():
            return False
        candidate_text = str(candidate)
        if candidate_text not in plugin_dirs:
            plugin_dirs.append(candidate_text)
        return True

    trusted_plugins_found = add_plugins_from(ci_source_path)

    if not trusted_plugins_found:
        configured_ci_path = os.environ.get("REACTORCIDE_CI_SOURCE_DIR")
        trusted_plugins_found = add_plugins_from(configured_ci_path)

    if not trusted_plugins_found:
        application_path = source_path or config.code_dir
        add_plugins_from(application_path)

    if explicit_plugin_dir and explicit_plugin_dir not in plugin_dirs:
        plugin_dirs.append(explicit_plugin_dir)

    return plugin_dirs


@app.callback()
def _install_signal_handling(ctx: typer.Context) -> None:
    """Runs before every subcommand. Installs the SIGTERM -> TermRequested
    trap (see src/signals.py) so whichever command ends up as PID 1 in a job
    container (normally `run`) can route a graceful-cancel SIGTERM into its
    existing cleanup path instead of dying mid-job."""
    install_sigterm_handler()


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
        # start_new_session=True puts the process in its own process group so we
        # can kill background daemons (buildkitd, dockerd, etc.) when the main
        # command exits, preventing the job from hanging until timeout. It also
        # means _kill_child_process_group below can safely target this whole
        # group via os.killpg(process.pid, ...) — process.pid IS the pgid here.
        process = subprocess.Popen(
            config.job_command,
            shell=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.STDOUT,  # Merge stderr into stdout for simpler handling
            env=env,
            text=True,
            bufsize=1,  # Line buffered
            start_new_session=True,
        )

        # Read output in a background thread so we can wait on the main process
        # independently. Without this, readline() blocks until ALL processes
        # holding the stdout fd exit (including background daemons like buildkitd).
        def _stream_output():
            try:
                for line in iter(process.stdout.readline, ''):
                    if line:
                        masked_line = masker.mask_string(line.rstrip())
                        log_stdout(masked_line)
            except (ValueError, OSError):
                pass  # Pipe closed

        reader = threading.Thread(target=_stream_output, daemon=True)
        reader.start()

        # TERM-then-KILL reaper for the job's whole process group, used both
        # for the normal post-exit cleanup below (any background daemons the
        # job left running) and, via _reap_after_term, for a SIGTERM-driven
        # shutdown.
        def _kill_process_group(sig):
            try:
                os.killpg(process.pid, sig)
            except OSError:
                pass

        def _reap_with_grace(grace_seconds):
            """Waits up to grace_seconds for the process to exit, escalating
            to SIGKILL if it hasn't. Must only be called once the *original*
            process.wait() call is no longer in flight — Popen serializes
            wait()/poll() on an internal lock, so calling this while that
            original call is still unwinding would just block until it does."""
            try:
                process.wait(timeout=grace_seconds)
            except subprocess.TimeoutExpired:
                _kill_process_group(signal.SIGKILL)
                try:
                    process.wait(timeout=2)
                except subprocess.TimeoutExpired:
                    pass
            reader.join(timeout=2)

        # Registered as the SIGTERM handler's cleanup callback (see
        # src/signals.py): forwards SIGTERM to the job's process group the
        # instant the signal arrives. Deliberately does NOT wait/poll here —
        # the process.wait() call below is what's being interrupted by the
        # signal and is still holding Popen's internal waitpid lock for the
        # duration of the handler, so polling from inside the handler would
        # just block blindly until that lock frees up on its own. The
        # TERM-then-KILL escalation happens afterward, in the `except
        # TermRequested` clause below, once that lock is free again.
        def _forward_sigterm_immediately():
            _kill_process_group(signal.SIGTERM)

        register_sigterm_cleanup(_forward_sigterm_immediately)
        try:
            try:
                # Wait for the main process (the shell) to exit. This returns
                # as soon as the shell exits, even if background children are
                # still running — or raises TermRequested if a SIGTERM
                # arrives first (see src/signals.py; _forward_sigterm_immediately
                # has already sent SIGTERM to the group by the time this
                # propagates).
                exit_code = process.wait()

                # Give the reader thread a moment to flush trailing output.
                reader.join(timeout=5)
                if reader.is_alive():
                    # Reader is still going (background daemon still holding
                    # the pipe open) — clean up the process group.
                    _kill_process_group(signal.SIGTERM)
                    reader.join(timeout=2)
                    if reader.is_alive():
                        _kill_process_group(signal.SIGKILL)
                        reader.join(timeout=2)
            except TermRequested:
                # SIGTERM already forwarded to the group above; give it up to
                # 5s to exit gracefully, then force-kill and reap so we don't
                # leave a zombie/orphan behind.
                _reap_with_grace(5)
                raise
        finally:
            unregister_sigterm_cleanup(_forward_sigterm_immediately)

        # Close the pipe to unblock any remaining reads
        try:
            process.stdout.close()
        except OSError:
            pass

        if exit_code == 0:
            log_stdout(f"✓ Job completed successfully (exit code: {exit_code})")
        else:
            log_stderr(f"✗ Job failed with exit code: {exit_code}")

        return exit_code

    except TermRequested:
        # Let this propagate to run()'s outer handler, which runs
        # PluginPhase.CLEANUP and exits with TERM_EXIT_CODE — do not let the
        # broad except below swallow it as a generic execution error.
        raise
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
    secret_values_list: Optional[str] = typer.Option(None, "--secret-values-list", help="Comma-separated list of secret values to mask in logs, or path to secrets file"),
    secret_env_names: Optional[str] = typer.Option(None, "--secret-env-names", help="Comma-separated list of env var names whose values should be masked"),
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
    if secret_values_list is not None:
        cli_overrides['secret_values_list'] = secret_values_list
    if secret_env_names is not None:
        cli_overrides['secret_env_names'] = secret_env_names
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
        # NOTE: Plugin loading is deferred until after source prep so plugins
        # can be loaded from the checked-out repository.

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
        # NOTE: We don't execute PRE_SOURCE_PREP plugins yet because plugins haven't been loaded.
        # Plugins are loaded from the source after checkout.

        from src.source_prep import (
            cleanup_vcs_auth,
            prepare_ci_source,
            prepare_shared_eval_sources,
            prepare_source,
        )

        try:
            shared_sources = prepare_shared_eval_sources(config)
            if shared_sources:
                source_path, ci_source_path = shared_sources
            else:
                # Prepare CI source first so plugins never load from an
                # untrusted application checkout.
                ci_source_path = prepare_ci_source(config)
                source_path = prepare_source(config)
            if ci_source_path:
                plugin_context.metadata['ci_source_path'] = str(ci_source_path)
            if source_path:
                plugin_context.metadata['source_path'] = str(source_path)
        finally:
            cleanup_vcs_auth()

        # Load trusted CI plugins when a CI checkout exists. Do not also load
        # application-source plugins in that case: application source can be
        # an untrusted pull request. For run-local, discover plugins from the
        # pre-mounted code directory when no source preparation was necessary.
        plugin_dirs = discover_run_plugin_directories(
            config,
            ci_source_path=ci_source_path,
            source_path=source_path,
            explicit_plugin_dir=plugin_dir,
        )

        # Initialize plugins from all discovered directories
        initialize_plugins(None)  # Load built-in plugins first
        for pdir in plugin_dirs:
            log_stdout(f"Loading plugins from: {pdir}")
            plugin_manager.load_plugins_from_directory(pdir)

        if plugin_manager.plugins:
            log_stdout(f"Loaded {len(plugin_manager.plugins)} plugins: {', '.join(plugin_manager.list_plugins())}")

        # Now execute POST_SOURCE_PREP plugins (they can access the checked-out source)
        plugin_context.phase = PluginPhase.POST_SOURCE_PREP
        plugin_manager.execute_phase(PluginPhase.POST_SOURCE_PREP, plugin_context)

        # Execute the job (use_container already determined before validation).
        # Wrapped in its own try/finally so PluginPhase.CLEANUP is guaranteed
        # to run for the local-execution path even on a SIGTERM-triggered
        # TermRequested (see src/signals.py) — run_container() already
        # guarantees this via its own internal try/finally, so we only need
        # to do it here for the non-container (_run_local) branch.
        try:
            if use_container:
                # Container execution mode - for integration testing
                log_stdout("Running job in CONTAINER mode")
                exit_code = run_container(config=config, additional_args=command_args)
            else:
                # Local execution mode (default) - for deployment and emergency use
                exit_code = _run_local(config, command_args)
        finally:
            if not use_container:
                plugin_context.phase = PluginPhase.CLEANUP
                try:
                    plugin_manager.execute_phase(PluginPhase.CLEANUP, plugin_context)
                except Exception:
                    log_stderr("Cleanup plugin phase failed")

        raise typer.Exit(exit_code)
    except typer.Exit:
        # Re-raise typer.Exit to avoid catching it as a generic exception
        raise
    except TermRequested:
        # SIGTERM was received (graceful cancel from the coordinator). The
        # child process
        # has already been terminated and PluginPhase.CLEANUP has already run
        # (both via the try/finally above); cleanup_vcs_auth already ran in
        # the source-prep try/finally earlier in this function regardless of
        # when the signal arrived. Exit with a distinct, recognizable code
        # rather than falling through to the generic error path.
        log_stderr(f"Received SIGTERM — job terminated (exit code {TERM_EXIT_CODE})")
        raise typer.Exit(TERM_EXIT_CODE)
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
        
        from src.source_prep import cleanup_vcs_auth
        try:
            checkout_git_repo(git_url, git_ref, config)
        finally:
            cleanup_vcs_auth()
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

        from src.source_prep import get_job_base_path
        job_path = get_job_base_path()
        
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
    secret_values_list: Optional[str] = typer.Option(None, "--secret-values-list", help="Comma-separated list of secret values to mask in logs, or path to secrets file"),
    secret_env_names: Optional[str] = typer.Option(None, "--secret-env-names", help="Comma-separated list of env var names whose values should be masked"),
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
    if secret_values_list is not None:
        cli_overrides['secret_values_list'] = secret_values_list
    if secret_env_names is not None:
        cli_overrides['secret_env_names'] = secret_env_names

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
    secret_values_list: Optional[str] = typer.Option(None, "--secret-values-list", help="Comma-separated list of secret values to mask in logs, or path to secrets file"),
    secret_env_names: Optional[str] = typer.Option(None, "--secret-env-names", help="Comma-separated list of env var names whose values should be masked"),
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
    if secret_values_list is not None:
        cli_overrides['secret_values_list'] = secret_values_list
    if secret_env_names is not None:
        cli_overrides['secret_env_names'] = secret_env_names

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


@app.command("eval")
def eval_cmd(
    ci_source_dir: str = typer.Option("/job/ci", envvar="REACTORCIDE_CI_SOURCE_DIR", help="CI source directory containing job definitions"),
    source_dir: str = typer.Option("/job/src", envvar="REACTORCIDE_SOURCE_DIR", help="Source code directory"),
    event_type: str = typer.Option(..., envvar="REACTORCIDE_EVENT_TYPE", help="Generic event type (e.g., push, pull_request_opened)"),
    branch: str = typer.Option("", envvar="REACTORCIDE_BRANCH", help="Branch name"),
    pr_base_ref: str = typer.Option("", envvar="REACTORCIDE_PR_BASE_REF", help="PR base branch reference"),
    diff_base: str = typer.Option("", envvar="REACTORCIDE_DIFF_BASE", help="Base commit SHA for file change detection"),
    pr_number: str = typer.Option("", envvar="REACTORCIDE_PR_NUMBER", help="Pull request number"),
    source_url: str = typer.Option("", envvar="REACTORCIDE_SOURCE_URL", help="Source repository URL"),
    source_ref: str = typer.Option("", envvar="REACTORCIDE_SHA", help="Source git reference (SHA)"),
    ci_source_url: str = typer.Option("", envvar="REACTORCIDE_CI_SOURCE_URL", help="CI source repository URL"),
    ci_source_ref: str = typer.Option("", envvar="REACTORCIDE_CI_SOURCE_REF", help="CI source git reference"),
    head_url: str = typer.Option("", envvar="REACTORCIDE_HEAD_URL", help="PR head repository URL (fork URL for cross-repo PRs)"),
    head_ref: str = typer.Option("", envvar="REACTORCIDE_HEAD_REF", help="PR head branch name"),
    base_url: str = typer.Option("", envvar="REACTORCIDE_BASE_URL", help="PR base/upstream repository URL"),
    base_ref: str = typer.Option("", envvar="REACTORCIDE_BASE_REF", help="PR base branch name"),
    is_fork_pr: str = typer.Option("", envvar="REACTORCIDE_IS_FORK_PR", help="Set to 'true' when PR is cross-repository"),
    triggers_file: str = typer.Option("/job/triggers.json", help="Path to write triggers output"),
    workflow_file: str = typer.Option("", "--workflow-file", help="Evaluate only this workflow file for the selected event"),
    changed_file: Optional[List[str]] = typer.Option(None, "--changed-file", help="Changed source path; repeat for each path"),
    allow_insecure_transport: bool = typer.Option(False, "--allow-insecure-transport", help="Allow API credentials without TLS on an isolated development network"),
):
    """Evaluate job definitions against an event and generate triggers.

    Reads job definitions from the CI source directory, matches them against
    the current event type/branch/changed files, and writes matched triggers
    to a JSON file for the worker to pick up.
    """
    from pathlib import Path
    from src.eval import (
        load_job_definitions,
        load_workflow_definitions,
        evaluate_event,
        workflow_match_reason,
        generate_triggers,
        EventContext,
        VALID_EVENT_TYPES,
    )
    from src.workflow import WorkflowContext, changed_files

    # Validate event type
    if event_type not in VALID_EVENT_TYPES:
        log_stderr(f"Invalid event type: {event_type}")
        log_stderr(f"Valid types: {', '.join(sorted(VALID_EVENT_TYPES))}")
        raise typer.Exit(1)

    ci_root_path = Path(ci_source_dir)
    ci_source_path = ci_root_path
    source_path = Path(source_dir)

    # A pull request always evaluates trusted definitions from the exact base
    # checkout. The head checkout is separate and remains candidate data until
    # the coordinator policy permits a workflow to use it.
    dual_checkout = bool(pr_number and not (ci_root_path / ".reactorcide").is_dir())
    if dual_checkout:
        ci_source_path = ci_root_path / "base"

    # Prepare CI source if not already present.
    # When running as an eval job, the coordinator passes CI source info via env vars
    # but doesn't pre-clone the repository — the eval command needs to do it.
    if ci_source_url and not (ci_source_path / ".reactorcide").is_dir():
        log_stdout(f"CI source not found at {ci_source_path}, cloning from {ci_source_url}")
        from src.source_prep import _prepare_git_source
        _prepare_git_source(ci_source_url, ci_source_ref or None, ci_source_path)

    if dual_checkout and head_url and source_ref:
        head_ci_path = ci_root_path / "head"
        if not (head_ci_path / ".reactorcide").is_dir():
            log_stdout(f"Preparing PR head candidate data at {head_ci_path}")
            from src.source_prep import _prepare_git_source
            _prepare_git_source(head_url, source_ref, head_ci_path)

    # Prepare regular source if not already present.
    # Needed for git diff to detect changed files for path-based triggers.
    if source_url and not (source_path / ".git").is_dir():
        log_stdout(f"Source not found at {source_path}, cloning from {source_url}")
        from src.source_prep import _prepare_git_source
        _prepare_git_source(source_url, source_ref or None, source_path)

    # Get changed files via git diff if source dir is a git repo. Both the
    # workflow and bare-jobs paths use this for path-based trigger filtering.
    changed = list(changed_file) if changed_file else None
    if changed is None and source_path.exists() and (source_path / ".git").exists():
        try:
            if diff_base:
                # Use the stable base SHA (works correctly even after merge)
                changed = changed_files(diff_base, "HEAD", str(source_path))
            elif pr_base_ref:
                changed = changed_files(f"origin/{pr_base_ref}", "HEAD", str(source_path))
            else:
                changed = changed_files("HEAD^", "HEAD", str(source_path))
            if changed:
                log_stdout(f"Found {len(changed)} changed file(s)")
        except Exception as e:
            log_stderr(f"Warning: could not determine changed files: {e}")

    # Build event context (shared by both evaluation paths).
    ctx = EventContext(
        event_type=event_type,
        branch=branch,
        source_url=source_url,
        source_ref=source_ref,
        ci_source_url=ci_source_url,
        ci_source_ref=ci_source_ref,
        pr_base_ref=pr_base_ref,
        pr_number=pr_number,
        head_url=head_url,
        head_ref=head_ref,
        base_url=base_url,
        base_ref=base_ref,
        is_fork_pr=is_fork_pr,
    )

    workflow_ctx = WorkflowContext(triggers_file=triggers_file, allow_insecure_transport=allow_insecure_transport)

    # Prefer workflow-centric definitions (.reactorcide/workflows/*.yaml). A
    # workflow names its pipeline and groups its jobs, and one event can match
    # several workflows (per team/product/etc). Fall back to bare job files
    # (.reactorcide/jobs/*.yaml) when no workflow files exist.
    workflow_defs = load_workflow_definitions(ci_source_path)

    selected_workflow_path = None
    if workflow_file:
        selected_workflow_path = Path(workflow_file)
        if not selected_workflow_path.is_absolute():
            selected_workflow_path = ci_source_path / selected_workflow_path
        selected_workflow_path = selected_workflow_path.resolve()
        try:
            selected_workflow_path.relative_to(ci_source_path.resolve())
        except ValueError:
            raise ValueError("selected workflow file is outside the CI source directory")
        workflow_defs = [
            workflow for workflow in workflow_defs
            if Path(workflow.source_file).resolve() == selected_workflow_path
        ]
        if not workflow_defs:
            raise ValueError(f"selected workflow file was not loaded: {workflow_file}")

    if workflow_defs:
        log_stdout(
            f"Loaded {len(workflow_defs)} workflow definition(s) from "
            f"{ci_source_path / '.reactorcide' / 'workflows'}: "
            f"{', '.join(w.name for w in workflow_defs)}"
        )
        log_stdout(f"Evaluating for event '{event_type}'"
                   + (f", branch '{branch}'" if branch else "")
                   + (f", {len(changed)} changed file(s)" if changed else ""))

        from src.ci_policy import ci_paths, decide_workflow, load_coordinator_policy

        trusted_policy = load_coordinator_policy(os.getenv("REACTORCIDE_CI_POLICY", ""))
        changed_ci = ci_paths(changed)
        head_workflows = {}
        if dual_checkout and (ci_root_path / "head").is_dir():
            loaded_head_workflows = load_workflow_definitions(ci_root_path / "head")
            head_id_counts = {}
            for workflow in loaded_head_workflows:
                head_id_counts[workflow.workflow_id] = head_id_counts.get(workflow.workflow_id, 0) + 1
            head_workflows = {
                workflow.workflow_id: workflow
                for workflow in loaded_head_workflows
                if head_id_counts[workflow.workflow_id] == 1
            }
        base_ids = [workflow.workflow_id for workflow in workflow_defs]
        if len(base_ids) != len(set(base_ids)):
            raise ValueError("trusted base has duplicate workflow security ids")
        try:
            actor_subjects = set(json.loads(os.getenv("REACTORCIDE_ACTOR_SUBJECTS", "[]")))
        except (TypeError, ValueError, json.JSONDecodeError):
            actor_subjects = set()
        try:
            approvals = json.loads(os.getenv("REACTORCIDE_CI_APPROVALS", "[]"))
            if not isinstance(approvals, list):
                approvals = []
        except (TypeError, ValueError, json.JSONDecodeError):
            approvals = []

        def repo_relative(path_value):
            if not path_value:
                return ""
            resolved = Path(path_value).resolve()
            for checkout in (ci_source_path, ci_root_path / "head"):
                try:
                    return str(resolved.relative_to(checkout.resolve())).replace("\\", "/")
                except ValueError:
                    continue
            raise ValueError(f"CI path escapes prepared checkouts: {path_value}")

        batches = []
        selected_no_match = None
        authorized_paths = set()
        violations = []
        base_workflow_ids = {workflow.workflow_id for workflow in workflow_defs}
        # Shared CI paths: changed .reactorcide/ files no workflow claims as
        # its own YAML or job file — plugins, scripts, tests, helper data.
        # Plugin/script code loads globally, so a change to it is a CI change
        # for every workflow. Attributing these paths to each candidate
        # workflow lets a head-CI rule whose paths cover them authorize the
        # change (and run the workflow from head so the changed code is what
        # gets tested); a rule with narrower paths still refuses them.
        claimed_ci_paths = set()
        for workflow in list(workflow_defs) + list(head_workflows.values()):
            claimed_ci_paths.add(repo_relative(workflow.source_file))
            claimed_ci_paths.update(
                repo_relative(job.source_file) for job in workflow.jobs if job.source_file
            )
        claimed_ci_paths.discard("")
        shared_ci_paths = {path for path in changed_ci if path not in claimed_ci_paths}
        candidate_workflows = list(workflow_defs)
        if trusted_policy:
            permitted_new_ids = {
                str(workflow_id)
                for rule in trusted_policy.rules
                for workflow_id in rule.get("workflows") or []
            }
            candidate_workflows.extend(
                workflow
                for workflow_id, workflow in head_workflows.items()
                if workflow_id not in base_workflow_ids
                and workflow_id in permitted_new_ids
                and workflow.explicit_id
            )

        for wf in candidate_workflows:
            is_new_workflow = wf.workflow_id not in base_workflow_ids
            matched_wf, reason = workflow_match_reason(wf, event_type, branch, changed)
            if not matched_wf:
                log_stdout(f"  – skipped workflow '{wf.name}': {reason}")
                if selected_workflow_path is not None:
                    selected_no_match = wf
                continue
            selected_wf = wf
            decision = None
            dependency_paths = {repo_relative(wf.source_file)}
            dependency_paths.update(repo_relative(job.source_file) for job in wf.jobs if job.source_file)
            dependency_paths.discard("")
            candidate = head_workflows.get(wf.workflow_id)
            policy_dependency_paths = set(dependency_paths)
            if candidate is not None:
                policy_dependency_paths = {repo_relative(candidate.source_file)}
                policy_dependency_paths.update(repo_relative(job.source_file) for job in candidate.jobs if job.source_file)
                policy_dependency_paths.discard("")
            policy_dependency_paths |= shared_ci_paths
            affected_paths = sorted(set(changed_ci).intersection(policy_dependency_paths))
            if dual_checkout and changed_ci and trusted_policy and affected_paths:
                decision = decide_workflow(
                    trusted_policy,
                    wf.workflow_id,
                    sorted(policy_dependency_paths),
                    changed_ci,
                    event_type,
                    base_ref or pr_base_ref or branch,
                    "fork" if is_fork_pr == "true" else "same",
                    actor_subjects,
                    approvals,
                    diff_base,
                    source_ref,
                )
                if decision.allowed:
                    if candidate is None or not candidate.explicit_id:
                        decision.allowed = False
                        decision.ci_origin = "base"
                        decision.reasons = ["head workflow is missing an explicit trusted policy id"]
                    else:
                        selected_wf = candidate
                        authorized_paths.update(changed_ci)
                if not decision.allowed:
                    for path_value in affected_paths:
                        violations.append({
                            "path": path_value,
                            "workflow_id": wf.workflow_id,
                            "actor": os.getenv("REACTORCIDE_HEAD_ACTOR", ""),
                            "rule": decision.rule_id,
                            "base_sha": diff_base,
                            "head_sha": source_ref,
                        })

            if is_new_workflow and (decision is None or not decision.allowed):
                continue

            job_triggers = generate_triggers(selected_wf.jobs, ctx)
            if decision and decision.allowed:
                for trigger in job_triggers:
                    trigger.ci_source_url = head_url or source_url
                    trigger.ci_source_ref = source_ref
                    trigger.worker_class = decision.worker_class
            batches.append({
                "id": wf.workflow_id,
                "name": wf.name,
                "source_file": repo_relative(selected_wf.source_file),
                "ci_origin": decision.ci_origin if decision else "base",
                "ci_repository": (head_url or source_url) if decision and decision.allowed else ci_source_url,
                "ci_sha": source_ref if decision and decision.allowed else ci_source_ref,
                "execution_profile": decision.profile if decision else "standard",
                "worker_class": decision.worker_class if decision else "default",
                "policy_revision": trusted_policy.revision if trusted_policy else "",
                "policy_rule_id": decision.rule_id if decision else "",
                "approval_id": decision.approval_id if decision and decision.approval_id else None,
                "dependency_paths": sorted(policy_dependency_paths),
                "vars": selected_wf.vars,
                "jobs": job_triggers,
            })
            job_names = ", ".join(t.job_name for t in job_triggers) or "(no jobs)"
            log_stdout(f"  ✓ matched workflow '{wf.name}' ({reason}): {len(job_triggers)} job(s): {job_names}")

        if dual_checkout and changed_ci:
            for path_value in changed_ci:
                if path_value not in authorized_paths and not any(item["path"] == path_value for item in violations):
                    violations.append({
                        "path": path_value,
                        "actor": os.getenv("REACTORCIDE_HEAD_ACTOR", ""),
                        "base_sha": diff_base,
                        "head_sha": source_ref,
                    })
        # Drop workflows that matched but resolved to zero jobs — nothing to run.
        batches = [b for b in batches if b["jobs"]]

        # A local caller that selected one workflow still needs a result when
        # the chosen event does not match. Emit an empty workflow so run-local
        # reports a skipped workflow instead of treating a missing trigger
        # file as an evaluation failure.
        if selected_no_match is not None and not batches and not violations:
            batches.append({
                "id": selected_no_match.workflow_id,
                "name": selected_no_match.name,
                "source_file": repo_relative(selected_no_match.source_file),
                "ci_origin": "base",
                "ci_repository": ci_source_url,
                "ci_sha": ci_source_ref,
                "execution_profile": "standard",
                "worker_class": "default",
                "policy_revision": trusted_policy.revision if trusted_policy else "",
                "policy_rule_id": "",
                "approval_id": None,
                "dependency_paths": [repo_relative(selected_no_match.source_file)],
                "vars": selected_no_match.vars,
                "jobs": [],
            })

        if not batches and not violations:
            log_stdout(
                f"No workflows produced jobs for event '{event_type}'. "
                f"Evaluated {len(workflow_defs)} workflow(s); nothing to run. "
                f"This eval is successful."
            )
            raise typer.Exit(0)

        workflow_ctx.flush_workflow_batches(batches, violations, changed_ci, sorted(actor_subjects))
        total = sum(len(b["jobs"]) for b in batches)
        log_stdout(f"Wrote {len(batches)} workflow(s) / {total} trigger(s) to {triggers_file}")
        raise typer.Exit(0)

    # Back-compat: bare .reactorcide/jobs/*.yaml. These collapse to one default
    # workflow named "Reactorcide Jobs, repo: <name>" on the coordinator side.
    log_stdout(f"No workflow definitions found; loading bare job definitions from {ci_source_path}")
    definitions = load_job_definitions(ci_source_path)

    if not definitions:
        log_stdout(
            "No workflow or job definitions found (looked in .reactorcide/workflows "
            "and .reactorcide/jobs); nothing to evaluate. This eval is successful."
        )
        raise typer.Exit(0)

    log_stdout(f"Loaded {len(definitions)} job definition(s): "
               f"{', '.join(d.name for d in definitions)}")
    log_stdout(f"Evaluating for event '{event_type}'"
               + (f", branch '{branch}'" if branch else "")
               + (f", {len(changed)} changed file(s)" if changed else ""))

    # Evaluate definitions against event
    matched = evaluate_event(definitions, event_type, branch, changed)

    if not matched:
        skipped = ", ".join(d.name for d in definitions)
        log_stdout(
            f"No jobs matched event '{event_type}'. Evaluated {len(definitions)} "
            f"job(s) ({skipped}); nothing to run. This eval is successful."
        )
        raise typer.Exit(0)

    log_stdout(f"Matched {len(matched)} of {len(definitions)} job(s) for event '{event_type}': "
               f"{', '.join(d.name for d in matched)}")

    # Generate triggers
    triggers = generate_triggers(matched, ctx)

    workflow_ctx.triggers = triggers
    workflow_ctx.flush_triggers()

    for trigger in triggers:
        log_stdout(f"  Triggered: {trigger.job_name}")

    log_stdout(f"Wrote {len(triggers)} trigger(s) to {triggers_file}")


@app.command("trigger")
def trigger_cmd(
    job_files: List[str] = typer.Argument(..., help="Paths to job definition YAML files"),
    triggers_file: str = typer.Option("/job/triggers.json", help="Path to write triggers output"),
    allow_insecure_transport: bool = typer.Option(False, "--allow-insecure-transport", help="Allow API credentials without TLS on an isolated development network"),
):
    """Read job definition files and submit them as triggers.

    Reads the specified YAML job files, resolves them to inline job specs,
    and submits them as triggers via both file and API. This allows jobs to
    trigger other jobs by referencing their YAML definitions directly,
    without the coordinator needing to access the source repository.
    """
    import yaml
    from pathlib import Path
    from src.eval import parse_job_definition
    from src.workflow import WorkflowContext, JobTrigger

    if not job_files:
        log_stderr("No job files specified")
        raise typer.Exit(1)

    # Pick up source context from environment (inherited from parent job)
    source_url = os.environ.get("REACTORCIDE_SOURCE_URL", "")
    source_ref = os.environ.get("REACTORCIDE_SHA", "")
    ci_source_url = os.environ.get("REACTORCIDE_CI_SOURCE_URL", "")
    ci_source_ref = os.environ.get("REACTORCIDE_CI_SOURCE_REF", "")

    # Environment variables to pass through to child jobs
    pass_through_env_keys = [
        "REACTORCIDE_EVENT_TYPE",
        "REACTORCIDE_BRANCH",
        "REACTORCIDE_SHA",
        "REACTORCIDE_SOURCE_URL",
        "REACTORCIDE_PR_BASE_REF",
        "REACTORCIDE_PR_NUMBER",
        "REACTORCIDE_PR_REF",
        "REACTORCIDE_DIFF_BASE",
        "REACTORCIDE_CI_SOURCE_URL",
        "REACTORCIDE_CI_SOURCE_REF",
        "REACTORCIDE_HEAD_URL",
        "REACTORCIDE_HEAD_REF",
        "REACTORCIDE_BASE_URL",
        "REACTORCIDE_BASE_REF",
        "REACTORCIDE_IS_FORK_PR",
    ]

    triggers: List[JobTrigger] = []

    for job_file in job_files:
        path = Path(job_file)
        if not path.exists():
            log_stderr(f"Job file not found: {job_file}")
            raise typer.Exit(1)

        try:
            with open(path, 'r') as f:
                data = yaml.safe_load(f)
        except yaml.YAMLError as e:
            log_stderr(f"Failed to parse {job_file}: {e}")
            raise typer.Exit(1)

        if not isinstance(data, dict):
            log_stderr(f"Invalid YAML in {job_file}: not a mapping")
            raise typer.Exit(1)

        try:
            defn = parse_job_definition(data, source_file=str(path))
        except ValueError as e:
            log_stderr(f"Invalid job definition: {e}")
            raise typer.Exit(1)

        # Build environment: definition env + pass-through env vars
        env = dict(defn.environment)
        for key in pass_through_env_keys:
            val = os.environ.get(key)
            if val:
                env[key] = val

        # Handle raw_command wrapping (same logic as generate_triggers in eval.py)
        command = defn.job.command or None
        if command and not defn.job.raw_command and not command.strip().startswith("runnerlib "):
            command = f"runnerlib run --job-command '{command}'"

        trigger = JobTrigger(
            job_name=defn.name,
            depends_on=defn.job.depends_on,
            condition=defn.job.condition,
            env=env,
            container_image=defn.job.image or None,
            job_command=command,
            priority=defn.job.priority,
            timeout=defn.job.timeout,
            capabilities=defn.job.capabilities or None,
            code_dir=defn.job.code_dir or None,
            job_dir=defn.job.job_dir or None,
            working_dir=defn.job.working_dir or None,
            run_as_user=defn.job.run_as_user or None,
            worker_class=defn.job.worker_class or None,
            characteristics=defn.job.characteristics or None,
            resources=defn.job.resources or None,
            disable_run_local=True if defn.job.disable_run_local else None,
            run_local=defn.job.run_local or None,
            for_each=defn.job.for_each or None,
            item_var=defn.job.item_var or None,
            source_type="git" if source_url else None,
            source_url=source_url or None,
            source_ref=source_ref or None,
            ci_source_type="git" if ci_source_url else None,
            ci_source_url=ci_source_url or None,
            ci_source_ref=ci_source_ref or None,
        )
        triggers.append(trigger)
        log_stdout(f"Loaded trigger: {defn.name} (image: {defn.job.image})")

    if not triggers:
        log_stdout("No triggers to submit")
        raise typer.Exit(0)

    # Write triggers using WorkflowContext (handles both file and API submission)
    workflow_ctx = WorkflowContext(triggers_file=triggers_file, allow_insecure_transport=allow_insecure_transport)
    workflow_ctx.triggers = triggers
    workflow_ctx.flush_triggers()

    for trigger in triggers:
        log_stdout(f"  Triggered: {trigger.job_name}")

    log_stdout(f"Submitted {len(triggers)} trigger(s)")


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
    from src.source_prep import get_job_base_path, is_in_container_mode
    job_path = get_job_base_path()
    if is_in_container_mode():
        log_stdout(f"  Job Directory (container mode): {job_path}")
    else:
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
