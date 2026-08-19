"""
Tests for workflow-centric definitions (.reactorcide/workflows/*.yaml).

Covers parsing (inline jobs, job_file references, the `needs` alias),
loading, event/branch/path matching, and trigger generation for matched
workflows.
"""

import tempfile
from pathlib import Path

import pytest
import yaml

from src.eval import (
    EventContext,
    WorkflowDefinition,
    evaluate_workflows,
    generate_triggers,
    load_workflow_definitions,
    parse_workflow_definition,
    workflow_match_reason,
)


@pytest.fixture
def temp_ci_dir():
    """Create a temp CI source dir with .reactorcide/{workflows,jobs}/ layout."""
    with tempfile.TemporaryDirectory() as tmpdir:
        root = Path(tmpdir)
        (root / ".reactorcide" / "workflows").mkdir(parents=True)
        (root / ".reactorcide" / "jobs").mkdir(parents=True)
        yield root


def _write(path: Path, data: dict) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    with open(path, "w") as f:
        yaml.safe_dump(data, f)


# --- parse_workflow_definition ---


def test_parse_inline_jobs_map(temp_ci_dir):
    data = {
        "name": "Reactorcide PR",
        "on": {"events": ["pull_request_opened"], "branches": ["main"]},
        "jobs": {
            "build": {"image": "golang:1.26", "command": "go build ./..."},
            "test": {
                "image": "golang:1.26",
                "command": "go test ./...",
                "depends_on": ["build"],
            },
        },
    }
    wf = parse_workflow_definition(data, temp_ci_dir, source_file="pr.yaml")

    assert wf.name == "Reactorcide PR"
    assert wf.triggers.events == ["pull_request_opened"]
    assert wf.triggers.branches == ["main"]
    assert len(wf.jobs) == 2

    by_name = {j.name: j for j in wf.jobs}
    assert by_name["build"].job.image == "golang:1.26"
    assert by_name["build"].job.command == "go build ./..."
    assert by_name["test"].job.depends_on == ["build"]


def test_parse_needs_alias_maps_to_depends_on(temp_ci_dir):
    data = {
        "name": "wf",
        "on": {"events": ["push"]},
        "jobs": {
            "a": {"image": "x", "command": "echo a"},
            "b": {"image": "x", "command": "echo b", "needs": ["a"]},
        },
    }
    wf = parse_workflow_definition(data, temp_ci_dir)
    by_name = {j.name: j for j in wf.jobs}
    assert by_name["b"].job.depends_on == ["a"]


def test_parse_jobs_as_list(temp_ci_dir):
    data = {
        "name": "wf",
        "on": {"events": ["push"]},
        "jobs": [
            {"name": "a", "image": "x", "command": "echo a"},
            {"name": "b", "image": "x", "command": "echo b", "depends_on": ["a"]},
        ],
    }
    wf = parse_workflow_definition(data, temp_ci_dir)
    assert [j.name for j in wf.jobs] == ["a", "b"]
    assert wf.jobs[1].job.depends_on == ["a"]


def test_parse_job_file_reference_is_resolved(temp_ci_dir):
    # A reusable job definition under .reactorcide/jobs/
    _write(
        temp_ci_dir / ".reactorcide" / "jobs" / "test-go.yaml",
        {
            "name": "test-go",
            "description": "run go tests",
            "triggers": {"events": ["push"]},  # ignored when referenced
            "job": {
                "image": "golang:1.26",
                "command": "go test ./...",
                "timeout": 1800,
            },
            "environment": {"FOO": "bar"},
        },
    )
    data = {
        "name": "Reactorcide PR",
        "on": {"events": ["pull_request_opened"]},
        "jobs": {
            "test-go": {
                "job_file": "test-go.yaml",
                "depends_on": ["lint"],
            }
        },
    }
    wf = parse_workflow_definition(data, temp_ci_dir)
    job = wf.jobs[0]
    # Name comes from the workflow entry key.
    assert job.name == "test-go"
    # Base fields come from the referenced file.
    assert job.job.image == "golang:1.26"
    assert job.job.command == "go test ./..."
    assert job.job.timeout == 1800
    assert job.environment == {"FOO": "bar"}
    # Overlay adds the DAG edge.
    assert job.job.depends_on == ["lint"]


def test_parse_job_file_inline_overrides_win(temp_ci_dir):
    _write(
        temp_ci_dir / ".reactorcide" / "jobs" / "base.yaml",
        {"name": "base", "job": {"image": "old:1", "command": "old", "priority": 1}},
    )
    data = {
        "name": "wf",
        "on": {"events": ["push"]},
        "jobs": {
            "base": {
                "job_file": "base.yaml",
                "image": "new:2",  # override
                "environment": {"K": "V"},
            }
        },
    }
    wf = parse_workflow_definition(data, temp_ci_dir)
    job = wf.jobs[0]
    assert job.job.image == "new:2"        # inline wins
    assert job.job.command == "old"        # base preserved
    assert job.job.priority == 1           # base preserved
    assert job.environment == {"K": "V"}


def test_parse_missing_name_raises(temp_ci_dir):
    with pytest.raises(ValueError, match="missing required 'name'"):
        parse_workflow_definition({"on": {"events": ["push"]}, "jobs": {}}, temp_ci_dir)


def test_parse_missing_jobs_raises(temp_ci_dir):
    with pytest.raises(ValueError, match="missing required 'jobs'"):
        parse_workflow_definition({"name": "wf", "on": {"events": ["push"]}}, temp_ci_dir)


def test_parse_missing_job_file_raises(temp_ci_dir):
    data = {
        "name": "wf",
        "on": {"events": ["push"]},
        "jobs": {"x": {"job_file": "does-not-exist.yaml"}},
    }
    with pytest.raises(FileNotFoundError):
        parse_workflow_definition(data, temp_ci_dir)


# --- load_workflow_definitions ---


def test_load_no_workflows_dir_returns_empty():
    with tempfile.TemporaryDirectory() as tmpdir:
        assert load_workflow_definitions(Path(tmpdir)) == []


def test_load_multiple_workflows_sorted(temp_ci_dir):
    _write(
        temp_ci_dir / ".reactorcide" / "workflows" / "pr.yaml",
        {"name": "Reactorcide PR", "on": {"events": ["pull_request_opened"]},
         "jobs": {"a": {"image": "x", "command": "c"}}},
    )
    _write(
        temp_ci_dir / ".reactorcide" / "workflows" / "release.yaml",
        {"name": "Reactorcide Release", "on": {"events": ["pull_request_merged"]},
         "jobs": {"r": {"image": "x", "command": "c"}}},
    )
    defs = load_workflow_definitions(temp_ci_dir)
    assert [d.name for d in defs] == ["Reactorcide PR", "Reactorcide Release"]


def test_load_then_evaluate_through_yaml_roundtrip(temp_ci_dir):
    # Regression: PyYAML parses the bare key `on:` as the boolean True.
    # load -> evaluate must still see the trigger block after a YAML round-trip.
    _write(
        temp_ci_dir / ".reactorcide" / "workflows" / "pr.yaml",
        {"name": "Reactorcide PR",
         "on": {"events": ["pull_request_opened"], "branches": ["main"]},
         "jobs": {"a": {"image": "x", "command": "c"}}},
    )
    defs = load_workflow_definitions(temp_ci_dir)
    assert defs[0].triggers.events == ["pull_request_opened"]
    assert defs[0].triggers.branches == ["main"]
    matched = evaluate_workflows(defs, "pull_request_opened", "main")
    assert [w.name for w in matched] == ["Reactorcide PR"]
    assert evaluate_workflows(defs, "push", "main") == []


def test_parse_on_key_parsed_as_bool_true(temp_ci_dir):
    # Simulate exactly what PyYAML produces for `on:` — a True key.
    data = {"name": "wf", True: {"events": ["push"]},
            "jobs": {"a": {"image": "x", "command": "c"}}}
    wf = parse_workflow_definition(data, temp_ci_dir)
    assert wf.triggers.events == ["push"]


def test_load_skips_invalid_workflow_but_keeps_valid(temp_ci_dir, capsys):
    _write(
        temp_ci_dir / ".reactorcide" / "workflows" / "good.yaml",
        {"name": "Good", "on": {"events": ["push"]}, "jobs": {"a": {"image": "x", "command": "c"}}},
    )
    # Missing 'name' -> invalid, should be skipped, not fatal.
    _write(
        temp_ci_dir / ".reactorcide" / "workflows" / "bad.yaml",
        {"on": {"events": ["push"]}, "jobs": {"a": {"image": "x", "command": "c"}}},
    )
    defs = load_workflow_definitions(temp_ci_dir)
    assert [d.name for d in defs] == ["Good"]


# --- matching ---


def _wf(events, branches=None, include=None):
    from src.eval import PathsConfig, TriggersConfig
    return WorkflowDefinition(
        name="wf",
        triggers=TriggersConfig(events=events, branches=branches or []),
        paths=PathsConfig(include=include or []),
        jobs=[],
    )


def test_match_event_type():
    wf = _wf(["pull_request_opened"])
    matched, _ = workflow_match_reason(wf, "pull_request_opened")
    assert matched
    matched, reason = workflow_match_reason(wf, "push")
    assert not matched
    assert "not in configured events" in reason


def test_match_branch_filter():
    wf = _wf(["push"], branches=["main"])
    assert workflow_match_reason(wf, "push", branch="main")[0]
    matched, reason = workflow_match_reason(wf, "push", branch="feature/x")
    assert not matched
    assert "branch" in reason


def test_match_path_filter():
    wf = _wf(["push"], include=["coordinator_api/**"])
    assert workflow_match_reason(wf, "push", changed_files=["coordinator_api/main.go"])[0]
    matched, reason = workflow_match_reason(wf, "push", changed_files=["docs/readme.md"])
    assert not matched
    assert "path filters" in reason


def test_match_path_filter_skipped_when_no_changed_info():
    wf = _wf(["push"], include=["coordinator_api/**"])
    # changed_files=None means we cannot filter -> allow.
    assert workflow_match_reason(wf, "push", changed_files=None)[0]


def test_evaluate_workflows_returns_only_matches():
    a = _wf(["pull_request_opened"])
    a.name = "a"
    b = _wf(["pull_request_merged"])
    b.name = "b"
    matched = evaluate_workflows([a, b], "pull_request_opened")
    assert [w.name for w in matched] == ["a"]


# --- trigger generation for a matched workflow ---


def test_generate_triggers_for_workflow_jobs(temp_ci_dir):
    data = {
        "name": "Reactorcide PR",
        "on": {"events": ["pull_request_opened"]},
        "jobs": {
            "test-go": {"image": "golang:1.26", "command": "go test ./..."},
        },
    }
    wf = parse_workflow_definition(data, temp_ci_dir)
    ctx = EventContext(
        event_type="pull_request_opened",
        branch="main",
        source_url="https://github.com/fork/repo.git",
        ci_source_url="https://github.com/upstream/repo.git",
    )
    triggers = generate_triggers(wf.jobs, ctx)
    assert len(triggers) == 1
    t = triggers[0]
    d = t.to_dict()

    assert t.job_name == "test-go"
    assert d["container_image"] == "golang:1.26"
    # Command wrapped in runnerlib run by default.
    assert d["job_command"] == "runnerlib run --job-command 'go test ./...'"
    # Trusted CI source stays upstream; source can be the fork.
    assert d["source_url"] == "https://github.com/fork/repo.git"
    assert d["ci_source_url"] == "https://github.com/upstream/repo.git"
    # No job_file leaks into the emitted trigger (coordinator can't resolve it
    # in coordinator-mediated mode).
    assert "job_file" not in d


@pytest.mark.parametrize("reserved", [
    "authority", "ci_origin", "execution_profile",
    "policy_revision", "policy_rule_id", "approval_id",
])
def test_reserved_authority_fields_are_rejected(temp_ci_dir, reserved):
    """Repository YAML must not be able to set or replace node authority."""
    data = {
        "name": "wf",
        "on": {"events": ["push"]},
        "jobs": {"build": {"image": "x", "command": "echo a", reserved: "standard"}},
    }
    with pytest.raises(ValueError, match="reserved authority field"):
        parse_workflow_definition(data, temp_ci_dir)


def test_reserved_authority_field_in_job_file_is_rejected(temp_ci_dir):
    _write(temp_ci_dir / ".reactorcide" / "jobs" / "build.yaml", {
        "name": "build",
        "execution_profile": "standard",
        "job": {"image": "x", "command": "echo a"},
    })
    data = {
        "name": "wf",
        "on": {"events": ["push"]},
        "jobs": {"build": {"job_file": "build.yaml"}},
    }
    with pytest.raises(ValueError, match="reserved authority field"):
        parse_workflow_definition(data, temp_ci_dir)
