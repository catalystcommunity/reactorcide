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

    def test_eval_workflow_mode_multi_workflow(self, temp_dirs):
        """Workflow files take precedence and emit the multi-workflow form."""
        ci_dir, src_dir, jobs_dir, triggers_file = temp_dirs
        wf_dir = ci_dir / ".reactorcide" / "workflows"
        wf_dir.mkdir(parents=True)

        # A reusable job referenced by the PR workflow.
        _write_yaml(jobs_dir / "test-go.yaml", {
            "name": "test-go",
            "job": {
                "image": "golang:1.26",
                "command": "go test ./...",
                "worker_class": "vm",
                "characteristics": {"os": "windows"},
                "resources": {"cpu": {"limit": "2"}, "memory": {"limit": "4Gi"}},
                "disable_run_local": True,
                "run_local": {"as_runner": True},
            },
        })
        # PR workflow matches push; release workflow does not.
        _write_yaml(wf_dir / "pr.yaml", {
            "name": "Reactorcide PR",
            "on": {"events": ["push"]},
            "jobs": {
                "lint": {"image": "alpine", "command": "make lint"},
                "test-go": {"job_file": "test-go.yaml", "depends_on": ["lint"]},
            },
        })
        _write_yaml(wf_dir / "release.yaml", {
            "name": "Reactorcide Release",
            "on": {"events": ["pull_request_merged"]},
            "jobs": {"release": {"image": "alpine", "command": "make release"}},
        })

        result = runner.invoke(app, [
            "eval",
            "--ci-source-dir", str(ci_dir),
            "--source-dir", str(src_dir),
            "--event-type", "push",
            "--branch", "main",
            "--triggers-file", str(triggers_file),
        ])

        assert result.exit_code == 0, result.stdout
        assert triggers_file.exists()

        with open(triggers_file) as f:
            data = json.load(f)

        assert data["type"] == "trigger_job"
        # Only the PR workflow matched push.
        assert [w["name"] for w in data["workflows"]] == ["Reactorcide PR"]
        pr = data["workflows"][0]
        by_name = {j["job_name"]: j for j in pr["jobs"]}
        assert set(by_name) == {"lint", "test-go"}
        # job_file was resolved by runnerlib; it must not leak to the coordinator.
        assert "job_file" not in by_name["test-go"]
        assert by_name["test-go"]["container_image"] == "golang:1.26"
        assert by_name["test-go"]["depends_on"] == ["lint"]
        assert by_name["test-go"]["worker_class"] == "vm"
        assert by_name["test-go"]["characteristics"] == {"os": "windows"}
        assert by_name["test-go"]["resources"] == {"cpu": {"limit": "2"}, "memory": {"limit": "4Gi"}}
        assert by_name["test-go"]["disable_run_local"] is True
        assert by_name["test-go"]["run_local"] == {"as_runner": True}

    def test_explicit_workflow_uses_event_filter(self, temp_dirs):
        """An explicit workflow still evaluates its selected event trigger."""
        ci_dir, src_dir, jobs_dir, triggers_file = temp_dirs
        wf_dir = ci_dir / ".reactorcide" / "workflows"
        wf_dir.mkdir(parents=True)
        selected = _write_yaml(wf_dir / "release.yaml", {
            "name": "Release",
            "on": {"events": ["pull_request_merged"]},
            "vars": {"channel": "stable"},
            "jobs": {"release": {"image": "alpine", "command": "echo release"}},
        })
        _write_yaml(wf_dir / "other.yaml", {
            "name": "Other",
            "on": {"events": ["push"]},
            "jobs": {"other": {"image": "alpine", "command": "echo other"}},
        })

        result = runner.invoke(app, [
            "eval",
            "--ci-source-dir", str(ci_dir),
            "--source-dir", str(src_dir),
            "--event-type", "push",
            "--workflow-file", str(selected),
            "--triggers-file", str(triggers_file),
        ])

        assert result.exit_code == 0, result.stdout
        with open(triggers_file) as f:
            data = json.load(f)
        assert [workflow["name"] for workflow in data["workflows"]] == ["Release"]
        assert data["workflows"][0]["jobs"] == []

        triggers_file.unlink()
        result = runner.invoke(app, [
            "eval",
            "--ci-source-dir", str(ci_dir),
            "--source-dir", str(src_dir),
            "--event-type", "pull_request_merged",
            "--workflow-file", str(selected),
            "--triggers-file", str(triggers_file),
        ])

        assert result.exit_code == 0, result.stdout
        with open(triggers_file) as f:
            data = json.load(f)
        assert data["workflows"][0]["vars"] == {"channel": "stable"}
        assert [job["job_name"] for job in data["workflows"][0]["jobs"]] == ["release"]

    def test_changed_file_option_controls_workflow_paths(self, temp_dirs):
        """Explicit changed paths replace automatic Git change detection."""
        ci_dir, src_dir, jobs_dir, triggers_file = temp_dirs
        wf_dir = ci_dir / ".reactorcide" / "workflows"
        wf_dir.mkdir(parents=True)
        _write_yaml(wf_dir / "docs.yaml", {
            "name": "Docs",
            "on": {"events": ["push"]},
            "paths": {"include": ["docs/**"]},
            "jobs": {"docs": {"image": "alpine", "command": "echo docs"}},
        })

        result = runner.invoke(app, [
            "eval",
            "--ci-source-dir", str(ci_dir),
            "--source-dir", str(src_dir),
            "--event-type", "push",
            "--changed-file", "docs/guide.md",
            "--triggers-file", str(triggers_file),
        ])

        assert result.exit_code == 0, result.stdout
        with open(triggers_file) as f:
            data = json.load(f)
        assert [workflow["name"] for workflow in data["workflows"]] == ["Docs"]

    def test_eval_workflow_mode_no_match_is_success(self, temp_dirs):
        """A workflow that matches no event still exits 0 with an explanation."""
        ci_dir, src_dir, jobs_dir, triggers_file = temp_dirs
        wf_dir = ci_dir / ".reactorcide" / "workflows"
        wf_dir.mkdir(parents=True)
        _write_yaml(wf_dir / "release.yaml", {
            "name": "Reactorcide Release",
            "on": {"events": ["pull_request_merged"]},
            "jobs": {"release": {"image": "alpine", "command": "make release"}},
        })

        result = runner.invoke(app, [
            "eval",
            "--ci-source-dir", str(ci_dir),
            "--source-dir", str(src_dir),
            "--event-type", "push",
            "--triggers-file", str(triggers_file),
        ])

        assert result.exit_code == 0
        assert "nothing to run" in result.stdout
        assert "skipped workflow 'Reactorcide Release'" in result.stdout
        assert not triggers_file.exists()

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
        assert "No workflow or job definitions found" in result.stdout
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
        assert data["jobs"][0]["job_command"] == "runnerlib run --job-command 'pytest'"
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


class TestSharedCIPathAttribution:
    """Changed .reactorcide/ paths no workflow claims (plugins, scripts,
    tests) attribute to every candidate workflow, so a head-CI rule whose
    paths cover them can authorize the change."""

    POLICY_BROAD = """
version: 1
defaults:
  ci_source: base
  profile: standard
head_ci:
  - id: ci-dev
    actors:
      any:
        - vcs_user:github/todpunk
    workflows:
      - app-pr
    paths:
      - ".reactorcide/**"
    events:
      - pull_request_updated
    base_branches:
      - main
    head_repository: same
    use:
      ci_source: head
      profile: pr-untrusted
      workers: default
"""

    POLICY_NARROW = POLICY_BROAD.replace(
        '- ".reactorcide/**"',
        '- ".reactorcide/workflows/**"\n      - ".reactorcide/jobs/**"',
    )

    def _make_checkout(self, root: Path) -> None:
        workflows = root / ".reactorcide" / "workflows"
        jobs = root / ".reactorcide" / "jobs"
        plugins = root / ".reactorcide" / "plugins"
        workflows.mkdir(parents=True)
        jobs.mkdir(parents=True)
        plugins.mkdir(parents=True)
        _write_yaml(workflows / "pr.yaml", {
            "id": "app-pr",
            "name": "App PR",
            "on": {"events": ["pull_request_updated"]},
            "jobs": {"test": {"job_file": "test.yaml"}},
        })
        _write_yaml(jobs / "test.yaml", {
            "name": "test",
            "job": {"image": "alpine:latest", "command": "make test"},
        })
        (plugins / "plugin_ci.py").write_text("VALUE = 1\n")

    def _run_eval(self, policy: str):
        with tempfile.TemporaryDirectory() as tmpdir:
            base = Path(tmpdir)
            ci_dir = base / "ci"
            src_dir = base / "src"
            src_dir.mkdir()
            self._make_checkout(ci_dir / "base")
            self._make_checkout(ci_dir / "head")
            (ci_dir / "head" / ".reactorcide" / "plugins" / "plugin_ci.py").write_text("VALUE = 2\n")
            triggers_file = base / "triggers.json"

            result = runner.invoke(app, [
                "eval",
                "--ci-source-dir", str(ci_dir),
                "--source-dir", str(src_dir),
                "--event-type", "pull_request_updated",
                "--pr-number", "5",
                "--base-ref", "main",
                "--changed-file", ".reactorcide/plugins/plugin_ci.py",
                "--triggers-file", str(triggers_file),
            ], env={
                "REACTORCIDE_CI_POLICY": policy,
                "REACTORCIDE_ACTOR_SUBJECTS": '["vcs_user:github/todpunk"]',
                "REACTORCIDE_SHA": "headsha",
                "REACTORCIDE_DIFF_BASE": "basesha",
            })
            assert result.exit_code == 0, result.output
            with open(triggers_file) as f:
                return json.load(f)

    def test_plugin_change_qualifies_for_authorized_head_ci(self):
        data = self._run_eval(self.POLICY_BROAD)
        assert data["policy_violations"] == []
        workflow = data["workflows"][0]
        assert workflow["ci_origin"] == "head"
        assert workflow["policy_rule_id"] == "ci-dev"
        assert ".reactorcide/plugins/plugin_ci.py" in workflow["dependency_paths"]

    def test_plugin_change_denied_by_narrow_rule_paths(self):
        data = self._run_eval(self.POLICY_NARROW)
        assert data["workflows"][0]["ci_origin"] == "base"
        violation_paths = {item["path"] for item in data["policy_violations"]}
        assert ".reactorcide/plugins/plugin_ci.py" in violation_paths


class TestEvalMixedTrustNodeAuthority:
    """Policy-controlled trusted base nodes inside a head-CI workflow.

    The coordinator policy names asset-prepare and asset-seal as trusted base
    nodes. The eval must resolve those nodes from the exact base workflow
    specification while the other nodes run head CI with the untrusted
    profile."""

    POLICY = """
version: 1
defaults:
  ci_source: base
  profile: standard
head_ci:
  - id: csilgen
    actors:
      any: [repository_write]
    workflows: [csilgen-pr]
    paths: ['.reactorcide/**']
    events: [pull_request_updated]
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

    BASE_URL = "https://example.test/upstream.git"
    HEAD_URL = "https://example.test/fork.git"

    def _make_base_checkout(self, root: Path, include_seal: bool = True) -> None:
        workflows = root / ".reactorcide" / "workflows"
        jobs = root / ".reactorcide" / "jobs"
        workflows.mkdir(parents=True)
        jobs.mkdir(parents=True)
        wf_jobs = {
            "asset-prepare": {"job_file": "prepare.yaml"},
            "build": {"job_file": "build.yaml", "depends_on": ["asset-prepare"]},
        }
        if include_seal:
            wf_jobs["asset-seal"] = {"job_file": "seal.yaml", "depends_on": ["build"]}
        _write_yaml(workflows / "pr.yaml", {
            "id": "csilgen-pr",
            "name": "CSILgen PR",
            "on": {"events": ["pull_request_updated"], "branches": ["main"]},
            "jobs": wf_jobs,
        })
        _write_yaml(jobs / "prepare.yaml", {
            "name": "asset-prepare",
            "job": {"image": "alpine:latest", "command": "prepare-base"},
        })
        _write_yaml(jobs / "build.yaml", {
            "name": "build",
            "job": {"image": "alpine:latest", "command": "make build"},
        })
        _write_yaml(jobs / "seal.yaml", {
            "name": "asset-seal",
            "job": {"image": "alpine:latest", "command": "seal-base"},
        })

    def _make_head_checkout(self, root: Path, tamper: bool = True, reserved_field: bool = False,
                            drop_seal: bool = False) -> None:
        self._make_base_checkout(root)
        workflows = root / ".reactorcide" / "workflows"
        jobs = root / ".reactorcide" / "jobs"
        wf_jobs = {
            "asset-prepare": {"job_file": "prepare.yaml"},
            "build": {"job_file": "build.yaml", "depends_on": ["asset-prepare"]},
            "asset-seal": {"job_file": "seal.yaml", "depends_on": ["build"]},
        }
        if tamper:
            # The head tries to replace every part of the trusted node
            # specification. None of this may reach the trusted node that runs.
            wf_jobs["asset-prepare"] = {
                "image": "evil:latest",
                "command": "exfiltrate",
                "depends_on": [],
                "for_each": ["a", "b"],
                "condition": "always",
                "capabilities": ["docker"],
                "environment": {"EVIL": "yes"},
            }
            _write_yaml(jobs / "prepare.yaml", {
                "name": "asset-prepare",
                "job": {"image": "evil:latest", "command": "exfiltrate-jobfile"},
            })
            wf_jobs["build"] = {"job_file": "build.yaml", "depends_on": ["asset-prepare"]}
            _write_yaml(jobs / "build.yaml", {
                "name": "build",
                "job": {"image": "alpine:latest", "command": "make build-head"},
            })
        if reserved_field:
            wf_jobs["build"] = {"job_file": "build.yaml", "execution_profile": "standard"}
        if drop_seal:
            wf_jobs.pop("asset-seal", None)
        _write_yaml(workflows / "pr.yaml", {
            "id": "csilgen-pr",
            "name": "CSILgen PR",
            "on": {"events": ["pull_request_updated"], "branches": ["main"]},
            "jobs": wf_jobs,
        })

    def _run_eval(self, make_base, make_head):
        with tempfile.TemporaryDirectory() as tmpdir:
            base = Path(tmpdir)
            ci_dir = base / "ci"
            src_dir = base / "src"
            src_dir.mkdir()
            make_base(ci_dir / "base")
            make_head(ci_dir / "head")
            triggers_file = base / "triggers.json"
            result = runner.invoke(app, [
                "eval",
                "--ci-source-dir", str(ci_dir),
                "--source-dir", str(src_dir),
                "--event-type", "pull_request_updated",
                "--pr-number", "7",
                "--base-ref", "main",
                "--changed-file", ".reactorcide/workflows/pr.yaml",
                "--triggers-file", str(triggers_file),
            ], env={
                "REACTORCIDE_CI_POLICY": self.POLICY,
                "REACTORCIDE_ACTOR_SUBJECTS": '["repository_write"]',
                "REACTORCIDE_SHA": "headsha",
                "REACTORCIDE_DIFF_BASE": "basesha",
                "REACTORCIDE_CI_SOURCE_URL": self.BASE_URL,
                "REACTORCIDE_CI_SOURCE_REF": "basesha",
                "REACTORCIDE_HEAD_URL": self.HEAD_URL,
            })
            assert result.exit_code == 0, result.output
            with open(triggers_file) as f:
                return json.load(f)

    def _jobs_by_name(self, workflow):
        return {job["job_name"]: job for job in workflow["jobs"]}

    def test_mixed_trust_workflow_pins_node_authority(self):
        data = self._run_eval(self._make_base_checkout, self._make_head_checkout)
        assert data["policy_violations"] == []
        workflow = data["workflows"][0]
        assert workflow["ci_origin"] == "head"
        assert workflow["execution_profile"] == "pr-untrusted"
        jobs = self._jobs_by_name(workflow)

        prepare = jobs["asset-prepare"]
        assert prepare["ci_origin"] == "base"
        assert prepare["execution_profile"] == "standard"
        assert prepare["worker_class"] == "default"
        assert prepare["ci_source_url"] == self.BASE_URL
        assert prepare["ci_source_ref"] == "basesha"

        build = jobs["build"]
        assert "ci_origin" not in build
        assert "execution_profile" not in build
        assert build["ci_source_url"] == self.HEAD_URL
        assert build["ci_source_ref"] == "headsha"
        assert "make build-head" in build["job_command"]

        seal = jobs["asset-seal"]
        assert seal["ci_origin"] == "base"
        assert seal["execution_profile"] == "standard"
        assert seal["ci_source_ref"] == "basesha"
        assert seal["depends_on"] == ["build"]
        assert "seal-base" in seal["job_command"]

    def test_head_changes_to_trusted_node_have_no_effect(self):
        data = self._run_eval(self._make_base_checkout, self._make_head_checkout)
        prepare = self._jobs_by_name(data["workflows"][0])["asset-prepare"]
        # The base specification wins for every field the head tampered with.
        assert prepare["container_image"] == "alpine:latest"
        assert "prepare-base" in prepare["job_command"]
        assert "exfiltrate" not in prepare["job_command"]
        assert prepare.get("depends_on", []) == []
        assert "for_each" not in prepare
        assert "capabilities" not in prepare
        assert prepare["env"].get("EVIL") is None
        assert prepare.get("condition", "all_success") == "all_success"

    def test_head_removing_trusted_node_still_runs_it(self):
        data = self._run_eval(
            self._make_base_checkout,
            lambda root: self._make_head_checkout(root, drop_seal=True),
        )
        workflow = data["workflows"][0]
        assert workflow["ci_origin"] == "head"
        jobs = self._jobs_by_name(workflow)
        assert "asset-seal" in jobs
        assert jobs["asset-seal"]["ci_origin"] == "base"
        assert "seal-base" in jobs["asset-seal"]["job_command"]

    def test_missing_base_node_fails_closed_to_base_ci(self):
        data = self._run_eval(
            lambda root: self._make_base_checkout(root, include_seal=False),
            self._make_head_checkout,
        )
        workflow = data["workflows"][0]
        assert workflow["ci_origin"] == "base"
        for job in workflow["jobs"]:
            assert "ci_origin" not in job
            assert "execution_profile" not in job
        violation_ids = {item.get("workflow_id") for item in data["policy_violations"]}
        assert "csilgen-pr" in violation_ids

    def test_reserved_authority_field_in_head_yaml_fails_closed(self):
        data = self._run_eval(
            self._make_base_checkout,
            lambda root: self._make_head_checkout(root, tamper=False, reserved_field=True),
        )
        workflow = data["workflows"][0]
        # The head workflow file is rejected, so no head candidate exists and
        # the decision fails closed to trusted base CI.
        assert workflow["ci_origin"] == "base"
        assert data["policy_violations"] != []
