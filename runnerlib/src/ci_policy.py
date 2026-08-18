"""Trusted-base CI admission policy evaluation."""

from __future__ import annotations

import hashlib
import json
import re
from dataclasses import dataclass, field
from typing import Any, Dict, Iterable, List, Optional, Set

import yaml


ID_RE = re.compile(r"^[a-z0-9][a-z0-9-]{0,62}$")
SUBJECT_PREFIXES = ("vcs_team:", "vcs_user:", "reactorcide_group:")


@dataclass
class PolicyDecision:
    ci_origin: str = "base"
    profile: str = "standard"
    worker_class: str = "default"
    rule_id: str = ""
    approval_id: str = ""
    allowed: bool = False
    reasons: List[str] = field(default_factory=list)


@dataclass
class TrustedPolicy:
    raw: Dict[str, Any]
    revision: str
    rules: List[Dict[str, Any]]
    defaults: Dict[str, str]


def _safe_relative_path(value: str) -> str:
    if "\\" in value:
        raise ValueError(f"unsafe repository path {value!r}")
    if not value or value.startswith("/"):
        raise ValueError(f"unsafe repository path {value!r}")
    parts = value.split("/")
    if any(part in ("", ".", "..") for part in parts):
        raise ValueError(f"unsafe repository path {value!r}")
    return value


def _known_keys(value: Dict[str, Any], allowed: Iterable[str], where: str) -> None:
    unknown = sorted(set(value) - set(allowed))
    if unknown:
        raise ValueError(f"{where} has unknown fields: {', '.join(unknown)}")


def _subjects(value: Any, where: str, allow_owner: bool = False) -> List[str]:
    if value is None:
        return []
    if not isinstance(value, dict):
        raise ValueError(f"{where} must be a mapping")
    _known_keys(value, ("any",), where)
    subjects = value.get("any") or []
    if not isinstance(subjects, list):
        raise ValueError(f"{where}.any must be a list")
    for subject in subjects:
        valid = subject == "repository_write" or (allow_owner and subject == "project_owner")
        valid = valid or any(str(subject).startswith(prefix) and len(str(subject)) > len(prefix) for prefix in SUBJECT_PREFIXES)
        if not valid:
            raise ValueError(f"{where} has unknown subject {subject!r}")
    return [str(subject) for subject in subjects]


def _canonical_policy(value: Dict[str, Any]) -> str:
    encoded = json.dumps(value, sort_keys=True, separators=(",", ":")).encode()
    return hashlib.sha256(encoded).hexdigest()


def load_coordinator_policy(document: str) -> Optional[TrustedPolicy]:
    """Load a policy document that the coordinator pinned to this eval job."""
    if not document:
        return None
    data = yaml.safe_load(document) or {}
    if not isinstance(data, dict):
        raise ValueError("coordinator CI policy must contain a mapping")
    _known_keys(data, ("version", "defaults", "head_ci"), "coordinator CI policy")
    if data.get("version") != 1:
        raise ValueError("coordinator CI policy version must be 1")

    rules: List[Dict[str, Any]] = []
    seen_rules: Set[str] = set()
    seen_workflows: Set[str] = set()
    for rule in data.get("head_ci") or []:
        if not isinstance(rule, dict):
            raise ValueError("coordinator CI policy head_ci entries must be mappings")
        _known_keys(rule, ("id", "actors", "workflows", "paths", "events", "base_branches", "head_repository", "approval", "use"), "coordinator CI policy rule")
        rule_id = str(rule.get("id") or "")
        if not ID_RE.fullmatch(rule_id) or rule_id in seen_rules:
            raise ValueError(f"invalid or duplicate policy rule id {rule_id!r}")
        seen_rules.add(rule_id)
        workflows = rule.get("workflows") or []
        paths = rule.get("paths") or []
        if not workflows or not paths:
            raise ValueError(f"rule {rule_id} must select workflows and paths")
        for workflow_id in workflows:
            if not ID_RE.fullmatch(str(workflow_id)) or workflow_id in seen_workflows:
                raise ValueError(f"invalid or duplicate workflow security id {workflow_id!r}")
            seen_workflows.add(str(workflow_id))
        for path_value in paths:
            _safe_relative_path(str(path_value))
        _subjects(rule.get("actors"), f"rule {rule_id} actors")
        _subjects(rule.get("approval"), f"rule {rule_id} approval", allow_owner=True)
        relation = rule.get("head_repository") or "same"
        if relation not in ("same", "fork", "any"):
            raise ValueError(f"rule {rule_id} has invalid head_repository")
        use = rule.get("use") or {}
        _known_keys(use, ("ci_source", "profile", "workers"), f"rule {rule_id} use")
        if use.get("ci_source") != "head" or not use.get("profile") or not use.get("workers"):
            raise ValueError(f"rule {rule_id} must select head, profile, and workers")
        rules.append(rule)

    defaults = data.get("defaults") or {}
    _known_keys(defaults, ("ci_source", "profile"), "defaults")
    if defaults.get("ci_source", "base") != "base":
        raise ValueError("default ci_source must be base")
    canonical_rules = []
    for rule in rules:
        use = rule.get("use") or {}
        canonical_rules.append({
            "actors": {"any": _subjects(rule.get("actors"), "actors")},
            "approval": {"any": _subjects(rule.get("approval"), "approval", allow_owner=True)},
            "base_branches": [str(item) for item in rule.get("base_branches") or []],
            "events": [str(item) for item in rule.get("events") or []],
            "head_repository": str(rule.get("head_repository") or "same"),
            "id": str(rule["id"]),
            "paths": [str(item) for item in rule.get("paths") or []],
            "use": {"ci_source": "head", "profile": str(use["profile"]), "workers": str(use["workers"])},
            "workflows": [str(item) for item in rule.get("workflows") or []],
        })
    combined = {
        "defaults": {"ci_source": "base", "profile": str(defaults.get("profile") or "standard")},
        "head_ci": canonical_rules,
        "version": 1,
    }
    return TrustedPolicy(
        raw=combined,
        revision=_canonical_policy(combined),
        rules=rules,
        defaults={"ci_source": "base", "profile": str(defaults.get("profile") or "standard")},
    )


def path_matches(pattern: str, value: str) -> bool:
    regex = ""
    index = 0
    while index < len(pattern):
        char = pattern[index]
        if char == "*":
            if index + 1 < len(pattern) and pattern[index + 1] == "*":
                regex += ".*"
                index += 2
                continue
            regex += "[^/]*"
        elif char == "?":
            regex += "[^/]"
        else:
            regex += re.escape(char)
        index += 1
    return re.fullmatch(regex, value) is not None


def _any_subject(required: List[str], facts: Set[str]) -> bool:
    return not required or any(subject in facts for subject in required)


def decide_workflow(
    policy: TrustedPolicy,
    workflow_id: str,
    dependency_paths: List[str],
    changed_ci_paths: List[str],
    event: str,
    base_branch: str,
    relation: str,
    actor_subjects: Set[str],
    approvals: List[Dict[str, Any]],
    base_sha: str,
    head_sha: str,
) -> PolicyDecision:
    """Select one atomic CI origin from coordinator policy."""
    base = PolicyDecision(profile=policy.defaults["profile"], reasons=["trusted base CI continues"])
    candidates: List[PolicyDecision] = []
    relevant_paths = sorted(set(changed_ci_paths).intersection(dependency_paths))
    for rule in policy.rules:
        if workflow_id not in [str(item) for item in rule.get("workflows") or []]:
            continue
        patterns = [str(item) for item in rule.get("paths") or []]
        if not relevant_paths:
            continue
        if not all(any(path_matches(pattern, item) for pattern in patterns) for item in dependency_paths):
            continue
        if not all(any(path_matches(pattern, item) for pattern in patterns) for item in changed_ci_paths):
            continue
        if rule.get("events") and event not in rule["events"]:
            continue
        if rule.get("base_branches") and not any(path_matches(str(item), base_branch) for item in rule["base_branches"]):
            continue
        required_relation = rule.get("head_repository") or "same"
        if required_relation not in ("any", relation):
            continue
        if not _any_subject(_subjects(rule.get("actors"), "actors"), actor_subjects):
            continue
        approval_id = ""
        required_approvers = _subjects(rule.get("approval"), "approval", allow_owner=True)
        if required_approvers:
            for approval in approvals:
                if (
                    approval.get("head_sha") == head_sha
                    and approval.get("base_sha") == base_sha
                    and approval.get("policy_revision") == policy.revision
                    and approval.get("workflow_scope") in (workflow_id, "*")
                    and approval.get("execution_profile") == rule["use"]["profile"]
                    and approval.get("approver_subject") in required_approvers
                ):
                    approval_id = str(approval.get("approval_id") or "")
                    break
            if not approval_id:
                continue
        candidates.append(PolicyDecision(
            ci_origin="head",
            profile=str(rule["use"]["profile"]),
            worker_class=str(rule["use"]["workers"]),
            rule_id=str(rule["id"]),
            approval_id=approval_id,
            allowed=True,
        ))
    if not candidates:
        base.reasons.insert(0, "no complete coordinator rule authorized head CI")
        return base
    selected = candidates[0]
    for candidate in candidates[1:]:
        if (candidate.profile, candidate.worker_class) != (selected.profile, selected.worker_class):
            raise ValueError(f"ambiguous authority for workflow {workflow_id!r}")
    return selected


def ci_paths(changed_paths: Optional[List[str]]) -> List[str]:
    result = set()
    for value in changed_paths or []:
        if not value.startswith(".reactorcide/"):
            continue
        path = _safe_relative_path(value)
        if path == ".reactorcide/policy.yaml" or path.startswith(".reactorcide/policies/"):
            continue
        result.add(path)
    return sorted(result)
