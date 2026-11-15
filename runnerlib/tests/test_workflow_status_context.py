"""
Tests for a workflow's optional extra commit status, `status: {context: ...}`.

Every workflow counts toward the event's "Reactorcide Checks" status. A
workflow with a status context also posts a status of its own under that name.
"""

import json
import tempfile
from pathlib import Path

import pytest
import yaml
from typer.testing import CliRunner

from src.cli import app
from src.eval import EVENT_CHECKS_CONTEXT, load_workflow_definitions_with_errors, parse_workflow_definition

runner = CliRunner()


def _parse(status):
    data = {"id": "lint", "name": "Lint", "on": {"events": ["push"]}, "jobs": {}}
    if status is not None:
        data["status"] = status
    return parse_workflow_definition(data, Path("/nonexistent"), "lint.yaml")


def test_no_status_block_means_no_extra_context():
    assert _parse(None).status_context == ""


def test_context_is_parsed_and_trimmed():
    assert _parse({"context": "  lint  "}).status_context == "lint"


@pytest.mark.parametrize(
    "status, message",
    [
        ("lint", "non-mapping 'status'"),
        ({}, "non-empty string"),
        ({"context": ""}, "non-empty string"),
        ({"context": "   "}, "non-empty string"),
        ({"context": 5}, "non-empty string"),
        ({"context": "x" * 101}, "longer than 100"),
        ({"context": EVENT_CHECKS_CONTEXT}, "cannot be"),
        ({"context": "lint", "target_url": "x"}, "unknown field"),
    ],
)
def test_invalid_status_blocks_are_rejected(status, message):
    with pytest.raises(ValueError, match=message):
        _parse(status)


def test_invalid_status_makes_only_that_file_invalid():
    with tempfile.TemporaryDirectory() as tmpdir:
        root = Path(tmpdir)
        workflows = root / ".reactorcide" / "workflows"
        workflows.mkdir(parents=True)
        (workflows / "good.yaml").write_text(yaml.dump({"id": "good", "name": "Good", "on": {"events": ["push"]}, "jobs": {}}))
        (workflows / "bad.yaml").write_text(yaml.dump({
            "id": "bad", "name": "Bad", "on": {"events": ["push"]}, "jobs": {},
            "status": {"context": EVENT_CHECKS_CONTEXT},
        }))
        definitions, invalid = load_workflow_definitions_with_errors(root)
        assert [d.workflow_id for d in definitions] == ["good"]
        assert len(invalid) == 1


def test_eval_passes_status_context_in_the_workflow_batch():
    with tempfile.TemporaryDirectory() as tmpdir:
        tmp = Path(tmpdir)
        ci_dir, src_dir = tmp / "ci", tmp / "src"
        src_dir.mkdir()
        workflows = ci_dir / ".reactorcide" / "workflows"
        workflows.mkdir(parents=True)
        (workflows / "lint.yaml").write_text(yaml.dump({
            "id": "lint", "name": "Lint", "on": {"events": ["push"]}, "status": {"context": "lint"},
            "jobs": {"check": {"job": {"image": "alpine", "command": "make lint"}}},
        }))
        (workflows / "build.yaml").write_text(yaml.dump({
            "id": "build", "name": "Build", "on": {"events": ["push"]},
            "jobs": {"compile": {"job": {"image": "alpine", "command": "make"}}},
        }))
        triggers_file = tmp / "triggers.json"
        result = runner.invoke(app, [
            "eval", "--ci-source-dir", str(ci_dir), "--source-dir", str(src_dir),
            "--event-type", "push", "--branch", "main", "--triggers-file", str(triggers_file),
            "--changed-file", "main.go",
        ], env={})
        assert result.exit_code == 0, result.output
        batches = {b["id"]: b for b in json.loads(triggers_file.read_text())["workflows"]}
        assert batches["lint"]["status_context"] == "lint"
        assert "status_context" not in batches["build"]
