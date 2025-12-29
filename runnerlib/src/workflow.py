"""
Workflow orchestration utilities for Reactorcide.

This module provides functions for:
- Triggering follow-up jobs (workflow orchestration)
- Querying git information (changed files, branch info)
- Checking job state (is another job running?)
- Getting previous job results

These utilities enable jobs to orchestrate multi-step workflows where
one job can trigger subsequent jobs based on conditions, results, or state.
"""

import json
import os
import subprocess
import sys
from pathlib import Path
from typing import Dict, List, Optional, Any, Union
from dataclasses import dataclass, asdict, field


@dataclass
class JobTrigger:
    """
    Represents a job to be triggered as part of a workflow.

    Attributes:
        job_name: Name of the job to trigger
        depends_on: List of job names this job depends on (optional)
        condition: Condition for triggering ("all_success", "any_success", "always")
        env: Environment variables to pass to the job
        source_type: Source type (git, copy, none)
        source_url: URL of source code (for git)
        source_ref: Git ref (branch, tag, commit)
        ci_source_type: CI source type (git, copy, none)
        ci_source_url: URL of trusted CI code
        ci_source_ref: Git ref for CI code
        container_image: Container image to use for job
        job_command: Command to run in the job
        priority: Job priority (higher = more important)
        timeout: Job timeout in seconds
    """
    job_name: str
    depends_on: List[str] = field(default_factory=list)
    condition: str = "all_success"
    env: Dict[str, str] = field(default_factory=dict)
    source_type: Optional[str] = None
    source_url: Optional[str] = None
    source_ref: Optional[str] = None
    ci_source_type: Optional[str] = None
    ci_source_url: Optional[str] = None
    ci_source_ref: Optional[str] = None
    container_image: Optional[str] = None
    job_command: Optional[str] = None
    priority: Optional[int] = None
    timeout: Optional[int] = None

    def to_dict(self) -> Dict[str, Any]:
        """Convert to dictionary, excluding None values."""
        return {k: v for k, v in asdict(self).items() if v is not None}


class WorkflowContext:
    """
    Context for workflow execution, providing access to environment,
    job state, and trigger mechanisms.
    """

    def __init__(self, triggers_file: str = "/job/triggers.json"):
        self.triggers_file = Path(triggers_file)
        self.triggers: List[JobTrigger] = []
        self._coordinator_url = os.getenv("REACTORCIDE_COORDINATOR_URL")
        self._api_token = os.getenv("REACTORCIDE_API_TOKEN")
        self._job_id = os.getenv("REACTORCIDE_JOB_ID")

    @property
    def job_id(self) -> Optional[str]:
        """Get current job ID from environment."""
        return self._job_id

    @property
    def branch(self) -> Optional[str]:
        """Get current branch from environment."""
        return os.getenv("REACTORCIDE_GIT_BRANCH")

    @property
    def commit(self) -> Optional[str]:
        """Get current commit SHA from environment."""
        return os.getenv("REACTORCIDE_GIT_COMMIT")

    @property
    def ref(self) -> Optional[str]:
        """Get current git ref from environment."""
        return os.getenv("REACTORCIDE_GIT_REF")

    def trigger_job(
        self,
        job_name: str,
        env: Optional[Dict[str, str]] = None,
        depends_on: Optional[List[str]] = None,
        condition: str = "all_success",
        **kwargs
    ) -> None:
        """
        Trigger a follow-up job.

        Args:
            job_name: Name of the job to trigger
            env: Environment variables to pass to the job
            depends_on: List of job names this job depends on
            condition: Condition for triggering ("all_success", "any_success", "always")
            **kwargs: Additional job parameters (source_url, source_ref, container_image, etc.)

        Example:
            ctx.trigger_job("deploy", env={"TARGET": "staging"}, depends_on=["test", "build"])
        """
        trigger = JobTrigger(
            job_name=job_name,
            env=env or {},
            depends_on=depends_on or [],
            condition=condition,
            **kwargs
        )
        self.triggers.append(trigger)
        print(f"✓ Scheduled job: {job_name}", file=sys.stderr)

    def submit_job(
        self,
        job_name: str,
        env: Optional[Dict[str, str]] = None,
        **kwargs
    ) -> None:
        """
        Alias for trigger_job() for backward compatibility.

        Args:
            job_name: Name of the job to submit
            env: Environment variables to pass to the job
            **kwargs: Additional job parameters
        """
        self.trigger_job(job_name, env=env, **kwargs)

    def flush_triggers(self) -> None:
        """
        Write accumulated triggers to the triggers file.

        This is called automatically at the end of job execution,
        but can be called manually if needed.
        """
        if not self.triggers:
            return

        # Create the triggers file directory if it doesn't exist
        self.triggers_file.parent.mkdir(parents=True, exist_ok=True)

        # Load existing triggers if file exists
        existing_triggers = []
        if self.triggers_file.exists():
            try:
                with open(self.triggers_file, 'r') as f:
                    data = json.load(f)
                    existing_triggers = data.get("jobs", [])
            except (json.JSONDecodeError, KeyError):
                pass

        # Append new triggers
        all_triggers = existing_triggers + [t.to_dict() for t in self.triggers]

        # Write to file
        trigger_data = {
            "type": "trigger_job",
            "jobs": all_triggers
        }

        with open(self.triggers_file, 'w') as f:
            json.dump(trigger_data, f, indent=2)

        print(f"✓ Wrote {len(self.triggers)} job trigger(s) to {self.triggers_file}", file=sys.stderr)

    def is_job_running(self, job_name: str) -> bool:
        """
        Check if a job is currently running.

        Args:
            job_name: Name of the job to check

        Returns:
            True if the job is running, False otherwise

        Note:
            This requires API access to the Coordinator.
            Returns False if API is not configured.
        """
        if not self._coordinator_url or not self._api_token:
            print("⚠ Warning: Cannot check job state - API not configured", file=sys.stderr)
            return False

        # TODO: Implement API call to check job state
        print(f"⚠ Warning: is_job_running() not yet implemented for job: {job_name}", file=sys.stderr)
        return False

    def get_job_result(self, job_name: str) -> Optional[Dict[str, Any]]:
        """
        Get the result of a previous job.

        Args:
            job_name: Name of the job to query

        Returns:
            Dictionary with job result information (exit_code, status, logs_url, etc.)
            or None if job not found or API not configured

        Note:
            This requires API access to the Coordinator.
            Returns None if API is not configured.
        """
        if not self._coordinator_url or not self._api_token:
            print("⚠ Warning: Cannot get job result - API not configured", file=sys.stderr)
            return None

        # TODO: Implement API call to get job result
        print(f"⚠ Warning: get_job_result() not yet implemented for job: {job_name}", file=sys.stderr)
        return None

    def log_next_job(self, job_name: str, reason: str = "") -> None:
        """
        Log what job should run next (for local execution mode).

        Args:
            job_name: Name of the next job to run
            reason: Optional reason/description

        This is useful when running locally to show what would happen next
        in the workflow without actually triggering it.
        """
        msg = f"📋 Next job to run: {job_name}"
        if reason:
            msg += f" (reason: {reason})"
        print(msg, file=sys.stderr)


# Module-level convenience functions for simpler API

# Global context instance
_global_context: Optional[WorkflowContext] = None


def _get_context() -> WorkflowContext:
    """Get or create the global workflow context."""
    global _global_context
    if _global_context is None:
        _global_context = WorkflowContext()
    return _global_context


def trigger_job(
    job_name: str,
    env: Optional[Dict[str, str]] = None,
    depends_on: Optional[List[str]] = None,
    condition: str = "all_success",
    **kwargs
) -> None:
    """
    Trigger a follow-up job.

    Args:
        job_name: Name of the job to trigger
        env: Environment variables to pass to the job
        depends_on: List of job names this job depends on
        condition: Condition for triggering ("all_success", "any_success", "always")
        **kwargs: Additional job parameters (source_url, source_ref, container_image, etc.)

    Example:
        trigger_job("deploy", env={"TARGET": "staging"}, depends_on=["test"])
    """
    ctx = _get_context()
    ctx.trigger_job(job_name, env, depends_on, condition, **kwargs)


def submit_job(job_name: str, env: Optional[Dict[str, str]] = None, **kwargs) -> None:
    """
    Alias for trigger_job() for backward compatibility.

    Args:
        job_name: Name of the job to submit
        env: Environment variables to pass to the job
        **kwargs: Additional job parameters
    """
    trigger_job(job_name, env, **kwargs)


def flush_triggers() -> None:
    """
    Write accumulated triggers to the triggers file.

    This should be called at the end of your job script to ensure
    all triggered jobs are recorded.
    """
    ctx = _get_context()
    ctx.flush_triggers()


def is_job_running(job_name: str) -> bool:
    """
    Check if a job is currently running.

    Args:
        job_name: Name of the job to check

    Returns:
        True if the job is running, False otherwise
    """
    ctx = _get_context()
    return ctx.is_job_running(job_name)


def get_job_result(job_name: str) -> Optional[Dict[str, Any]]:
    """
    Get the result of a previous job.

    Args:
        job_name: Name of the job to query

    Returns:
        Dictionary with job result information or None if not found
    """
    ctx = _get_context()
    return ctx.get_job_result(job_name)


def log_next_job(job_name: str, reason: str = "") -> None:
    """
    Log what job should run next (for local execution mode).

    Args:
        job_name: Name of the next job to run
        reason: Optional reason/description
    """
    ctx = _get_context()
    ctx.log_next_job(job_name, reason)


# Git utility functions

def changed_files(from_ref: str = "HEAD^", to_ref: str = "HEAD", base_dir: str = "/job/src") -> List[str]:
    """
    Get list of changed files between two git refs.

    Args:
        from_ref: Starting git ref (default: HEAD^)
        to_ref: Ending git ref (default: HEAD)
        base_dir: Base directory of git repository (default: /job/src)

    Returns:
        List of file paths that changed between the refs

    Example:
        # Get files changed in the last commit
        files = changed_files()

        # Get files changed in a PR
        files = changed_files("origin/main", "HEAD")
    """
    try:
        result = subprocess.run(
            ["git", "diff", "--name-only", from_ref, to_ref],
            cwd=base_dir,
            capture_output=True,
            text=True,
            check=True
        )
        files = result.stdout.strip().split("\n")
        return [f for f in files if f]  # Filter empty strings
    except subprocess.CalledProcessError as e:
        print(f"⚠ Error getting changed files: {e}", file=sys.stderr)
        return []


def git_info(base_dir: str = "/job/src") -> Dict[str, Optional[str]]:
    """
    Get git repository information.

    Args:
        base_dir: Base directory of git repository (default: /job/src)

    Returns:
        Dictionary with git information:
        - branch: Current branch name
        - commit: Current commit SHA
        - short_commit: Short commit SHA (7 chars)
        - tag: Current tag (if on a tag)
        - remote_url: Remote origin URL

    Example:
        info = git_info()
        print(f"Building commit {info['commit']} on branch {info['branch']}")
    """
    info: Dict[str, Optional[str]] = {
        "branch": None,
        "commit": None,
        "short_commit": None,
        "tag": None,
        "remote_url": None,
    }

    try:
        # Get current branch
        result = subprocess.run(
            ["git", "rev-parse", "--abbrev-ref", "HEAD"],
            cwd=base_dir,
            capture_output=True,
            text=True,
            check=True
        )
        info["branch"] = result.stdout.strip()
    except subprocess.CalledProcessError:
        pass

    try:
        # Get current commit
        result = subprocess.run(
            ["git", "rev-parse", "HEAD"],
            cwd=base_dir,
            capture_output=True,
            text=True,
            check=True
        )
        commit = result.stdout.strip()
        info["commit"] = commit
        info["short_commit"] = commit[:7] if commit else None
    except subprocess.CalledProcessError:
        pass

    try:
        # Get current tag (if any)
        result = subprocess.run(
            ["git", "describe", "--exact-match", "--tags"],
            cwd=base_dir,
            capture_output=True,
            text=True,
            check=True
        )
        info["tag"] = result.stdout.strip()
    except subprocess.CalledProcessError:
        pass

    try:
        # Get remote URL
        result = subprocess.run(
            ["git", "config", "--get", "remote.origin.url"],
            cwd=base_dir,
            capture_output=True,
            text=True,
            check=True
        )
        info["remote_url"] = result.stdout.strip()
    except subprocess.CalledProcessError:
        pass

    return info


# Context manager for automatic trigger flushing

class workflow_context:
    """
    Context manager that automatically flushes triggers on exit.

    Example:
        with workflow_context() as ctx:
            ctx.trigger_job("deploy", env={"TARGET": "staging"})
            # Triggers are automatically flushed on exit
    """

    def __init__(self, triggers_file: str = "/job/triggers.json"):
        self.ctx = WorkflowContext(triggers_file)

    def __enter__(self) -> WorkflowContext:
        return self.ctx

    def __exit__(self, exc_type, exc_val, exc_tb):
        if exc_type is None:  # Only flush if no exception
            self.ctx.flush_triggers()
        return False  # Don't suppress exceptions
