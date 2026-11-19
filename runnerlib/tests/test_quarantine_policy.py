"""Tests for the quarantine_policy job option (WP15, docs/artifacts-and-tests.md)."""

import pytest

from src.eval import EventContext, generate_triggers, parse_job_definition


def _definition(policy):
    job = {"image": "alpine", "command": "make test"}
    if policy is not None:
        job["quarantine_policy"] = policy
    return parse_job_definition({"name": "unit", "triggers": {"events": ["push"]}, "job": job})


def test_quarantine_policy_default_is_empty():
    assert _definition(None).job.quarantine_policy == ""


@pytest.mark.parametrize("policy", ["report", "ignore_quarantined"])
def test_quarantine_policy_reaches_the_trigger(policy):
    triggers = generate_triggers([_definition(policy)], EventContext(event_type="push"))
    assert triggers[0].quarantine_policy == policy
    assert triggers[0].to_dict()["quarantine_policy"] == policy


def test_quarantine_policy_absent_from_trigger_by_default():
    triggers = generate_triggers([_definition(None)], EventContext(event_type="push"))
    assert "quarantine_policy" not in triggers[0].to_dict()


@pytest.mark.parametrize("policy", ["ignore", True, "IGNORE_QUARANTINED"])
def test_quarantine_policy_refuses_unknown_values(policy):
    with pytest.raises(ValueError, match="quarantine_policy"):
        _definition(policy)
