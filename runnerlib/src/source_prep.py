"""Source preparation utilities for runnerlib."""

import os
import shutil
import subprocess
from pathlib import Path
from typing import Optional
from git import Repo
from src.logging import log_stdout, log_stderr
from src.config import RunnerConfig, get_config


def prepare_job_directory(config: Optional[RunnerConfig] = None) -> Path:
    """Prepare the job directory structure.
    
    Args:
        config: Runner configuration (if None, will get default config)
        
    Returns:
        Path to the job directory
    """
    if config is None:
        # Get minimal config for directory preparation
        try:
            config = get_config(job_command="dummy")  # Dummy command to satisfy validation
        except ValueError:
            # If we can't get config, fall back to basic structure
            job_path = Path("./job").resolve()
            src_path = job_path / "src"
            job_path.mkdir(parents=True, exist_ok=True)
            src_path.mkdir(parents=True, exist_ok=True)
            return job_path
    
    job_path = Path("./job").resolve()
    
    # Create job directory structure
    job_path.mkdir(parents=True, exist_ok=True)
    
    # Create code directory (relative to container mount point for validation)
    # Remove /job prefix if present since we're working in host context
    code_dir = config.code_dir
    if code_dir.startswith('/job/'):
        code_dir = code_dir[5:]  # Remove '/job/' prefix
    elif code_dir.startswith('/job'):
        code_dir = code_dir[4:]  # Remove '/job' prefix
    
    code_path = job_path / code_dir if code_dir else job_path / "src"
    code_path.mkdir(parents=True, exist_ok=True)
    
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
    
    log_stdout(f"Cloning repository: {git_url}")
    
    try:
        # Clone the repository
        repo = Repo.clone_from(git_url, src_path)
        
        # Checkout specific ref if provided
        if git_ref:
            log_stdout(f"Checking out ref: {git_ref}")
            repo.git.checkout(git_ref)
        
        log_stdout(f"Repository checked out to: {src_path}")
        return src_path
        
    except Exception as e:
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
    
    log_stdout(f"Copying directory: {source_path} -> {src_path}")
    
    try:
        # Copy the directory tree
        shutil.copytree(source_path, src_path)
        log_stdout(f"Directory copied to: {src_path}")
        return src_path
        
    except Exception as e:
        log_stderr(f"Failed to copy directory: {e}")
        raise


def cleanup_job_directory(config: Optional[RunnerConfig] = None) -> None:
    """Clean up the job directory.
    
    Args:
        config: Runner configuration (if None, will clean up default ./job directory)
    """
    # Always clean up the fixed ./job directory structure
    job_path = Path("./job")
    
    if job_path.exists():
        log_stdout(f"Cleaning up job directory: {job_path}")
        shutil.rmtree(job_path)
    else:
        log_stdout(f"Job directory does not exist: {job_path}")


def get_code_directory_path(config: RunnerConfig) -> Path:
    """Get the host path for the code directory.
    
    Args:
        config: Runner configuration
        
    Returns:
        Path to the code directory on the host
    """
    job_path = Path("./job").resolve()
    
    # Convert container path to host path
    code_dir = config.code_dir
    if code_dir.startswith('/job/'):
        code_dir = code_dir[5:]
    elif code_dir.startswith('/job'):
        code_dir = code_dir[4:]
    
    return job_path / code_dir if code_dir else job_path / "src"


def get_job_directory_path(config: RunnerConfig) -> Path:
    """Get the host path for the job directory.
    
    Args:
        config: Runner configuration
        
    Returns:
        Path to the job directory on the host
    """
    job_path = Path("./job").resolve()
    
    # Convert container path to host path
    job_dir = config.job_dir
    if job_dir.startswith('/job/'):
        job_dir = job_dir[5:]
    elif job_dir.startswith('/job'):
        job_dir = job_dir[4:]
    
    return job_path / job_dir if job_dir else job_path