"""Tests for git_ops module."""

import os
import tempfile
import shutil
from pathlib import Path
import pytest
from git import Repo


from src.git_ops import get_files_changed


class TestGetFilesChanged:
    """Test cases for get_files_changed function."""

    def setup_method(self):
        """Set up a temporary git repository for each test."""
        self.temp_dir = tempfile.mkdtemp()
        self.repo = Repo.init(self.temp_dir)
        
        # Configure git user for commits
        with self.repo.config_writer() as git_config:
            git_config.set_value("user", "name", "Test User")
            git_config.set_value("user", "email", "test@example.com")

    def teardown_method(self):
        """Clean up the temporary repository."""
        # Close git repo explicitly before cleanup
        if hasattr(self, 'repo'):
            self.repo.close()
        if hasattr(self, 'temp_dir') and os.path.exists(self.temp_dir):
            shutil.rmtree(self.temp_dir, ignore_errors=True)

    def test_get_files_changed_with_new_files(self):
        """Test getting changed files when new files are added."""
        # Create initial commit
        initial_file = Path(self.temp_dir) / "initial.txt"
        initial_file.write_text("initial content")
        self.repo.index.add([str(initial_file)])
        initial_commit = self.repo.index.commit("Initial commit")
        
        # Add new files
        new_file1 = Path(self.temp_dir) / "new1.txt"
        new_file2 = Path(self.temp_dir) / "new2.txt"
        new_file1.write_text("new file 1")
        new_file2.write_text("new file 2")
        self.repo.index.add([str(new_file1), str(new_file2)])
        self.repo.index.commit("Add new files")
        
        # Test with initial commit hash
        changed_files = get_files_changed(initial_commit.hexsha, self.temp_dir)
        
        assert len(changed_files) == 2
        assert "new1.txt" in changed_files
        assert "new2.txt" in changed_files

    def test_get_files_changed_with_modified_files(self):
        """Test getting changed files when existing files are modified."""
        # Create initial commit
        file1 = Path(self.temp_dir) / "file1.txt"
        file2 = Path(self.temp_dir) / "file2.txt"
        file1.write_text("original content 1")
        file2.write_text("original content 2")
        self.repo.index.add([str(file1), str(file2)])
        initial_commit = self.repo.index.commit("Initial commit")
        
        # Modify files
        file1.write_text("modified content 1")
        file2.write_text("modified content 2")
        self.repo.index.add([str(file1), str(file2)])
        self.repo.index.commit("Modify files")
        
        # Test with initial commit hash
        changed_files = get_files_changed(initial_commit.hexsha, self.temp_dir)
        
        assert len(changed_files) == 2
        assert "file1.txt" in changed_files
        assert "file2.txt" in changed_files

    def test_get_files_changed_with_mixed_changes(self):
        """Test getting changed files with mixed new, modified, and deleted files."""
        # Create initial commit
        existing_file = Path(self.temp_dir) / "existing.txt"
        to_delete_file = Path(self.temp_dir) / "to_delete.txt"
        existing_file.write_text("existing content")
        to_delete_file.write_text("will be deleted")
        self.repo.index.add([str(existing_file), str(to_delete_file)])
        initial_commit = self.repo.index.commit("Initial commit")
        
        # Modify existing file
        existing_file.write_text("modified existing content")
        self.repo.index.add([str(existing_file)])
        
        # Delete file
        to_delete_file.unlink()
        self.repo.index.remove([str(to_delete_file)])
        
        # Add new file
        new_file = Path(self.temp_dir) / "new.txt"
        new_file.write_text("new content")
        self.repo.index.add([str(new_file)])
        
        self.repo.index.commit("Mixed changes")
        
        # Test with initial commit hash
        changed_files = get_files_changed(initial_commit.hexsha, self.temp_dir)
        
        assert len(changed_files) == 3
        assert "existing.txt" in changed_files
        assert "to_delete.txt" in changed_files
        assert "new.txt" in changed_files

    def test_get_files_changed_no_changes(self):
        """Test getting changed files when there are no changes."""
        # Create initial commit
        file1 = Path(self.temp_dir) / "file1.txt"
        file1.write_text("content")
        self.repo.index.add([str(file1)])
        commit = self.repo.index.commit("Initial commit")
        
        # Test with same commit
        changed_files = get_files_changed(commit.hexsha, self.temp_dir)
        
        assert len(changed_files) == 0

    def test_get_files_changed_with_relative_ref(self):
        """Test getting changed files using relative references like HEAD~1."""
        # Create multiple commits
        file1 = Path(self.temp_dir) / "file1.txt"
        file1.write_text("content 1")
        self.repo.index.add([str(file1)])
        self.repo.index.commit("First commit")
        
        file2 = Path(self.temp_dir) / "file2.txt"
        file2.write_text("content 2")
        self.repo.index.add([str(file2)])
        self.repo.index.commit("Second commit")
        
        # Test with HEAD~1
        changed_files = get_files_changed("HEAD~1", self.temp_dir)
        
        assert len(changed_files) == 1
        assert "file2.txt" in changed_files

    def test_get_files_changed_nonexistent_repo(self):
        """Test that FileNotFoundError is raised for non-existent repository."""
        with pytest.raises(FileNotFoundError, match="Repository path does not exist"):
            get_files_changed("HEAD~1", "/nonexistent/path")

    def test_get_files_changed_with_subdirectories(self):
        """Test getting changed files in subdirectories."""
        # Create initial commit
        subdir = Path(self.temp_dir) / "subdir"
        subdir.mkdir()
        subfile = subdir / "subfile.txt"
        subfile.write_text("sub content")
        self.repo.index.add([str(subfile)])
        initial_commit = self.repo.index.commit("Initial commit")
        
        # Add file in subdirectory
        new_subfile = subdir / "new_subfile.txt"
        new_subfile.write_text("new sub content")
        self.repo.index.add([str(new_subfile)])
        self.repo.index.commit("Add subdir file")
        
        # Test with initial commit hash
        changed_files = get_files_changed(initial_commit.hexsha, self.temp_dir)
        
        assert len(changed_files) == 1
        assert "subdir/new_subfile.txt" in changed_files