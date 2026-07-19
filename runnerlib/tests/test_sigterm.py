"""End-to-end tests for runnerlib's SIGTERM handling (src/signals.py).

These spawn `python -m src.cli run ...` as a real OS subprocess (rather than
using typer.testing.CliRunner, which runs in-process and can't receive a
real signal) and send it SIGTERM while it's blocked running a long-lived job
command — mirroring exactly what the coordinator's graceful cancel path does
to a job container's PID 1 (see coordinator_api's JobRunner.Stop /
job_processor.go's pollForCancel, and UI_AUTH_PLAN.md's "Cancel vs Kill"
section). Verifies: the child job process is actually killed (not left
running until the job's own timeout), PluginPhase.CLEANUP still runs, and
the process exits with the distinct TERM_EXIT_CODE (143) rather than a
generic error code.
"""

import os
import re
import signal
import subprocess
import sys
import textwrap
import time
from pathlib import Path

import pytest

from src.signals import TERM_EXIT_CODE

RUNNERLIB_ROOT = Path(__file__).resolve().parents[1]

CLEANUP_MARKER_PLUGIN = textwrap.dedent(
    """
    import os
    from pathlib import Path
    from src.plugins import Plugin, PluginPhase, PluginContext


    class CleanupMarkerPlugin(Plugin):
        def __init__(self):
            super().__init__(name="cleanup_marker_plugin")

        def supported_phases(self):
            return [PluginPhase.CLEANUP]

        def execute(self, context: PluginContext) -> None:
            marker_path = os.environ.get("RUNNERLIB_TEST_CLEANUP_MARKER")
            if marker_path:
                Path(marker_path).write_text("cleanup ran\\n")
    """
)


def _spawn_runnerlib_job(work_dir: Path, plugin_dir: Path, job_command: str, marker_path: Path) -> subprocess.Popen:
    """Spawns `python -m src.cli run ...` as PID 1 of its own session, the
    same way a job container's entrypoint invokes runnerlib (see
    job_processor.go's buildJobConfig / ParseCommandWithPrefix).

    PYTHONUNBUFFERED=1 keeps the child's stdout from block-buffering when
    it's writing to a pipe instead of a tty, so captured output reflects
    what actually happened rather than whatever happened to be flushed by
    the time the process exited.
    """
    env = os.environ.copy()
    env["RUNNERLIB_TEST_CLEANUP_MARKER"] = str(marker_path)
    env["PYTHONUNBUFFERED"] = "1"
    return subprocess.Popen(
        [
            sys.executable, "-m", "src.cli", "run",
            "--job-command", job_command,
            "--plugin-dir", str(plugin_dir),
            "--work-dir", str(work_dir),
        ],
        cwd=str(RUNNERLIB_ROOT),
        env=env,
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
        text=True,
        start_new_session=True,  # isolate its process group like a container's PID 1
    )


@pytest.fixture
def sigterm_job_dirs(tmp_path):
    plugin_dir = tmp_path / "plugins"
    plugin_dir.mkdir()
    (plugin_dir / "plugin_cleanup_marker.py").write_text(CLEANUP_MARKER_PLUGIN)
    work_dir = tmp_path / "work"
    work_dir.mkdir()
    marker_path = tmp_path / "cleanup-marker.txt"
    return work_dir, plugin_dir, marker_path


def test_sigterm_terminates_job_runs_cleanup_and_exits_with_term_code(sigterm_job_dirs):
    work_dir, plugin_dir, marker_path = sigterm_job_dirs

    proc = _spawn_runnerlib_job(work_dir, plugin_dir, "sleep 30", marker_path)
    try:
        # A trivial "sleep 30" job command is running well within this —
        # give it a moment to actually start before signaling it.
        time.sleep(1.5)
        assert proc.poll() is None, "job exited before we could signal it"

        proc.send_signal(signal.SIGTERM)
        full_output, _ = proc.communicate(timeout=15)
        returncode = proc.returncode
    finally:
        if proc.poll() is None:
            proc.kill()
            proc.wait(timeout=5)

    assert returncode == TERM_EXIT_CODE, (
        f"expected exit code {TERM_EXIT_CODE}, got {returncode}:\n{full_output}"
    )
    assert marker_path.exists(), (
        f"expected PluginPhase.CLEANUP to run and write the marker file:\n{full_output}"
    )
    assert marker_path.read_text() == "cleanup ran\n"


def test_sigterm_reaps_child_process_group(sigterm_job_dirs):
    """The 30s sleep must actually die when SIGTERM arrives — not linger
    until its own timeout while runnerlib itself exits, which would leave an
    orphaned process behind in the job container."""
    work_dir, plugin_dir, marker_path = sigterm_job_dirs

    proc = _spawn_runnerlib_job(work_dir, plugin_dir, "sleep 30 & child_pid=$!; echo CHILD_PID=$child_pid; wait $child_pid", marker_path)
    try:
        time.sleep(1.5)
        assert proc.poll() is None, "job exited before we could signal it"

        proc.send_signal(signal.SIGTERM)
        full_output, _ = proc.communicate(timeout=15)
        returncode = proc.returncode
    finally:
        if proc.poll() is None:
            proc.kill()
            proc.wait(timeout=5)

    assert returncode == TERM_EXIT_CODE, full_output

    # log_stdout prefixes each line with a timestamp, so search for the
    # marker rather than anchoring on line start. The job command's own
    # "Command: ..." echo (logged before execution) also contains the
    # literal, unexpanded text "CHILD_PID=$child_pid" — only match the
    # actual echoed (numeric) value.
    match = re.search(r"CHILD_PID=(\d+)", full_output)
    assert match is not None, f"could not find CHILD_PID in output:\n{full_output}"
    child_pid = int(match.group(1))

    # The sleep's pid should no longer exist (or should already be a zombie
    # with no living process behind it) — os.kill with signal 0 raises
    # ProcessLookupError once it's fully reaped, or succeeds harmlessly if
    # it's a zombie awaiting reap by its (now-dead) parent; either way it
    # must not still be an active, running "sleep" process.
    for _ in range(20):
        try:
            os.kill(child_pid, 0)
        except ProcessLookupError:
            break
        time.sleep(0.1)
    else:
        pytest.fail(f"child pid {child_pid} (sleep 30) was still alive after SIGTERM + grace period")
