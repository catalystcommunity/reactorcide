"""
Tests for a workflow's optional `owners:` and `contact:` blocks.

runnerlib parses and validates them and passes them in the workflow batch.
The coordinator uses them only from the trusted (base) definition.
"""

import json
import tempfile
from pathlib import Path

import pytest
import yaml
from typer.testing import CliRunner

from src.cli import app
from src.eval import load_workflow_definitions_with_errors, parse_workflow_definition

runner = CliRunner()


def _parse(owners=None, contact=None):
    data = {"id": "pay", "name": "Payments", "on": {"events": ["push"]}, "jobs": {}}
    if owners is not None:
        data["owners"] = owners
    if contact is not None:
        data["contact"] = contact
    return parse_workflow_definition(data, Path("/nonexistent"), "pay.yaml")


def test_no_blocks_means_no_owner_and_no_contact():
    wf = _parse()
    assert wf.owners is None
    assert wf.contact is None


def test_group_and_subjects_are_parsed():
    wf = _parse({"group": " payments-ci ", "subjects": ["vcs_user:github/alice", "vcs_user:github/alice"]})
    assert wf.owners == {"group": "payments-ci", "subjects": ["vcs_user:github/alice"]}


@pytest.mark.parametrize(
    "group, expected",
    [
        ("reactorcide_group:payments-ci", "payments-ci"),
        ("vcs_team:Acme/Payments", "vcs_team:acme/payments"),
    ],
)
def test_group_forms_are_normalized(group, expected):
    assert _parse({"group": group}).owners == {"group": expected}


def test_subjects_without_group_are_allowed():
    assert _parse({"subjects": ["principal:0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b"]}).owners == {
        "subjects": ["principal:0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b"]}


def test_contact_is_parsed_and_trimmed():
    wf = _parse(contact={"channel": " #payments-ci ", "help_url": "https://wiki.example/pay", "email": "pay@example.com"})
    assert wf.contact == {"channel": "#payments-ci", "help_url": "https://wiki.example/pay", "email": "pay@example.com"}


def test_empty_contact_values_are_dropped():
    assert _parse(contact={"channel": "  "}).contact is None


@pytest.mark.parametrize(
    "owners, message",
    [
        ("payments", "non-mapping 'owners'"),
        ({}, "must set 'group' or 'subjects'"),
        ({"group": "Payments CI"}, "Reactorcide group name"),
        ({"group": "vcs_team:acme"}, "vcs_team:ORG/SLUG"),
        ({"group": 5}, "must be a string"),
        ({"group": "x" * 201}, "longer than 200"),
        ({"group": "pay", "team": "x"}, "unknown field"),
        ({"subjects": "vcs_user:github/a"}, "must be a list"),
        ({"subjects": ["*"]}, "must name people"),
        ({"subjects": ["repository_write"]}, "must name people"),
        ({"subjects": ["workflow_owner"]}, "must name people"),
        ({"subjects": ["vcs_user:"]}, "must name people"),
        ({"subjects": [f"vcs_user:github/u{i}" for i in range(21)]}, "more than 20"),
    ],
)
def test_invalid_owners_are_rejected(owners, message):
    with pytest.raises(ValueError, match=message):
        _parse(owners=owners)


@pytest.mark.parametrize(
    "contact, message",
    [
        ("#pay", "non-mapping 'contact'"),
        ({"slack": "#pay"}, "unknown field"),
        ({"channel": "x" * 101}, "longer than 100"),
        ({"channel": "a\nb"}, "control character"),
        ({"help_url": "javascript:alert(1)"}, "http or https URL"),
        ({"help_url": "https://user@host/x"}, "http or https URL"),
        ({"email": "not-an-email"}, "plain email"),
        ({"email": "Pay <pay@example.com>"}, "plain email"),
        ({"email": 3}, "must be a string"),
    ],
)
def test_invalid_contact_is_rejected(contact, message):
    with pytest.raises(ValueError, match=message):
        _parse(contact=contact)


def test_invalid_owners_make_only_that_file_invalid():
    with tempfile.TemporaryDirectory() as tmpdir:
        root = Path(tmpdir)
        workflows = root / ".reactorcide" / "workflows"
        workflows.mkdir(parents=True)
        (workflows / "good.yaml").write_text(yaml.dump({"id": "good", "name": "Good", "on": {"events": ["push"]}, "jobs": {}}))
        (workflows / "bad.yaml").write_text(yaml.dump({
            "id": "bad", "name": "Bad", "on": {"events": ["push"]}, "jobs": {}, "owners": {"group": "Bad Group"},
        }))
        definitions, invalid = load_workflow_definitions_with_errors(root)
        assert [d.workflow_id for d in definitions] == ["good"]
        assert len(invalid) == 1
        assert invalid[0].workflow_id == "bad"


def test_eval_passes_owners_and_contact_in_the_workflow_batch():
    with tempfile.TemporaryDirectory() as tmpdir:
        tmp = Path(tmpdir)
        ci_dir, src_dir = tmp / "ci", tmp / "src"
        src_dir.mkdir()
        workflows = ci_dir / ".reactorcide" / "workflows"
        workflows.mkdir(parents=True)
        (workflows / "pay.yaml").write_text(yaml.dump({
            "id": "pay", "name": "Payments", "on": {"events": ["push"]},
            "owners": {"group": "payments-ci"}, "contact": {"channel": "#payments-ci"},
            "jobs": {"check": {"job": {"image": "alpine", "command": "make test"}}},
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
        assert batches["pay"]["owners"] == {"group": "payments-ci"}
        assert batches["pay"]["contact"] == {"channel": "#payments-ci"}
        assert "owners" not in batches["build"]
        assert "contact" not in batches["build"]


def test_unknown_top_level_keys_are_ignored():
    # An older runnerlib ignores unknown top-level keys; this one too. This
    # is the property that lets repositories add `owners:` before every
    # runnerlib in use knows it.
    data = {"id": "pay", "name": "Payments", "on": {"events": ["push"]}, "jobs": {}, "future_key": {"x": 1}}
    assert parse_workflow_definition(data, Path("/nonexistent"), "pay.yaml").workflow_id == "pay"
