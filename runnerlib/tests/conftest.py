"""
Global pytest configuration for runnerlib tests.

This conftest provides autouse fixtures that make the test suite safe and
deterministic when running inside a Reactorcide CI container (where
REACTORCIDE_* env vars are set and /job exists).
"""

import os

import pytest


@pytest.fixture(autouse=True)
def _clean_reactorcide_env(monkeypatch):
    """Strip all REACTORCIDE_* environment variables for test isolation.

    When tests run inside a Reactorcide job container, the environment has
    many REACTORCIDE_* variables set (SOURCE_URL, SHA, BRANCH, API_TOKEN,
    etc.).  These leak into get_config() and WorkflowContext, causing tests
    to use real URLs, submit real jobs, or skip expected validation errors.

    This fixture removes ALL REACTORCIDE_* vars, then sets only
    REACTORCIDE_IN_CONTAINER=false to prevent container-mode auto-detection.
    """
    for key in list(os.environ):
        if key.startswith("REACTORCIDE_"):
            monkeypatch.delenv(key, raising=False)
    monkeypatch.setenv("REACTORCIDE_IN_CONTAINER", "false")
