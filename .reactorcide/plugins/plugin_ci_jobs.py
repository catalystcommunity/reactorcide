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


# The Node version the web UI is built and tested with, pinned exactly.
#
# webi resolves a bare major ("node@22") to the newest release in that line, so
# the toolchain would drift under CI without anyone changing a file. An exact
# pin makes a Node upgrade a visible commit, the same way GO_VERSION is pinned
# in runnerlib/Dockerfile.runner.
NODE_VERSION = "22.23.2"


def _node_environment() -> Dict[str, str]:
    """Return an environment with a webi-installed Node on PATH.

    webi is per-user by design (it hardcodes WEBI_HOME="$HOME/.local"), and it
    installs each package to ~/.local/opt/<name>/bin. runnerbase already puts
    both that and ~/.local/bin on PATH, so this only has to make the same true
    when HOME is somewhere other than /home/runner -- a job using `run_as`.
    """
    environment = os.environ.copy()
    home = Path(environment.get("HOME", "/home/runner"))
    for entry in (home / ".local" / "opt" / "node" / "bin", home / ".local" / "bin"):
        if str(entry) not in environment.get("PATH", "").split(os.pathsep):
            environment["PATH"] = f"{entry}{os.pathsep}{environment.get('PATH', '')}"
    return environment


def _install_node(environment: Dict[str, str]) -> None:
    """Install the pinned Node toolchain through webi.

    Fast enough to do per job -- about two seconds on a warm CDN, one when the
    version is already present -- which is the whole reason the toolchain is
    fetched at job time rather than baked into runnerbase. A job needing a
    different version asks for a different version.
    """
    try:
        _run(["webi", f"node@{NODE_VERSION}"], cwd=Path("/tmp"), env=environment)
    except FileNotFoundError as error:
        # The likely cause, and it is an ordering problem rather than a code
        # one: this job is running on a runnerbase image published before webi
        # was added to runnerlib/Dockerfile.runner. Say so, because
        # "No such file or directory: 'webi'" does not.
        raise RuntimeError(
            "webi is not installed in this runner image, so the pinned Node "
            "toolchain cannot be fetched. Publish a runnerbase built from a "
            "Dockerfile that installs webi (see runnerlib/Dockerfile.runner) "
            "and re-run."
        ) from error


def _require_writable_ui_dir(ui_dir: Path) -> None:
    """Fail early, and legibly, when npm could not write into the UI directory.

    `npm ci` creates (and first removes) node_modules INSIDE the package
    directory, so it needs write access to webapp/ui itself.

    In CI this always holds: the source is a fresh clone the job user made. It
    does not hold under `run-local --as-runner`, where the container runs as uid
    1001 while the bind-mounted source is still the developer's own tree with
    their ownership -- npm then reports EACCES on either mkdir or rmdir
    depending on whether node_modules happens to exist, neither of which names
    the actual cause.

    Nothing here can fix that: writing into a host-owned tree as a different uid
    needs a chown that an unprivileged run-local cannot do. test-web.yaml
    already declares `run_local: user: host` so the default local run never hits
    it; `--as-runner` explicitly overrides that choice.

    The Go half of this job is unaffected, which is why it only started
    mattering when the UI build joined it: GOPATH and GOCACHE live under HOME
    (see _go_environment), not in the source tree.
    """
    if os.access(ui_dir, os.W_OK | os.X_OK):
        return
    raise RuntimeError(
        f"this user (uid {os.getuid()}) cannot write into {ui_dir}, and npm "
        "must create node_modules there.\n"
        "That is a uid mismatch against the bind-mounted source, not a code "
        "problem. Run this job as the host user -- its own run_local block "
        "already asks for that, so drop --as-runner."
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
    """Build and test the web application: the SolidJS SPA, then the Go bridge.

    The SPA is built FIRST, on purpose. It compiles into the Go binary through
    `go:embed`, so building it before the Go tests means those tests exercise
    the real embedded bundle rather than the placeholder a bare checkout
    carries -- which is what the asset-serving tests in
    webapp/internal/handlers actually want to assert against.
    """
    ui_dir = code_dir / "webapp" / "ui"
    _require_writable_ui_dir(ui_dir)

    node_environment = _node_environment()
    _install_node(node_environment)

    # `npm ci` rather than `npm install`: it installs exactly what
    # package-lock.json pins and fails if the lock file and package.json have
    # drifted, which is the behaviour CI should have.
    _run(["npm", "ci"], cwd=ui_dir, env=node_environment)
    _run(["npm", "run", "typecheck"], cwd=ui_dir, env=node_environment)
    _run(["npm", "run", "test"], cwd=ui_dir, env=node_environment)
    _run(["npm", "run", "build"], cwd=ui_dir, env=node_environment)

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
