"""
Tests for the eval CLI command.
"""

import json
import tempfile
from pathlib import Path
from unittest.mock import patch

import pytest
import yaml
from typer.testing import CliRunner

from src.cli import app

runner = CliRunner()


# --- Fixtures ---


@pytest.fixture
def temp_dirs():
    """Create temporary CI source and source directories with job definitions."""
    with tempfile.TemporaryDirectory() as tmpdir:
        base = Path(tmpdir)
        ci_dir = base / "ci"
        src_dir = base / "src"
        jobs_dir = ci_dir / ".reactorcide" / "jobs"
        jobs_dir.mkdir(parents=True)
        src_dir.mkdir(parents=True)
        triggers_file = base / "triggers.json"
        yield ci_dir, src_dir, jobs_dir, triggers_file


def _write_yaml(path: Path, data: dict) -> Path:
    """Helper to write a YAML file."""
    with open(path, "w") as f:
        yaml.dump(data, f)
    return path


# --- Test eval command ---


class TestEvalCommand:
    """Tests for the eval CLI command."""

    def test_basic_eval_with_match(self, temp_dirs):
        """Test eval command with a matching job definition."""
        ci_dir, src_dir, jobs_dir, triggers_file = temp_dirs

        _write_yaml(jobs_dir / "test.yaml", {
            "name": "test",
            "triggers": {"events": ["push"]},
            "job": {"image": "alpine:latest", "command": "make test"},
        })

        result = runner.invoke(app, [
            "eval",
            "--ci-source-dir", str(ci_dir),
            "--source-dir", str(src_dir),
            "--event-type", "push",
            "--branch", "main",
            "--triggers-file", str(triggers_file),
        ])

        assert result.exit_code == 0
        assert triggers_file.exists()

        with open(triggers_file) as f:
            data = json.load(f)

        assert data["type"] == "trigger_job"
        assert len(data["jobs"]) == 1
        assert data["jobs"][0]["job_name"] == "test"
        assert data["jobs"][0]["env"]["REACTORCIDE_EVENT_TYPE"] == "push"
        assert data["jobs"][0]["env"]["REACTORCIDE_BRANCH"] == "main"

    def test_eval_no_match(self, temp_dirs):
        """Test eval command when no definitions match the event."""
        ci_dir, src_dir, jobs_dir, triggers_file = temp_dirs

        _write_yaml(jobs_dir / "test.yaml", {
            "name": "test",
            "triggers": {"events": ["pull_request_opened"]},
        })

        result = runner.invoke(app, [
            "eval",
            "--ci-source-dir", str(ci_dir),
            "--source-dir", str(src_dir),
            "--event-type", "push",
            "--triggers-file", str(triggers_file),
        ])

        assert result.exit_code == 0
        assert "No jobs matched" in result.stdout
        assert not triggers_file.exists()

    def test_eval_no_definitions(self, temp_dirs):
        """Test eval command when no job definitions exist."""
        ci_dir, src_dir, jobs_dir, triggers_file = temp_dirs

        result = runner.invoke(app, [
            "eval",
            "--ci-source-dir", str(ci_dir),
            "--source-dir", str(src_dir),
            "--event-type", "push",
            "--triggers-file", str(triggers_file),
        ])

        assert result.exit_code == 0
        assert "No job definitions found" in result.stdout
        assert not triggers_file.exists()

    def test_eval_invalid_event_type(self, temp_dirs):
        """Test eval command with an invalid event type."""
        ci_dir, src_dir, jobs_dir, triggers_file = temp_dirs

        result = runner.invoke(app, [
            "eval",
            "--ci-source-dir", str(ci_dir),
            "--source-dir", str(src_dir),
            "--event-type", "invalid_event",
            "--triggers-file", str(triggers_file),
        ])

        assert result.exit_code == 1
        assert not triggers_file.exists()

    def test_eval_multiple_matches(self, temp_dirs):
        """Test eval command with multiple matching definitions."""
        ci_dir, src_dir, jobs_dir, triggers_file = temp_dirs

        _write_yaml(jobs_dir / "test.yaml", {
            "name": "test",
            "triggers": {"events": ["push"]},
            "job": {"image": "alpine:latest", "command": "make test"},
        })
        _write_yaml(jobs_dir / "lint.yaml", {
            "name": "lint",
            "triggers": {"events": ["push"]},
            "job": {"image": "python:3.11", "command": "ruff check"},
        })

        result = runner.invoke(app, [
            "eval",
            "--ci-source-dir", str(ci_dir),
            "--source-dir", str(src_dir),
            "--event-type", "push",
            "--branch", "main",
            "--triggers-file", str(triggers_file),
        ])

        assert result.exit_code == 0
        assert triggers_file.exists()

        with open(triggers_file) as f:
            data = json.load(f)

        assert len(data["jobs"]) == 2
        names = [j["job_name"] for j in data["jobs"]]
        assert "test" in names
        assert "lint" in names

    def test_eval_branch_filter(self, temp_dirs):
        """Test eval command respects branch filtering."""
        ci_dir, src_dir, jobs_dir, triggers_file = temp_dirs

        _write_yaml(jobs_dir / "deploy.yaml", {
            "name": "deploy",
            "triggers": {"events": ["push"], "branches": ["main"]},
            "job": {"image": "deploy:latest", "command": "deploy.sh"},
        })

        # Push to feature branch should not match
        result = runner.invoke(app, [
            "eval",
            "--ci-source-dir", str(ci_dir),
            "--source-dir", str(src_dir),
            "--event-type", "push",
            "--branch", "feature/foo",
            "--triggers-file", str(triggers_file),
        ])

        assert result.exit_code == 0
        assert "No jobs matched" in result.stdout
        assert not triggers_file.exists()

        # Push to main should match
        result = runner.invoke(app, [
            "eval",
            "--ci-source-dir", str(ci_dir),
            "--source-dir", str(src_dir),
            "--event-type", "push",
            "--branch", "main",
            "--triggers-file", str(triggers_file),
        ])

        assert result.exit_code == 0
        assert triggers_file.exists()

        with open(triggers_file) as f:
            data = json.load(f)

        assert data["jobs"][0]["job_name"] == "deploy"

    def test_eval_full_event_context(self, temp_dirs):
        """Test eval command passes full event context to triggers."""
        ci_dir, src_dir, jobs_dir, triggers_file = temp_dirs

        _write_yaml(jobs_dir / "test.yaml", {
            "name": "test",
            "triggers": {"events": ["pull_request_opened"]},
            "job": {"image": "alpine:latest", "command": "make test"},
            "environment": {"BUILD_TYPE": "test"},
        })

        # Create .git dir so eval doesn't try to clone from the fake URL
        (src_dir / ".git").mkdir()

        result = runner.invoke(app, [
            "eval",
            "--ci-source-dir", str(ci_dir),
            "--source-dir", str(src_dir),
            "--event-type", "pull_request_opened",
            "--branch", "feature/foo",
            "--pr-base-ref", "main",
            "--pr-number", "42",
            "--source-url", "https://github.com/org/repo.git",
            "--source-ref", "abc123",
            "--ci-source-url", "https://github.com/org/ci.git",
            "--ci-source-ref", "def456",
            "--triggers-file", str(triggers_file),
        ])

        assert result.exit_code == 0
        assert triggers_file.exists()

        with open(triggers_file) as f:
            data = json.load(f)

        job = data["jobs"][0]
        assert job["job_name"] == "test"
        assert job["env"]["REACTORCIDE_EVENT_TYPE"] == "pull_request_opened"
        assert job["env"]["REACTORCIDE_BRANCH"] == "feature/foo"
        assert job["env"]["REACTORCIDE_SHA"] == "abc123"
        assert job["env"]["REACTORCIDE_SOURCE_URL"] == "https://github.com/org/repo.git"
        assert job["env"]["REACTORCIDE_PR_BASE_REF"] == "main"
        assert job["env"]["REACTORCIDE_PR_NUMBER"] == "42"
        assert job["env"]["REACTORCIDE_CI_SOURCE_URL"] == "https://github.com/org/ci.git"
        assert job["env"]["REACTORCIDE_CI_SOURCE_REF"] == "def456"
        assert job["env"]["BUILD_TYPE"] == "test"
        assert job["source_type"] == "git"
        assert job["source_url"] == "https://github.com/org/repo.git"
        assert job["source_ref"] == "abc123"
        assert job["ci_source_type"] == "git"
        assert job["ci_source_url"] == "https://github.com/org/ci.git"
        assert job["ci_source_ref"] == "def456"

    def test_eval_with_changed_files(self, temp_dirs):
        """Test eval command uses git changed files for path filtering."""
        ci_dir, src_dir, jobs_dir, triggers_file = temp_dirs

        _write_yaml(jobs_dir / "test.yaml", {
            "name": "test",
            "triggers": {"events": ["push"]},
            "paths": {"include": ["src/**"]},
            "job": {"image": "alpine:latest", "command": "make test"},
        })

        # Create a fake .git dir so the code tries to get changed files
        (src_dir / ".git").mkdir()

        with patch("src.workflow.changed_files", return_value=["src/main.py"]):
            result = runner.invoke(app, [
                "eval",
                "--ci-source-dir", str(ci_dir),
                "--source-dir", str(src_dir),
                "--event-type", "push",
                "--branch", "main",
                "--triggers-file", str(triggers_file),
            ])

        assert result.exit_code == 0
        assert triggers_file.exists()

        with open(triggers_file) as f:
            data = json.load(f)

        assert data["jobs"][0]["job_name"] == "test"

    def test_eval_changed_files_no_match(self, temp_dirs):
        """Test eval command with changed files that don't match path filters."""
        ci_dir, src_dir, jobs_dir, triggers_file = temp_dirs

        _write_yaml(jobs_dir / "test.yaml", {
            "name": "test",
            "triggers": {"events": ["push"]},
            "paths": {"include": ["src/**"]},
            "job": {"image": "alpine:latest", "command": "make test"},
        })

        (src_dir / ".git").mkdir()

        with patch("src.workflow.changed_files", return_value=["docs/readme.md"]):
            result = runner.invoke(app, [
                "eval",
                "--ci-source-dir", str(ci_dir),
                "--source-dir", str(src_dir),
                "--event-type", "push",
                "--branch", "main",
                "--triggers-file", str(triggers_file),
            ])

        assert result.exit_code == 0
        assert "No jobs matched" in result.stdout

    def test_eval_pr_uses_base_ref_for_diff(self, temp_dirs):
        """Test that PR events use pr_base_ref for changed files diff."""
        ci_dir, src_dir, jobs_dir, triggers_file = temp_dirs

        _write_yaml(jobs_dir / "test.yaml", {
            "name": "test",
            "triggers": {"events": ["pull_request_opened"]},
            "job": {"image": "alpine:latest", "command": "make test"},
        })

        (src_dir / ".git").mkdir()

        with patch("src.workflow.changed_files", return_value=["file.py"]) as mock_changed:
            result = runner.invoke(app, [
                "eval",
                "--ci-source-dir", str(ci_dir),
                "--source-dir", str(src_dir),
                "--event-type", "pull_request_opened",
                "--branch", "feature/foo",
                "--pr-base-ref", "main",
                "--triggers-file", str(triggers_file),
            ])

            # Verify it was called with origin/main as the from_ref
            mock_changed.assert_called_once_with(
                "origin/main", "HEAD", str(src_dir)
            )

        assert result.exit_code == 0

    def test_eval_push_uses_head_parent_for_diff(self, temp_dirs):
        """Test that push events use HEAD^ for changed files diff."""
        ci_dir, src_dir, jobs_dir, triggers_file = temp_dirs

        _write_yaml(jobs_dir / "test.yaml", {
            "name": "test",
            "triggers": {"events": ["push"]},
            "job": {"image": "alpine:latest", "command": "make test"},
        })

        (src_dir / ".git").mkdir()

        with patch("src.workflow.changed_files", return_value=["file.py"]) as mock_changed:
            result = runner.invoke(app, [
                "eval",
                "--ci-source-dir", str(ci_dir),
                "--source-dir", str(src_dir),
                "--event-type", "push",
                "--branch", "main",
                "--triggers-file", str(triggers_file),
            ])

            mock_changed.assert_called_once_with(
                "HEAD^", "HEAD", str(src_dir)
            )

        assert result.exit_code == 0

    def test_eval_no_git_dir_skips_changed_files(self, temp_dirs):
        """Test that eval skips changed files detection when no .git dir exists."""
        ci_dir, src_dir, jobs_dir, triggers_file = temp_dirs

        _write_yaml(jobs_dir / "test.yaml", {
            "name": "test",
            "triggers": {"events": ["push"]},
            "paths": {"include": ["src/**"]},
            "job": {"image": "alpine:latest", "command": "make test"},
        })

        # No .git directory - should skip changed files and still match
        # (path filtering is skipped when changed_files is None)
        result = runner.invoke(app, [
            "eval",
            "--ci-source-dir", str(ci_dir),
            "--source-dir", str(src_dir),
            "--event-type", "push",
            "--triggers-file", str(triggers_file),
        ])

        assert result.exit_code == 0
        assert triggers_file.exists()

    def test_eval_env_vars(self, temp_dirs):
        """Test that eval reads options from environment variables."""
        ci_dir, src_dir, jobs_dir, triggers_file = temp_dirs

        _write_yaml(jobs_dir / "test.yaml", {
            "name": "test",
            "triggers": {"events": ["push"]},
            "job": {"image": "alpine:latest", "command": "make test"},
        })

        env = {
            "REACTORCIDE_CI_SOURCE_DIR": str(ci_dir),
            "REACTORCIDE_SOURCE_DIR": str(src_dir),
            "REACTORCIDE_EVENT_TYPE": "push",
            "REACTORCIDE_BRANCH": "main",
        }

        result = runner.invoke(app, [
            "eval",
            "--triggers-file", str(triggers_file),
        ], env=env)

        assert result.exit_code == 0
        assert triggers_file.exists()

    def test_eval_job_priority_and_timeout(self, temp_dirs):
        """Test that job priority and timeout are passed through to triggers."""
        ci_dir, src_dir, jobs_dir, triggers_file = temp_dirs

        _write_yaml(jobs_dir / "build.yaml", {
            "name": "build",
            "triggers": {"events": ["push"]},
            "job": {
                "image": "gcc:latest",
                "command": "make",
                "timeout": 3600,
                "priority": 20,
            },
        })

        result = runner.invoke(app, [
            "eval",
            "--ci-source-dir", str(ci_dir),
            "--source-dir", str(src_dir),
            "--event-type", "push",
            "--triggers-file", str(triggers_file),
        ])

        assert result.exit_code == 0

        with open(triggers_file) as f:
            data = json.load(f)

        assert data["jobs"][0]["priority"] == 20
        assert data["jobs"][0]["timeout"] == 3600

    def test_eval_git_error_continues(self, temp_dirs):
        """Test that git errors during changed files detection don't fail the command."""
        ci_dir, src_dir, jobs_dir, triggers_file = temp_dirs

        _write_yaml(jobs_dir / "test.yaml", {
            "name": "test",
            "triggers": {"events": ["push"]},
            "job": {"image": "alpine:latest", "command": "make test"},
        })

        (src_dir / ".git").mkdir()

        with patch("src.workflow.changed_files", side_effect=Exception("git error")):
            result = runner.invoke(app, [
                "eval",
                "--ci-source-dir", str(ci_dir),
                "--source-dir", str(src_dir),
                "--event-type", "push",
                "--triggers-file", str(triggers_file),
            ])

        assert result.exit_code == 0
        assert triggers_file.exists()


class TestEvalSourcePreparation:
    """Tests for eval command source preparation (cloning CI/source repos)."""

    def test_eval_clones_ci_source_when_missing(self):
        """Test that eval clones CI source when the jobs dir doesn't exist."""
        with tempfile.TemporaryDirectory() as tmpdir:
            base = Path(tmpdir)
            ci_dir = base / "ci"  # Does not exist yet
            src_dir = base / "src"
            src_dir.mkdir()
            (src_dir / ".git").mkdir()
            triggers_file = base / "triggers.json"

            # Create a fake "remote" repo with job definitions
            remote_dir = base / "remote"
            remote_dir.mkdir()
            from git import Repo
            remote_repo = Repo.init(remote_dir)
            remote_jobs_dir = remote_dir / ".reactorcide" / "jobs"
            remote_jobs_dir.mkdir(parents=True)
            _write_yaml(remote_jobs_dir / "test.yaml", {
                "name": "test",
                "triggers": {"events": ["push"]},
                "job": {"image": "alpine:latest", "command": "make test"},
            })
            remote_repo.index.add([str(remote_jobs_dir / "test.yaml")])
            remote_repo.index.commit("Add job def")

            result = runner.invoke(app, [
                "eval",
                "--ci-source-dir", str(ci_dir),
                "--source-dir", str(src_dir),
                "--event-type", "push",
                "--branch", "main",
                "--ci-source-url", str(remote_dir),
                "--ci-source-ref", "",
                "--triggers-file", str(triggers_file),
            ])

            assert result.exit_code == 0
            assert triggers_file.exists()

            with open(triggers_file) as f:
                data = json.load(f)

            assert len(data["jobs"]) == 1
            assert data["jobs"][0]["job_name"] == "test"

    def test_eval_clones_source_when_missing(self):
        """Test that eval clones source repo when .git dir doesn't exist."""
        with tempfile.TemporaryDirectory() as tmpdir:
            base = Path(tmpdir)
            ci_dir = base / "ci"
            src_dir = base / "src"
            src_dir.mkdir()  # Exists but no .git (like worker creates)
            jobs_dir = ci_dir / ".reactorcide" / "jobs"
            jobs_dir.mkdir(parents=True)
            triggers_file = base / "triggers.json"

            # Create a fake "remote" source repo
            remote_dir = base / "remote_src"
            remote_dir.mkdir()
            from git import Repo
            remote_repo = Repo.init(remote_dir)
            (remote_dir / "main.py").write_text("print('hello')")
            remote_repo.index.add(["main.py"])
            remote_repo.index.commit("Initial commit")

            _write_yaml(jobs_dir / "test.yaml", {
                "name": "test",
                "triggers": {"events": ["push"]},
                "job": {"image": "alpine:latest", "command": "make test"},
            })

            result = runner.invoke(app, [
                "eval",
                "--ci-source-dir", str(ci_dir),
                "--source-dir", str(src_dir),
                "--event-type", "push",
                "--branch", "main",
                "--source-url", str(remote_dir),
                "--triggers-file", str(triggers_file),
            ])

            assert result.exit_code == 0
            # Source should have been cloned
            assert (src_dir / ".git").is_dir()

    def test_eval_skips_clone_when_dirs_exist(self, temp_dirs):
        """Test that eval doesn't clone when directories already have content."""
        ci_dir, src_dir, jobs_dir, triggers_file = temp_dirs

        _write_yaml(jobs_dir / "test.yaml", {
            "name": "test",
            "triggers": {"events": ["push"]},
            "job": {"image": "alpine:latest", "command": "make test"},
        })

        # CI source has .reactorcide/jobs already, source has .git
        (src_dir / ".git").mkdir()

        with patch("src.source_prep._prepare_git_source") as mock_clone:
            result = runner.invoke(app, [
                "eval",
                "--ci-source-dir", str(ci_dir),
                "--source-dir", str(src_dir),
                "--event-type", "push",
                "--branch", "main",
                "--source-url", "https://example.com/repo.git",
                "--ci-source-url", "https://example.com/ci.git",
                "--triggers-file", str(triggers_file),
            ])

            # Should not have called clone since dirs exist
            mock_clone.assert_not_called()

        assert result.exit_code == 0


class TestEvalEndToEnd:
    """Integration tests for the eval CLI command."""

    def test_pr_opened_triggers_test_not_deploy(self, temp_dirs):
        """Test full pipeline: PR opened triggers test job but not deploy."""
        ci_dir, src_dir, jobs_dir, triggers_file = temp_dirs

        _write_yaml(jobs_dir / "test.yaml", {
            "name": "test",
            "description": "Run tests",
            "triggers": {
                "events": ["pull_request_opened", "pull_request_updated"],
                "branches": ["main", "feature/*"],
            },
            "job": {"image": "python:3.11", "command": "pytest", "timeout": 1800},
            "environment": {"PYTEST_ARGS": "-v"},
        })
        _write_yaml(jobs_dir / "deploy.yaml", {
            "name": "deploy",
            "description": "Deploy to production",
            "triggers": {"events": ["push"], "branches": ["main"]},
            "job": {"image": "deploy:latest", "command": "deploy.sh", "priority": 5},
        })

        # Create .git dir so eval doesn't try to clone from the fake URL
        (src_dir / ".git").mkdir()

        result = runner.invoke(app, [
            "eval",
            "--ci-source-dir", str(ci_dir),
            "--source-dir", str(src_dir),
            "--event-type", "pull_request_opened",
            "--branch", "feature/my-feature",
            "--source-url", "https://github.com/org/repo.git",
            "--source-ref", "abc123",
            "--pr-base-ref", "main",
            "--pr-number", "42",
            "--triggers-file", str(triggers_file),
        ])

        assert result.exit_code == 0
        assert triggers_file.exists()

        with open(triggers_file) as f:
            data = json.load(f)

        assert len(data["jobs"]) == 1
        assert data["jobs"][0]["job_name"] == "test"
        assert data["jobs"][0]["env"]["REACTORCIDE_PR_NUMBER"] == "42"
        assert data["jobs"][0]["env"]["PYTEST_ARGS"] == "-v"
        assert data["jobs"][0]["container_image"] == "python:3.11"
        assert data["jobs"][0]["job_command"] == "pytest"
        assert data["jobs"][0]["timeout"] == 1800

    def test_push_to_main_triggers_deploy(self, temp_dirs):
        """Test full pipeline: push to main triggers deploy but not PR test."""
        ci_dir, src_dir, jobs_dir, triggers_file = temp_dirs

        _write_yaml(jobs_dir / "test.yaml", {
            "name": "test",
            "triggers": {"events": ["pull_request_opened"]},
        })
        _write_yaml(jobs_dir / "deploy.yaml", {
            "name": "deploy",
            "triggers": {"events": ["push"], "branches": ["main"]},
            "job": {"image": "deploy:latest", "command": "deploy.sh"},
        })

        result = runner.invoke(app, [
            "eval",
            "--ci-source-dir", str(ci_dir),
            "--source-dir", str(src_dir),
            "--event-type", "push",
            "--branch", "main",
            "--triggers-file", str(triggers_file),
        ])

        assert result.exit_code == 0

        with open(triggers_file) as f:
            data = json.load(f)

        assert len(data["jobs"]) == 1
        assert data["jobs"][0]["job_name"] == "deploy"

    def test_tag_created_triggers_release(self, temp_dirs):
        """Test full pipeline: tag_created triggers release job."""
        ci_dir, src_dir, jobs_dir, triggers_file = temp_dirs

        _write_yaml(jobs_dir / "release.yaml", {
            "name": "release",
            "triggers": {"events": ["tag_created"]},
            "job": {"image": "builder:latest", "command": "make release"},
        })

        result = runner.invoke(app, [
            "eval",
            "--ci-source-dir", str(ci_dir),
            "--source-dir", str(src_dir),
            "--event-type", "tag_created",
            "--triggers-file", str(triggers_file),
        ])

        assert result.exit_code == 0

        with open(triggers_file) as f:
            data = json.load(f)

        assert len(data["jobs"]) == 1
        assert data["jobs"][0]["job_name"] == "release"
