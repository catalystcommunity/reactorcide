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


NODE_AUTHORITY_POLICY = """version: 1
defaults: {ci_source: base, profile: standard}
head_ci:
- id: csilgen
  actors: {any: [repository_write]}
  workflows: [csilgen-pr]
  paths: ['.reactorcide/**']
  events: [pull_request_opened, pull_request_updated]
  base_branches: [main]
  head_repository: any
  use:
    ci_source: head
    profile: pr-untrusted
    workers: default
    base_nodes:
    - nodes: [asset-prepare, asset-seal]
      ci_source: base
      profile: standard
      workers: default
"""


def test_base_nodes_decision_and_revision_match_coordinator():
    policy = load_coordinator_policy(NODE_AUTHORITY_POLICY)
    # Golden value shared with the Go cipolicy package
    # (TestNodeAuthorityRevisionMatchesRunnerlibCanonicalForm).
    assert policy.revision == "d8cdd4911ee9b9e51e369d447deeffaa0f0c80cfe744a43e01ebe556cbc1a04b"
    decision = decide_workflow(
        policy, "csilgen-pr", [".reactorcide/workflows/pr.yaml"],
        [".reactorcide/workflows/pr.yaml"], "pull_request_updated",
        "main", "same", {"repository_write"}, [], "base", "head",
    )
    assert decision.allowed
    assert decision.base_nodes == {
        "asset-prepare": {"ci_source": "base", "profile": "standard", "worker_class": "default"},
        "asset-seal": {"ci_source": "base", "profile": "standard", "worker_class": "default"},
    }
    # A policy without base_nodes keeps an empty node authority map.
    plain = load_coordinator_policy(POLICY)
    plain_decision = decide_workflow(
        plain, "backend-tests", [".reactorcide/workflows/backend.yaml"],
        [".reactorcide/workflows/backend.yaml"], "pull_request_updated",
        "main", "same", {"repository_write"}, [], "base", "head",
    )
    assert plain_decision.allowed
    assert plain_decision.base_nodes == {}


@pytest.mark.parametrize("replacement", [
    ("- nodes: [asset-prepare, asset-seal]", "- nodes: []"),
    ("- nodes: [asset-prepare, asset-seal]", "- nodes: ['bad name']"),
    ("      ci_source: base\n      profile: standard\n      workers: default",
     "      ci_source: head\n      profile: standard\n      workers: default"),
    ("      profile: standard\n      workers: default", "      profile: ''\n      workers: default"),
    ("      profile: standard\n      workers: default", "      profile: standard\n      workers: ''"),
])
def test_base_nodes_validation_rejects_invalid_entries(replacement):
    old, new = replacement
    body = NODE_AUTHORITY_POLICY.replace(old, new)
    assert body != NODE_AUTHORITY_POLICY
    with pytest.raises(ValueError):
        load_coordinator_policy(body)


def test_base_nodes_do_not_change_plain_policy_revision():
    with_nodes = load_coordinator_policy(NODE_AUTHORITY_POLICY)
    marker = "    base_nodes:\n    - nodes: [asset-prepare, asset-seal]\n      ci_source: base\n      profile: standard\n      workers: default\n"
    without_nodes = load_coordinator_policy(NODE_AUTHORITY_POLICY.replace(marker, ""))
    assert with_nodes.revision != without_nodes.revision
    # The canonical form of a policy without node authority has no base_nodes
    # key at all, so pre-existing policies keep their stored revision.
    assert "base_nodes" not in str(without_nodes.raw)


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
