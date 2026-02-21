"""
Global pytest configuration for runnerlib tests.

This conftest provides autouse fixtures that make the test suite safe and
deterministic when running inside a Reactorcide CI container (where
REACTORCIDE_COORDINATOR_URL, REACTORCIDE_API_TOKEN, REACTORCIDE_JOB_ID are
set and /job exists).
"""

import pytest


@pytest.fixture(autouse=True)
def _block_api_trigger_submissions(monkeypatch):
    """Prevent any test from submitting real triggers via the coordinator API.

    When tests run inside a Reactorcide job container, the environment has
    REACTORCIDE_COORDINATOR_URL, REACTORCIDE_API_TOKEN, and REACTORCIDE_JOB_ID
    set.  WorkflowContext.flush_triggers() uses these to POST triggers to the
    live API, which creates real jobs.  Removing them ensures flush_triggers()
    falls back to file-only mode.
    """
    monkeypatch.delenv("REACTORCIDE_COORDINATOR_URL", raising=False)
    monkeypatch.delenv("REACTORCIDE_API_TOKEN", raising=False)
    monkeypatch.delenv("REACTORCIDE_JOB_ID", raising=False)


@pytest.fixture(autouse=True)
def _prevent_container_mode_autodetect(monkeypatch):
    """Prevent auto-detection of container mode in CI.

    When tests run inside a Reactorcide job container, the /job directory
    exists and is writable, causing is_in_container_mode() to return True.
    This changes test behavior (e.g. source prep uses /job instead of ./job).
    Setting REACTORCIDE_IN_CONTAINER=false forces host-mode behavior.
    """
    monkeypatch.setenv("REACTORCIDE_IN_CONTAINER", "false")
