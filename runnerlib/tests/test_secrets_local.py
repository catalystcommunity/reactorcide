"""Tests for local encrypted secrets storage."""

import pytest
from pathlib import Path
import tempfile
import os

from src.secrets_local import (
    secret_get, secret_set, secret_delete,
    secret_list_keys, secret_list_paths,
    secrets_init, validate_path, validate_key,
    get_default_base_path, is_initialized,
    SCRYPT_N, SCRYPT_R, SCRYPT_P,
)
import src.secrets_local as secrets_local_module


# Use lower scrypt parameters for fast tests
@pytest.fixture(autouse=True)
def fast_scrypt(monkeypatch):
    """Use lower scrypt parameters for fast tests."""
    monkeypatch.setattr(secrets_local_module, 'SCRYPT_N', 2**14)


class TestValidation:
    """Tests for path and key validation."""

    def test_valid_paths(self):
        """Test that valid paths are accepted."""
        valid = ["project", "my-project", "org/project", "a/b/c/d", "my_project"]
        for p in valid:
            validate_path(p)  # Should not raise

    def test_invalid_paths(self):
        """Test that invalid paths are rejected."""
        invalid = ["", "..", "../foo", "foo..bar", "foo bar", "a:b", "a;b"]
        for p in invalid:
            with pytest.raises(ValueError):
                validate_path(p)

    def test_valid_keys(self):
        """Test that valid keys are accepted."""
        valid = ["NPM_TOKEN", "api-key", "myKey123", "KEY"]
        for k in valid:
            validate_key(k)  # Should not raise

    def test_invalid_keys(self):
        """Test that invalid keys are rejected."""
        invalid = ["", "a/b", "key with space", "key:colon"]
        for k in invalid:
            with pytest.raises(ValueError):
                validate_key(k)


class TestDefaultBasePath:
    """Tests for get_default_base_path."""

    def test_default_path_uses_home(self):
        """Test that default path is under ~/.config/reactorcide/secrets."""
        path = get_default_base_path()
        assert "reactorcide" in str(path)
        assert "secrets" in str(path)

    def test_respects_xdg_config_home(self, monkeypatch, tmp_path):
        """Test that XDG_CONFIG_HOME is respected."""
        monkeypatch.setenv("XDG_CONFIG_HOME", str(tmp_path))
        path = get_default_base_path()
        assert str(path).startswith(str(tmp_path))
        assert path == tmp_path / "reactorcide" / "secrets"


class TestSecretsStorage:
    """Tests for secrets storage operations."""

    @pytest.fixture
    def temp_secrets_dir(self):
        """Create a temporary directory for secrets storage."""
        with tempfile.TemporaryDirectory() as tmpdir:
            yield Path(tmpdir)

    def test_init_creates_files(self, temp_secrets_dir):
        """Test that init creates the necessary files."""
        secrets_init("testpassword", base_path=temp_secrets_dir)
        assert (temp_secrets_dir / ".salt").exists()
        assert (temp_secrets_dir / "secrets.enc").exists()

    def test_init_refuses_reinit_without_force(self, temp_secrets_dir):
        """Test that init refuses to reinitialize without force."""
        secrets_init("testpassword", base_path=temp_secrets_dir)
        with pytest.raises(ValueError):
            secrets_init("testpassword", base_path=temp_secrets_dir)

    def test_init_allows_force_reinit(self, temp_secrets_dir):
        """Test that init allows reinitialize with force."""
        secrets_init("testpassword", base_path=temp_secrets_dir)
        secrets_init("newpassword", base_path=temp_secrets_dir, force=True)

    def test_is_initialized(self, temp_secrets_dir):
        """Test checking if secrets storage is initialized."""
        assert not is_initialized(base_path=temp_secrets_dir)
        secrets_init("testpassword", base_path=temp_secrets_dir)
        assert is_initialized(base_path=temp_secrets_dir)

    def test_set_and_get_secret(self, temp_secrets_dir):
        """Test setting and getting a secret."""
        pw = "testpassword"
        secrets_init(pw, base_path=temp_secrets_dir)

        secret_set("myproject", "API_KEY", "secret123", pw, base_path=temp_secrets_dir)
        value = secret_get("myproject", "API_KEY", pw, base_path=temp_secrets_dir)

        assert value == "secret123"

    def test_get_nonexistent_returns_none(self, temp_secrets_dir):
        """Test that getting a nonexistent secret returns None."""
        pw = "testpassword"
        secrets_init(pw, base_path=temp_secrets_dir)

        value = secret_get("nonexistent", "KEY", pw, base_path=temp_secrets_dir)
        assert value is None

    def test_set_with_slashes_in_path(self, temp_secrets_dir):
        """Test setting secrets with hierarchical paths."""
        pw = "testpassword"
        secrets_init(pw, base_path=temp_secrets_dir)

        secret_set("org/project/prod", "DB_PASSWORD", "dbpass", pw, base_path=temp_secrets_dir)
        value = secret_get("org/project/prod", "DB_PASSWORD", pw, base_path=temp_secrets_dir)

        assert value == "dbpass"

    def test_delete_existing_secret(self, temp_secrets_dir):
        """Test deleting an existing secret."""
        pw = "testpassword"
        secrets_init(pw, base_path=temp_secrets_dir)

        secret_set("project", "KEY", "value", pw, base_path=temp_secrets_dir)
        deleted = secret_delete("project", "KEY", pw, base_path=temp_secrets_dir)

        assert deleted is True
        assert secret_get("project", "KEY", pw, base_path=temp_secrets_dir) is None

    def test_delete_nonexistent_returns_false(self, temp_secrets_dir):
        """Test that deleting nonexistent returns False."""
        pw = "testpassword"
        secrets_init(pw, base_path=temp_secrets_dir)

        deleted = secret_delete("nonexistent", "KEY", pw, base_path=temp_secrets_dir)
        assert deleted is False

    def test_delete_removes_empty_path(self, temp_secrets_dir):
        """Test that deleting the last key removes the path."""
        pw = "testpassword"
        secrets_init(pw, base_path=temp_secrets_dir)

        secret_set("project", "KEY", "value", pw, base_path=temp_secrets_dir)
        secret_delete("project", "KEY", pw, base_path=temp_secrets_dir)

        paths = secret_list_paths(pw, base_path=temp_secrets_dir)
        assert "project" not in paths

    def test_list_keys(self, temp_secrets_dir):
        """Test listing keys in a path."""
        pw = "testpassword"
        secrets_init(pw, base_path=temp_secrets_dir)

        secret_set("project", "KEY1", "val1", pw, base_path=temp_secrets_dir)
        secret_set("project", "KEY2", "val2", pw, base_path=temp_secrets_dir)
        secret_set("other", "KEY3", "val3", pw, base_path=temp_secrets_dir)

        keys = secret_list_keys("project", pw, base_path=temp_secrets_dir)
        assert sorted(keys) == ["KEY1", "KEY2"]

    def test_list_keys_empty_path(self, temp_secrets_dir):
        """Test listing keys in a nonexistent path returns empty list."""
        pw = "testpassword"
        secrets_init(pw, base_path=temp_secrets_dir)

        keys = secret_list_keys("nonexistent", pw, base_path=temp_secrets_dir)
        assert keys == []

    def test_list_paths(self, temp_secrets_dir):
        """Test listing all paths."""
        pw = "testpassword"
        secrets_init(pw, base_path=temp_secrets_dir)

        secret_set("project/a", "KEY", "val", pw, base_path=temp_secrets_dir)
        secret_set("project/b", "KEY", "val", pw, base_path=temp_secrets_dir)
        secret_set("other", "KEY", "val", pw, base_path=temp_secrets_dir)

        paths = secret_list_paths(pw, base_path=temp_secrets_dir)
        assert sorted(paths) == ["other", "project/a", "project/b"]

    def test_list_paths_empty(self, temp_secrets_dir):
        """Test listing paths when none exist."""
        pw = "testpassword"
        secrets_init(pw, base_path=temp_secrets_dir)

        paths = secret_list_paths(pw, base_path=temp_secrets_dir)
        assert paths == []

    def test_wrong_password_fails(self, temp_secrets_dir):
        """Test that wrong password fails to decrypt."""
        secrets_init("correct", base_path=temp_secrets_dir)
        secret_set("project", "KEY", "value", "correct", base_path=temp_secrets_dir)

        with pytest.raises(ValueError, match="Invalid password"):
            secret_get("project", "KEY", "wrong", base_path=temp_secrets_dir)

    def test_file_permissions(self, temp_secrets_dir):
        """Test that secret files have restricted permissions."""
        secrets_init("testpassword", base_path=temp_secrets_dir)

        salt_file = temp_secrets_dir / ".salt"
        secrets_file = temp_secrets_dir / "secrets.enc"

        # Check permissions are 0o600 (owner read/write only)
        assert (salt_file.stat().st_mode & 0o777) == 0o600
        assert (secrets_file.stat().st_mode & 0o777) == 0o600

    def test_update_existing_secret(self, temp_secrets_dir):
        """Test that setting an existing secret updates it."""
        pw = "testpassword"
        secrets_init(pw, base_path=temp_secrets_dir)

        secret_set("project", "KEY", "value1", pw, base_path=temp_secrets_dir)
        secret_set("project", "KEY", "value2", pw, base_path=temp_secrets_dir)

        value = secret_get("project", "KEY", pw, base_path=temp_secrets_dir)
        assert value == "value2"

    def test_multiple_keys_same_path(self, temp_secrets_dir):
        """Test storing multiple keys in the same path."""
        pw = "testpassword"
        secrets_init(pw, base_path=temp_secrets_dir)

        secret_set("project", "KEY1", "val1", pw, base_path=temp_secrets_dir)
        secret_set("project", "KEY2", "val2", pw, base_path=temp_secrets_dir)
        secret_set("project", "KEY3", "val3", pw, base_path=temp_secrets_dir)

        assert secret_get("project", "KEY1", pw, base_path=temp_secrets_dir) == "val1"
        assert secret_get("project", "KEY2", pw, base_path=temp_secrets_dir) == "val2"
        assert secret_get("project", "KEY3", pw, base_path=temp_secrets_dir) == "val3"

    def test_special_characters_in_value(self, temp_secrets_dir):
        """Test storing values with special characters."""
        pw = "testpassword"
        secrets_init(pw, base_path=temp_secrets_dir)

        special_value = 'pa$$w0rd!@#$%^&*(){}[]|\\:";\'<>,.?/'
        secret_set("project", "KEY", special_value, pw, base_path=temp_secrets_dir)

        value = secret_get("project", "KEY", pw, base_path=temp_secrets_dir)
        assert value == special_value

    def test_unicode_in_value(self, temp_secrets_dir):
        """Test storing values with unicode characters."""
        pw = "testpassword"
        secrets_init(pw, base_path=temp_secrets_dir)

        unicode_value = "secret-\u4e2d\u6587-\U0001F511"  # Chinese characters and key emoji
        secret_set("project", "KEY", unicode_value, pw, base_path=temp_secrets_dir)

        value = secret_get("project", "KEY", pw, base_path=temp_secrets_dir)
        assert value == unicode_value

    def test_empty_secrets_file_before_init(self, temp_secrets_dir):
        """Test that get returns empty dict before initialization."""
        pw = "testpassword"
        # Don't initialize - just try to list
        paths = secret_list_paths(pw, base_path=temp_secrets_dir)
        assert paths == []

    def test_set_creates_storage_if_not_initialized(self, temp_secrets_dir):
        """Test that set works even without explicit init."""
        pw = "testpassword"
        # Don't call secrets_init - just set directly
        secret_set("project", "KEY", "value", pw, base_path=temp_secrets_dir)

        value = secret_get("project", "KEY", pw, base_path=temp_secrets_dir)
        assert value == "value"
