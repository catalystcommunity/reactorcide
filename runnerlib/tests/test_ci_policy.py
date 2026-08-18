import pytest

from src.ci_policy import ci_paths, decide_workflow, load_coordinator_policy, path_matches


POLICY = """
version: 1
defaults:
  ci_source: base
  profile: standard
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


def test_coordinator_policy_authorizes_one_complete_rule():
    policy = load_coordinator_policy(POLICY)
    decision = decide_workflow(
        policy, "backend-tests", [".reactorcide/workflows/backend.yaml"],
        [".reactorcide/workflows/backend.yaml"], "pull_request_updated",
        "main", "fork", {"repository_write"}, [], "base", "head",
    )
    assert decision.allowed
    assert decision.ci_origin == "head"
    assert decision.profile == "pr-untrusted"


def test_unmatched_path_falls_back_to_base():
    policy = load_coordinator_policy(POLICY)
    decision = decide_workflow(
        policy, "backend-tests", [".reactorcide/jobs/release.yaml"],
        [".reactorcide/jobs/release.yaml"], "pull_request_updated",
        "main", "same", {"repository_write"}, [], "base", "head",
    )
    assert not decision.allowed
    assert decision.ci_origin == "base"


def test_vcs_user_can_authorize_head_ci():
    body = POLICY.replace("[repository_write]", "[vcs_user:github/junipuff]")
    policy = load_coordinator_policy(body)
    decision = decide_workflow(
        policy, "backend-tests", [".reactorcide/workflows/backend.yaml"],
        [".reactorcide/workflows/backend.yaml"], "pull_request_updated",
        "main", "same", {"vcs_user:github/junipuff"}, [], "base", "head",
    )
    assert decision.allowed
    assert decision.ci_origin == "head"


@pytest.mark.parametrize("value", ["/absolute", "../escape", "a/../b", "a//b", "a\\b", "./a"])
def test_policy_rejects_unsafe_paths(value):
    with pytest.raises(ValueError):
        load_coordinator_policy(POLICY.replace(".reactorcide/jobs/backend/**", value))


def test_double_star_does_not_broaden_other_paths():
    assert path_matches(".reactorcide/jobs/backend/**", ".reactorcide/jobs/backend/test.yaml")
    assert not path_matches(".reactorcide/jobs/backend/**", ".reactorcide/jobs/release.yaml")


def test_missing_coordinator_policy_has_no_head_authority():
    assert load_coordinator_policy("") is None


def test_old_repository_policy_paths_have_no_admission_effect():
    assert ci_paths([
        ".reactorcide/policy.yaml",
        ".reactorcide/policies/team.yaml",
        ".reactorcide/workflows/pr.yaml",
    ]) == [".reactorcide/workflows/pr.yaml"]


@pytest.mark.parametrize("replacement", [
    POLICY.replace("  - id: backend", "  - id: backend\n    unknown: true"),
    POLICY + POLICY.split("head_ci:\n", 1)[1],
    POLICY.replace("workflows: [backend-tests]", "workflows: [backend-tests, backend-tests]"),
])
def test_policy_rejects_unknown_and_duplicate_security_fields(replacement):
    with pytest.raises(ValueError):
        load_coordinator_policy(replacement)


def test_approval_is_bound_to_shas_revision_profile_and_workflow():
    body = POLICY.replace("    use:\n", "    approval:\n      any: [reactorcide_group:reviewers]\n    use:\n")
    policy = load_coordinator_policy(body)
    approval = {"approval_id": "approval-1", "head_sha": "head", "base_sha": "base",
                "policy_revision": policy.revision, "workflow_scope": "backend-tests",
                "execution_profile": "pr-untrusted", "approver_subject": "reactorcide_group:reviewers"}
    args = (policy, "backend-tests", [".reactorcide/workflows/backend.yaml"],
            [".reactorcide/workflows/backend.yaml"], "pull_request_updated", "main", "fork",
            {"repository_write"})
    assert decide_workflow(*args, [approval], "base", "head").allowed
    approval["head_sha"] = "new-head"
    assert not decide_workflow(*args, [approval], "base", "head").allowed
