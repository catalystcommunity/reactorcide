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
    """
    image: str = ""
    command: str = ""
    timeout: Optional[int] = None
    priority: Optional[int] = None
    raw_command: bool = False


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
class EventContext:
    """Context about the current event for trigger generation.

    Attributes:
        event_type: The generic event type (e.g. "push", "pull_request_opened").
        branch: The branch name.
        source_url: URL of the source code repository.
        source_ref: Git ref (SHA) of the source code.
        ci_source_url: URL of the CI source repository.
        ci_source_ref: Git ref of the CI source.
        pr_base_ref: Base branch for pull requests.
        pr_number: Pull request number.
    """
    event_type: str = ""
    branch: str = ""
    source_url: str = ""
    source_ref: str = ""
    ci_source_url: str = ""
    ci_source_ref: str = ""
    pr_base_ref: str = ""
    pr_number: str = ""


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
    return JobConfig(
        image=data.get("image", "") or "",
        command=data.get("command", "") or "",
        timeout=data.get("timeout"),
        priority=data.get("priority"),
        raw_command=bool(data.get("raw_command", False)),
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

        # By default, wrap the command with "runnerlib run --job-command" so that
        # runnerlib handles source checkout, CI source checkout, secret resolution,
        # and plugin execution. Job definitions can opt out with raw_command: true.
        command = defn.job.command or None
        if command and not defn.job.raw_command and not command.strip().startswith("runnerlib "):
            command = f"runnerlib run --job-command '{command}'"

        trigger = JobTrigger(
            job_name=defn.name,
            env=env,
            container_image=defn.job.image or None,
            job_command=command,
            priority=defn.job.priority,
            timeout=defn.job.timeout,
            source_type="git" if event_context.source_url else None,
            source_url=event_context.source_url or None,
            source_ref=event_context.source_ref or None,
            ci_source_type="git" if event_context.ci_source_url else None,
            ci_source_url=event_context.ci_source_url or None,
            ci_source_ref=event_context.ci_source_ref or None,
        )
        triggers.append(trigger)

    return triggers
