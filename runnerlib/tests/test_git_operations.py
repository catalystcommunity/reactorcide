"""Integration tests for git operations in runnerlib."""

import tempfile
import shutil
import subprocess
from pathlib import Path
import pytest
from unittest.mock import patch, MagicMock
from git.exc import GitCommandError

from src.source_prep import checkout_git_repo, get_code_directory_path
from src.git_ops import get_files_changed, get_repository_info
from src.config import RunnerConfig


class TestGitOperations:
    """Test git operations including clone, checkout, and files-changed."""

    @pytest.fixture
    def test_repo(self):
        """Create a test git repository."""
        repo_dir = tempfile.mkdtemp()

        # Initialize git repo
        subprocess.run(["git", "init"], cwd=repo_dir, check=True)
        subprocess.run(["git", "config", "user.name", "Test User"], cwd=repo_dir, check=True)
        subprocess.run(["git", "config", "user.email", "test@example.com"], cwd=repo_dir, check=True)
        # Disable GPG signing for tests
        subprocess.run(["git", "config", "commit.gpgsign", "false"], cwd=repo_dir, check=True)

        # Create initial commit
        test_file = Path(repo_dir) / "test.txt"
        test_file.write_text("Initial content")
        subprocess.run(["git", "add", "."], cwd=repo_dir, check=True)
        subprocess.run(["git", "commit", "-m", "Initial commit"], cwd=repo_dir, check=True)

        # Create a branch with changes
        subprocess.run(["git", "checkout", "-b", "feature"], cwd=repo_dir, check=True)
        test_file.write_text("Modified content")
        new_file = Path(repo_dir) / "new.txt"
        new_file.write_text("New file")
        subprocess.run(["git", "add", "."], cwd=repo_dir, check=True)
        subprocess.run(["git", "commit", "-m", "Feature changes"], cwd=repo_dir, check=True)

        # Go back to main (or master)
        try:
            subprocess.run(["git", "checkout", "main"], cwd=repo_dir, check=True, capture_output=True)
        except Exception:
            subprocess.run(["git", "checkout", "master"], cwd=repo_dir, check=True, capture_output=True)

        yield repo_dir
        shutil.rmtree(repo_dir, ignore_errors=True)

    @pytest.fixture
    def job_config(self):
        """Create a basic job configuration."""
        # Create config that uses a real job directory
        return RunnerConfig(
            code_dir="/job/src",
            job_dir="/job",
            job_command="echo test",
            runner_image="alpine:latest"
        )

    def test_checkout_local_repo(self, test_repo, job_config):
        """Test checking out a local git repository."""
        # Perform checkout
        checkout_git_repo(test_repo, "feature", job_config)

        # Verify files were checked out
        code_path = get_code_directory_path(job_config)
        assert code_path.exists()
        assert (code_path / "test.txt").exists()
        assert (code_path / "new.txt").exists()
        assert (code_path / "test.txt").read_text() == "Modified content"

        # Cleanup
        shutil.rmtree("./job", ignore_errors=True)

    def test_checkout_specific_commit(self, test_repo, job_config):
        """Test checking out a specific commit."""
        # Get the commit hash of the feature branch
        result = subprocess.run(
            ["git", "rev-parse", "feature"],
            cwd=test_repo,
            capture_output=True,
            text=True,
            check=True
        )
        commit_hash = result.stdout.strip()

        checkout_git_repo(test_repo, commit_hash[:8], job_config)

        # Verify correct commit was checked out
        code_path = get_code_directory_path(job_config)
        assert (code_path / "new.txt").exists()

        # Cleanup
        shutil.rmtree("./job", ignore_errors=True)

    def test_checkout_main_branch(self, test_repo, job_config):
        """Test checking out the main/master branch."""
        # Try main first, then master
        try:
            checkout_git_repo(test_repo, "main", job_config)
        except Exception:
            checkout_git_repo(test_repo, "master", job_config)

        # Verify main/master branch content
        code_path = get_code_directory_path(job_config)
        assert (code_path / "test.txt").exists()
        assert not (code_path / "new.txt").exists()
        assert (code_path / "test.txt").read_text() == "Initial content"

        # Cleanup
        shutil.rmtree("./job", ignore_errors=True)

    def test_git_files_changed(self, test_repo, job_config):
        """Test git files-changed command."""
        # Checkout the feature branch
        checkout_git_repo(test_repo, "feature", job_config)

        # Get files changed from main/master to feature
        code_path = get_code_directory_path(job_config)
        try:
            changed_files = get_files_changed("main", str(code_path))
        except Exception:
            changed_files = get_files_changed("master", str(code_path))

        # Should show test.txt as modified and new.txt as added
        assert "test.txt" in str(changed_files)
        assert "new.txt" in str(changed_files)

        # Cleanup
        shutil.rmtree("./job", ignore_errors=True)

    def test_git_info(self, test_repo, job_config):
        """Test git info command."""
        # Checkout the feature branch
        checkout_git_repo(test_repo, "feature", job_config)

        # Get git info
        code_path = get_code_directory_path(job_config)
        info = get_repository_info(str(code_path))

        # Verify info contains expected fields
        assert info["current_branch"] == "feature"
        assert info["current_commit"] is not None
        assert info["is_dirty"] is False
        assert info["error"] is None

        # Cleanup
        shutil.rmtree("./job", ignore_errors=True)

    @patch('src.source_prep.Repo')
    def test_checkout_remote_repo(self, mock_repo_class, job_config):
        """Test checking out a remote repository (mocked)."""
        # Mock the Repo class and its methods
        mock_repo = MagicMock()
        mock_repo_class.clone_from.return_value = mock_repo
        mock_repo.git.checkout.return_value = None

        # This should call git clone
        checkout_git_repo("https://github.com/example/repo.git", "main", job_config)

        # Verify clone_from was called with correct arguments
        mock_repo_class.clone_from.assert_called_once()
        call_args = mock_repo_class.clone_from.call_args
        assert "https://github.com/example/repo.git" in str(call_args)

    def test_checkout_invalid_ref(self, test_repo, job_config):
        """Test checking out an invalid ref."""
        # Should raise an error (GitCommandError from GitPython)
        with pytest.raises(GitCommandError):
            checkout_git_repo(test_repo, "nonexistent-branch", job_config)

        # Cleanup
        shutil.rmtree("./job", ignore_errors=True)

    def test_git_files_changed_no_changes(self, test_repo, job_config):
        """Test git files-changed when there are no changes."""
        # Checkout main/master
        try:
            checkout_git_repo(test_repo, "main", job_config)
            ref = "main"
        except Exception:
            checkout_git_repo(test_repo, "master", job_config)
            ref = "master"

        # Compare main to itself - should show no changes
        code_path = get_code_directory_path(job_config)
        changed_files = get_files_changed(ref, str(code_path))

        # Should be empty or minimal
        assert len(changed_files) == 0

        # Cleanup
        shutil.rmtree("./job", ignore_errors=True)

    def test_checkout_creates_job_directory(self, test_repo, job_config):
        """Test that checkout creates the job directory structure."""
        # Ensure job dir doesn't exist initially
        if Path("./job").exists():
            shutil.rmtree("./job")

        # Try main first, fallback to master
        try:
            checkout_git_repo(test_repo, "main", job_config)
        except Exception:
            checkout_git_repo(test_repo, "master", job_config)

        # Verify job directory was created
        assert Path("./job").exists()
        assert Path("./job").is_dir()

        # Cleanup
        shutil.rmtree("./job", ignore_errors=True)

    def test_multiple_checkouts_clean_state(self, test_repo, job_config):
        """Test that multiple checkouts maintain clean state."""
        # First checkout main/master
        try:
            checkout_git_repo(test_repo, "main", job_config)
        except Exception:
            checkout_git_repo(test_repo, "master", job_config)

        code_path = get_code_directory_path(job_config)
        assert not (code_path / "new.txt").exists()

        # Then checkout feature - should clean and replace
        checkout_git_repo(test_repo, "feature", job_config)
        assert (code_path / "new.txt").exists()

        # Checkout main/master again - new.txt should be gone
        try:
            checkout_git_repo(test_repo, "main", job_config)
        except Exception:
            checkout_git_repo(test_repo, "master", job_config)
        assert not (code_path / "new.txt").exists()

        # Cleanup
        shutil.rmtree("./job", ignore_errors=True)