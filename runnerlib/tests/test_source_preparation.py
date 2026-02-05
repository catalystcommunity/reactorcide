"""Tests for source preparation with multiple strategies."""

import os
import tempfile
import shutil
import pytest
from pathlib import Path
from git import Repo

from src.config import get_config
from src.source_prep import prepare_source, prepare_ci_source


class TestSourcePreparation:
    """Test cases for source preparation strategies."""

    def setup_method(self):
        """Set up test environment."""
        self.temp_dir = tempfile.mkdtemp()

        # Get current directory, handling case where we're in a deleted directory
        try:
            self.original_cwd = os.getcwd()
        except (FileNotFoundError, OSError):
            # If current directory doesn't exist (previous test deleted it),
            # change to a safe location first
            os.chdir(tempfile.gettempdir())
            self.original_cwd = os.getcwd()

        os.chdir(self.temp_dir)

    def teardown_method(self):
        """Clean up test environment."""
        # Always change back to original directory, even if cleanup fails
        try:
            os.chdir(self.original_cwd)
        except Exception:
            pass  # Best effort to restore directory

        # Clean up temp directory
        if os.path.exists(self.temp_dir):
            # For git repos, we need to ensure .git directories are writable
            try:
                for root, dirs, files in os.walk(self.temp_dir):
                    for d in dirs:
                        os.chmod(os.path.join(root, d), 0o755)
                    for f in files:
                        os.chmod(os.path.join(root, f), 0o644)
            except Exception:
                pass  # Best effort

            # Now remove the directory
            try:
                shutil.rmtree(self.temp_dir)
            except Exception:
                # If still fails, try harder with onerror handler
                def handle_remove_readonly(func, path, exc):
                    os.chmod(path, 0o777)
                    func(path)
                try:
                    shutil.rmtree(self.temp_dir, onerror=handle_remove_readonly)
                except Exception:
                    pass  # Final fallback - ignore errors

    def test_no_source_preparation(self):
        """Test job with no source preparation (source_type=none)."""
        # Configure with no source
        config = get_config(
            job_command="echo 'hello'",
            source_type="none"
        )

        # Prepare source should return None (no source preparation needed)
        result = prepare_source(config)
        assert result is None

        # Job directory is NOT created when source_type=none
        # (it may be pre-mounted or not needed at all)
        job_path = Path("./job")
        # Note: Job directory will be created later by other parts of the system if needed

    def test_no_source_preparation_default(self):
        """Test job with no source preparation (default - source_type not set)."""
        # Configure without specifying source_type
        config = get_config(job_command="echo 'hello'")

        # Prepare source should return None
        result = prepare_source(config)
        assert result is None

    def test_git_source_preparation(self):
        """Test git source preparation."""
        # Create a test git repository
        test_repo_dir = Path(self.temp_dir) / "test_repo"
        test_repo_dir.mkdir()
        repo = Repo.init(test_repo_dir)

        # Add a test file
        test_file = test_repo_dir / "test.txt"
        test_file.write_text("test content")
        repo.index.add([str(test_file)])
        repo.index.commit("Initial commit")

        # Configure with git source
        config = get_config(
            job_command="cat /job/src/test.txt",
            source_type="git",
            source_url=str(test_repo_dir),
            source_ref="main"
        )

        # Prepare source
        result = prepare_source(config)
        assert result is not None
        assert result.exists()
        assert (result / "test.txt").exists()
        assert (result / "test.txt").read_text() == "test content"

    def test_copy_source_preparation(self):
        """Test copy source preparation."""
        # Create a source directory
        source_dir = Path(self.temp_dir) / "source"
        source_dir.mkdir()
        (source_dir / "file.txt").write_text("source file")

        # Configure with copy source
        config = get_config(
            job_command="cat /job/src/file.txt",
            source_type="copy",
            source_url=str(source_dir)
        )

        # Prepare source
        result = prepare_source(config)
        assert result is not None
        assert result.exists()
        assert (result / "file.txt").exists()
        assert (result / "file.txt").read_text() == "source file"

    def test_dual_source_preparation(self):
        """Test preparation of both source and ci_source."""
        # Create source repo (untrusted code)
        source_repo_dir = Path(self.temp_dir) / "source_repo"
        source_repo_dir.mkdir()
        source_repo = Repo.init(source_repo_dir)
        (source_repo_dir / "app.py").write_text("print('hello from PR')")
        source_repo.index.add(["app.py"])
        source_repo.index.commit("PR commit")

        # Create CI repo (trusted code)
        ci_repo_dir = Path(self.temp_dir) / "ci_repo"
        ci_repo_dir.mkdir()
        ci_repo = Repo.init(ci_repo_dir)
        (ci_repo_dir / "pipeline.py").write_text("print('running tests')")
        ci_repo.index.add(["pipeline.py"])
        ci_repo.index.commit("CI commit")

        # Configure with both sources
        config = get_config(
            job_command="python /job/ci/pipeline.py",
            source_type="git",
            source_url=str(source_repo_dir),
            source_ref="main",
            ci_source_type="git",
            ci_source_url=str(ci_repo_dir),
            ci_source_ref="main"
        )

        # Prepare CI source first (as the CLI does)
        ci_result = prepare_ci_source(config)
        assert ci_result is not None
        assert ci_result.exists()
        assert (ci_result / "pipeline.py").exists()

        # Prepare regular source
        source_result = prepare_source(config)
        assert source_result is not None
        assert source_result.exists()
        assert (source_result / "app.py").exists()

        # Verify they're in different directories under the same job path
        assert ci_result != source_result
        assert ci_result.parent == source_result.parent  # Both under job/
        assert ci_result.name == "ci"
        assert source_result.name == "src"

    def test_ci_source_only(self):
        """Test preparation of CI source without regular source."""
        # Create CI repo
        ci_repo_dir = Path(self.temp_dir) / "ci_repo"
        ci_repo_dir.mkdir()
        ci_repo = Repo.init(ci_repo_dir)
        (ci_repo_dir / "deploy.sh").write_text("#!/bin/bash\necho deploying")
        ci_repo.index.add(["deploy.sh"])
        ci_repo.index.commit("CI commit")

        # Configure with only CI source
        config = get_config(
            job_command="bash /job/ci/deploy.sh",
            ci_source_type="git",
            ci_source_url=str(ci_repo_dir),
            ci_source_ref="main"
        )

        # Prepare CI source
        ci_result = prepare_ci_source(config)
        assert ci_result is not None
        assert (ci_result / "deploy.sh").exists()

        # Prepare regular source (should return None)
        source_result = prepare_source(config)
        assert source_result is None

    def test_invalid_source_type(self):
        """Test that invalid source_type raises ValueError."""
        config = get_config(
            job_command="echo 'test'",
            source_type="invalid_type",
            source_url="some_url"
        )

        with pytest.raises(ValueError, match="Invalid source_type"):
            prepare_source(config)

    def test_git_source_missing_url(self):
        """Test that git source without URL raises ValueError."""
        config = get_config(
            job_command="echo 'test'",
            source_type="git"
            # source_url not provided
        )

        with pytest.raises(ValueError, match="source_url is required"):
            prepare_source(config)

    def test_copy_source_missing_url(self):
        """Test that copy source without URL raises ValueError."""
        config = get_config(
            job_command="echo 'test'",
            source_type="copy"
            # source_url not provided
        )

        with pytest.raises(ValueError, match="source_url is required"):
            prepare_source(config)

    def test_tarball_source_not_implemented(self):
        """Test that tarball source raises NotImplementedError."""
        config = get_config(
            job_command="echo 'test'",
            source_type="tarball",
            source_url="https://example.com/archive.tar.gz"
        )

        with pytest.raises(NotImplementedError, match="Tarball source preparation is not yet implemented"):
            prepare_source(config)

    def test_hg_source_not_implemented(self):
        """Test that Mercurial source raises NotImplementedError."""
        config = get_config(
            job_command="echo 'test'",
            source_type="hg",
            source_url="https://example.com/repo"
        )

        with pytest.raises(NotImplementedError, match="Mercurial source preparation is not yet implemented"):
            prepare_source(config)

    def test_svn_source_not_implemented(self):
        """Test that Subversion source raises NotImplementedError."""
        config = get_config(
            job_command="echo 'test'",
            source_type="svn",
            source_url="https://example.com/svn/repo"
        )

        with pytest.raises(NotImplementedError, match="Subversion source preparation is not yet implemented"):
            prepare_source(config)

    def test_copy_nonexistent_source(self):
        """Test that copying from nonexistent source raises FileNotFoundError."""
        config = get_config(
            job_command="echo 'test'",
            source_type="copy",
            source_url="/nonexistent/path"
        )

        with pytest.raises(FileNotFoundError):
            prepare_source(config)
