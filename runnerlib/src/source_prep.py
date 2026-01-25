"""Source preparation utilities for runnerlib."""

import os
import shutil
from pathlib import Path
from typing import Optional
from git import Repo
from src.logging import log_stdout, log_stderr, logger
from src.config import RunnerConfig, get_config


def is_in_container_mode() -> bool:
    """Check if runnerlib is running inside a container.

    In container mode, the /job directory is already mounted and we should
    use it directly instead of creating ./job on the host.

    Returns:
        True if running in container mode
    """
    # Check explicit environment variable
    if os.getenv("REACTORCIDE_IN_CONTAINER", "").lower() in ("true", "1", "yes"):
        return True

    # Auto-detect: if /job exists and we're not at the root filesystem,
    # we're likely inside a container
    job_path = Path("/job")
    if job_path.exists() and job_path.is_dir():
        # Additional check: if cwd starts with /job, we're definitely in container
        cwd = Path.cwd()
        try:
            cwd.relative_to(job_path)
            return True
        except ValueError:
            pass
        # If /job exists and is writable, assume container mode
        if os.access(job_path, os.W_OK):
            return True

    return False


def get_job_base_path() -> Path:
    """Get the base job path, accounting for container mode.

    Returns:
        Path object for the job base directory
    """
    if is_in_container_mode():
        return Path("/job")
    return Path("./job").resolve()


def prepare_job_directory(config: Optional[RunnerConfig] = None) -> Path:
    """Prepare the job directory structure.

    Args:
        config: Runner configuration (if None, will get default config)

    Returns:
        Path to the job directory
    """
    # Get base job path (handles container mode vs host mode)
    job_path = get_job_base_path()

    if config is None:
        # Get minimal config for directory preparation
        try:
            config = get_config(job_command="dummy")  # Dummy command to satisfy validation
        except ValueError:
            # If we can't get config, fall back to basic structure
            src_path = job_path / "src"
            job_path.mkdir(parents=True, exist_ok=True)
            src_path.mkdir(parents=True, exist_ok=True)
            return job_path
    
    # Create job directory structure
    job_path.mkdir(parents=True, exist_ok=True)
    logger.debug("Created job directory", fields={"path": str(job_path)})
    
    # Create code directory (relative to container mount point for validation)
    # Remove /job prefix if present since we're working in host context
    code_dir = config.code_dir
    if code_dir.startswith('/job/'):
        code_dir = code_dir[5:]  # Remove '/job/' prefix
    elif code_dir.startswith('/job'):
        code_dir = code_dir[4:]  # Remove '/job' prefix
    
    code_path = job_path / code_dir if code_dir else job_path / "src"
    code_path.mkdir(parents=True, exist_ok=True)
    logger.debug("Created code directory", fields={"path": str(code_path)})
    
    # Create job directory if different from code directory
    if config.job_dir != config.code_dir:
        job_dir = config.job_dir
        if job_dir.startswith('/job/'):
            job_dir = job_dir[5:]
        elif job_dir.startswith('/job'):
            job_dir = job_dir[4:]
        
        job_dir_path = job_path / job_dir if job_dir else job_path
        job_dir_path.mkdir(parents=True, exist_ok=True)
    
    return job_path


def checkout_git_repo(
    git_url: str,
    git_ref: Optional[str] = None,
    config: Optional[RunnerConfig] = None
) -> Path:
    """Checkout a git repository to the configured code directory.
    
    Args:
        git_url: Git repository URL
        git_ref: Git reference to checkout (branch, tag, or commit hash)
        config: Runner configuration (if None, will get default config)
        
    Returns:
        Path to the source directory
        
    Raises:
        Exception: If git operations fail
    """
    if config is None:
        config = get_config(job_command="dummy")  # Dummy command for directory setup
    
    job_path = prepare_job_directory(config)
    
    # Get the code directory path (remove /job prefix for host context)
    code_dir = config.code_dir
    if code_dir.startswith('/job/'):
        code_dir = code_dir[5:]
    elif code_dir.startswith('/job'):
        code_dir = code_dir[4:]
    
    src_path = job_path / code_dir if code_dir else job_path / "src"
    
    # Remove existing source if it exists
    if src_path.exists():
        shutil.rmtree(src_path)
    
    logger.info("Cloning git repository", fields={"url": git_url, "ref": git_ref or "default"})
    log_stdout(f"Cloning repository: {git_url}")

    try:
        # Clone the repository
        repo = Repo.clone_from(git_url, src_path)

        # Checkout specific ref if provided
        if git_ref:
            logger.debug("Checking out git ref", fields={"ref": git_ref})
            log_stdout(f"Checking out ref: {git_ref}")
            repo.git.checkout(git_ref)

        logger.info("Repository cloned successfully", fields={"path": str(src_path)})
        log_stdout(f"Repository checked out to: {src_path}")
        return src_path

    except Exception as e:
        logger.error("Failed to clone repository", error=e, fields={"url": git_url})
        log_stderr(f"Failed to checkout repository: {e}")
        raise


def copy_directory(
    source_dir: str,
    config: Optional[RunnerConfig] = None
) -> Path:
    """Copy a directory to the configured code directory.
    
    Args:
        source_dir: Source directory to copy
        config: Runner configuration (if None, will get default config)
        
    Returns:
        Path to the destination source directory
        
    Raises:
        FileNotFoundError: If source directory doesn't exist
        Exception: If copy operation fails
    """
    source_path = Path(source_dir).resolve()
    
    if not source_path.exists():
        raise FileNotFoundError(f"Source directory does not exist: {source_path}")
    
    if not source_path.is_dir():
        raise ValueError(f"Source path is not a directory: {source_path}")
    
    if config is None:
        config = get_config(job_command="dummy")  # Dummy command for directory setup
    
    job_path = prepare_job_directory(config)
    
    # Get the code directory path (remove /job prefix for host context)
    code_dir = config.code_dir
    if code_dir.startswith('/job/'):
        code_dir = code_dir[5:]
    elif code_dir.startswith('/job'):
        code_dir = code_dir[4:]
    
    src_path = job_path / code_dir if code_dir else job_path / "src"
    
    # Remove existing source if it exists
    if src_path.exists():
        shutil.rmtree(src_path)
    
    logger.info("Copying directory", fields={"source": str(source_path), "destination": str(src_path)})
    log_stdout(f"Copying directory: {source_path} -> {src_path}")

    try:
        # Copy the directory tree
        shutil.copytree(source_path, src_path)
        logger.info("Directory copied successfully", fields={"path": str(src_path)})
        log_stdout(f"Directory copied to: {src_path}")
        return src_path

    except Exception as e:
        logger.error("Failed to copy directory", error=e, fields={"source": str(source_path)})
        log_stderr(f"Failed to copy directory: {e}")
        raise


def cleanup_job_directory(config: Optional[RunnerConfig] = None) -> None:
    """Clean up the job directory.

    Args:
        config: Runner configuration (if None, will clean up default job directory)
    """
    # Get job path based on mode (container vs host)
    job_path = get_job_base_path()

    # In container mode, don't clean up /job as it's a mount point
    if is_in_container_mode():
        logger.debug("Skipping cleanup in container mode", fields={"path": str(job_path)})
        return

    if job_path.exists():
        logger.info("Cleaning up job directory", fields={"path": str(job_path)})
        log_stdout(f"Cleaning up job directory: {job_path}")
        shutil.rmtree(job_path)
    else:
        logger.debug("Job directory does not exist", fields={"path": str(job_path)})
        log_stdout(f"Job directory does not exist: {job_path}")


def get_code_directory_path(config: RunnerConfig) -> Path:
    """Get the path for the code directory.

    Args:
        config: Runner configuration

    Returns:
        Path to the code directory
    """
    job_path = get_job_base_path()

    # In container mode, code_dir is already an absolute path
    if is_in_container_mode() and config.code_dir.startswith('/'):
        return Path(config.code_dir)

    # Convert container path to host path
    code_dir = config.code_dir
    if code_dir.startswith('/job/'):
        code_dir = code_dir[5:]
    elif code_dir.startswith('/job'):
        code_dir = code_dir[4:]

    return job_path / code_dir if code_dir else job_path / "src"


def get_job_directory_path(config: RunnerConfig) -> Path:
    """Get the path for the job directory.

    Args:
        config: Runner configuration

    Returns:
        Path to the job directory
    """
    job_path = get_job_base_path()

    # In container mode, job_dir is already an absolute path
    if is_in_container_mode() and config.job_dir.startswith('/'):
        return Path(config.job_dir)

    # Convert container path to host path
    job_dir = config.job_dir
    if job_dir.startswith('/job/'):
        job_dir = job_dir[5:]
    elif job_dir.startswith('/job'):
        job_dir = job_dir[4:]

    return job_path / job_dir if job_dir else job_path


def _prepare_git_source(source_url: str, source_ref: Optional[str], target_path: Path) -> Path:
    """Prepare source code from a git repository.

    Args:
        source_url: Git repository URL
        source_ref: Git reference (branch, tag, commit)
        target_path: Where to clone the repository

    Returns:
        Path to the cloned repository
    """
    logger.info("Preparing git source", fields={"url": source_url, "ref": source_ref or "default", "target": str(target_path)})
    log_stdout(f"Cloning git repository: {source_url}")

    # Remove existing source if it exists
    if target_path.exists():
        shutil.rmtree(target_path)

    try:
        # Clone the repository
        repo = Repo.clone_from(source_url, target_path)

        # Checkout specific ref if provided
        if source_ref:
            logger.debug("Checking out git ref", fields={"ref": source_ref})
            log_stdout(f"Checking out ref: {source_ref}")
            repo.git.checkout(source_ref)

        logger.info("Git source prepared successfully", fields={"path": str(target_path)})
        log_stdout(f"Repository checked out to: {target_path}")
        return target_path

    except Exception as e:
        logger.error("Failed to prepare git source", error=e, fields={"url": source_url})
        log_stderr(f"Failed to checkout repository: {e}")
        raise


def _prepare_copy_source(source_url: str, target_path: Path) -> Path:
    """Prepare source code by copying from a local directory.

    Args:
        source_url: Path to source directory
        target_path: Where to copy the directory

    Returns:
        Path to the copied directory
    """
    source_path = Path(source_url).resolve()

    if not source_path.exists():
        raise FileNotFoundError(f"Source directory does not exist: {source_path}")

    if not source_path.is_dir():
        raise ValueError(f"Source path is not a directory: {source_path}")

    logger.info("Preparing copy source", fields={"source": str(source_path), "target": str(target_path)})
    log_stdout(f"Copying directory: {source_path} -> {target_path}")

    # Remove existing source if it exists
    if target_path.exists():
        shutil.rmtree(target_path)

    try:
        # Copy the directory tree
        shutil.copytree(source_path, target_path)
        logger.info("Copy source prepared successfully", fields={"path": str(target_path)})
        log_stdout(f"Directory copied to: {target_path}")
        return target_path

    except Exception as e:
        logger.error("Failed to prepare copy source", error=e, fields={"source": str(source_path)})
        log_stderr(f"Failed to copy directory: {e}")
        raise


def _prepare_tarball_source(source_url: str, source_ref: Optional[str], target_path: Path) -> Path:
    """Prepare source code from a tarball (STUB - not yet implemented).

    Args:
        source_url: URL or path to tarball
        source_ref: Version or tag (optional)
        target_path: Where to extract the tarball

    Returns:
        Path to the extracted directory

    Raises:
        NotImplementedError: Tarball support is not yet implemented
    """
    logger.info("Tarball source preparation requested", fields={"url": source_url})
    log_stderr("⚠️  Tarball source preparation is not yet implemented")
    raise NotImplementedError(
        f"Tarball source preparation is not yet implemented. "
        f"URL: {source_url}, ref: {source_ref}, target: {target_path}"
    )


def _prepare_hg_source(source_url: str, source_ref: Optional[str], target_path: Path) -> Path:
    """Prepare source code from a Mercurial repository (STUB - not yet implemented).

    Args:
        source_url: Mercurial repository URL
        source_ref: Mercurial changeset/bookmark/tag
        target_path: Where to clone the repository

    Returns:
        Path to the cloned repository

    Raises:
        NotImplementedError: Mercurial support is not yet implemented
    """
    logger.info("Mercurial source preparation requested", fields={"url": source_url})
    log_stderr("⚠️  Mercurial (hg) source preparation is not yet implemented")
    raise NotImplementedError(
        f"Mercurial source preparation is not yet implemented. "
        f"URL: {source_url}, ref: {source_ref}, target: {target_path}"
    )


def _prepare_svn_source(source_url: str, source_ref: Optional[str], target_path: Path) -> Path:
    """Prepare source code from a Subversion repository (STUB - not yet implemented).

    Args:
        source_url: SVN repository URL
        source_ref: SVN revision number
        target_path: Where to checkout the repository

    Returns:
        Path to the checked out repository

    Raises:
        NotImplementedError: Subversion support is not yet implemented
    """
    logger.info("Subversion source preparation requested", fields={"url": source_url})
    log_stderr("⚠️  Subversion (svn) source preparation is not yet implemented")
    raise NotImplementedError(
        f"Subversion source preparation is not yet implemented. "
        f"URL: {source_url}, ref: {source_ref}, target: {target_path}"
    )


def prepare_source(config: RunnerConfig) -> Optional[Path]:
    """Prepare source code based on configuration.

    This is the main entry point for source preparation. It dispatches to the appropriate
    strategy based on the source_type in the configuration.

    Args:
        config: Runner configuration with source settings

    Returns:
        Path to the prepared source directory, or None if no source preparation needed

    Raises:
        ValueError: If source_type is invalid
        NotImplementedError: If source_type is not yet supported (tarball, hg, svn)
    """
    # If no source type specified, assume no source preparation needed
    if not config.source_type or config.source_type == 'none':
        logger.debug("No source preparation configured (source_type=none or not set)")
        log_stdout("ℹ️  No source preparation configured - using pre-mounted source or no source")
        return None

    # Ensure we have a job directory
    job_path = prepare_job_directory(config)

    # Determine target path for source code
    # Source code goes in /job/src/ by default
    target_path = job_path / "src"

    logger.info("Preparing source", fields={
        "type": config.source_type,
        "url": config.source_url or "none",
        "ref": config.source_ref or "default"
    })

    # Dispatch based on source type
    if config.source_type == 'git':
        if not config.source_url:
            raise ValueError("source_url is required when source_type='git'")
        return _prepare_git_source(config.source_url, config.source_ref, target_path)

    elif config.source_type == 'copy':
        if not config.source_url:
            raise ValueError("source_url is required when source_type='copy'")
        return _prepare_copy_source(config.source_url, target_path)

    elif config.source_type == 'tarball':
        if not config.source_url:
            raise ValueError("source_url is required when source_type='tarball'")
        return _prepare_tarball_source(config.source_url, config.source_ref, target_path)

    elif config.source_type == 'hg':
        if not config.source_url:
            raise ValueError("source_url is required when source_type='hg'")
        return _prepare_hg_source(config.source_url, config.source_ref, target_path)

    elif config.source_type == 'svn':
        if not config.source_url:
            raise ValueError("source_url is required when source_type='svn'")
        return _prepare_svn_source(config.source_url, config.source_ref, target_path)

    else:
        raise ValueError(
            f"Invalid source_type: {config.source_type}. "
            f"Supported types: git, copy, tarball, hg, svn, none"
        )


def prepare_ci_source(config: RunnerConfig) -> Optional[Path]:
    """Prepare CI source code (trusted scripts) based on configuration.

    CI source code is kept separate from regular source code for security. This allows
    running CI/CD scripts from a trusted repository while testing code from untrusted
    sources (e.g., pull requests from external contributors).

    Args:
        config: Runner configuration with ci_source settings

    Returns:
        Path to the prepared CI source directory, or None if no CI source preparation needed

    Raises:
        ValueError: If ci_source_type is invalid
        NotImplementedError: If ci_source_type is not yet supported (tarball, hg, svn)
    """
    # If no CI source type specified, assume no CI source preparation needed
    if not config.ci_source_type or config.ci_source_type == 'none':
        logger.debug("No CI source preparation configured (ci_source_type=none or not set)")
        return None

    # Ensure we have a job directory
    job_path = prepare_job_directory(config)

    # Determine target path for CI source code
    # CI source code goes in /job/ci/ (separate from regular source)
    target_path = job_path / "ci"

    logger.info("Preparing CI source", fields={
        "type": config.ci_source_type,
        "url": config.ci_source_url or "none",
        "ref": config.ci_source_ref or "default"
    })

    log_stdout(f"🔐 Preparing trusted CI source (type: {config.ci_source_type})")

    # Dispatch based on CI source type
    if config.ci_source_type == 'git':
        if not config.ci_source_url:
            raise ValueError("ci_source_url is required when ci_source_type='git'")
        return _prepare_git_source(config.ci_source_url, config.ci_source_ref, target_path)

    elif config.ci_source_type == 'copy':
        if not config.ci_source_url:
            raise ValueError("ci_source_url is required when ci_source_type='copy'")
        return _prepare_copy_source(config.ci_source_url, target_path)

    elif config.ci_source_type == 'tarball':
        if not config.ci_source_url:
            raise ValueError("ci_source_url is required when ci_source_type='tarball'")
        return _prepare_tarball_source(config.ci_source_url, config.ci_source_ref, target_path)

    elif config.ci_source_type == 'hg':
        if not config.ci_source_url:
            raise ValueError("ci_source_url is required when ci_source_type='hg'")
        return _prepare_hg_source(config.ci_source_url, config.ci_source_ref, target_path)

    elif config.ci_source_type == 'svn':
        if not config.ci_source_url:
            raise ValueError("ci_source_url is required when ci_source_type='svn'")
        return _prepare_svn_source(config.ci_source_url, config.ci_source_ref, target_path)

    else:
        raise ValueError(
            f"Invalid ci_source_type: {config.ci_source_type}. "
            f"Supported types: git, copy, tarball, hg, svn, none"
        )