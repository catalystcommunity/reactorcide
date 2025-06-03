"""Git operations for runnerlib."""

import os
from pathlib import Path
from git import Repo, InvalidGitRepositoryError, GitCommandError
from typing import List


def get_files_changed(gitref: str, repo_path: str = "/job/source") -> List[str]:
    """Get list of files changed from the given git reference.
    
    Args:
        gitref: Git reference to compare against (e.g., 'HEAD~1', 'main', commit hash)
        repo_path: Path to the git repository (default: /job/source)
        
    Returns:
        List of relative file paths that have changed
        
    Raises:
        FileNotFoundError: If repository path doesn't exist
        InvalidGitRepositoryError: If path is not a git repository
        GitCommandError: If git reference is invalid or git operation fails
        ValueError: If gitref is empty or invalid format
    """
    if not gitref or not gitref.strip():
        raise ValueError("Git reference cannot be empty")
    
    gitref = gitref.strip()
    
    if not os.path.exists(repo_path):
        raise FileNotFoundError(f"Repository path does not exist: {repo_path}")
    
    try:
        repo = Repo(repo_path)
    except InvalidGitRepositoryError:
        raise InvalidGitRepositoryError(f"Path is not a git repository: {repo_path}")
    
    try:
        # Verify that the git reference exists
        try:
            repo.commit(gitref)
        except Exception:
            raise GitCommandError(f"Git reference '{gitref}' not found in repository")
        
        # Get the diff between the reference and current HEAD
        diff = repo.git.diff("--name-only", gitref, "HEAD")
        
        if not diff:
            return []
        
        # Split by newlines and filter out empty strings
        changed_files = [f for f in diff.split('\n') if f.strip()]
        
        return changed_files
        
    except GitCommandError as e:
        # Re-raise with more context
        raise GitCommandError(f"Git operation failed: {e}")


def validate_git_repository(repo_path: str) -> tuple[bool, str]:
    """Validate that a path contains a valid git repository.
    
    Args:
        repo_path: Path to check
        
    Returns:
        Tuple of (is_valid, error_message)
    """
    if not os.path.exists(repo_path):
        return False, f"Path does not exist: {repo_path}"
    
    if not os.path.isdir(repo_path):
        return False, f"Path is not a directory: {repo_path}"
    
    try:
        repo = Repo(repo_path)
        # Try to access the HEAD to ensure repo is valid
        _ = repo.head.commit
        return True, "Valid git repository"
    except InvalidGitRepositoryError:
        return False, f"Not a git repository: {repo_path}"
    except Exception as e:
        return False, f"Git repository error: {e}"


def get_repository_info(repo_path: str) -> dict:
    """Get information about a git repository.
    
    Args:
        repo_path: Path to the git repository
        
    Returns:
        Dictionary with repository information
    """
    info = {
        "is_valid": False,
        "current_branch": None,
        "current_commit": None,
        "is_dirty": None,
        "remotes": [],
        "error": None
    }
    
    try:
        repo = Repo(repo_path)
        info["is_valid"] = True
        
        try:
            info["current_branch"] = repo.active_branch.name
        except:
            info["current_branch"] = "detached HEAD"
        
        info["current_commit"] = str(repo.head.commit)[:8]
        info["is_dirty"] = repo.is_dirty()
        info["remotes"] = [remote.name for remote in repo.remotes]
        
    except Exception as e:
        info["error"] = str(e)
    
    return info