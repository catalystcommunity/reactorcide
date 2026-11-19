"""Scheduled evals: the coordinator fires a schedule that names one workflow.

A schedule eval gets ``REACTORCIDE_EVENT_TYPE=schedule`` and
``REACTORCIDE_WORKFLOW_REF``. The ref is an explicit workflow ID, or a workflow
file path under ``.reactorcide/workflows/``. Only that workflow is evaluated,
and its ``on``/``paths`` filters do not apply: the schedule chose it. Every
other workflow file gets no decision. When the ref names nothing on the
branch, the eval reports one ``invalid`` decision ("workflow not found") and
succeeds, so the coordinator can show the error on the schedule.
"""

import os
from pathlib import Path
from typing import Dict, List, Mapping, Optional, Sequence, Tuple

SCHEDULE_EVENT = "schedule"
WORKFLOW_REF_ENV = "REACTORCIDE_WORKFLOW_REF"
WORKFLOW_NOT_FOUND = "workflow not found"


def workflow_ref_from_env(environ: Optional[Mapping[str, str]] = None) -> str:
    """Return the schedule's workflow ref, or "" for an ordinary eval."""
    env = os.environ if environ is None else environ
    return (env.get(WORKFLOW_REF_ENV) or "").strip()


def schedule_reason_from_env(environ: Optional[Mapping[str, str]] = None) -> Dict:
    """Build the ``schedule`` decision reason from the fire's env vars."""
    env = os.environ if environ is None else environ
    schedule_id = (env.get("REACTORCIDE_SCHEDULE_ID") or "").strip()
    name = (env.get("REACTORCIDE_SCHEDULE_NAME") or "").strip()
    cron = (env.get("REACTORCIDE_SCHEDULE_CRON") or "").strip()
    fire_time = (env.get("REACTORCIDE_SCHEDULE_FIRE_TIME") or "").strip()
    reason: Dict = {"kind": "schedule", "matched": True}
    if cron:
        reason["value"] = cron
    if schedule_id:
        reason["schedule_id"] = schedule_id
    if fire_time:
        reason["fire_time"] = fire_time
    label = f"schedule '{name}'" if name else "schedule"
    reason["detail"] = f"{label} fired at {fire_time}" if fire_time else f"{label} fired"
    return reason


def selection_reason_from_env(event_type: str, workflow_ref: str,
                              environ: Optional[Mapping[str, str]] = None) -> Dict:
    """Build the reason of a workflow that REACTORCIDE_WORKFLOW_REF selected.

    A schedule fire gets the ``schedule`` reason. A manual run on a ref (the
    coordinator sets REACTORCIDE_EVENT_SOURCE=manual) that names one workflow
    gets an ``event`` reason that says a manual run selected it.
    """
    env = os.environ if environ is None else environ
    if event_type == SCHEDULE_EVENT or (env.get("REACTORCIDE_EVENT_SOURCE") or "").strip() != "manual":
        return schedule_reason_from_env(env)
    return {
        "kind": "event",
        "matched": True,
        "value": "manual",
        "patterns": ["manual"],
        "detail": f"a manual run selected workflow '{workflow_ref}'",
    }


def _relative(path_value: Optional[str], root: Path) -> str:
    if not path_value:
        return ""
    try:
        return str(Path(path_value).resolve().relative_to(root.resolve())).replace("\\", "/")
    except ValueError:
        return ""


def _matches(ref: str, workflow_id: str, source_file: Optional[str], root: Path) -> bool:
    if "/" in ref:
        return _relative(source_file, root) == ref
    return bool(workflow_id) and workflow_id == ref


def select_scheduled_workflow(
    workflow_ref: str,
    workflow_defs: Sequence,
    invalid_files: Sequence,
    ci_source_path: Path,
) -> Tuple[List, List]:
    """Keep only the workflow (or invalid file) the ref names."""
    defs = [wf for wf in workflow_defs
            if _matches(workflow_ref, wf.workflow_id, wf.source_file, ci_source_path)]
    invalid = [item for item in invalid_files
               if _matches(workflow_ref, item.workflow_id, item.source_file, ci_source_path)]
    return defs, invalid


def not_found_decision(workflow_ref: str) -> Dict:
    """The decision for a ref that names no workflow on the branch."""
    return {
        "id": f"ref:{workflow_ref}",
        "name": workflow_ref,
        "source_file": workflow_ref if "/" in workflow_ref else "",
        "decision": "invalid",
        "reasons": [{"kind": "invalid", "detail": WORKFLOW_NOT_FOUND}],
    }
