"""
Tests for scheduled evals: REACTORCIDE_WORKFLOW_REF selects the one workflow a
coordinator schedule runs, its on/paths filters do not apply, other workflow
files get no decision, and a ref that names nothing on the branch is one
invalid "workflow not found" decision with a successful eval.
"""

import json
import tempfile
from pathlib import Path
from unittest.mock import patch

import yaml
from typer.testing import CliRunner

from src.cli import app
from src.schedule_eval import (
    not_found_decision,
    schedule_reason_from_env,
    select_scheduled_workflow,
    workflow_ref_from_env,
)
from src.workflow import WorkflowContext

runner = CliRunner()

SCHEDULE_ENV = {
    "REACTORCIDE_SCHEDULE_ID": "11111111-1111-1111-1111-111111111111",
    "REACTORCIDE_SCHEDULE_NAME": "nightly",
    "REACTORCIDE_SCHEDULE_CRON": "0 3 * * *",
    "REACTORCIDE_SCHEDULE_FIRE_TIME": "2026-11-15T03:00:00Z",
}


def _write_yaml(path: Path, data: dict) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    with open(path, "w") as f:
        yaml.dump(data, f)


def _make_ci(root: Path) -> None:
    workflows = root / ".reactorcide" / "workflows"
    # The scheduled workflow does not list `schedule` or the branch: the
    # schedule chooses it, so its filters do not apply.
    _write_yaml(workflows / "nightly.yaml", {
        "id": "nightly", "name": "Nightly",
        "on": {"events": ["push"], "branches": ["main"]},
        "paths": {"include": ["never/**"]},
        "jobs": {"soak": {"job": {"image": "alpine", "command": "make soak"}}},
    })
    _write_yaml(workflows / "api.yaml", {
        "id": "api", "name": "API",
        "on": {"events": ["push"]},
        "jobs": {"test": {"job": {"image": "alpine", "command": "make test"}}},
    })
    (workflows / "broken.yaml").write_text("id: broken\nname: Broken\njobs: [not, a, mapping]\non: {events: [push]}\n")


def _run_schedule_eval(tmp: Path, workflow_ref: str, extra_env=None):
    ci_dir = tmp / "ci"
    src_dir = tmp / "src"
    src_dir.mkdir(exist_ok=True)
    _make_ci(ci_dir)
    triggers_file = tmp / "triggers.json"
    env = {"REACTORCIDE_WORKFLOW_REF": workflow_ref, **SCHEDULE_ENV, **(extra_env or {})}
    cmd = [
        "eval", "--ci-source-dir", str(ci_dir), "--source-dir", str(src_dir),
        "--event-type", "schedule", "--branch", "release", "--triggers-file", str(triggers_file),
    ]
    return runner.invoke(app, cmd, env=env), triggers_file


class TestScheduleHelpers:
    def test_reason_from_env(self):
        reason = schedule_reason_from_env(SCHEDULE_ENV)
        assert reason["kind"] == "schedule"
        assert reason["matched"] is True
        assert reason["value"] == "0 3 * * *"
        assert reason["schedule_id"] == SCHEDULE_ENV["REACTORCIDE_SCHEDULE_ID"]
        assert reason["fire_time"] == "2026-11-15T03:00:00Z"
        assert "nightly" in reason["detail"]

    def test_ref_from_env(self):
        assert workflow_ref_from_env({"REACTORCIDE_WORKFLOW_REF": "  nightly "}) == "nightly"
        assert workflow_ref_from_env({}) == ""

    def test_not_found_decision(self):
        decision = not_found_decision(".reactorcide/workflows/gone.yaml")
        assert decision["id"] == "ref:.reactorcide/workflows/gone.yaml"
        assert decision["decision"] == "invalid"
        assert decision["source_file"] == ".reactorcide/workflows/gone.yaml"
        assert decision["reasons"] == [{"kind": "invalid", "detail": "workflow not found"}]
        assert not_found_decision("gone")["source_file"] == ""

    def test_select_by_id_and_by_path(self):
        from src.eval import load_workflow_definitions_with_errors
        with tempfile.TemporaryDirectory() as tmpdir:
            root = Path(tmpdir)
            _make_ci(root)
            defs, invalid = load_workflow_definitions_with_errors(root)
            by_id, _ = select_scheduled_workflow("nightly", defs, invalid, root)
            assert [wf.workflow_id for wf in by_id] == ["nightly"]
            by_path, _ = select_scheduled_workflow(".reactorcide/workflows/api.yaml", defs, invalid, root)
            assert [wf.workflow_id for wf in by_path] == ["api"]
            none, none_invalid = select_scheduled_workflow("missing", defs, invalid, root)
            assert none == [] and none_invalid == []
            broken_defs, broken = select_scheduled_workflow(".reactorcide/workflows/broken.yaml", defs, invalid, root)
            assert broken_defs == [] and len(broken) == 1


class TestScheduledEval:
    def test_runs_only_the_named_workflow_without_its_filters(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            result, triggers_file = _run_schedule_eval(Path(tmpdir), "nightly")
            assert result.exit_code == 0, result.output
            data = json.loads(triggers_file.read_text())
        assert [w["id"] for w in data["workflows"]] == ["nightly"]
        assert [j["job_name"] for j in data["workflows"][0]["jobs"]] == ["soak"]
        assert len(data["decisions"]) == 1, "other workflow files get no decision"
        decision = data["decisions"][0]
        assert decision["id"] == "nightly"
        assert decision["decision"] == "run"
        assert decision["reasons"][0]["kind"] == "schedule"
        assert decision["reasons"][0]["fire_time"] == "2026-11-15T03:00:00Z"

    def test_selects_by_file_path(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            result, triggers_file = _run_schedule_eval(Path(tmpdir), ".reactorcide/workflows/api.yaml")
            assert result.exit_code == 0, result.output
            data = json.loads(triggers_file.read_text())
        assert [w["id"] for w in data["workflows"]] == ["api"]

    def test_missing_workflow_reports_not_found_and_succeeds(self):
        submitted = {}

        def fake_submit(self, trigger_data):
            submitted.update(trigger_data)
            return True

        with tempfile.TemporaryDirectory() as tmpdir:
            env = {"REACTORCIDE_COORDINATOR_URL": "https://coordinator.invalid", "REACTORCIDE_API_TOKEN": "token",
                   "REACTORCIDE_JOB_ID": "job-1", "REACTORCIDE_EVENT_ID": "event-1"}
            with patch.object(WorkflowContext, "_submit_triggers_via_api", fake_submit):
                result, _ = _run_schedule_eval(Path(tmpdir), ".reactorcide/workflows/gone.yaml", env)
        assert result.exit_code == 0, result.output
        assert submitted["workflows"] == []
        assert submitted["decisions"] == [not_found_decision(".reactorcide/workflows/gone.yaml")]

    def test_invalid_scheduled_file_reports_its_error(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            result, triggers_file = _run_schedule_eval(Path(tmpdir), ".reactorcide/workflows/broken.yaml")
            assert result.exit_code == 0, result.output
            # A local run with nothing to run writes no trigger file.
            assert not triggers_file.exists()

    def test_schedule_without_ref_is_refused(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            tmp = Path(tmpdir)
            (tmp / "src").mkdir()
            _make_ci(tmp / "ci")
            result = runner.invoke(app, [
                "eval", "--ci-source-dir", str(tmp / "ci"), "--source-dir", str(tmp / "src"),
                "--event-type", "schedule", "--triggers-file", str(tmp / "t.json"),
            ], env={"REACTORCIDE_WORKFLOW_REF": ""})
        assert result.exit_code == 1

    def test_ordinary_event_ignores_schedule_helpers(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            tmp = Path(tmpdir)
            (tmp / "src").mkdir()
            _make_ci(tmp / "ci")
            triggers_file = tmp / "t.json"
            result = runner.invoke(app, [
                "eval", "--ci-source-dir", str(tmp / "ci"), "--source-dir", str(tmp / "src"),
                "--event-type", "push", "--branch", "main", "--triggers-file", str(triggers_file),
                "--changed-file", "api/x.go",
            ], env={"REACTORCIDE_WORKFLOW_REF": ""})
            assert result.exit_code == 0, result.output
            data = json.loads(triggers_file.read_text())
        assert {d["id"] for d in data["decisions"]} == {"nightly", "api", "broken"}


def test_selection_reason_manual_run():
    from src.schedule_eval import selection_reason_from_env

    reason = selection_reason_from_env("push", "build", {"REACTORCIDE_EVENT_SOURCE": "manual"})
    assert reason["kind"] == "event"
    assert reason["matched"] is True
    assert "manual run selected workflow 'build'" in reason["detail"]


def test_selection_reason_schedule_keeps_schedule_reason():
    from src.schedule_eval import selection_reason_from_env

    reason = selection_reason_from_env("schedule", "nightly", SCHEDULE_ENV)
    assert reason["kind"] == "schedule"
    # A push with a workflow ref but no manual source keeps the old reason.
    assert selection_reason_from_env("push", "nightly", {})["kind"] == "schedule"
