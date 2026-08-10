from pathlib import Path

import pytest

from src.ci_policy import decide_workflow, load_trusted_policy, path_matches


POLICY = """
version: 1
defaults:
  ci_source: base
  profile: standard
policy_maintainers:
  any: [reactorcide_group:ci-admins]
head_ci:
  - id: backend
    actors:
      any: [repository_write]
    workflows: [backend-tests]
    paths:
      - .reactorcide/workflows/backend.yaml
      - .reactorcide/jobs/backend/**
    events: [pull_request_updated]
    base_branches: [main]
    head_repository: any
    use:
      ci_source: head
      profile: pr-untrusted
      workers: default
"""


def write_policy(root: Path, body: str = POLICY):
    policy = root / ".reactorcide" / "policy.yaml"
    policy.parent.mkdir(parents=True)
    policy.write_text(body)


def test_trusted_policy_authorizes_one_complete_rule(tmp_path):
    write_policy(tmp_path)
    policy = load_trusted_policy(tmp_path)
    decision = decide_workflow(
        policy, "backend-tests", [".reactorcide/workflows/backend.yaml"],
        [".reactorcide/workflows/backend.yaml"], "pull_request_updated",
        "main", "fork", {"repository_write"}, [], "base", "head",
    )
    assert decision.allowed
    assert decision.ci_origin == "head"
    assert decision.profile == "pr-untrusted"


def test_unmatched_path_falls_back_to_base(tmp_path):
    write_policy(tmp_path)
    policy = load_trusted_policy(tmp_path)
    decision = decide_workflow(
        policy, "backend-tests", [".reactorcide/jobs/release.yaml"],
        [".reactorcide/jobs/release.yaml"], "pull_request_updated",
        "main", "same", {"repository_write"}, [], "base", "head",
    )
    assert not decision.allowed
    assert decision.ci_origin == "base"


@pytest.mark.parametrize("value", ["/absolute", "../escape", "a/../b", "a//b", "a\\b", "./a"])
def test_policy_rejects_unsafe_paths(tmp_path, value):
    write_policy(tmp_path, POLICY.replace(".reactorcide/jobs/backend/**", value))
    with pytest.raises(ValueError):
        load_trusted_policy(tmp_path)


def test_double_star_does_not_broaden_other_paths():
    assert path_matches(".reactorcide/jobs/backend/**", ".reactorcide/jobs/backend/test.yaml")
    assert not path_matches(".reactorcide/jobs/backend/**", ".reactorcide/jobs/release.yaml")


def test_policy_symlink_cannot_escape_trusted_base(tmp_path):
    outside = tmp_path.parent / "outside-policy.yaml"
    outside.write_text(POLICY)
    policy = tmp_path / ".reactorcide" / "policy.yaml"
    policy.parent.mkdir(parents=True)
    policy.symlink_to(outside)
    with pytest.raises(ValueError, match="escapes trusted base"):
        load_trusted_policy(tmp_path)


@pytest.mark.parametrize("replacement", [
    POLICY.replace("  - id: backend", "  - id: backend\n    unknown: true"),
    POLICY + POLICY.split("head_ci:\n", 1)[1],
    POLICY.replace("workflows: [backend-tests]", "workflows: [backend-tests, backend-tests]"),
])
def test_policy_rejects_unknown_and_duplicate_security_fields(tmp_path, replacement):
    write_policy(tmp_path, replacement)
    with pytest.raises(ValueError):
        load_trusted_policy(tmp_path)


def test_approval_is_bound_to_shas_revision_profile_and_workflow(tmp_path):
    body = POLICY.replace("    use:\n", "    approval:\n      any: [reactorcide_group:reviewers]\n    use:\n")
    write_policy(tmp_path, body)
    policy = load_trusted_policy(tmp_path)
    approval = {"approval_id": "approval-1", "head_sha": "head", "base_sha": "base",
                "policy_revision": policy.revision, "workflow_scope": "backend-tests",
                "execution_profile": "pr-untrusted", "approver_subject": "reactorcide_group:reviewers"}
    args = (policy, "backend-tests", [".reactorcide/workflows/backend.yaml"],
            [".reactorcide/workflows/backend.yaml"], "pull_request_updated", "main", "fork",
            {"repository_write"})
    assert decide_workflow(*args, [approval], "base", "head").allowed
    approval["head_sha"] = "new-head"
    assert not decide_workflow(*args, [approval], "base", "head").allowed
