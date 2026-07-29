"""Runnerlib lifecycle jobs for the Reactorcide pull-request workflow."""

from __future__ import annotations

import os
import re
import shlex
import subprocess
from pathlib import Path
from typing import Callable, Dict, List, Optional

from src.logging import log_stdout
from src.plugins import Plugin, PluginContext, PluginPhase


CONVENTIONAL_COMMIT_PATTERN = re.compile(
    r"^(feat|fix|docs|style|refactor|perf|test|build|ci|chore|revert)"
    r"(\(.+\))?!?: .+"
)


def _run(
    command: List[str],
    *,
    cwd: Path,
    capture_output: bool = False,
    env: Optional[Dict[str, str]] = None,
) -> subprocess.CompletedProcess:
    """Run one CI command without a command shell."""
    log_stdout(f"Running: {shlex.join(command)}")
    return subprocess.run(
        command,
        cwd=cwd,
        check=True,
        text=True,
        capture_output=capture_output,
        env=env,
    )


def _go_environment() -> Dict[str, str]:
    """Return Go cache paths that the configured job user can write."""
    environment = os.environ.copy()
    home = Path(environment.get("HOME", "/home/runner"))
    environment["GOPATH"] = str(home / ".cache" / "go")
    environment["GOCACHE"] = str(home / ".cache" / "go-build")
    return environment


def _git_ref_exists(code_dir: Path, ref: str) -> bool:
    result = subprocess.run(
        ["git", "rev-parse", "--verify", "--quiet", f"{ref}^{{commit}}"],
        cwd=code_dir,
        check=False,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
    )
    return result.returncode == 0


def _merge_base(code_dir: Path, ref: str) -> Optional[str]:
    if not _git_ref_exists(code_dir, ref):
        return None
    result = _run(
        ["git", "merge-base", "HEAD", ref],
        cwd=code_dir,
        capture_output=True,
    )
    value = result.stdout.strip()
    return value or None


def _find_diff_base(code_dir: Path) -> Optional[str]:
    explicit_base = os.environ.get("REACTORCIDE_DIFF_BASE", "").strip()
    if explicit_base:
        if not _git_ref_exists(code_dir, explicit_base):
            raise RuntimeError(
                f"REACTORCIDE_DIFF_BASE is not a commit: {explicit_base}"
            )
        return explicit_base

    base_branch = (
        os.environ.get("REACTORCIDE_BASE_REF")
        or os.environ.get("REACTORCIDE_PR_BASE_REF")
        or "main"
    )
    candidates = (
        f"upstream/{base_branch}",
        f"origin/{base_branch}",
        base_branch,
    )
    for candidate in candidates:
        base = _merge_base(code_dir, candidate)
        if base:
            return base

    if _git_ref_exists(code_dir, "HEAD^"):
        result = _run(
            ["git", "rev-parse", "HEAD^"],
            cwd=code_dir,
            capture_output=True,
        )
        return result.stdout.strip()

    return None


def _commit_records(code_dir: Path, diff_base: Optional[str]) -> List[tuple[str, str]]:
    revision = f"{diff_base}..HEAD" if diff_base else "HEAD"
    result = _run(
        ["git", "log", revision, "--pretty=format:%H%x00%s"],
        cwd=code_dir,
        capture_output=True,
    )
    records = []
    for line in result.stdout.splitlines():
        commit_hash, separator, subject = line.partition("\0")
        if separator:
            records.append((commit_hash, subject))
    return records


def validate_conventional_commits(code_dir: Path) -> None:
    """Validate commit subjects for the current pull-request range."""
    log_stdout("Validating conventional commits")
    diff_base = _find_diff_base(code_dir)
    failed = []

    for commit_hash, subject in _commit_records(code_dir, diff_base):
        if CONVENTIONAL_COMMIT_PATTERN.fullmatch(subject):
            log_stdout(f"OK: {subject}")
        else:
            log_stdout(f"FAIL: {subject} ({commit_hash})")
            failed.append(subject)

    if failed:
        raise RuntimeError(
            "Commit messages must match 'type(scope)?: description'. "
            "Valid types: feat, fix, docs, style, refactor, perf, test, "
            "build, ci, chore, and revert."
        )

    log_stdout("All commits follow the conventional commit format.")


def test_coordinator(code_dir: Path) -> None:
    """Run coordinator unit and package tests, except the integration package."""
    module_dir = code_dir / "coordinator_api"
    environment = _go_environment()
    package_result = _run(
        ["go", "list", "./..."],
        cwd=module_dir,
        capture_output=True,
        env=environment,
    )
    packages = [
        package
        for package in package_result.stdout.splitlines()
        if not package.endswith("/test")
    ]
    if not packages:
        raise RuntimeError("go list did not return coordinator packages")
    _run(
        ["go", "test", *packages, "-count=1"],
        cwd=module_dir,
        env=environment,
    )


def test_runnerlib(code_dir: Path) -> None:
    """Run the runnerlib test set used by pull-request CI."""
    _run(
        [
            "uv",
            "run",
            "python",
            "-m",
            "pytest",
            "tests/",
            "--ignore=tests/test_docker_execution.py",
            "--ignore=tests/test_dynamic_secret_masking.py",
            "--ignore=tests/test_dynamic_secrets.py",
        ],
        cwd=code_dir / "runnerlib",
    )


def test_web(code_dir: Path) -> None:
    """Run web application unit tests."""
    _run(
        ["go", "test", "./internal/...", "-count=1"],
        cwd=code_dir / "webapp",
        env=_go_environment(),
    )


CI_JOBS: Dict[str, Callable[[Path], None]] = {
    "conventional-commits": validate_conventional_commits,
    "test-go": test_coordinator,
    "test-python": test_runnerlib,
    "test-web": test_web,
}


class ReactorcideCIJobsPlugin(Plugin):
    """Run one selected Reactorcide CI job after source preparation."""

    def __init__(self):
        super().__init__(name="reactorcide_ci_jobs", priority=50)

    def supported_phases(self):
        return [PluginPhase.POST_SOURCE_PREP]

    def execute(self, context: PluginContext) -> None:
        if context.phase != PluginPhase.POST_SOURCE_PREP:
            return

        job_name = os.environ.get("REACTORCIDE_CI_JOB", "").strip()
        if not job_name:
            return

        job = CI_JOBS.get(job_name)
        if job is None:
            names = ", ".join(sorted(CI_JOBS))
            raise RuntimeError(
                f"Unknown REACTORCIDE_CI_JOB '{job_name}'. Valid jobs: {names}"
            )

        code_dir = Path(context.config.code_dir)
        if not code_dir.is_dir():
            raise RuntimeError(f"Code directory does not exist: {code_dir}")

        log_stdout(f"Starting runnerlib lifecycle job: {job_name}")
        job(code_dir)
        log_stdout(f"Completed runnerlib lifecycle job: {job_name}")
