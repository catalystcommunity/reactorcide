"""Tests for register_secret module."""

import os
import sys
import tempfile
import unittest
from unittest.mock import patch, MagicMock, call
from io import StringIO

from src.register_secret import main


class TestRegisterSecret(unittest.TestCase):
    """Test the register_secret command-line tool."""

    def setUp(self):
        """Set up test fixtures."""
        self.socket_path = "/tmp/test_socket.sock"

    def tearDown(self):
        """Clean up after tests."""
        # Clear any environment variables
        if "REACTORCIDE_SECRETS_SOCKET" in os.environ:
            del os.environ["REACTORCIDE_SECRETS_SOCKET"]

    @patch("src.register_secret.register_secret_via_socket")
    @patch("sys.argv", ["register_secret", "my-secret", "--socket", "/tmp/test.sock"])
    def test_register_single_secret_with_socket_flag(self, mock_register):
        """Test registering a single secret with --socket flag."""
        mock_register.return_value = True

        with patch("sys.stdout", new_callable=StringIO) as mock_stdout:
            result = main()

        self.assertEqual(result, 0)
        mock_register.assert_called_once_with("my-secret", "/tmp/test.sock")
        self.assertIn("Successfully registered 1 secret(s)", mock_stdout.getvalue())

    @patch("src.register_secret.register_secrets_via_socket")
    @patch("sys.argv", ["register_secret", "secret1", "secret2", "secret3", "--socket", "/tmp/test.sock"])
    def test_register_multiple_secrets(self, mock_register):
        """Test registering multiple secrets."""
        mock_register.return_value = True

        with patch("sys.stdout", new_callable=StringIO) as mock_stdout:
            result = main()

        self.assertEqual(result, 0)
        mock_register.assert_called_once_with(["secret1", "secret2", "secret3"], "/tmp/test.sock")
        self.assertIn("Successfully registered 3 secret(s)", mock_stdout.getvalue())

    @patch("src.register_secret.register_secret_via_socket")
    @patch("sys.argv", ["register_secret", "my-secret"])
    def test_register_secret_with_env_var(self, mock_register):
        """Test registering a secret using REACTORCIDE_SECRETS_SOCKET env var."""
        os.environ["REACTORCIDE_SECRETS_SOCKET"] = "/tmp/env.sock"
        mock_register.return_value = True

        with patch("sys.stdout", new_callable=StringIO) as mock_stdout:
            result = main()

        self.assertEqual(result, 0)
        mock_register.assert_called_once_with("my-secret", "/tmp/env.sock")
        self.assertIn("Successfully registered 1 secret(s)", mock_stdout.getvalue())

    @patch("sys.argv", ["register_secret", "my-secret"])
    def test_missing_socket_path(self):
        """Test error when no socket path is provided."""
        with patch("sys.stderr", new_callable=StringIO) as mock_stderr:
            result = main()

        self.assertEqual(result, 1)
        self.assertIn("Error: No socket path provided", mock_stderr.getvalue())

    @patch("src.register_secret.register_secrets_via_socket")
    @patch("sys.stdin", StringIO("stdin-secret1\nstdin-secret2\n\n"))
    @patch("sys.argv", ["register_secret", "-", "--socket", "/tmp/test.sock"])
    def test_read_secrets_from_stdin(self, mock_register):
        """Test reading secrets from stdin with '-' argument."""
        mock_register.return_value = True

        with patch("sys.stdout", new_callable=StringIO) as mock_stdout:
            result = main()

        self.assertEqual(result, 0)
        mock_register.assert_called_once_with(["stdin-secret1", "stdin-secret2"], "/tmp/test.sock")
        self.assertIn("Successfully registered 2 secret(s)", mock_stdout.getvalue())

    @patch("src.register_secret.register_secrets_via_socket")
    @patch("sys.stdin", StringIO("stdin-secret\n"))
    @patch("sys.argv", ["register_secret", "arg-secret", "-", "--socket", "/tmp/test.sock"])
    def test_mixed_args_and_stdin(self, mock_register):
        """Test mixing command-line args and stdin input."""
        mock_register.return_value = True

        with patch("sys.stdout", new_callable=StringIO) as mock_stdout:
            result = main()

        self.assertEqual(result, 0)
        mock_register.assert_called_once_with(["arg-secret", "stdin-secret"], "/tmp/test.sock")
        self.assertIn("Successfully registered 2 secret(s)", mock_stdout.getvalue())

    @patch("src.register_secret.register_secret_via_socket")
    @patch("sys.argv", ["register_secret", "my-secret", "--socket", "/tmp/test.sock"])
    def test_registration_failure(self, mock_register):
        """Test handling of registration failure."""
        mock_register.return_value = False

        with patch("sys.stderr", new_callable=StringIO) as mock_stderr:
            result = main()

        self.assertEqual(result, 1)
        self.assertIn("Failed to register secrets", mock_stderr.getvalue())

    @patch("src.register_secret.register_secret_via_socket")
    @patch("sys.argv", ["register_secret", "my-secret", "--socket", "/tmp/test.sock"])
    def test_registration_exception(self, mock_register):
        """Test handling of exceptions during registration."""
        mock_register.side_effect = Exception("Socket error")

        with patch("sys.stderr", new_callable=StringIO) as mock_stderr:
            result = main()

        self.assertEqual(result, 1)
        self.assertIn("Error registering secrets: Socket error", mock_stderr.getvalue())

    @patch("sys.stdin", StringIO(""))
    @patch("sys.argv", ["register_secret", "-", "--socket", "/tmp/test.sock"])
    def test_empty_stdin(self):
        """Test reading from empty stdin."""
        with patch("sys.stderr", new_callable=StringIO) as mock_stderr:
            result = main()

        self.assertEqual(result, 0)
        self.assertIn("Warning: No secrets provided", mock_stderr.getvalue())

    @patch("sys.stdin", StringIO("   \n\n  \n"))
    @patch("sys.argv", ["register_secret", "-", "--socket", "/tmp/test.sock"])
    def test_whitespace_only_stdin(self):
        """Test reading whitespace-only lines from stdin."""
        with patch("sys.stderr", new_callable=StringIO) as mock_stderr:
            result = main()

        self.assertEqual(result, 0)
        self.assertIn("Warning: No secrets provided", mock_stderr.getvalue())

    @patch("src.register_secret.register_secret_via_socket")
    @patch("sys.stdin", StringIO("  secret-with-spaces  \n"))
    @patch("sys.argv", ["register_secret", "-", "--socket", "/tmp/test.sock"])
    def test_stdin_strips_whitespace(self, mock_register):
        """Test that stdin input is stripped of whitespace."""
        mock_register.return_value = True

        with patch("sys.stdout", new_callable=StringIO) as mock_stdout:
            result = main()

        self.assertEqual(result, 0)
        mock_register.assert_called_once_with("secret-with-spaces", "/tmp/test.sock")

    def test_main_as_script(self):
        """Test that the script can be called as a module."""
        # This tests the if __name__ == "__main__" block
        with patch("sys.argv", ["register_secret", "--help"]):
            with patch("sys.exit") as mock_exit:
                with patch("sys.stdout", new_callable=StringIO):
                    # Import and call the module's main directly
                    import src.register_secret
                    # The argparse help will cause sys.exit(0)
                    try:
                        src.register_secret.main()
                    except SystemExit:
                        pass  # Expected from --help


if __name__ == "__main__":
    unittest.main()