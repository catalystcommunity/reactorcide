"""Shared SIGTERM handling for runnerlib's two job-execution entrypoints.

The coordinator's graceful cancel path (see coordinator_api's
JobRunner.Stop / job_processor.go's pollForCancel) sends SIGTERM to the job
container's PID 1 and waits a grace period before force-killing the
container. Whichever runnerlib code path is actually PID 1 needs to catch
that SIGTERM, stop the job's child process(es), and still run the same
cleanup machinery (PluginPhase.CLEANUP/ON_ERROR, cleanup_vcs_auth) that runs
on normal completion — otherwise a cancelled job silently skips cleanup
hooks that other jobs may rely on (removing checked-out credentials, etc).

Two entrypoints can be PID 1 depending on how the job container's command is
built (see job_processor.go's buildJobConfig / ParseCommandWithPrefix):
  - cli.py's `run` command in the default (non --container) execution mode,
    which runs the job's own command as a child via subprocess.Popen
    (cli.py's _run_local).
  - container.py's run_container, used for the `--container` (nested
    docker) execution mode.

Rather than have this module know how to terminate either kind of child
(cli.py's is a process-group leader reaped with TERM-then-KILL via
os.killpg; container.py's is a plain `docker run` subprocess reaped with a
single terminate() since docker forwards the signal into the container),
call sites register a zero-arg cleanup callback for the duration of their
blocking wait via register_sigterm_cleanup/unregister_sigterm_cleanup. The
installed handler runs all registered callbacks synchronously (best-effort —
one callback's exception doesn't block the others or stop TermRequested from
being raised) before raising TermRequested, which the caller's normal
try/finally structure then routes through cleanup, exactly like any other
exception.
"""

import signal
import threading
from typing import Callable

# Conventional shell "killed by signal N" exit code: 128 + SIGTERM(15).
TERM_EXIT_CODE = 143


class TermRequested(Exception):
    """Raised by the installed SIGTERM handler so callers can route
    termination through their existing try/finally cleanup path instead of
    the interpreter dying mid-job with no chance to clean up."""


# _callbacks is rebound (never mutated in place) so the signal handler can
# read it without taking _callbacks_lock: the handler runs synchronously on
# the main thread, so if it acquired the (non-reentrant) lock while the main
# thread was mid-register/unregister it would deadlock. Rebinding a tuple is
# atomic under the GIL; the lock only serializes writers on other threads.
_callbacks_lock = threading.Lock()
_callbacks: tuple = ()
_installed = False


def register_sigterm_cleanup(fn: Callable[[], None]) -> None:
    """Registers a zero-arg callback to run (best-effort) inside the SIGTERM
    handler, before TermRequested propagates. Typically used to terminate
    whatever child process the caller is currently blocked waiting on."""
    global _callbacks
    with _callbacks_lock:
        _callbacks = _callbacks + (fn,)


def unregister_sigterm_cleanup(fn: Callable[[], None]) -> None:
    """Removes a previously registered callback. Safe to call even if fn
    was never registered or already removed."""
    global _callbacks
    with _callbacks_lock:
        _callbacks = tuple(cb for cb in _callbacks if cb is not fn)


def _handle_sigterm(signum, frame) -> None:
    # Deliberately lock-free: see the _callbacks comment above.
    callbacks = _callbacks
    for fn in callbacks:
        try:
            fn()
        except Exception:
            # A failing cleanup callback must not prevent TermRequested from
            # being raised, nor block any other registered callback.
            pass
    raise TermRequested()


def install_sigterm_handler() -> None:
    """Installs the SIGTERM -> TermRequested trap. Idempotent — safe to call
    on every CLI invocation (e.g. from a Typer app callback)."""
    global _installed
    if _installed:
        return
    signal.signal(signal.SIGTERM, _handle_sigterm)
    _installed = True
