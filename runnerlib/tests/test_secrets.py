"""Tests for value-based secret masking."""

from src.secrets import (
    SecretMasker,
    register_secret,
    register_secrets,
    register_env_vars,
    mask_string,
    mask_command_args,
    mask_dict_values,
    clear_secrets,
    get_default_masker,
)


class TestSecretMasker:
    """Tests for the SecretMasker class."""

    def test_register_and_mask_single_secret(self):
        """Test registering and masking a single secret."""
        masker = SecretMasker()
        masker.register_secret("super-secret-token-123")

        text = "The token is super-secret-token-123 and it's sensitive"
        expected = "The token is [REDACTED] and it's sensitive"
        assert masker.mask_string(text) == expected

    def test_register_and_mask_multiple_secrets(self):
        """Test registering and masking multiple secrets."""
        masker = SecretMasker()
        masker.register_secrets(["my-password-456", "api-key-789", "token-xyz"])

        text = "Auth with my-password-456 and key api-key-789"
        expected = "Auth with [REDACTED] and key [REDACTED]"
        assert masker.mask_string(text) == expected

    def test_no_secrets_to_mask(self):
        """Test that text without secrets is unchanged."""
        masker = SecretMasker()
        masker.register_secret("some-secret")

        text = "This text has no secrets at all"
        assert masker.mask_string(text) == text

    def test_secret_appears_multiple_times(self):
        """Test masking when a secret appears multiple times."""
        masker = SecretMasker()
        masker.register_secret("repeat-secret-123")

        text = "Token: repeat-secret-123, again: repeat-secret-123"
        expected = "Token: [REDACTED], again: [REDACTED]"
        assert masker.mask_string(text) == expected

    def test_secret_in_json(self):
        """Test masking secrets in JSON-like strings."""
        masker = SecretMasker()
        masker.register_secrets(["token-abc", "key-def"])

        text = '{"token": "token-abc", "api_key": "key-def"}'
        expected = '{"token": "[REDACTED]", "api_key": "[REDACTED]"}'
        assert masker.mask_string(text) == expected

    def test_register_env_vars(self):
        """Test registering environment variable values as secrets."""
        masker = SecretMasker()
        env_vars = {
            "ZEALOUS": "my-zealous-secret-999",
            "CUSTOM_API_TOKEN": "token-xyz-123",
            "DATABASE_URL": "postgres://user:pass@localhost/db",
            "PORT": 8080,  # Non-string, should be converted
            "EMPTY": "",   # Empty, should be ignored
            "NONE_VALUE": None,  # None, should be ignored
        }

        masker.register_env_vars(env_vars)

        # Test that VALUES are masked, not keys
        assert masker.mask_string("Using my-zealous-secret-999") == "Using [REDACTED]"
        assert masker.mask_string("The env var ZEALOUS is set") == "The env var ZEALOUS is set"
        assert masker.mask_string("Connect to postgres://user:pass@localhost/db") == "Connect to [REDACTED]"
        assert masker.mask_string("Auth: token-xyz-123") == "Auth: [REDACTED]"
        assert masker.mask_string("Port is 8080") == "Port is [REDACTED]"

    def test_short_string_handling(self):
        """Test that very short strings are not masked to avoid false positives."""
        masker = SecretMasker()
        masker.register_secrets(["a", "ab", "abc", "long-secret"])

        # Single and two-char strings should NOT be masked
        assert masker.mask_string("This is a test") == "This is a test"
        assert masker.mask_string("About this") == "About this"

        # Three+ char strings SHOULD be masked
        assert masker.mask_string("Code is abc") == "Code is [REDACTED]"
        assert masker.mask_string("Token: long-secret") == "Token: [REDACTED]"

    def test_mask_command_args(self):
        """Test masking secrets in command arguments."""
        masker = SecretMasker()
        masker.register_secrets(["secret-token-123", "my-password"])

        args = [
            "docker",
            "run",
            "-e",
            "TOKEN=secret-token-123",
            "-e",
            "PASSWORD=my-password",
            "myimage",
        ]

        expected = [
            "docker",
            "run",
            "-e",
            "TOKEN=[REDACTED]",
            "-e",
            "PASSWORD=[REDACTED]",
            "myimage",
        ]

        assert masker.mask_command_args(args) == expected

    def test_mask_dict_values(self):
        """Test masking secrets in dictionary values."""
        masker = SecretMasker()
        masker.register_secrets(["secret-123", "password-456"])

        data = {
            "token": "secret-123",
            "password": "password-456",
            "normal": "not-a-secret",
            "nested": {
                "api_key": "secret-123",
                "value": "password-456",
            },
            "list": ["secret-123", "normal", "password-456"],
            "number": 42,
            "none": None,
        }

        masked = masker.mask_dict_values(data)

        assert masked["token"] == "[REDACTED]"
        assert masked["password"] == "[REDACTED]"
        assert masked["normal"] == "not-a-secret"
        assert masked["nested"]["api_key"] == "[REDACTED]"
        assert masked["nested"]["value"] == "[REDACTED]"
        assert masked["list"] == ["[REDACTED]", "normal", "[REDACTED]"]
        assert masked["number"] == "42"  # Numbers are converted to strings
        assert masked["none"] is None

    def test_custom_redaction_text(self):
        """Test using custom redaction text."""
        masker = SecretMasker(redaction_text="***HIDDEN***")
        masker.register_secret("my-secret")

        text = "The secret is my-secret"
        expected = "The secret is ***HIDDEN***"
        assert masker.mask_string(text) == expected

    def test_special_regex_characters(self):
        """Test that secrets with regex special characters are handled correctly."""
        masker = SecretMasker()
        masker.register_secret("secret.with*special+chars?")

        text = "Token: secret.with*special+chars? should be masked"
        expected = "Token: [REDACTED] should be masked"
        assert masker.mask_string(text) == expected

    def test_clear_and_size(self):
        """Test clearing secrets and checking size."""
        masker = SecretMasker()
        masker.register_secrets(["secret1", "secret2", "secret3"])

        assert masker.size() == 3

        masker.clear()
        assert masker.size() == 0

        # After clearing, secrets should not be masked
        assert masker.mask_string("secret1 and secret2") == "secret1 and secret2"

    def test_has_secret(self):
        """Test checking if a value is registered as a secret."""
        masker = SecretMasker()
        masker.register_secret("known-secret")

        assert masker.has_secret("known-secret")
        assert not masker.has_secret("unknown-value")

    def test_thread_safety(self):
        """Test that the masker is thread-safe."""
        import threading

        masker = SecretMasker()
        errors = []

        def register_secrets():
            try:
                for i in range(100):
                    masker.register_secret(f"secret-{i}")
            except Exception as e:
                errors.append(e)

        def mask_strings():
            try:
                for i in range(100):
                    masker.mask_string("Some text with secrets")
            except Exception as e:
                errors.append(e)

        threads = []
        for _ in range(5):
            t1 = threading.Thread(target=register_secrets)
            t2 = threading.Thread(target=mask_strings)
            threads.extend([t1, t2])

        for t in threads:
            t.start()

        for t in threads:
            t.join()

        assert len(errors) == 0


class TestGlobalMasker:
    """Tests for the global masker functions."""

    def setup_method(self):
        """Clear the global masker before each test."""
        clear_secrets()

    def test_global_register_and_mask(self):
        """Test the global masker functions."""
        register_secret("global-secret-123")
        register_secrets(["another-secret", "third-secret"])

        text = "Secrets: global-secret-123, another-secret, third-secret"
        expected = "Secrets: [REDACTED], [REDACTED], [REDACTED]"
        assert mask_string(text) == expected

    def test_global_mask_command_args(self):
        """Test global command args masking."""
        register_secrets(["token-123", "key-456"])

        args = ["--token=token-123", "--key=key-456", "--normal=value"]
        expected = ["--token=[REDACTED]", "--key=[REDACTED]", "--normal=value"]
        assert mask_command_args(args) == expected

    def test_global_register_env_vars(self):
        """Test global environment variable registration."""
        env_vars = {
            "API_TOKEN": "secret-api-token",
            "DATABASE_PASSWORD": "db-pass-123",
        }
        register_env_vars(env_vars)

        text = "Using secret-api-token with db-pass-123"
        expected = "Using [REDACTED] with [REDACTED]"
        assert mask_string(text) == expected

    def test_global_mask_dict_values(self):
        """Test global dictionary masking."""
        register_secrets(["secret-value", "another-secret"])

        data = {
            "field1": "secret-value",
            "field2": "normal-value",
            "field3": "another-secret",
        }

        masked = mask_dict_values(data)
        assert masked["field1"] == "[REDACTED]"
        assert masked["field2"] == "normal-value"
        assert masked["field3"] == "[REDACTED]"

    def test_get_default_masker(self):
        """Test getting the default masker instance."""
        masker = get_default_masker()
        assert isinstance(masker, SecretMasker)

        # Registering via global function should affect the instance
        register_secret("test-secret")
        assert masker.has_secret("test-secret")