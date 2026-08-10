"""
Eval module for Reactorcide job definition parsing and event matching.

This module provides:
- Job definition YAML parsing from .reactorcide/jobs/*.yaml
- Event type matching against job triggers
- Branch glob pattern matching
- Path include/exclude matching for changed files
- Trigger generation for matched job definitions

The eval module runs inside eval job containers. It reads job definitions
from the CI source repo, matches them against the current event, and
outputs a triggers.json file that the worker picks up to create child jobs.
"""

import fnmatch
import re
import sys
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any, Dict, List, Optional

import yaml

from src.workflow import JobTrigger


# Valid event types matching the Go EventType constants
VALID_EVENT_TYPES = frozenset({
    "push",
    "pull_request_opened",
    "pull_request_updated",
    "pull_request_merged",
    "pull_request_closed",
    "tag_created",
})


@dataclass
class PathsConfig:
    """Path include/exclude configuration for a job definition.

    Attributes:
        include: Glob patterns for files that trigger this job.
        exclude: Glob patterns for files to exclude from triggering.
    """
    include: List[str] = field(default_factory=list)
    exclude: List[str] = field(default_factory=list)


@dataclass
class TriggersConfig:
    """Trigger configuration for a job definition.

    Attributes:
        events: List of event types that trigger this job.
        branches: Optional list of branch glob patterns. Empty means all branches.
    """
    events: List[str] = field(default_factory=list)
    branches: List[str] = field(default_factory=list)


@dataclass
class JobConfig:
    """Container job configuration.

    Attributes:
        image: Container image to run.
        command: Command to execute in the container.
        timeout: Job timeout in seconds.
        priority: Job priority (higher = more important).
        raw_command: If True, run the command as-is without wrapping in runnerlib.
            By default, eval wraps commands with "runnerlib run --job-command" so
            that source checkout and other runnerlib features are available.
        disable_run_local: If True, the reactorcide CLI's run-local command
            refuses to execute this job. Used for jobs that fundamentally need
            CI-only state (PR diff base, push back to remote, etc.). Has no
            effect in CI — eval/trigger generation ignores it.
        capabilities: Runtime capabilities the job needs (e.g. "builder").
        depends_on: Workflow node names that must finish before this job.
        condition: Workflow condition evaluated after dependencies finish.
        for_each: Literal values used to expand one trigger into many nodes.
        item_var: Env var name for the current for_each value.
        code_dir: Container path where source code is mounted or checked out.
        job_dir: Container path runnerlib treats as the job directory.
        working_dir: Raw process working directory.
        run_as_user: Container user for deployed workers.
    """
    image: str = ""
    command: str = ""
    timeout: Optional[int] = None
    priority: Optional[int] = None
    raw_command: bool = False
    disable_run_local: bool = False
    capabilities: List[str] = field(default_factory=list)
    depends_on: List[str] = field(default_factory=list)
    condition: str = "all_success"
    for_each: List[Any] = field(default_factory=list)
    item_var: str = ""
    code_dir: str = ""
    job_dir: str = ""
    working_dir: str = ""
    run_as_user: str = ""


@dataclass
class JobDefinition:
    """A parsed job definition from a YAML file.

    Attributes:
        name: Unique name for this job definition.
        description: Human-readable description.
        triggers: Event and branch trigger configuration.
        paths: Optional path include/exclude configuration.
        job: Container execution configuration.
        environment: Environment variables to pass to the job.
        source_file: Path to the YAML file this was loaded from.
    """
    name: str
    description: str = ""
    triggers: TriggersConfig = field(default_factory=TriggersConfig)
    paths: PathsConfig = field(default_factory=PathsConfig)
    job: JobConfig = field(default_factory=JobConfig)
    environment: Dict[str, str] = field(default_factory=dict)
    source_file: Optional[str] = None


@dataclass
class WorkflowDefinition:
    """A parsed workflow definition from a .reactorcide/workflows/*.yaml file.

    A workflow names a pipeline and groups the jobs that run together for it.
    One event can match several workflows, so a repo can split its pipelines
    per team, org, or product. Each job is a fully resolved JobDefinition: a
    job entry can reference a reusable .reactorcide/jobs/*.yaml through
    ``job_file`` and overlay inline fields on top of it.

    Attributes:
        name: Human-readable workflow name (e.g. "Reactorcide PR").
        description: Human-readable description.
        triggers: Event and branch trigger configuration (from ``on:``).
        paths: Optional path include/exclude configuration.
        jobs: Resolved job definitions that make up this workflow.
        source_file: Path to the workflow YAML this was loaded from.
    """
    name: str
    workflow_id: str = ""
    explicit_id: bool = False
    description: str = ""
    triggers: TriggersConfig = field(default_factory=TriggersConfig)
    paths: PathsConfig = field(default_factory=PathsConfig)
    jobs: List[JobDefinition] = field(default_factory=list)
    source_file: Optional[str] = None


_SECURITY_ID_RE = re.compile(r"^[a-z0-9][a-z0-9-]{0,62}$")


def compatibility_workflow_id(source_file: Optional[str]) -> str:
    """Create a stable legacy ID from the workflow source path."""
    if not source_file:
        # Direct parser callers do not have a repository path. Real workflow
        # loading always supplies source_file, so this test/library fallback is
        # stable and does not depend on the display name.
        return "legacy-workflow"
    normalized = source_file.replace("\\", "/")
    marker = "/.reactorcide/workflows/"
    if marker in normalized:
        normalized = normalized.split(marker, 1)[1]
    normalized = re.sub(r"\.ya?ml$", "", normalized, flags=re.IGNORECASE)
    value = re.sub(r"[^a-z0-9]+", "-", normalized.lower()).strip("-")
    if not value:
        raise ValueError(f"Cannot derive a workflow id from {source_file!r}")
    return value


@dataclass
class EventContext:
    """Context about the current event for trigger generation.

    Attributes:
        event_type: The generic event type (e.g. "push", "pull_request_opened").
        branch: The branch name.
        source_url: URL of the source code repository (fork URL for cross-repo PRs).
        source_ref: Git ref (SHA) of the source code.
        ci_source_url: URL of the CI source repository (always trusted upstream).
        ci_source_ref: Git ref of the CI source.
        pr_base_ref: Base branch for pull requests.
        pr_number: Pull request number.
        head_url: Explicit head repo URL for PRs (== source_url; convenience).
        head_ref: PR head branch name (== pr ref for PRs).
        base_url: Upstream/base repo URL for PRs — useful when jobs need a
            second remote (e.g. for `git log base..head` operations).
        base_ref: Target branch for PRs (== pr_base_ref; convenience).
        is_fork_pr: "true" when the PR head is on a different repo than base.
    """
    event_type: str = ""
    branch: str = ""
    source_url: str = ""
    source_ref: str = ""
    ci_source_url: str = ""
    ci_source_ref: str = ""
    pr_base_ref: str = ""
    pr_number: str = ""
    head_url: str = ""
    head_ref: str = ""
    base_url: str = ""
    base_ref: str = ""
    is_fork_pr: str = ""


def _parse_triggers_config(data: Any) -> TriggersConfig:
    """Parse triggers config from YAML data."""
    if not isinstance(data, dict):
        return TriggersConfig()
    return TriggersConfig(
        events=data.get("events", []) or [],
        branches=data.get("branches", []) or [],
    )


def _parse_paths_config(data: Any) -> PathsConfig:
    """Parse paths config from YAML data."""
    if not isinstance(data, dict):
        return PathsConfig()
    return PathsConfig(
        include=data.get("include", []) or [],
        exclude=data.get("exclude", []) or [],
    )


def _parse_job_config(data: Any) -> JobConfig:
    """Parse job config from YAML data."""
    if not isinstance(data, dict):
        return JobConfig()
    run_as = data.get("run_as") or {}
    run_as_user = ""
    if isinstance(run_as, dict):
        run_as_user = str(run_as.get("user", "") or "")
    return JobConfig(
        image=data.get("image", "") or "",
        command=data.get("command", "") or "",
        timeout=data.get("timeout"),
        priority=data.get("priority"),
        raw_command=bool(data.get("raw_command", False)),
        disable_run_local=bool(data.get("disable_run_local", False)),
        capabilities=data.get("capabilities") or [],
        depends_on=data.get("depends_on") or [],
        condition=data.get("condition", "all_success") or "all_success",
        for_each=data.get("for_each") or [],
        item_var=data.get("item_var", "") or "",
        code_dir=data.get("code_dir", "") or "",
        job_dir=data.get("job_dir", "") or "",
        working_dir=data.get("working_dir", "") or "",
        run_as_user=run_as_user,
    )


def _parse_environment(data: Any) -> Dict[str, str]:
    """Parse environment variables, converting all values to strings."""
    if not isinstance(data, dict):
        return {}
    return {str(k): str(v) for k, v in data.items()}


def parse_job_definition(data: Dict[str, Any], source_file: Optional[str] = None) -> JobDefinition:
    """Parse a single job definition from a YAML dictionary.

    Args:
        data: Parsed YAML dictionary.
        source_file: Optional path to the source YAML file.

    Returns:
        A JobDefinition instance.

    Raises:
        ValueError: If required fields are missing.
    """
    name = data.get("name")
    if not name:
        raise ValueError(f"Job definition missing required 'name' field{f' in {source_file}' if source_file else ''}")

    return JobDefinition(
        name=str(name),
        description=str(data.get("description", "") or ""),
        triggers=_parse_triggers_config(data.get("triggers")),
        paths=_parse_paths_config(data.get("paths")),
        job=_parse_job_config(data.get("job")),
        environment=_parse_environment(data.get("environment")),
        source_file=source_file,
    )


def load_job_definitions(ci_source_path: Path) -> List[JobDefinition]:
    """Load all job definitions from the CI source directory.

    Reads all .yaml and .yml files from {ci_source_path}/.reactorcide/jobs/

    Args:
        ci_source_path: Path to the CI source checkout (e.g. /job/ci).

    Returns:
        List of parsed JobDefinition instances.
    """
    jobs_dir = ci_source_path / ".reactorcide" / "jobs"
    if not jobs_dir.is_dir():
        print(f"No job definitions directory found at {jobs_dir}", file=sys.stderr)
        return []

    definitions: List[JobDefinition] = []

    yaml_files = sorted(
        list(jobs_dir.glob("*.yaml")) + list(jobs_dir.glob("*.yml"))
    )

    for yaml_file in yaml_files:
        try:
            with open(yaml_file, "r") as f:
                data = yaml.safe_load(f)

            if not isinstance(data, dict):
                print(f"Skipping {yaml_file}: not a valid YAML mapping", file=sys.stderr)
                continue

            definition = parse_job_definition(data, source_file=str(yaml_file))
            definitions.append(definition)
        except yaml.YAMLError as e:
            print(f"Error parsing {yaml_file}: {e}", file=sys.stderr)
        except ValueError as e:
            print(f"Invalid job definition: {e}", file=sys.stderr)

    return definitions


def branch_matches(pattern: str, branch: str) -> bool:
    """Check if a branch name matches a glob pattern.

    Uses segment-based matching where * only matches within a single
    path segment (separated by /), and ** matches across segments.

    - "main" matches exactly "main"
    - "feature/*" matches "feature/foo" but not "feature/foo/bar"
    - "release/**" matches "release/1.0" and "release/1.0/rc1"
    - "*" matches any single-segment branch name
    - "**" matches any branch name regardless of depth

    Args:
        pattern: Glob pattern to match against.
        branch: Branch name to test.

    Returns:
        True if the branch matches the pattern.
    """
    pattern_parts = pattern.split("/")
    branch_parts = branch.split("/")
    return _match_segments(pattern_parts, branch_parts)


def _match_segments(pattern_parts: List[str], branch_parts: List[str]) -> bool:
    """Recursively match path segments with ** support.

    Args:
        pattern_parts: Pattern segments split by /.
        branch_parts: Branch name segments split by /.

    Returns:
        True if segments match.
    """
    if not pattern_parts and not branch_parts:
        return True
    if not pattern_parts:
        return False
    if not branch_parts:
        # Remaining pattern parts must all be ** to match empty remaining branch
        return all(p == "**" for p in pattern_parts)

    if pattern_parts[0] == "**":
        # ** matches zero or more segments
        # Try matching zero segments (skip **)
        if _match_segments(pattern_parts[1:], branch_parts):
            return True
        # Try matching one or more segments (consume one branch part, keep **)
        return _match_segments(pattern_parts, branch_parts[1:])

    if fnmatch.fnmatch(branch_parts[0], pattern_parts[0]):
        return _match_segments(pattern_parts[1:], branch_parts[1:])

    return False


def paths_match(paths_config: PathsConfig, changed_files: List[str]) -> bool:
    """Check if changed files match path include/exclude configuration.

    If no include patterns are specified, all files match. Exclude patterns
    are applied after include patterns.

    Args:
        paths_config: Path configuration with include and exclude patterns.
        changed_files: List of file paths that changed.

    Returns:
        True if any changed file matches the path configuration.
    """
    if not paths_config.include and not paths_config.exclude:
        return True

    if not changed_files:
        # If paths config exists but no changed files, nothing matches
        return bool(not paths_config.include and not paths_config.exclude)

    for file_path in changed_files:
        # Check include: if include patterns exist, file must match at least one
        if paths_config.include:
            included = any(
                _glob_match_path(pattern, file_path)
                for pattern in paths_config.include
            )
            if not included:
                continue

        # Check exclude: if file matches any exclude pattern, skip it
        if paths_config.exclude:
            excluded = any(
                _glob_match_path(pattern, file_path)
                for pattern in paths_config.exclude
            )
            if excluded:
                continue

        # File passed both include and exclude filters
        return True

    return False


def _glob_match_path(pattern: str, file_path: str) -> bool:
    """Match a file path against a glob pattern.

    Uses segment-based matching where * only matches within a single
    path segment, and ** matches across directory boundaries.

    Args:
        pattern: Glob pattern (e.g., "src/**", "*.md").
        file_path: File path to match.

    Returns:
        True if the path matches the pattern.
    """
    pattern_parts = pattern.split("/")
    path_parts = file_path.split("/")
    return _match_segments(pattern_parts, path_parts)


def evaluate_event(
    definitions: List[JobDefinition],
    event_type: str,
    branch: str = "",
    changed_files: Optional[List[str]] = None,
) -> List[JobDefinition]:
    """Evaluate which job definitions match the current event.

    Args:
        definitions: List of job definitions to evaluate.
        event_type: The generic event type (e.g., "push", "pull_request_opened").
        branch: Current branch name (optional).
        changed_files: List of changed file paths (optional).

    Returns:
        List of job definitions that match the event.
    """
    matched: List[JobDefinition] = []

    for defn in definitions:
        # Check event type
        if not defn.triggers.events:
            continue
        if event_type not in defn.triggers.events:
            continue

        # Check branch filter (empty means all branches match)
        if defn.triggers.branches and branch:
            branch_matched = any(
                branch_matches(pattern, branch)
                for pattern in defn.triggers.branches
            )
            if not branch_matched:
                continue

        # Check path filters
        if defn.paths.include or defn.paths.exclude:
            if changed_files is None:
                # No changed files info available, skip path filtering
                pass
            elif not paths_match(defn.paths, changed_files):
                continue

        matched.append(defn)

    return matched


def generate_triggers(
    matched_definitions: List[JobDefinition],
    event_context: EventContext,
) -> List[JobTrigger]:
    """Generate job triggers from matched definitions and event context.

    Creates a JobTrigger for each matched definition, populating it with
    the job's container config and the event's source information.

    Args:
        matched_definitions: Job definitions that matched the event.
        event_context: Context about the current event.

    Returns:
        List of JobTrigger instances ready for submission.
    """
    triggers: List[JobTrigger] = []

    for defn in matched_definitions:
        # Build environment from definition + event context
        env = dict(defn.environment)
        env["REACTORCIDE_EVENT_TYPE"] = event_context.event_type
        if event_context.branch:
            env["REACTORCIDE_BRANCH"] = event_context.branch
        if event_context.source_ref:
            env["REACTORCIDE_SHA"] = event_context.source_ref
        if event_context.source_url:
            env["REACTORCIDE_SOURCE_URL"] = event_context.source_url
        if event_context.pr_base_ref:
            env["REACTORCIDE_PR_BASE_REF"] = event_context.pr_base_ref
        if event_context.pr_number:
            env["REACTORCIDE_PR_NUMBER"] = event_context.pr_number
        if event_context.ci_source_url:
            env["REACTORCIDE_CI_SOURCE_URL"] = event_context.ci_source_url
        if event_context.ci_source_ref:
            env["REACTORCIDE_CI_SOURCE_REF"] = event_context.ci_source_ref
        if event_context.head_url:
            env["REACTORCIDE_HEAD_URL"] = event_context.head_url
        if event_context.head_ref:
            env["REACTORCIDE_HEAD_REF"] = event_context.head_ref
        if event_context.base_url:
            env["REACTORCIDE_BASE_URL"] = event_context.base_url
        if event_context.base_ref:
            env["REACTORCIDE_BASE_REF"] = event_context.base_ref
        if event_context.is_fork_pr:
            env["REACTORCIDE_IS_FORK_PR"] = event_context.is_fork_pr

        # By default, wrap the command with "runnerlib run --job-command" so that
        # runnerlib handles source checkout, CI source checkout, secret resolution,
        # and plugin execution. Job definitions can opt out with raw_command: true.
        command = defn.job.command or None
        if command and not defn.job.raw_command and not command.strip().startswith("runnerlib "):
            command = f"runnerlib run --job-command '{command}'"

        trigger = JobTrigger(
            job_name=defn.name,
            depends_on=defn.job.depends_on,
            condition=defn.job.condition,
            env=env,
            container_image=defn.job.image or None,
            job_command=command,
            priority=defn.job.priority,
            timeout=defn.job.timeout,
            capabilities=defn.job.capabilities or None,
            code_dir=defn.job.code_dir or None,
            job_dir=defn.job.job_dir or None,
            working_dir=defn.job.working_dir or None,
            run_as_user=defn.job.run_as_user or None,
            for_each=defn.job.for_each or None,
            item_var=defn.job.item_var or None,
            source_type="git" if event_context.source_url else None,
            source_url=event_context.source_url or None,
            source_ref=event_context.source_ref or None,
            ci_source_type="git" if event_context.ci_source_url else None,
            ci_source_url=event_context.ci_source_url or None,
            ci_source_ref=event_context.ci_source_ref or None,
        )
        triggers.append(trigger)

    return triggers


def _load_job_file_raw(ci_source_path: Path, job_file: str) -> tuple:
    """Load a referenced job YAML file as a raw dictionary.

    A workflow job entry can point at a reusable job definition through
    ``job_file``. The path is resolved relative to the CI source root first,
    then relative to ``.reactorcide/jobs/`` for convenience.

    Args:
        ci_source_path: Path to the CI source checkout.
        job_file: Path to the job YAML, relative to the CI source root.

    Returns:
        A (data, resolved_path) tuple.

    Raises:
        FileNotFoundError: If the file cannot be found.
        ValueError: If the file is not a YAML mapping.
    """
    raw_path = Path(job_file)
    if raw_path.is_absolute() or ".." in raw_path.parts:
        raise ValueError(f"job_file {job_file!r} must stay inside the CI checkout")
    root = ci_source_path.resolve()
    candidates = [ci_source_path / raw_path, ci_source_path / ".reactorcide" / "jobs" / raw_path]
    for candidate in candidates:
        resolved = candidate.resolve()
        try:
            resolved.relative_to(root)
        except ValueError as exc:
            raise ValueError(f"job_file {job_file!r} escapes the CI checkout") from exc
        if resolved.is_file():
            with open(resolved, "r") as f:
                data = yaml.safe_load(f)
            if not isinstance(data, dict):
                raise ValueError(f"job_file {resolved} is not a valid YAML mapping")
            return data, str(resolved)
    looked = ", ".join(str(c) for c in candidates)
    raise FileNotFoundError(f"job_file '{job_file}' not found (looked in: {looked})")


def _entry_to_job_dict(name: str, entry: Dict[str, Any]) -> Dict[str, Any]:
    """Normalize a workflow job entry into a job-file-shaped dictionary.

    Workflow job entries are ergonomic: container config fields can be written
    flat at the entry level (``image``, ``command``, ``depends_on`` ...) or
    nested under ``job:``. ``needs`` is accepted as an alias for
    ``depends_on``. The result uses the same shape parse_job_definition reads.
    """
    entry = dict(entry or {})
    out: Dict[str, Any] = {"name": name}

    if "description" in entry:
        out["description"] = entry.pop("description")
    if "environment" in entry:
        out["environment"] = entry.pop("environment")

    # A workflow's triggers/paths live at the workflow level, not per job.
    entry.pop("triggers", None)
    entry.pop("paths", None)
    entry.pop("job_file", None)

    job_cfg = dict(entry.pop("job", {}) or {})
    if "needs" in entry:
        entry["depends_on"] = entry.pop("needs")
    # Remaining flat keys are job config; explicit `job:` values win.
    for key, val in entry.items():
        job_cfg.setdefault(key, val)
    if job_cfg:
        out["job"] = job_cfg
    return out


def _parse_workflow_job(
    name: str,
    entry: Any,
    ci_source_path: Path,
    workflow_source_file: Optional[str],
) -> JobDefinition:
    """Parse and fully resolve a single job entry within a workflow.

    If the entry references a ``job_file``, its contents form the base and the
    inline entry fields overlay on top (deep-merged for ``job`` and
    ``environment``). Otherwise the entry is parsed directly.
    """
    entry = dict(entry or {})
    job_file = entry.get("job_file")

    base: Dict[str, Any] = {}
    resolved_from = None
    if job_file:
        base, resolved_from = _load_job_file_raw(ci_source_path, job_file)

    inline = _entry_to_job_dict(name, entry)

    merged: Dict[str, Any] = dict(base)
    for key, val in inline.items():
        if key in ("job", "environment") and isinstance(val, dict) and isinstance(merged.get(key), dict):
            merged[key] = {**merged[key], **val}
        else:
            merged[key] = val

    # The workflow entry key is the authoritative node name; a workflow never
    # inherits per-job triggers/paths from a referenced job file.
    merged["name"] = name
    merged.pop("triggers", None)
    merged.pop("paths", None)

    return parse_job_definition(merged, source_file=resolved_from or workflow_source_file)


def parse_workflow_definition(
    data: Dict[str, Any],
    ci_source_path: Path,
    source_file: Optional[str] = None,
) -> WorkflowDefinition:
    """Parse a single workflow definition from a YAML dictionary.

    Args:
        data: Parsed YAML dictionary.
        ci_source_path: Path to the CI source checkout (to resolve job_file refs).
        source_file: Optional path to the source YAML file.

    Returns:
        A WorkflowDefinition instance with fully resolved jobs.

    Raises:
        ValueError: If required fields are missing or malformed.
    """
    where = f" in {source_file}" if source_file else ""
    name = data.get("name")
    if not name:
        raise ValueError(f"Workflow definition missing required 'name' field{where}")

    workflow_id = str(data.get("id") or compatibility_workflow_id(source_file))
    if not _SECURITY_ID_RE.fullmatch(workflow_id):
        raise ValueError(
            f"Workflow id {workflow_id!r}{where} must contain 1 to 63 lowercase "
            "letters, digits, or hyphens, and must start with a letter or digit"
        )

    # `on:` is the workflow trigger block; accept `triggers:` as an alias.
    # NOTE: YAML 1.1 (PyYAML) parses the bare key `on` as the boolean True
    # (the same "Norway problem" GitHub Actions hits), so check True too.
    on = data.get("on")
    if on is None:
        on = data.get(True)
    if on is None:
        on = data.get("triggers")
    triggers = _parse_triggers_config(on)
    paths = _parse_paths_config(data.get("paths"))

    jobs_raw = data.get("jobs")
    job_defs: List[JobDefinition] = []
    if isinstance(jobs_raw, dict):
        for jname, jentry in jobs_raw.items():
            job_defs.append(_parse_workflow_job(str(jname), jentry, ci_source_path, source_file))
    elif isinstance(jobs_raw, list):
        for jentry in jobs_raw:
            jentry = jentry or {}
            jname = jentry.get("name")
            if not jname:
                raise ValueError(f"Workflow '{name}'{where} has a job entry without a 'name'")
            job_defs.append(_parse_workflow_job(str(jname), jentry, ci_source_path, source_file))
    else:
        raise ValueError(f"Workflow '{name}'{where} missing required 'jobs' mapping or list")

    return WorkflowDefinition(
        name=str(name),
        workflow_id=workflow_id,
        explicit_id=bool(data.get("id")),
        description=str(data.get("description", "") or ""),
        triggers=triggers,
        paths=paths,
        jobs=job_defs,
        source_file=source_file,
    )


def load_workflow_definitions(ci_source_path: Path) -> List[WorkflowDefinition]:
    """Load all workflow definitions from the CI source directory.

    Reads all .yaml and .yml files from {ci_source_path}/.reactorcide/workflows/

    Args:
        ci_source_path: Path to the CI source checkout (e.g. /job/ci).

    Returns:
        List of parsed WorkflowDefinition instances. Empty if the directory
        does not exist (the caller falls back to bare .reactorcide/jobs).
    """
    wf_dir = ci_source_path / ".reactorcide" / "workflows"
    if not wf_dir.is_dir():
        return []

    definitions: List[WorkflowDefinition] = []
    yaml_files = sorted(
        list(wf_dir.glob("*.yaml")) + list(wf_dir.glob("*.yml"))
    )

    for yaml_file in yaml_files:
        try:
            with open(yaml_file, "r") as f:
                data = yaml.safe_load(f)
            if not isinstance(data, dict):
                print(f"Skipping {yaml_file}: not a valid YAML mapping", file=sys.stderr)
                continue
            definitions.append(parse_workflow_definition(data, ci_source_path, source_file=str(yaml_file)))
        except yaml.YAMLError as e:
            print(f"Error parsing {yaml_file}: {e}", file=sys.stderr)
        except (ValueError, FileNotFoundError) as e:
            print(f"Invalid workflow definition in {yaml_file}: {e}", file=sys.stderr)

    return definitions


def workflow_match_reason(
    workflow: WorkflowDefinition,
    event_type: str,
    branch: str = "",
    changed_files: Optional[List[str]] = None,
) -> tuple:
    """Explain whether a workflow matches the current event.

    Returns a (matched, reason) tuple. The reason is a short human-readable
    string suitable for the eval log so people can see what was evaluated and
    why a workflow did or did not run.
    """
    t = workflow.triggers
    if not t.events:
        return False, "no events configured"
    if event_type not in t.events:
        return False, f"event '{event_type}' not in configured events {t.events}"
    if t.branches and branch:
        if not any(branch_matches(pattern, branch) for pattern in t.branches):
            return False, f"branch '{branch}' did not match {t.branches}"
    if workflow.paths.include or workflow.paths.exclude:
        if changed_files is None:
            pass  # No changed-file info available, do not filter on paths.
        elif not paths_match(workflow.paths, changed_files):
            return False, "no changed files matched the path filters"
    reason = f"event '{event_type}'"
    if branch:
        reason += f", branch '{branch}'"
    return True, reason


def evaluate_workflows(
    workflow_defs: List[WorkflowDefinition],
    event_type: str,
    branch: str = "",
    changed_files: Optional[List[str]] = None,
) -> List[WorkflowDefinition]:
    """Evaluate which workflows match the current event.

    Args:
        workflow_defs: Workflow definitions to evaluate.
        event_type: The generic event type (e.g. "push", "pull_request_opened").
        branch: Current branch name (optional).
        changed_files: List of changed file paths (optional).

    Returns:
        List of workflow definitions that match the event.
    """
    return [
        wf for wf in workflow_defs
        if workflow_match_reason(wf, event_type, branch, changed_files)[0]
    ]
