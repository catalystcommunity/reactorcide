"""
Tests for job-level ``if:`` predicates: parsing, the legacy per-job
triggers/paths mapping, invalid keys, evaluation reasons, and the skip marker
the eval sends to the coordinator.
"""

import json
import tempfile
from pathlib import Path

import pytest
import yaml
from typer.testing import CliRunner

from src.cli import app
from src.eval import (
    JobIfConfig,
    PathsConfig,
    job_if_reasons,
    load_workflow_definitions_with_errors,
    parse_workflow_definition,
)

runner = CliRunner()


def _wf(jobs: dict) -> dict:
    return {"id": "ci", "name": "CI", "on": {"events": ["push", "schedule"]}, "jobs": jobs}


def _job(**extra) -> dict:
    return {"image": "alpine", "command": "true", **extra}


class TestParse:
    def test_if_predicates_parse(self, tmp_path):
        wf = parse_workflow_definition(_wf({"docs": _job(**{"if": {
            "paths": {"include": ["docs/**"], "exclude": ["docs/*.tmp"]},
            "branches": ["main", "release/*"],
            "events": "push",
            "schedule": False,
        }})}), tmp_path)
        cond = wf.jobs[0].run_if
        assert cond.paths.include == ["docs/**"]
        assert cond.paths.exclude == ["docs/*.tmp"]
        assert cond.branches == ["main", "release/*"]
        assert cond.events == ["push"]
        assert cond.schedule is False

    def test_paths_list_is_include(self, tmp_path):
        wf = parse_workflow_definition(_wf({"a": _job(**{"if": {"paths": ["src/**"]}})}), tmp_path)
        assert wf.jobs[0].run_if.paths == PathsConfig(include=["src/**"])

    def test_job_without_if_has_none(self, tmp_path):
        wf = parse_workflow_definition(_wf({"a": _job()}), tmp_path)
        assert wf.jobs[0].run_if is None

    def test_legacy_per_job_keys_map_onto_if(self, tmp_path):
        wf = parse_workflow_definition(_wf({"a": _job(
            triggers={"events": ["push"], "branches": ["main"]},
            paths={"include": ["api/**"]},
        )}), tmp_path)
        cond = wf.jobs[0].run_if
        assert cond.events == ["push"]
        assert cond.branches == ["main"]
        assert cond.paths.include == ["api/**"]

    def test_explicit_if_wins_over_legacy_key(self, tmp_path):
        wf = parse_workflow_definition(_wf({"a": _job(
            paths={"include": ["old/**"]},
            **{"if": {"paths": {"include": ["new/**"]}}},
        )}), tmp_path)
        assert wf.jobs[0].run_if.paths.include == ["new/**"]

    def test_job_file_legacy_triggers_are_not_inherited(self, tmp_path):
        jobs_dir = tmp_path / ".reactorcide" / "jobs"
        jobs_dir.mkdir(parents=True)
        (jobs_dir / "build.yaml").write_text(yaml.dump({
            "name": "build", "triggers": {"events": ["pull_request_opened"]},
            "paths": {"include": ["x/**"]}, "job": {"image": "alpine", "command": "true"},
        }))
        wf = parse_workflow_definition(_wf({"build": {"job_file": ".reactorcide/jobs/build.yaml"}}), tmp_path)
        assert wf.jobs[0].run_if is None

    def test_job_file_if_is_inherited(self, tmp_path):
        jobs_dir = tmp_path / ".reactorcide" / "jobs"
        jobs_dir.mkdir(parents=True)
        (jobs_dir / "build.yaml").write_text(yaml.dump({
            "name": "build", "if": {"branches": ["main"]},
            "job": {"image": "alpine", "command": "true"},
        }))
        wf = parse_workflow_definition(_wf({"build": {"job_file": ".reactorcide/jobs/build.yaml"}}), tmp_path)
        assert wf.jobs[0].run_if.branches == ["main"]

    @pytest.mark.parametrize("bad_if, message", [
        ({"path": ["docs/**"]}, "unknown keys ['path']"),
        ({"paths": {"includes": ["x"]}}, "unknown keys ['includes']"),
        ({"schedule": "yes"}, "must be true or false"),
        ({"branches": [1]}, "list of strings"),
        ("docs/**", "must be a mapping"),
    ])
    def test_invalid_if_is_rejected(self, tmp_path, bad_if, message):
        with pytest.raises(ValueError, match=message.replace("[", r"\[").replace("]", r"\]")):
            parse_workflow_definition(_wf({"a": _job(**{"if": bad_if})}), tmp_path)

    def test_invalid_if_makes_an_invalid_workflow_not_a_crash(self, tmp_path):
        wf_dir = tmp_path / ".reactorcide" / "workflows"
        wf_dir.mkdir(parents=True)
        (wf_dir / "bad.yaml").write_text(yaml.dump(_wf({"a": _job(**{"if": {"when": "x"}})})))
        (wf_dir / "good.yaml").write_text(yaml.dump({**_wf({"a": _job()}), "id": "good"}))
        definitions, invalid = load_workflow_definitions_with_errors(tmp_path)
        assert [d.workflow_id for d in definitions] == ["good"]
        assert len(invalid) == 1 and "unknown keys ['when']" in invalid[0].error


class TestEvaluate:
    def test_no_if_matches_with_no_reasons(self):
        assert job_if_reasons(None, "push", "main", []) == (True, [])

    def test_all_predicates_must_hold_and_all_are_explained(self):
        cond = JobIfConfig(paths=PathsConfig(include=["docs/**"]), branches=["main"], events=["push"])
        matched, reasons = job_if_reasons(cond, "push", "main", ["src/a.go"])
        assert matched is False
        assert [r["kind"] for r in reasons] == ["event", "branch", "paths"]
        assert reasons[0]["matched"] and reasons[1]["matched"]
        assert reasons[2] == {"kind": "paths", "include": ["docs/**"], "exclude": [],
                              "matched": False, "matched_files": [], "matched_count": 0}

    def test_paths_match_caps_files_and_keeps_count(self):
        cond = JobIfConfig(paths=PathsConfig(include=["**"]))
        files = [f"f{i}.txt" for i in range(70)]
        matched, reasons = job_if_reasons(cond, "push", "main", files)
        assert matched
        assert len(reasons[0]["matched_files"]) == 50
        assert reasons[0]["matched_count"] == 70

    def test_unknown_changed_files_match_and_say_so(self):
        cond = JobIfConfig(paths=PathsConfig(include=["docs/**"]))
        matched, reasons = job_if_reasons(cond, "push", "main", None)
        assert matched
        assert "unknown" in reasons[0]["detail"]

    def test_unknown_branch_matches_and_says_so(self):
        matched, reasons = job_if_reasons(JobIfConfig(branches=["main"]), "push", "", [])
        assert matched and "unknown" in reasons[0]["detail"]

    @pytest.mark.parametrize("required, event, expected", [
        (True, "schedule", True), (True, "push", False),
        (False, "schedule", False), (False, "push", True),
    ])
    def test_schedule_predicate(self, required, event, expected):
        matched, reasons = job_if_reasons(JobIfConfig(schedule=required), event, "main", [])
        assert matched is expected
        assert reasons == [{"kind": "schedule", "matched": expected, "value": event, "required": required}]


class TestEvalOutput:
    def test_skipped_job_is_sent_with_reasons(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            tmp = Path(tmpdir)
            wf_dir = tmp / "ci" / ".reactorcide" / "workflows"
            wf_dir.mkdir(parents=True)
            (wf_dir / "ci.yaml").write_text(yaml.dump(_wf({
                "build": _job(),
                "docs": _job(**{"if": {"paths": ["docs/**"]}}),
                "api": _job(**{"if": {"paths": ["api/**"]}}),
            })))
            (tmp / "src").mkdir()
            triggers_file = tmp / "triggers.json"
            result = runner.invoke(app, [
                "eval", "--ci-source-dir", str(tmp / "ci"), "--source-dir", str(tmp / "src"),
                "--event-type", "push", "--branch", "main", "--triggers-file", str(triggers_file),
                "--changed-file", "api/server.go",
            ], env={})
            assert result.exit_code == 0, result.output
            data = json.loads(triggers_file.read_text())

        jobs = {j["job_name"]: j for j in data["workflows"][0]["jobs"]}
        assert "skip" not in jobs["build"] and "decision_reasons" not in jobs["build"]
        assert jobs["docs"]["skip"] is True
        assert jobs["docs"]["decision_reasons"][0]["matched"] is False
        assert "skip" not in jobs["api"]
        assert jobs["api"]["decision_reasons"][0]["matched_files"] == ["api/server.go"]
