"""
Tests for eval decision records: one decision per workflow file, with
structured reasons, the changed-file list, the event ID as operation ID, and
the dual CI checkout for push events.
"""

import json
import tempfile
from pathlib import Path
from unittest.mock import patch

import yaml
from typer.testing import CliRunner

from src.cli import app
from src.eval import (
    MATCHED_FILES_LIMIT,
    PathsConfig,
    load_workflow_definitions_with_errors,
    matching_files,
    parse_workflow_definition,
    workflow_decision_reasons,
)
from src.workflow import WorkflowContext

runner = CliRunner()


def _write_yaml(path: Path, data: dict) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    with open(path, "w") as f:
        yaml.dump(data, f)


def _make_ci(root: Path) -> None:
    workflows = root / ".reactorcide" / "workflows"
    _write_yaml(workflows / "api.yaml", {
        "id": "api", "name": "API",
        "on": {"events": ["push"], "branches": ["main"]},
        "paths": {"include": ["api/**"]},
        "jobs": {"test": {"job": {"image": "alpine", "command": "make test"}}},
    })
    _write_yaml(workflows / "docs.yaml", {
        "id": "docs", "name": "Docs",
        "on": {"events": ["push"]},
        "paths": {"include": ["docs/**"]},
        "jobs": {"build": {"job": {"image": "alpine", "command": "make docs"}}},
    })
    _write_yaml(workflows / "release.yaml", {
        "id": "release", "name": "Release",
        "on": {"events": ["tag_created"]},
        "jobs": {"ship": {"job": {"image": "alpine", "command": "ship"}}},
    })
    (workflows / "broken.yaml").write_text("id: broken\nname: Broken\njobs: [not, a, mapping]\non: {events: [push]}\n")


def _run_eval(tmp: Path, changed, env=None, args=None):
    ci_dir = tmp / "ci"
    src_dir = tmp / "src"
    src_dir.mkdir(exist_ok=True)
    _make_ci(ci_dir)
    triggers_file = tmp / "triggers.json"
    cmd = [
        "eval", "--ci-source-dir", str(ci_dir), "--source-dir", str(src_dir),
        "--event-type", "push", "--branch", "main", "--triggers-file", str(triggers_file),
    ]
    for value in changed:
        cmd += ["--changed-file", value]
    cmd += args or []
    result = runner.invoke(app, cmd, env=env or {})
    return result, triggers_file


class TestDecisionReasons:
    def test_reasons_explain_each_check_in_order(self):
        wf = parse_workflow_definition({
            "id": "api", "name": "API",
            "on": {"events": ["push"], "branches": ["main"]},
            "paths": {"include": ["api/**"], "exclude": ["api/**/*.md"]},
            "jobs": {},
        }, Path("/nonexistent"))

        matched, reasons = workflow_decision_reasons(wf, "push", "main", ["api/a.go", "api/README.md", "web/x.ts"])
        assert matched is True
        assert [r["kind"] for r in reasons] == ["event", "branch", "paths"]
        assert reasons[2]["matched_files"] == ["api/a.go"]
        assert reasons[2]["matched_count"] == 1

        matched, reasons = workflow_decision_reasons(wf, "pull_request_opened", "main", [])
        assert matched is False
        assert reasons == [{"kind": "event", "matched": False, "value": "pull_request_opened", "patterns": ["push"]}]

        matched, reasons = workflow_decision_reasons(wf, "push", "dev", [])
        assert matched is False
        assert reasons[-1] == {"kind": "branch", "matched": False, "value": "dev", "patterns": ["main"]}

        matched, reasons = workflow_decision_reasons(wf, "push", "main", ["web/x.ts"])
        assert matched is False
        assert reasons[-1]["kind"] == "paths" and reasons[-1]["matched"] is False
        assert reasons[-1]["matched_count"] == 0

    def test_unknown_changed_files_do_not_filter(self):
        wf = parse_workflow_definition({
            "id": "api", "name": "API", "on": {"events": ["push"]}, "paths": {"include": ["api/**"]}, "jobs": {},
        }, Path("/nonexistent"))
        matched, reasons = workflow_decision_reasons(wf, "push", "main", None)
        assert matched is True
        assert "unknown" in reasons[-1]["detail"]

    def test_matched_files_are_capped_but_count_is_exact(self):
        files = [f"api/f{i}.go" for i in range(MATCHED_FILES_LIMIT + 25)]
        wf = parse_workflow_definition({
            "id": "api", "name": "API", "on": {"events": ["push"]}, "paths": {"include": ["api/**"]}, "jobs": {},
        }, Path("/nonexistent"))
        _, reasons = workflow_decision_reasons(wf, "push", "", files)
        assert len(reasons[-1]["matched_files"]) == MATCHED_FILES_LIMIT
        assert reasons[-1]["matched_count"] == MATCHED_FILES_LIMIT + 25

    def test_matching_files_applies_include_and_exclude(self):
        config = PathsConfig(include=["src/**"], exclude=["src/**/*_test.go"])
        assert matching_files(config, ["src/a.go", "src/a_test.go", "doc.md"]) == ["src/a.go"]

    def test_invalid_files_are_reported(self, tmp_path):
        _make_ci(tmp_path)
        definitions, invalid = load_workflow_definitions_with_errors(tmp_path)
        assert sorted(d.workflow_id for d in definitions) == ["api", "docs", "release"]
        assert len(invalid) == 1
        assert invalid[0].source_file.endswith("broken.yaml")
        assert invalid[0].workflow_id == "broken"
        assert invalid[0].error


class TestEvalDecisionOutput:
    def test_every_workflow_file_gets_a_decision(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            result, triggers_file = _run_eval(Path(tmpdir), ["api/server.go", "README.md"])
            assert result.exit_code == 0, result.output
            data = json.loads(triggers_file.read_text())

        by_id = {d["id"]: d for d in data["decisions"]}
        assert set(by_id) == {"api", "docs", "release", "broken"}
        assert by_id["api"]["decision"] == "run"
        assert by_id["api"]["source_file"] == ".reactorcide/workflows/api.yaml"
        assert by_id["api"]["reasons"][-1]["matched_files"] == ["api/server.go"]
        assert by_id["docs"]["decision"] == "skipped"
        assert by_id["docs"]["reasons"][-1] == {
            "kind": "paths", "include": ["docs/**"], "exclude": [], "matched": False,
            "matched_files": [], "matched_count": 0,
        }
        assert by_id["release"]["decision"] == "skipped"
        assert by_id["release"]["reasons"][0]["kind"] == "event"
        assert by_id["broken"]["decision"] == "invalid"
        assert by_id["broken"]["reasons"][0]["kind"] == "invalid"
        assert [w["id"] for w in data["workflows"]] == ["api"]
        assert sorted(data["changed_files"]) == ["README.md", "api/server.go"]
        assert data["changed_files_total"] == 2

    def test_nothing_to_run_still_reports_decisions_to_the_coordinator(self):
        submitted = {}

        def fake_submit(self, trigger_data):
            submitted.update(trigger_data)
            return True

        with tempfile.TemporaryDirectory() as tmpdir:
            env = {"REACTORCIDE_COORDINATOR_URL": "https://coordinator.invalid", "REACTORCIDE_API_TOKEN": "token",
                   "REACTORCIDE_JOB_ID": "job-1", "REACTORCIDE_EVENT_ID": "event-1"}
            with patch.object(WorkflowContext, "_submit_triggers_via_api", fake_submit):
                result, _ = _run_eval(Path(tmpdir), ["web/x.ts"], env=env)
        assert result.exit_code == 0, result.output
        assert submitted["workflows"] == []
        assert submitted["operation_id"] == "event-1"
        assert {d["id"]: d["decision"] for d in submitted["decisions"]} == {
            "api": "skipped", "docs": "skipped", "release": "skipped", "broken": "invalid",
        }

    def test_local_run_with_nothing_to_run_writes_no_trigger_file(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            result, triggers_file = _run_eval(Path(tmpdir), ["web/x.ts"])
            assert result.exit_code == 0, result.output
            assert not triggers_file.exists()


class TestOperationID:
    def test_event_id_is_the_operation_id(self, monkeypatch):
        monkeypatch.setenv("REACTORCIDE_EVENT_ID", "event-1")
        assert WorkflowContext()._trigger_operation_id == "event-1"

    def test_without_event_a_random_id_is_used(self, monkeypatch):
        monkeypatch.delenv("REACTORCIDE_EVENT_ID", raising=False)
        first, second = WorkflowContext()._trigger_operation_id, WorkflowContext()._trigger_operation_id
        assert first and second and first != second


class TestPushDualCheckout:
    def test_push_with_dual_ci_reports_head_candidates(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            tmp = Path(tmpdir)
            ci_dir = tmp / "ci"
            _make_ci(ci_dir / "base")
            _make_ci(ci_dir / "head")
            src_dir = tmp / "src"
            src_dir.mkdir()
            triggers_file = tmp / "triggers.json"
            result = runner.invoke(app, [
                "eval", "--ci-source-dir", str(ci_dir), "--source-dir", str(src_dir),
                "--event-type", "push", "--branch", "main",
                "--changed-file", ".reactorcide/workflows/api.yaml", "--changed-file", "api/a.go",
                "--triggers-file", str(triggers_file),
            ], env={"REACTORCIDE_EVAL_DUAL_CI": "true", "REACTORCIDE_SHA": "after"})
            assert result.exit_code == 0, result.output
            data = json.loads(triggers_file.read_text())
        assert data["changed_ci_paths"] == [".reactorcide/workflows/api.yaml"]
        assert {c["id"] for c in data["policy_candidates"]["head"]} == {"api", "docs", "release"}
        assert {c["id"] for c in data["policy_candidates"]["base"]} == {"api", "docs", "release"}
