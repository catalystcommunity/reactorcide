"""
Tests for workflow orchestration utilities.
"""

import json
import os
import subprocess
import tempfile
from pathlib import Path
from unittest.mock import patch, MagicMock, call

from src.workflow import (
    JobTrigger,
    WorkflowContext,
    trigger_job,
    submit_job,
    flush_triggers,
    is_job_running,
    get_job_result,
    log_next_job,
    changed_files,
    git_info,
    workflow_context,
    _get_context,
)


class TestJobTrigger:
    """Tests for JobTrigger dataclass."""

    def test_basic_trigger(self):
        """Test creating a basic job trigger."""
        trigger = JobTrigger(job_name="test")

        assert trigger.job_name == "test"
        assert trigger.depends_on == []
        assert trigger.condition == "all_success"
        assert trigger.env == {}

    def test_trigger_with_all_fields(self):
        """Test creating a trigger with all fields."""
        trigger = JobTrigger(
            job_name="deploy",
            depends_on=["test", "build"],
            condition="all_success",
            env={"TARGET": "production"},
            source_type="git",
            source_url="https://github.com/user/repo.git",
            source_ref="main",
            ci_source_type="git",
            ci_source_url="https://github.com/user/ci.git",
            ci_source_ref="main",
            container_image="reactorcide/runner:latest",
            job_command="make deploy",
            priority=10,
            timeout=1800,
        )

        assert trigger.job_name == "deploy"
        assert trigger.depends_on == ["test", "build"]
        assert trigger.env == {"TARGET": "production"}
        assert trigger.source_type == "git"
        assert trigger.priority == 10
        assert trigger.timeout == 1800

    def test_to_dict_excludes_none(self):
        """Test that to_dict() excludes None values."""
        trigger = JobTrigger(
            job_name="test",
            env={"KEY": "value"},
            source_url="https://github.com/user/repo.git",
        )

        result = trigger.to_dict()

        # Should include non-None values
        assert result["job_name"] == "test"
        assert result["env"] == {"KEY": "value"}
        assert result["source_url"] == "https://github.com/user/repo.git"

        # Should exclude None values
        assert "source_type" not in result
        assert "priority" not in result
        assert "timeout" not in result


class TestWorkflowContext:
    """Tests for WorkflowContext class."""

    def test_initialization(self):
        """Test WorkflowContext initialization."""
        ctx = WorkflowContext(triggers_file="/tmp/test-triggers.json")

        assert ctx.triggers_file == Path("/tmp/test-triggers.json")
        assert ctx.triggers == []

    def test_environment_properties(self):
        """Test accessing environment properties."""
        with patch.dict(os.environ, {
            "REACTORCIDE_JOB_ID": "job-123",
            "REACTORCIDE_GIT_BRANCH": "main",
            "REACTORCIDE_GIT_COMMIT": "abc123",
            "REACTORCIDE_GIT_REF": "refs/heads/main",
        }):
            ctx = WorkflowContext()

            assert ctx.job_id == "job-123"
            assert ctx.branch == "main"
            assert ctx.commit == "abc123"
            assert ctx.ref == "refs/heads/main"

    def test_trigger_job(self):
        """Test triggering a job."""
        ctx = WorkflowContext()

        ctx.trigger_job("deploy", env={"TARGET": "staging"})

        assert len(ctx.triggers) == 1
        assert ctx.triggers[0].job_name == "deploy"
        assert ctx.triggers[0].env == {"TARGET": "staging"}

    def test_trigger_job_with_dependencies(self):
        """Test triggering a job with dependencies."""
        ctx = WorkflowContext()

        ctx.trigger_job(
            "deploy",
            env={"TARGET": "production"},
            depends_on=["test", "build"],
            condition="all_success",
        )

        assert len(ctx.triggers) == 1
        trigger = ctx.triggers[0]
        assert trigger.job_name == "deploy"
        assert trigger.depends_on == ["test", "build"]
        assert trigger.condition == "all_success"

    def test_submit_job_alias(self):
        """Test that submit_job is an alias for trigger_job."""
        ctx = WorkflowContext()

        ctx.submit_job("test", env={"KEY": "value"})

        assert len(ctx.triggers) == 1
        assert ctx.triggers[0].job_name == "test"

    def test_flush_triggers_creates_file(self):
        """Test that flush_triggers creates a JSON file."""
        with tempfile.TemporaryDirectory() as tmpdir:
            triggers_file = Path(tmpdir) / "triggers.json"
            ctx = WorkflowContext(triggers_file=str(triggers_file))

            ctx.trigger_job("test")
            ctx.trigger_job("deploy", depends_on=["test"])
            ctx.flush_triggers()

            assert triggers_file.exists()

            with open(triggers_file) as f:
                data = json.load(f)

            assert data["type"] == "trigger_job"
            assert len(data["jobs"]) == 2
            assert data["jobs"][0]["job_name"] == "test"
            assert data["jobs"][1]["job_name"] == "deploy"

    def test_flush_triggers_appends_to_existing(self):
        """Test that flush_triggers appends to existing file."""
        with tempfile.TemporaryDirectory() as tmpdir:
            triggers_file = Path(tmpdir) / "triggers.json"

            # Create existing file
            existing_data = {
                "type": "trigger_job",
                "jobs": [{"job_name": "existing"}]
            }
            with open(triggers_file, 'w') as f:
                json.dump(existing_data, f)

            # Add new triggers
            ctx = WorkflowContext(triggers_file=str(triggers_file))
            ctx.trigger_job("new")
            ctx.flush_triggers()

            # Verify both exist
            with open(triggers_file) as f:
                data = json.load(f)

            assert len(data["jobs"]) == 2
            assert data["jobs"][0]["job_name"] == "existing"
            assert data["jobs"][1]["job_name"] == "new"

    def test_flush_triggers_empty_does_nothing(self):
        """Test that flush_triggers does nothing when no triggers."""
        with tempfile.TemporaryDirectory() as tmpdir:
            triggers_file = Path(tmpdir) / "triggers.json"
            ctx = WorkflowContext(triggers_file=str(triggers_file))

            ctx.flush_triggers()

            assert not triggers_file.exists()

    def test_is_job_running_without_api(self):
        """Test is_job_running returns False when API not configured."""
        ctx = WorkflowContext()

        result = ctx.is_job_running("deploy")

        assert result is False

    def test_get_job_result_without_api(self):
        """Test get_job_result returns None when API not configured."""
        ctx = WorkflowContext()

        result = ctx.get_job_result("test")

        assert result is None

    def test_log_next_job(self):
        """Test log_next_job prints message."""
        ctx = WorkflowContext()

        # Should not raise exception
        ctx.log_next_job("deploy", reason="tests passed")


class TestModuleLevelFunctions:
    """Tests for module-level convenience functions."""

    def setUp(self):
        """Reset global context before each test."""
        import src.workflow as workflow_module
        workflow_module._global_context = None

    def test_trigger_job_convenience(self):
        """Test module-level trigger_job function."""
        self.setUp()

        trigger_job("test", env={"KEY": "value"})

        ctx = _get_context()
        assert len(ctx.triggers) == 1
        assert ctx.triggers[0].job_name == "test"

    def test_submit_job_convenience(self):
        """Test module-level submit_job function."""
        self.setUp()

        submit_job("deploy", env={"TARGET": "staging"})

        ctx = _get_context()
        assert len(ctx.triggers) == 1
        assert ctx.triggers[0].job_name == "deploy"

    def test_flush_triggers_convenience(self):
        """Test module-level flush_triggers function."""
        self.setUp()

        with tempfile.TemporaryDirectory() as tmpdir:
            os.environ["TRIGGERS_FILE"] = str(Path(tmpdir) / "triggers.json")

            trigger_job("test")

            # Override the global context's triggers file
            ctx = _get_context()
            ctx.triggers_file = Path(tmpdir) / "triggers.json"

            flush_triggers()

            assert ctx.triggers_file.exists()

    def test_is_job_running_convenience(self):
        """Test module-level is_job_running function."""
        self.setUp()

        result = is_job_running("deploy")

        assert result is False

    def test_get_job_result_convenience(self):
        """Test module-level get_job_result function."""
        self.setUp()

        result = get_job_result("test")

        assert result is None

    def test_log_next_job_convenience(self):
        """Test module-level log_next_job function."""
        self.setUp()

        # Should not raise exception
        log_next_job("deploy", reason="tests passed")


class TestGitUtilities:
    """Tests for git utility functions."""

    @patch('subprocess.run')
    def test_changed_files(self, mock_run):
        """Test changed_files function."""
        mock_run.return_value = MagicMock(
            stdout="file1.py\nfile2.js\nfile3.md\n",
            returncode=0
        )

        files = changed_files("HEAD^", "HEAD", "/job/src")

        assert files == ["file1.py", "file2.js", "file3.md"]
        mock_run.assert_called_once_with(
            ["git", "diff", "--name-only", "HEAD^", "HEAD"],
            cwd="/job/src",
            capture_output=True,
            text=True,
            check=True
        )

    @patch('subprocess.run')
    def test_changed_files_with_custom_refs(self, mock_run):
        """Test changed_files with custom refs."""
        mock_run.return_value = MagicMock(
            stdout="src/main.py\n",
            returncode=0
        )

        files = changed_files("origin/main", "feature-branch", "/project")

        assert files == ["src/main.py"]
        mock_run.assert_called_once_with(
            ["git", "diff", "--name-only", "origin/main", "feature-branch"],
            cwd="/project",
            capture_output=True,
            text=True,
            check=True
        )

    @patch('subprocess.run')
    def test_changed_files_error(self, mock_run):
        """Test changed_files handles errors gracefully."""
        mock_run.side_effect = subprocess.CalledProcessError(1, "git")

        files = changed_files()

        assert files == []

    @patch('subprocess.run')
    def test_git_info(self, mock_run):
        """Test git_info function."""
        # Mock multiple git commands
        mock_run.side_effect = [
            MagicMock(stdout="main\n", returncode=0),  # branch
            MagicMock(stdout="abc123def456\n", returncode=0),  # commit
            subprocess.CalledProcessError(1, "git"),  # tag (not on a tag)
            MagicMock(stdout="https://github.com/user/repo.git\n", returncode=0),  # remote
        ]

        info = git_info("/job/src")

        assert info["branch"] == "main"
        assert info["commit"] == "abc123def456"
        assert info["short_commit"] == "abc123d"
        assert info["tag"] is None
        assert info["remote_url"] == "https://github.com/user/repo.git"

    @patch('subprocess.run')
    def test_git_info_with_tag(self, mock_run):
        """Test git_info when on a tag."""
        mock_run.side_effect = [
            MagicMock(stdout="main\n", returncode=0),  # branch
            MagicMock(stdout="abc123\n", returncode=0),  # commit
            MagicMock(stdout="v1.0.0\n", returncode=0),  # tag
            MagicMock(stdout="https://github.com/user/repo.git\n", returncode=0),  # remote
        ]

        info = git_info()

        assert info["tag"] == "v1.0.0"

    @patch('subprocess.run')
    def test_git_info_all_errors(self, mock_run):
        """Test git_info when all commands fail."""
        mock_run.side_effect = subprocess.CalledProcessError(1, "git")

        info = git_info()

        assert info["branch"] is None
        assert info["commit"] is None
        assert info["short_commit"] is None
        assert info["tag"] is None
        assert info["remote_url"] is None


class TestWorkflowContextManager:
    """Tests for workflow_context context manager."""

    def test_context_manager_success(self):
        """Test context manager flushes on success."""
        with tempfile.TemporaryDirectory() as tmpdir:
            triggers_file = Path(tmpdir) / "triggers.json"

            with workflow_context(str(triggers_file)) as ctx:
                ctx.trigger_job("test")

            # Verify triggers were flushed
            assert triggers_file.exists()

            with open(triggers_file) as f:
                data = json.load(f)

            assert len(data["jobs"]) == 1

    def test_context_manager_exception(self):
        """Test context manager does not flush on exception."""
        with tempfile.TemporaryDirectory() as tmpdir:
            triggers_file = Path(tmpdir) / "triggers.json"

            try:
                with workflow_context(str(triggers_file)) as ctx:
                    ctx.trigger_job("test")
                    raise RuntimeError("Test exception")
            except RuntimeError:
                pass

            # Verify triggers were NOT flushed
            assert not triggers_file.exists()

    def test_context_manager_provides_context(self):
        """Test context manager provides WorkflowContext."""
        with workflow_context() as ctx:
            assert isinstance(ctx, WorkflowContext)
            assert hasattr(ctx, 'trigger_job')
            assert hasattr(ctx, 'flush_triggers')


class TestAPITriggerSubmission:
    """Tests for API-based trigger submission."""

    def test_submit_triggers_via_api_success(self):
        """Test that successful API submission deletes triggers.json."""
        with tempfile.TemporaryDirectory() as tmpdir:
            triggers_file = Path(tmpdir) / "triggers.json"

            with patch.dict(os.environ, {
                "REACTORCIDE_COORDINATOR_URL": "http://coordinator:6080",
                "REACTORCIDE_API_TOKEN": "test-token",
                "REACTORCIDE_JOB_ID": "job-123",
            }):
                ctx = WorkflowContext(triggers_file=str(triggers_file))
                ctx.trigger_job("test", env={"KEY": "value"})

                # Mock the API call to succeed
                mock_response = MagicMock()
                mock_response.status = 201
                mock_response.__enter__ = MagicMock(return_value=mock_response)
                mock_response.__exit__ = MagicMock(return_value=False)

                with patch('urllib.request.urlopen', return_value=mock_response) as mock_urlopen:
                    ctx.flush_triggers()

                    # Verify API was called
                    mock_urlopen.assert_called_once()
                    req = mock_urlopen.call_args[0][0]
                    assert req.full_url == "http://coordinator:6080/api/v1/jobs/job-123/triggers"
                    assert req.get_header("Authorization") == "Bearer test-token"
                    assert req.get_header("Content-type") == "application/json"

                    # Verify body contains trigger data
                    body = json.loads(req.data.decode("utf-8"))
                    assert body["type"] == "trigger_job"
                    assert len(body["jobs"]) == 1
                    assert body["jobs"][0]["job_name"] == "test"

                # triggers.json should be deleted on API success
                assert not triggers_file.exists()

    def test_submit_triggers_via_api_failure_leaves_file(self):
        """Test that API failure leaves triggers.json as fallback."""
        with tempfile.TemporaryDirectory() as tmpdir:
            triggers_file = Path(tmpdir) / "triggers.json"

            with patch.dict(os.environ, {
                "REACTORCIDE_COORDINATOR_URL": "http://coordinator:6080",
                "REACTORCIDE_API_TOKEN": "test-token",
                "REACTORCIDE_JOB_ID": "job-123",
            }):
                ctx = WorkflowContext(triggers_file=str(triggers_file))
                ctx.trigger_job("test")

                # Mock the API call to fail
                import urllib.error
                with patch('urllib.request.urlopen', side_effect=urllib.error.URLError("connection refused")):
                    ctx.flush_triggers()

                # triggers.json should still exist as fallback
                assert triggers_file.exists()

                with open(triggers_file) as f:
                    data = json.load(f)
                assert data["type"] == "trigger_job"
                assert len(data["jobs"]) == 1

    def test_no_api_credentials_skips_api_submission(self):
        """Test that missing credentials skip API call and keep file."""
        with tempfile.TemporaryDirectory() as tmpdir:
            triggers_file = Path(tmpdir) / "triggers.json"

            # No REACTORCIDE_COORDINATOR_URL or REACTORCIDE_API_TOKEN set
            with patch.dict(os.environ, {}, clear=False):
                # Remove any existing env vars
                env = os.environ.copy()
                env.pop("REACTORCIDE_COORDINATOR_URL", None)
                env.pop("REACTORCIDE_API_TOKEN", None)
                env.pop("REACTORCIDE_JOB_ID", None)

                with patch.dict(os.environ, env, clear=True):
                    ctx = WorkflowContext(triggers_file=str(triggers_file))
                    ctx.trigger_job("test")

                    with patch('urllib.request.urlopen') as mock_urlopen:
                        ctx.flush_triggers()

                        # API should NOT be called
                        mock_urlopen.assert_not_called()

                    # File should exist
                    assert triggers_file.exists()

    def test_api_http_error_leaves_file(self):
        """Test that HTTP errors leave triggers.json as fallback."""
        with tempfile.TemporaryDirectory() as tmpdir:
            triggers_file = Path(tmpdir) / "triggers.json"

            with patch.dict(os.environ, {
                "REACTORCIDE_COORDINATOR_URL": "http://coordinator:6080",
                "REACTORCIDE_API_TOKEN": "test-token",
                "REACTORCIDE_JOB_ID": "job-123",
            }):
                ctx = WorkflowContext(triggers_file=str(triggers_file))
                ctx.trigger_job("test")

                import urllib.error
                with patch('urllib.request.urlopen', side_effect=urllib.error.HTTPError(
                    url="http://coordinator:6080/api/v1/jobs/job-123/triggers",
                    code=500,
                    msg="Internal Server Error",
                    hdrs={},
                    fp=None,
                )):
                    ctx.flush_triggers()

                # triggers.json should still exist
                assert triggers_file.exists()

    def test_missing_job_id_skips_api(self):
        """Test that missing job ID skips API submission."""
        with tempfile.TemporaryDirectory() as tmpdir:
            triggers_file = Path(tmpdir) / "triggers.json"

            with patch.dict(os.environ, {
                "REACTORCIDE_COORDINATOR_URL": "http://coordinator:6080",
                "REACTORCIDE_API_TOKEN": "test-token",
                # No REACTORCIDE_JOB_ID
            }, clear=False):
                env = os.environ.copy()
                env.pop("REACTORCIDE_JOB_ID", None)

                with patch.dict(os.environ, env, clear=True):
                    ctx = WorkflowContext(triggers_file=str(triggers_file))
                    ctx.trigger_job("test")

                    with patch('urllib.request.urlopen') as mock_urlopen:
                        ctx.flush_triggers()
                        mock_urlopen.assert_not_called()

                    assert triggers_file.exists()


class TestIntegrationPatterns:
    """Integration tests for common workflow patterns."""

    def test_simple_pipeline_pattern(self):
        """Test simple test-then-deploy pattern."""
        with tempfile.TemporaryDirectory() as tmpdir:
            triggers_file = Path(tmpdir) / "triggers.json"

            with patch.dict(os.environ, {"REACTORCIDE_GIT_BRANCH": "main"}):
                with workflow_context(str(triggers_file)) as ctx:
                    # Simulate test passing
                    test_passed = True

                    if test_passed and ctx.branch == "main":
                        ctx.trigger_job("deploy", env={"TARGET": "production"})

            # Verify deploy was triggered
            with open(triggers_file) as f:
                data = json.load(f)

            assert len(data["jobs"]) == 1
            assert data["jobs"][0]["job_name"] == "deploy"
            assert data["jobs"][0]["env"]["TARGET"] == "production"

    def test_parallel_pipeline_pattern(self):
        """Test parallel jobs with dependencies."""
        with tempfile.TemporaryDirectory() as tmpdir:
            triggers_file = Path(tmpdir) / "triggers.json"

            with workflow_context(str(triggers_file)) as ctx:
                # Trigger parallel jobs
                ctx.trigger_job("test", env={"SUITE": "unit"})
                ctx.trigger_job("lint", env={"TOOL": "ruff"})

                # Trigger job that depends on both
                ctx.trigger_job(
                    "build",
                    depends_on=["test", "lint"],
                    condition="all_success"
                )

            # Verify all three were triggered
            with open(triggers_file) as f:
                data = json.load(f)

            assert len(data["jobs"]) == 3

            job_names = [job["job_name"] for job in data["jobs"]]
            assert "test" in job_names
            assert "lint" in job_names
            assert "build" in job_names

            # Verify build depends on test and lint
            build_job = next(j for j in data["jobs"] if j["job_name"] == "build")
            assert set(build_job["depends_on"]) == {"test", "lint"}
            assert build_job["condition"] == "all_success"

    def test_conditional_deploy_pattern(self):
        """Test conditional deploy based on branch."""
        with tempfile.TemporaryDirectory() as tmpdir:
            triggers_file = Path(tmpdir) / "triggers.json"

            # Test on feature branch - should not deploy
            with patch.dict(os.environ, {"REACTORCIDE_GIT_BRANCH": "feature/test"}):
                with workflow_context(str(triggers_file)) as ctx:
                    ctx.trigger_job("test")

                    if ctx.branch == "main":
                        ctx.trigger_job("deploy")

            with open(triggers_file) as f:
                data = json.load(f)

            # Only test should be triggered
            assert len(data["jobs"]) == 1
            assert data["jobs"][0]["job_name"] == "test"

            # Clear file
            triggers_file.unlink()

            # Test on main branch - should deploy
            with patch.dict(os.environ, {"REACTORCIDE_GIT_BRANCH": "main"}):
                with workflow_context(str(triggers_file)) as ctx:
                    ctx.trigger_job("test")

                    if ctx.branch == "main":
                        ctx.trigger_job("deploy")

            with open(triggers_file) as f:
                data = json.load(f)

            # Both should be triggered
            assert len(data["jobs"]) == 2
            job_names = [job["job_name"] for job in data["jobs"]]
            assert "test" in job_names
            assert "deploy" in job_names
