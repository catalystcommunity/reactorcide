"""
Tests for the eval module - job definition parsing and event matching.
"""

import os
import tempfile
from pathlib import Path

import pytest
import yaml

from src.eval import (
    EventContext,
    JobConfig,
    JobDefinition,
    PathsConfig,
    TriggersConfig,
    VALID_EVENT_TYPES,
    branch_matches,
    evaluate_event,
    generate_triggers,
    load_job_definitions,
    parse_job_definition,
    paths_match,
)
from src.workflow import JobTrigger


# --- Fixtures ---


@pytest.fixture
def temp_ci_dir():
    """Create a temporary CI source directory with .reactorcide/jobs/ structure."""
    with tempfile.TemporaryDirectory() as tmpdir:
        jobs_dir = Path(tmpdir) / ".reactorcide" / "jobs"
        jobs_dir.mkdir(parents=True)
        yield Path(tmpdir), jobs_dir


def _write_yaml(path: Path, data: dict) -> Path:
    """Helper to write a YAML file."""
    with open(path, "w") as f:
        yaml.dump(data, f)
    return path


# --- Test parse_job_definition ---


class TestParseJobDefinition:
    """Tests for parsing a single job definition from YAML data."""

    def test_minimal_definition(self):
        """Test parsing a definition with only the required name field."""
        data = {"name": "test"}
        defn = parse_job_definition(data)

        assert defn.name == "test"
        assert defn.description == ""
        assert defn.triggers.events == []
        assert defn.triggers.branches == []
        assert defn.paths.include == []
        assert defn.paths.exclude == []
        assert defn.job.image == ""
        assert defn.job.command == ""
        assert defn.job.timeout is None
        assert defn.job.priority is None
        assert defn.environment == {}
        assert defn.source_file is None

    def test_full_definition(self):
        """Test parsing a fully-specified definition."""
        data = {
            "name": "test",
            "description": "Run tests on PRs",
            "triggers": {
                "events": ["pull_request_opened", "pull_request_updated"],
                "branches": ["main", "feature/*"],
            },
            "paths": {
                "include": ["src/**", "tests/**"],
                "exclude": ["docs/**", "*.md"],
            },
            "job": {
                "image": "alpine:latest",
                "command": "make test",
                "timeout": 1800,
                "priority": 10,
            },
            "environment": {
                "BUILD_TYPE": "test",
                "VERBOSE": "true",
            },
        }
        defn = parse_job_definition(data, source_file="/path/to/test.yaml")

        assert defn.name == "test"
        assert defn.description == "Run tests on PRs"
        assert defn.triggers.events == ["pull_request_opened", "pull_request_updated"]
        assert defn.triggers.branches == ["main", "feature/*"]
        assert defn.paths.include == ["src/**", "tests/**"]
        assert defn.paths.exclude == ["docs/**", "*.md"]
        assert defn.job.image == "alpine:latest"
        assert defn.job.command == "make test"
        assert defn.job.timeout == 1800
        assert defn.job.priority == 10
        assert defn.environment == {"BUILD_TYPE": "test", "VERBOSE": "true"}
        assert defn.source_file == "/path/to/test.yaml"

    def test_missing_name_raises_error(self):
        """Test that a missing name field raises ValueError."""
        with pytest.raises(ValueError, match="missing required 'name' field"):
            parse_job_definition({})

    def test_missing_name_with_source_file(self):
        """Test error message includes source file when provided."""
        with pytest.raises(ValueError, match="in /some/file.yaml"):
            parse_job_definition({}, source_file="/some/file.yaml")

    def test_empty_name_raises_error(self):
        """Test that an empty name raises ValueError."""
        with pytest.raises(ValueError, match="missing required 'name' field"):
            parse_job_definition({"name": ""})

    def test_none_name_raises_error(self):
        """Test that a None name raises ValueError."""
        with pytest.raises(ValueError, match="missing required 'name' field"):
            parse_job_definition({"name": None})

    def test_environment_values_converted_to_strings(self):
        """Test that environment values are converted to strings."""
        data = {
            "name": "test",
            "environment": {
                "PORT": 8080,
                "DEBUG": True,
                "FLOAT": 1.5,
            },
        }
        defn = parse_job_definition(data)

        assert defn.environment == {"PORT": "8080", "DEBUG": "True", "FLOAT": "1.5"}

    def test_null_optional_fields(self):
        """Test handling of null/None optional fields in YAML."""
        data = {
            "name": "test",
            "triggers": None,
            "paths": None,
            "job": None,
            "environment": None,
        }
        defn = parse_job_definition(data)

        assert defn.name == "test"
        assert defn.triggers.events == []
        assert defn.paths.include == []
        assert defn.job.image == ""
        assert defn.environment == {}

    def test_partial_triggers(self):
        """Test triggers with only events, no branches."""
        data = {
            "name": "test",
            "triggers": {
                "events": ["push"],
            },
        }
        defn = parse_job_definition(data)

        assert defn.triggers.events == ["push"]
        assert defn.triggers.branches == []

    def test_partial_job_config(self):
        """Test job config with only some fields."""
        data = {
            "name": "test",
            "job": {
                "image": "alpine:latest",
            },
        }
        defn = parse_job_definition(data)

        assert defn.job.image == "alpine:latest"
        assert defn.job.command == ""
        assert defn.job.timeout is None

    def test_raw_command_parsed(self):
        """Test that raw_command is parsed from job config."""
        data = {
            "name": "test",
            "job": {
                "image": "alpine:latest",
                "command": "echo hello",
                "raw_command": True,
            },
        }
        defn = parse_job_definition(data)

        assert defn.job.raw_command is True

    def test_raw_command_defaults_false(self):
        """Test that raw_command defaults to False."""
        data = {
            "name": "test",
            "job": {
                "image": "alpine:latest",
                "command": "echo hello",
            },
        }
        defn = parse_job_definition(data)

        assert defn.job.raw_command is False


# --- Test load_job_definitions ---


class TestLoadJobDefinitions:
    """Tests for loading job definitions from the filesystem."""

    def test_load_single_yaml(self, temp_ci_dir):
        """Test loading a single YAML file."""
        ci_path, jobs_dir = temp_ci_dir
        _write_yaml(jobs_dir / "test.yaml", {
            "name": "test",
            "triggers": {"events": ["push"]},
            "job": {"image": "alpine:latest", "command": "make test"},
        })

        definitions = load_job_definitions(ci_path)

        assert len(definitions) == 1
        assert definitions[0].name == "test"
        assert definitions[0].triggers.events == ["push"]

    def test_load_multiple_yaml_files(self, temp_ci_dir):
        """Test loading multiple YAML files in sorted order."""
        ci_path, jobs_dir = temp_ci_dir
        _write_yaml(jobs_dir / "build.yaml", {"name": "build"})
        _write_yaml(jobs_dir / "test.yaml", {"name": "test"})
        _write_yaml(jobs_dir / "deploy.yml", {"name": "deploy"})

        definitions = load_job_definitions(ci_path)

        assert len(definitions) == 3
        names = [d.name for d in definitions]
        # .yaml files come before .yml in sorted order
        assert "build" in names
        assert "test" in names
        assert "deploy" in names

    def test_load_no_directory(self, temp_ci_dir):
        """Test loading when .reactorcide/jobs doesn't exist."""
        ci_path, _ = temp_ci_dir
        # Use a path that has no .reactorcide/jobs
        empty_path = ci_path / "empty"
        empty_path.mkdir()

        definitions = load_job_definitions(empty_path)

        assert definitions == []

    def test_load_empty_directory(self, temp_ci_dir):
        """Test loading from an empty jobs directory."""
        ci_path, _ = temp_ci_dir

        definitions = load_job_definitions(ci_path)

        assert definitions == []

    def test_load_skips_invalid_yaml(self, temp_ci_dir, capsys):
        """Test that invalid YAML files are skipped with a warning."""
        ci_path, jobs_dir = temp_ci_dir
        # Write valid file
        _write_yaml(jobs_dir / "good.yaml", {"name": "good"})
        # Write invalid YAML
        with open(jobs_dir / "bad.yaml", "w") as f:
            f.write(": : : invalid yaml [[[")

        definitions = load_job_definitions(ci_path)

        assert len(definitions) == 1
        assert definitions[0].name == "good"

    def test_load_skips_non_mapping_yaml(self, temp_ci_dir, capsys):
        """Test that YAML files containing non-mapping data are skipped."""
        ci_path, jobs_dir = temp_ci_dir
        _write_yaml(jobs_dir / "good.yaml", {"name": "good"})
        # Write a YAML file that's a list, not a mapping
        with open(jobs_dir / "list.yaml", "w") as f:
            yaml.dump(["item1", "item2"], f)

        definitions = load_job_definitions(ci_path)

        assert len(definitions) == 1
        assert definitions[0].name == "good"

    def test_load_skips_missing_name(self, temp_ci_dir, capsys):
        """Test that definitions without a name are skipped."""
        ci_path, jobs_dir = temp_ci_dir
        _write_yaml(jobs_dir / "good.yaml", {"name": "good"})
        _write_yaml(jobs_dir / "noname.yaml", {"description": "no name"})

        definitions = load_job_definitions(ci_path)

        assert len(definitions) == 1
        assert definitions[0].name == "good"

    def test_load_sets_source_file(self, temp_ci_dir):
        """Test that source_file is set on loaded definitions."""
        ci_path, jobs_dir = temp_ci_dir
        _write_yaml(jobs_dir / "test.yaml", {"name": "test"})

        definitions = load_job_definitions(ci_path)

        assert definitions[0].source_file == str(jobs_dir / "test.yaml")

    def test_load_ignores_non_yaml_files(self, temp_ci_dir):
        """Test that non-YAML files are ignored."""
        ci_path, jobs_dir = temp_ci_dir
        _write_yaml(jobs_dir / "test.yaml", {"name": "test"})
        with open(jobs_dir / "readme.md", "w") as f:
            f.write("# Not a job definition")
        with open(jobs_dir / "config.json", "w") as f:
            f.write("{}")

        definitions = load_job_definitions(ci_path)

        assert len(definitions) == 1
        assert definitions[0].name == "test"


# --- Test branch_matches ---


class TestBranchMatches:
    """Tests for branch glob pattern matching."""

    def test_exact_match(self):
        """Test exact branch name matching."""
        assert branch_matches("main", "main") is True
        assert branch_matches("main", "develop") is False

    def test_single_wildcard(self):
        """Test single * wildcard matching."""
        assert branch_matches("feature/*", "feature/foo") is True
        assert branch_matches("feature/*", "feature/bar") is True
        assert branch_matches("feature/*", "feature/foo/bar") is False
        assert branch_matches("feature/*", "bugfix/foo") is False

    def test_double_wildcard(self):
        """Test ** recursive wildcard matching."""
        assert branch_matches("feature/**", "feature/foo") is True
        assert branch_matches("feature/**", "feature/foo/bar") is True
        assert branch_matches("feature/**", "feature/foo/bar/baz") is True
        assert branch_matches("feature/**", "bugfix/foo") is False

    def test_double_wildcard_matches_zero_segments(self):
        """Test that ** can match zero path segments."""
        assert branch_matches("**", "main") is True
        assert branch_matches("**", "feature/branch") is True

    def test_wildcard_at_start(self):
        """Test wildcard pattern at the start."""
        assert branch_matches("*/main", "release/main") is True
        assert branch_matches("*/main", "feature/main") is True
        assert branch_matches("*/main", "main") is False

    def test_complex_patterns(self):
        """Test more complex glob patterns."""
        assert branch_matches("release/*.*", "release/1.0") is True
        assert branch_matches("release/*.*", "release/2.1") is True
        assert branch_matches("release/*.*", "release/v1") is False

    def test_question_mark_wildcard(self):
        """Test ? single-character wildcard."""
        assert branch_matches("release-?", "release-1") is True
        assert branch_matches("release-?", "release-a") is True
        assert branch_matches("release-?", "release-12") is False

    def test_double_wildcard_in_middle(self):
        """Test ** in the middle of a pattern."""
        assert branch_matches("org/**/main", "org/team/main") is True
        assert branch_matches("org/**/main", "org/team/sub/main") is True
        assert branch_matches("org/**/main", "org/main") is True


# --- Test paths_match ---


class TestPathsMatch:
    """Tests for path include/exclude matching."""

    def test_no_config_matches_all(self):
        """Test that empty paths config matches everything."""
        config = PathsConfig()
        assert paths_match(config, ["any/file.py"]) is True

    def test_include_matches(self):
        """Test that include patterns match correctly."""
        config = PathsConfig(include=["src/**"])
        assert paths_match(config, ["src/main.py"]) is True
        assert paths_match(config, ["src/sub/module.py"]) is True
        assert paths_match(config, ["tests/test_main.py"]) is False

    def test_include_multiple_patterns(self):
        """Test multiple include patterns (OR logic)."""
        config = PathsConfig(include=["src/**", "tests/**"])
        assert paths_match(config, ["src/main.py"]) is True
        assert paths_match(config, ["tests/test_main.py"]) is True
        assert paths_match(config, ["docs/readme.md"]) is False

    def test_exclude_patterns(self):
        """Test that exclude patterns filter out files."""
        config = PathsConfig(include=["src/**"], exclude=["src/generated/**"])
        assert paths_match(config, ["src/main.py"]) is True
        assert paths_match(config, ["src/generated/auto.py"]) is False

    def test_exclude_with_wildcards(self):
        """Test exclude with file extension wildcards."""
        config = PathsConfig(include=["**"], exclude=["*.md", "docs/**"])
        assert paths_match(config, ["src/main.py"]) is True
        assert paths_match(config, ["README.md"]) is False
        assert paths_match(config, ["docs/guide.txt"]) is False

    def test_no_changed_files_with_config(self):
        """Test that empty changed files returns False when config has include."""
        config = PathsConfig(include=["src/**"])
        assert paths_match(config, []) is False

    def test_no_changed_files_without_config(self):
        """Test that empty changed files returns True without any config."""
        config = PathsConfig()
        assert paths_match(config, []) is True

    def test_all_excluded(self):
        """Test when all changed files are excluded."""
        config = PathsConfig(include=["**"], exclude=["*.md"])
        assert paths_match(config, ["README.md", "CHANGELOG.md"]) is False

    def test_mixed_files(self):
        """Test with a mix of matching and non-matching files."""
        config = PathsConfig(include=["src/**"])
        assert paths_match(config, ["docs/readme.md", "src/main.py"]) is True

    def test_only_exclude_no_include(self):
        """Test exclude patterns without include patterns."""
        config = PathsConfig(exclude=["*.md"])
        assert paths_match(config, ["src/main.py"]) is True
        assert paths_match(config, ["README.md"]) is False

    def test_include_simple_glob(self):
        """Test simple glob patterns without **."""
        config = PathsConfig(include=["*.py"])
        assert paths_match(config, ["main.py"]) is True
        assert paths_match(config, ["src/main.py"]) is False


# --- Test evaluate_event ---


class TestEvaluateEvent:
    """Tests for event evaluation against job definitions."""

    def _make_definition(self, **kwargs) -> JobDefinition:
        """Helper to create a job definition with defaults."""
        defaults = {
            "name": "test",
            "triggers": TriggersConfig(events=["push"]),
        }
        defaults.update(kwargs)
        return JobDefinition(**defaults)

    def test_event_type_match(self):
        """Test matching by event type."""
        defs = [
            self._make_definition(name="push-job", triggers=TriggersConfig(events=["push"])),
            self._make_definition(name="pr-job", triggers=TriggersConfig(events=["pull_request_opened"])),
        ]

        matched = evaluate_event(defs, "push")

        assert len(matched) == 1
        assert matched[0].name == "push-job"

    def test_multiple_event_types(self):
        """Test definition matching multiple event types."""
        defs = [
            self._make_definition(
                name="pr-job",
                triggers=TriggersConfig(events=["pull_request_opened", "pull_request_updated"]),
            ),
        ]

        assert len(evaluate_event(defs, "pull_request_opened")) == 1
        assert len(evaluate_event(defs, "pull_request_updated")) == 1
        assert len(evaluate_event(defs, "push")) == 0

    def test_no_events_configured(self):
        """Test that definitions with no events never match."""
        defs = [self._make_definition(triggers=TriggersConfig(events=[]))]

        assert len(evaluate_event(defs, "push")) == 0

    def test_branch_filter_match(self):
        """Test branch filtering."""
        defs = [
            self._make_definition(
                triggers=TriggersConfig(events=["push"], branches=["main", "release/*"]),
            ),
        ]

        assert len(evaluate_event(defs, "push", branch="main")) == 1
        assert len(evaluate_event(defs, "push", branch="release/1.0")) == 1
        assert len(evaluate_event(defs, "push", branch="feature/foo")) == 0

    def test_no_branch_filter_matches_all(self):
        """Test that empty branch filter matches all branches."""
        defs = [
            self._make_definition(
                triggers=TriggersConfig(events=["push"], branches=[]),
            ),
        ]

        assert len(evaluate_event(defs, "push", branch="any-branch")) == 1

    def test_path_filter_match(self):
        """Test path filtering with changed files."""
        defs = [
            self._make_definition(
                paths=PathsConfig(include=["src/**"]),
            ),
        ]

        assert len(evaluate_event(defs, "push", changed_files=["src/main.py"])) == 1
        assert len(evaluate_event(defs, "push", changed_files=["docs/readme.md"])) == 0

    def test_path_filter_with_no_changed_files_info(self):
        """Test that path filtering is skipped when changed_files is None."""
        defs = [
            self._make_definition(
                paths=PathsConfig(include=["src/**"]),
            ),
        ]

        # None means we don't have changed files info, so skip path filtering
        assert len(evaluate_event(defs, "push", changed_files=None)) == 1

    def test_combined_filters(self):
        """Test combining event, branch, and path filters."""
        defs = [
            self._make_definition(
                name="test",
                triggers=TriggersConfig(
                    events=["push"],
                    branches=["main"],
                ),
                paths=PathsConfig(include=["src/**"]),
            ),
        ]

        # All match
        assert len(evaluate_event(defs, "push", "main", ["src/main.py"])) == 1
        # Wrong event
        assert len(evaluate_event(defs, "pull_request_opened", "main", ["src/main.py"])) == 0
        # Wrong branch
        assert len(evaluate_event(defs, "push", "develop", ["src/main.py"])) == 0
        # Wrong paths
        assert len(evaluate_event(defs, "push", "main", ["docs/readme.md"])) == 0

    def test_multiple_definitions(self):
        """Test evaluating multiple definitions."""
        defs = [
            self._make_definition(name="test", triggers=TriggersConfig(events=["push"])),
            self._make_definition(name="lint", triggers=TriggersConfig(events=["push"])),
            self._make_definition(name="deploy", triggers=TriggersConfig(events=["tag_created"])),
        ]

        matched = evaluate_event(defs, "push")

        assert len(matched) == 2
        names = [d.name for d in matched]
        assert "test" in names
        assert "lint" in names
        assert "deploy" not in names

    def test_empty_definitions(self):
        """Test with no definitions."""
        assert evaluate_event([], "push") == []


# --- Test generate_triggers ---


class TestGenerateTriggers:
    """Tests for trigger generation from matched definitions."""

    def test_basic_trigger(self):
        """Test generating a basic trigger."""
        defs = [
            JobDefinition(
                name="test",
                job=JobConfig(image="alpine:latest", command="make test"),
            ),
        ]
        ctx = EventContext(event_type="push", branch="main")

        triggers = generate_triggers(defs, ctx)

        assert len(triggers) == 1
        assert isinstance(triggers[0], JobTrigger)
        assert triggers[0].job_name == "test"
        assert triggers[0].container_image == "alpine:latest"
        assert triggers[0].job_command == "runnerlib run --job-command 'make test'"
        assert triggers[0].env["REACTORCIDE_EVENT_TYPE"] == "push"
        assert triggers[0].env["REACTORCIDE_BRANCH"] == "main"

    def test_raw_command_skips_wrapping(self):
        """Test that raw_command: true prevents runnerlib wrapping."""
        defs = [
            JobDefinition(
                name="test",
                job=JobConfig(image="alpine:latest", command="echo hello", raw_command=True),
            ),
        ]
        ctx = EventContext(event_type="push", branch="main")

        triggers = generate_triggers(defs, ctx)

        assert len(triggers) == 1
        assert triggers[0].job_command == "echo hello"

    def test_runnerlib_command_not_double_wrapped(self):
        """Test that commands already starting with runnerlib are not wrapped again."""
        defs = [
            JobDefinition(
                name="test",
                job=JobConfig(image="runnerbase:dev", command="runnerlib run --job-command 'make test'"),
            ),
        ]
        ctx = EventContext(event_type="push", branch="main")

        triggers = generate_triggers(defs, ctx)

        assert len(triggers) == 1
        assert triggers[0].job_command == "runnerlib run --job-command 'make test'"

    def test_trigger_with_all_context(self):
        """Test trigger generation with full event context."""
        defs = [
            JobDefinition(
                name="test",
                job=JobConfig(image="alpine:latest", command="make test"),
                environment={"BUILD_TYPE": "test"},
            ),
        ]
        ctx = EventContext(
            event_type="pull_request_opened",
            branch="feature/foo",
            source_url="https://github.com/org/repo.git",
            source_ref="abc123",
            ci_source_url="https://github.com/org/ci.git",
            ci_source_ref="def456",
            pr_base_ref="main",
            pr_number="42",
        )

        triggers = generate_triggers(defs, ctx)

        assert len(triggers) == 1
        t = triggers[0]
        assert t.env["REACTORCIDE_EVENT_TYPE"] == "pull_request_opened"
        assert t.env["REACTORCIDE_BRANCH"] == "feature/foo"
        assert t.env["REACTORCIDE_SHA"] == "abc123"
        assert t.env["REACTORCIDE_SOURCE_URL"] == "https://github.com/org/repo.git"
        assert t.env["REACTORCIDE_PR_BASE_REF"] == "main"
        assert t.env["REACTORCIDE_PR_NUMBER"] == "42"
        assert t.env["REACTORCIDE_CI_SOURCE_URL"] == "https://github.com/org/ci.git"
        assert t.env["REACTORCIDE_CI_SOURCE_REF"] == "def456"
        assert t.env["BUILD_TYPE"] == "test"
        assert t.source_type == "git"
        assert t.source_url == "https://github.com/org/repo.git"
        assert t.source_ref == "abc123"
        assert t.ci_source_type == "git"
        assert t.ci_source_url == "https://github.com/org/ci.git"
        assert t.ci_source_ref == "def456"

    def test_trigger_with_priority_and_timeout(self):
        """Test that priority and timeout are passed through."""
        defs = [
            JobDefinition(
                name="build",
                job=JobConfig(image="gcc:latest", command="make", timeout=3600, priority=20),
            ),
        ]
        ctx = EventContext(event_type="push")

        triggers = generate_triggers(defs, ctx)

        assert triggers[0].priority == 20
        assert triggers[0].timeout == 3600

    def test_trigger_without_sources(self):
        """Test trigger when no source URLs are in context."""
        defs = [JobDefinition(name="test", job=JobConfig(image="alpine:latest"))]
        ctx = EventContext(event_type="push")

        triggers = generate_triggers(defs, ctx)

        assert triggers[0].source_type is None
        assert triggers[0].source_url is None
        assert triggers[0].ci_source_type is None
        assert triggers[0].ci_source_url is None

    def test_multiple_triggers(self):
        """Test generating triggers for multiple definitions."""
        defs = [
            JobDefinition(name="test", job=JobConfig(image="alpine:latest", command="make test")),
            JobDefinition(name="lint", job=JobConfig(image="python:3.11", command="ruff check")),
        ]
        ctx = EventContext(event_type="push", branch="main")

        triggers = generate_triggers(defs, ctx)

        assert len(triggers) == 2
        assert triggers[0].job_name == "test"
        assert triggers[1].job_name == "lint"

    def test_empty_definitions(self):
        """Test with no matched definitions."""
        triggers = generate_triggers([], EventContext(event_type="push"))
        assert triggers == []

    def test_environment_merged(self):
        """Test that definition environment is merged with event context."""
        defs = [
            JobDefinition(
                name="test",
                environment={"FOO": "bar", "REACTORCIDE_EVENT_TYPE": "should_be_overridden"},
            ),
        ]
        ctx = EventContext(event_type="push")

        triggers = generate_triggers(defs, ctx)

        # Event context should override definition env for REACTORCIDE_ vars
        assert triggers[0].env["REACTORCIDE_EVENT_TYPE"] == "push"
        assert triggers[0].env["FOO"] == "bar"

    def test_empty_job_config_fields_become_none(self):
        """Test that empty strings in job config become None in trigger."""
        defs = [JobDefinition(name="test", job=JobConfig(image="", command=""))]
        ctx = EventContext(event_type="push")

        triggers = generate_triggers(defs, ctx)

        assert triggers[0].container_image is None
        assert triggers[0].job_command is None

    def test_optional_context_fields_omitted(self):
        """Test that empty context fields don't appear in env."""
        defs = [JobDefinition(name="test")]
        ctx = EventContext(event_type="push")

        triggers = generate_triggers(defs, ctx)

        assert "REACTORCIDE_BRANCH" not in triggers[0].env
        assert "REACTORCIDE_SHA" not in triggers[0].env
        assert "REACTORCIDE_PR_BASE_REF" not in triggers[0].env
        assert "REACTORCIDE_PR_NUMBER" not in triggers[0].env


# --- Test VALID_EVENT_TYPES constant ---


class TestValidEventTypes:
    """Tests for the VALID_EVENT_TYPES constant."""

    def test_contains_all_expected_types(self):
        """Test that all expected event types are present."""
        expected = {
            "push",
            "pull_request_opened",
            "pull_request_updated",
            "pull_request_merged",
            "pull_request_closed",
            "tag_created",
        }
        assert VALID_EVENT_TYPES == expected

    def test_matches_go_constants(self):
        """Test that event types match the Go EventType constants."""
        # These must stay in sync with coordinator_api/internal/vcs/event_types.go
        assert "push" in VALID_EVENT_TYPES
        assert "pull_request_opened" in VALID_EVENT_TYPES
        assert "pull_request_updated" in VALID_EVENT_TYPES
        assert "pull_request_merged" in VALID_EVENT_TYPES
        assert "pull_request_closed" in VALID_EVENT_TYPES
        assert "tag_created" in VALID_EVENT_TYPES


# --- Integration tests ---


class TestEndToEnd:
    """Integration tests for the full eval pipeline."""

    def test_full_pipeline(self, temp_ci_dir):
        """Test loading definitions, evaluating, and generating triggers."""
        ci_path, jobs_dir = temp_ci_dir

        # Write test job definition
        _write_yaml(jobs_dir / "test.yaml", {
            "name": "test",
            "description": "Run tests",
            "triggers": {
                "events": ["pull_request_opened", "pull_request_updated"],
                "branches": ["main", "feature/*"],
            },
            "paths": {
                "include": ["src/**", "tests/**"],
            },
            "job": {
                "image": "python:3.11",
                "command": "pytest",
                "timeout": 1800,
            },
            "environment": {
                "PYTEST_ARGS": "-v",
            },
        })

        # Write deploy job definition
        _write_yaml(jobs_dir / "deploy.yaml", {
            "name": "deploy",
            "description": "Deploy to production",
            "triggers": {
                "events": ["push"],
                "branches": ["main"],
            },
            "job": {
                "image": "deploy:latest",
                "command": "deploy.sh",
                "priority": 5,
            },
        })

        # Load definitions
        definitions = load_job_definitions(ci_path)
        assert len(definitions) == 2

        # Evaluate PR opened on feature branch
        ctx = EventContext(
            event_type="pull_request_opened",
            branch="feature/my-feature",
            source_url="https://github.com/org/repo.git",
            source_ref="abc123",
            pr_base_ref="main",
            pr_number="42",
        )

        matched = evaluate_event(
            definitions,
            ctx.event_type,
            ctx.branch,
            changed_files=["src/main.py"],
        )

        assert len(matched) == 1
        assert matched[0].name == "test"

        # Generate triggers
        triggers = generate_triggers(matched, ctx)

        assert len(triggers) == 1
        assert triggers[0].job_name == "test"
        assert triggers[0].container_image == "python:3.11"
        assert triggers[0].job_command == "runnerlib run --job-command 'pytest'"
        assert triggers[0].timeout == 1800
        assert triggers[0].env["REACTORCIDE_EVENT_TYPE"] == "pull_request_opened"
        assert triggers[0].env["REACTORCIDE_BRANCH"] == "feature/my-feature"
        assert triggers[0].env["REACTORCIDE_PR_NUMBER"] == "42"
        assert triggers[0].env["PYTEST_ARGS"] == "-v"

    def test_push_to_main_triggers_deploy(self, temp_ci_dir):
        """Test that push to main triggers deploy but not PR test."""
        ci_path, jobs_dir = temp_ci_dir

        _write_yaml(jobs_dir / "test.yaml", {
            "name": "test",
            "triggers": {"events": ["pull_request_opened"]},
        })
        _write_yaml(jobs_dir / "deploy.yaml", {
            "name": "deploy",
            "triggers": {"events": ["push"], "branches": ["main"]},
            "job": {"image": "deploy:latest", "command": "deploy.sh"},
        })

        definitions = load_job_definitions(ci_path)
        matched = evaluate_event(definitions, "push", "main")

        assert len(matched) == 1
        assert matched[0].name == "deploy"

    def test_tag_triggers_release(self, temp_ci_dir):
        """Test that tag_created triggers release job."""
        ci_path, jobs_dir = temp_ci_dir

        _write_yaml(jobs_dir / "release.yaml", {
            "name": "release",
            "triggers": {"events": ["tag_created"]},
            "job": {"image": "builder:latest", "command": "make release"},
        })

        definitions = load_job_definitions(ci_path)
        matched = evaluate_event(definitions, "tag_created")

        assert len(matched) == 1
        assert matched[0].name == "release"
