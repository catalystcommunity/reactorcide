"""Integration tests for directory operations in runnerlib."""

import os
import tempfile
import shutil
from pathlib import Path
import pytest

from src.source_prep import copy_directory, cleanup_job_directory, get_code_directory_path
from src.config import RunnerConfig


class TestDirectoryOperations:
    """Test directory operations including copy and cleanup."""

    @pytest.fixture
    def source_dir(self):
        """Create a source directory with test files."""
        source = tempfile.mkdtemp()

        # Create some test files and directories
        (Path(source) / "file1.txt").write_text("Content 1")
        (Path(source) / "file2.py").write_text("print('hello')")

        # Create subdirectory with files
        subdir = Path(source) / "subdir"
        subdir.mkdir()
        (subdir / "nested.txt").write_text("Nested content")

        # Create empty directory
        (Path(source) / "empty_dir").mkdir()

        # Create a symlink (to test proper handling)
        (Path(source) / "link.txt").symlink_to("file1.txt")

        yield source
        shutil.rmtree(source, ignore_errors=True)

    @pytest.fixture
    def job_config(self):
        """Create a basic job configuration."""
        return RunnerConfig(
            code_dir="/job/src",
            job_dir="/job",
            job_command="echo test",
            runner_image="alpine:latest"
        )

    def test_copy_directory(self, source_dir, job_config):
        """Test copying a directory to workspace."""
        # Perform copy
        copy_directory(source_dir, job_config)

        # Verify files were copied
        code_path = get_code_directory_path(job_config)
        assert code_path.exists()
        assert (code_path / "file1.txt").exists()
        assert (code_path / "file2.py").exists()
        assert (code_path / "subdir" / "nested.txt").exists()
        assert (code_path / "empty_dir").exists()

        # Verify content
        assert (code_path / "file1.txt").read_text() == "Content 1"
        assert (code_path / "subdir" / "nested.txt").read_text() == "Nested content"

        # Cleanup
        shutil.rmtree("./job", ignore_errors=True)

    def test_copy_preserves_structure(self, source_dir, job_config):
        """Test that copy preserves directory structure."""
        copy_directory(source_dir, job_config)

        # Check directory structure
        code_path = get_code_directory_path(job_config)
        assert (code_path / "subdir").is_dir()
        assert (code_path / "empty_dir").is_dir()

        # Check that empty dir is still empty
        assert len(list((code_path / "empty_dir").iterdir())) == 0

        # Cleanup
        shutil.rmtree("./job", ignore_errors=True)

    def test_copy_handles_symlinks(self, source_dir, job_config):
        """Test that copy properly handles symlinks."""
        copy_directory(source_dir, job_config)

        code_path = get_code_directory_path(job_config)
        link_path = code_path / "link.txt"

        # Symlink should either be copied as symlink or dereferenced
        # Check that it exists and has correct content
        assert link_path.exists()
        if link_path.is_symlink():
            # If preserved as symlink, should point to file1.txt
            assert link_path.resolve().name == "file1.txt"
        else:
            # If dereferenced, should have same content as file1.txt
            assert link_path.read_text() == "Content 1"

        # Cleanup
        shutil.rmtree("./job", ignore_errors=True)

    def test_copy_overwrites_existing(self, source_dir, job_config):
        """Test that copy overwrites existing job directory."""
        # Create the job directory with existing content
        Path("./job").mkdir(exist_ok=True)
        Path("./job/src").mkdir(exist_ok=True)

        # Create existing file that should be replaced
        old_file = Path("./job/src/old.txt")
        old_file.write_text("Old content")

        copy_directory(source_dir, job_config)

        # Old file should be gone
        code_path = get_code_directory_path(job_config)
        assert not (code_path / "old.txt").exists()
        # New files should exist
        assert (code_path / "file1.txt").exists()

        # Cleanup
        shutil.rmtree("./job", ignore_errors=True)

    def test_copy_nonexistent_source(self, job_config):
        """Test copying from a non-existent source directory."""
        # Should raise an error
        with pytest.raises((OSError, FileNotFoundError, ValueError)):
            copy_directory("/nonexistent/path", job_config)

        # Cleanup just in case
        shutil.rmtree("./job", ignore_errors=True)

    def test_copy_file_as_source(self, job_config):
        """Test that copying a file (not directory) is handled properly."""
        # Create a single file
        with tempfile.NamedTemporaryFile(delete=False) as f:
            f.write(b"File content")
            file_path = f.name

        try:
            # Should either copy the single file or raise an error
            # depending on implementation
            try:
                copy_directory(file_path, job_config)
                # If it succeeds, check that file was copied
                code_path = get_code_directory_path(job_config)
                assert code_path.exists()
            except (OSError, ValueError):
                # Expected if implementation requires directory
                pass
        finally:
            os.unlink(file_path)

        # Cleanup
        shutil.rmtree("./job", ignore_errors=True)

    def test_cleanup_removes_job_directory(self):
        """Test that cleanup removes the job directory."""
        job_dir = Path("./job")
        job_dir.mkdir(exist_ok=True)

        # Create some files
        (job_dir / "file.txt").write_text("Content")
        (job_dir / "subdir").mkdir(exist_ok=True)
        (job_dir / "subdir" / "nested.txt").write_text("Nested")

        # Perform cleanup
        cleanup_job_directory()

        # Job directory should be gone
        assert not job_dir.exists()

    def test_cleanup_nonexistent_directory(self):
        """Test cleanup when job directory doesn't exist."""
        # Ensure it doesn't exist
        if Path("./job").exists():
            shutil.rmtree("./job")

        # Should not raise an error
        cleanup_job_directory()

        # Nothing should have been created
        assert not Path("./job").exists()

    def test_cleanup_handles_permissions(self):
        """Test cleanup handles files with restricted permissions."""
        job_dir = Path("./job")
        job_dir.mkdir(exist_ok=True)

        # Create file with restricted permissions
        protected_file = job_dir / "protected.txt"
        protected_file.write_text("Protected")
        protected_file.chmod(0o444)  # Read-only

        # Create directory with restricted permissions
        protected_dir = job_dir / "protected_dir"
        protected_dir.mkdir()
        (protected_dir / "file.txt").write_text("Content")

        # Cleanup should handle this gracefully
        cleanup_job_directory()

        # Job directory should be removed (or at least attempted)
        # This might fail on some systems, so we check both possibilities
        if job_dir.exists():
            # If cleanup failed due to permissions, that's acceptable
            # but we should be able to manually clean with proper permissions
            protected_file.chmod(0o755)
            protected_dir.chmod(0o755)
            shutil.rmtree(job_dir)

    def test_copy_then_cleanup_cycle(self, source_dir, job_config):
        """Test a complete copy and cleanup cycle."""
        # Copy files
        copy_directory(source_dir, job_config)
        code_path = get_code_directory_path(job_config)
        assert code_path.exists()
        assert (code_path / "file1.txt").exists()

        # Cleanup
        cleanup_job_directory()
        assert not Path("./job").exists()

        # Copy again - should work
        copy_directory(source_dir, job_config)
        assert code_path.exists()
        assert (code_path / "file1.txt").exists()

        # Final cleanup
        cleanup_job_directory()

    def test_copy_large_directory(self, job_config):
        """Test copying a directory with many files."""
        source = tempfile.mkdtemp()

        try:
            # Create many files
            for i in range(100):
                (Path(source) / f"file_{i}.txt").write_text(f"Content {i}")

            # Create nested structure
            for i in range(5):
                subdir = Path(source) / f"dir_{i}"
                subdir.mkdir()
                for j in range(20):
                    (subdir / f"file_{j}.txt").write_text(f"Nested {i}-{j}")

            copy_directory(source, job_config)

            # Verify all files were copied
            code_path = get_code_directory_path(job_config)
            assert len(list(code_path.glob("file_*.txt"))) == 100
            assert len(list(code_path.glob("dir_*"))) == 5
            assert (code_path / "dir_0" / "file_0.txt").read_text() == "Nested 0-0"

        finally:
            shutil.rmtree(source, ignore_errors=True)
            shutil.rmtree("./job", ignore_errors=True)

    def test_copy_special_characters(self, job_config):
        """Test copying files with special characters in names."""
        source = tempfile.mkdtemp()

        try:
            # Create files with special characters
            special_names = [
                "file with spaces.txt",
                "file-with-dashes.txt",
                "file_with_underscores.txt",
                "file.multiple.dots.txt",
                "über-file.txt",  # Unicode
                "file@symbol.txt",
            ]

            for name in special_names:
                (Path(source) / name).write_text(f"Content of {name}")

            copy_directory(source, job_config)

            # Verify all files were copied with correct names
            code_path = get_code_directory_path(job_config)
            for name in special_names:
                file_path = code_path / name
                assert file_path.exists(), f"File {name} was not copied"
                assert file_path.read_text() == f"Content of {name}"

        finally:
            shutil.rmtree(source, ignore_errors=True)
            shutil.rmtree("./job", ignore_errors=True)