"""Runnerlib lifecycle jobs for the Reactorcide pull-request workflow."""

from __future__ import annotations

import hashlib
import json
import os
import re
import shlex
import shutil
import subprocess
import tarfile
import tempfile
import urllib.request
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


# Fallback only. The real version lives in webapp/ui/.node-version -- see
# _node_version -- so this is what happens if that file is somehow missing.
DEFAULT_NODE_VERSION = "22.23.2"


def _node_version(code_dir: Path) -> str:
    """Read the pinned Node version from webapp/ui/.node-version.

    A FILE rather than a constant here, because plugin_release_jobs.py needs the
    same value and the two plugins cannot import each other: runnerlib loads
    each through spec_from_file_location without putting the directory on
    sys.path. A constant in each file would be two constants, and they would
    drift -- which is the exact failure an exact pin exists to prevent.

    `.node-version` is also the conventional file that nodenv, fnm and friends
    already read, so a developer's own tooling picks up the same version with no
    extra step.
    """
    version_file = code_dir / "webapp" / "ui" / ".node-version"
    try:
        version = version_file.read_text(encoding="utf-8").strip()
    except OSError:
        return DEFAULT_NODE_VERSION
    return version or DEFAULT_NODE_VERSION


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


def _install_node(environment: Dict[str, str], version: str) -> None:
    """Install the pinned Node toolchain through webi.

    Fast enough to do per job -- about two seconds on a warm CDN, one when the
    version is already present -- which is the whole reason the toolchain is
    fetched at job time rather than baked into runnerbase. A job needing a
    different version asks for a different version.
    """
    try:
        _run(["webi", f"node@{version}"], cwd=Path("/tmp"), env=environment)
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
    _install_node(node_environment, _node_version(code_dir))

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


# The generation scripts that own the csilgen pin, and every path they write.
# csil-gen-check reads CSILGEN_RELEASE from the scripts, so the pin has one
# home. The scripts must agree with each other.
CSIL_GENERATE_SCRIPTS = (
    "scripts/generate-csil-worker.sh",
    "scripts/generate-csil-ui.sh",
)
CSIL_GENERATED_PATHS = (
    "coordinator_api/internal/workerapi/csilapi",
    "coordinator_api/internal/workerclient/csilapi",
    "coordinator_api/internal/uiapi/csilapi",
    "webapp/internal/uiclient/csilapi",
    "webapp/ui/src/api/csilapi",
)
CSILGEN_RELEASE_API = (
    "https://api.github.com/repos/catalystcommunity/csilgen/releases/tags/"
)


def _csilgen_release(code_dir: Path) -> str:
    """Return the one CSILGEN_RELEASE that every generation script sets."""
    releases = set()
    for script in CSIL_GENERATE_SCRIPTS:
        text = (code_dir / script).read_text(encoding="utf-8")
        match = re.search(r'^CSILGEN_RELEASE="([^"]+)"', text, re.MULTILINE)
        if not match:
            raise RuntimeError(f"{script} does not set CSILGEN_RELEASE")
        releases.add(match.group(1))
    if len(releases) != 1:
        raise RuntimeError(
            "the generation scripts set different CSILGEN_RELEASE values: "
            + ", ".join(sorted(releases))
        )
    return releases.pop()


def _download_verified(url: str, dest: Path, sha256: str) -> Path:
    log_stdout(f"Download {url}")
    with urllib.request.urlopen(url, timeout=120) as response, dest.open("wb") as out:
        shutil.copyfileobj(response, out)
    digest = hashlib.sha256(dest.read_bytes()).hexdigest()
    if digest != sha256.lower():
        dest.unlink()
        raise RuntimeError(f"{url}: SHA-256 {digest} does not match {sha256}")
    return dest


def _install_csilgen(release: str, root: Path) -> Dict[str, str]:
    """Install exactly the pinned csilgen release under root.

    csilgen's own installer (tools.sh install-all) installs only the newest
    release, so a new csilgen release would fail this check with no change
    here. Download the assets of the pinned release and check each one against
    the SHA-256 digest that GitHub records. The CLI goes to root/bin and the
    generators to root/home/.csilgen/generators. Return an environment that
    uses only these, so no shared ~/.csilgen/generators is read or written.
    """
    match = re.fullmatch(r"csilgen/v(\d+\.\d+\.\d+)", release)
    if not match:
        raise RuntimeError(f"CSILGEN_RELEASE {release!r} is not csilgen/vX.Y.Z")
    version = match.group(1)
    log_stdout(f"Install {release}")
    api_url = CSILGEN_RELEASE_API + release.replace("/", "%2F")
    with urllib.request.urlopen(api_url, timeout=60) as response:
        info = json.loads(response.read().decode())
    assets = {asset["name"]: asset for asset in info.get("assets", [])}

    downloads = root / "downloads"
    bin_dir = root / "bin"
    home = root / "home"
    generators = home / ".csilgen" / "generators"
    for directory in (downloads, bin_dir, generators):
        directory.mkdir(parents=True, exist_ok=True)

    def fetch(name: str) -> Path:
        asset = assets.get(name)
        if asset is None:
            raise RuntimeError(f"{release} has no asset {name}")
        digest = str(asset.get("digest", ""))
        if not digest.startswith("sha256:"):
            raise RuntimeError(f"{release} asset {name} has no SHA-256 digest")
        return _download_verified(
            asset["browser_download_url"], downloads / name, digest.split(":", 1)[1]
        )

    cli_archive = fetch(f"csilgen-{version}-x86_64-unknown-linux-gnu.tar.gz")
    with tarfile.open(cli_archive) as tar:
        member = next(
            (
                info
                for info in tar.getmembers()
                if info.isfile() and info.name.removeprefix("./") == "csilgen"
            ),
            None,
        )
        if member is None:
            raise RuntimeError(f"csilgen is not in {cli_archive.name}")
        (bin_dir / "csilgen").write_bytes(tar.extractfile(member).read())
    (bin_dir / "csilgen").chmod(0o755)

    bundle = fetch(f"csilgen-generators-{version}.tar.gz")
    with tarfile.open(bundle) as tar:
        for member in tar.getmembers():
            name = member.name.removeprefix("./")
            if member.isfile() and name.endswith(".wasm") and "/" not in name:
                (generators / name).write_bytes(tar.extractfile(member).read())
    if not any(generators.glob("*.wasm")):
        raise RuntimeError(f"{release} generator bundle has no .wasm files")

    environment = os.environ.copy()
    environment["HOME"] = str(home)
    environment["PATH"] = f"{bin_dir}{os.pathsep}{environment.get('PATH', '')}"
    reported = _run(
        ["csilgen", "--version"], cwd=root, capture_output=True, env=environment
    ).stdout.strip()
    if reported != f"csilgen {version}":
        raise RuntimeError(f"installed csilgen reports {reported!r}, not {version}")
    log_stdout(f"Installed {reported}")
    return environment


def csil_gen_check(code_dir: Path) -> None:
    """Regenerate every CSIL target and fail if the committed code differs.

    Fails on changed files AND on new untracked files (git status
    --porcelain), because csilgen can add a file, such as the
    <spec>.csil-schema.cbor descriptor from 0.2.7.
    """
    release = _csilgen_release(code_dir)
    with tempfile.TemporaryDirectory(prefix="csilgen-") as scratch:
        environment = _install_csilgen(release, Path(scratch))
        for script in CSIL_GENERATE_SCRIPTS:
            _run(["bash", script], cwd=code_dir, env=environment)

    status = _run(
        ["git", "status", "--porcelain", "--", *CSIL_GENERATED_PATHS],
        cwd=code_dir,
        capture_output=True,
    )
    if status.stdout.strip():
        log_stdout(status.stdout)
        subprocess.run(
            ["git", "diff", "--stat", "--", *CSIL_GENERATED_PATHS],
            cwd=code_dir,
            check=False,
        )
        raise RuntimeError(
            f"the generated CSIL code is stale for {release}. Run "
            "scripts/generate-csil-worker.sh and scripts/generate-csil-ui.sh "
            "with that csilgen release and commit the result, including new files."
        )
    log_stdout(f"The generated CSIL code matches the contracts ({release})")


CI_JOBS: Dict[str, Callable[[Path], None]] = {
    "conventional-commits": validate_conventional_commits,
    "csil-gen-check": csil_gen_check,
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
