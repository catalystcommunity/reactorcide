"""
Global pytest configuration for runnerlib tests.

This conftest blocks API trigger submissions for ALL tests to prevent
test code from accidentally submitting real jobs to a live coordinator
when running in CI (where REACTORCIDE_COORDINATOR_URL, REACTORCIDE_API_TOKEN,
and REACTORCIDE_JOB_ID are set in the environment).
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
