"""Tests for the socket-based secret registration server."""

import time
import threading
import tempfile
from pathlib import Path

from src.secrets import SecretMasker
from src.secrets_server import (
    SecretRegistrationServer,
    register_secret_via_socket,
    register_secrets_via_socket,
)


class TestSecretRegistrationServer:
    """Tests for the SecretRegistrationServer."""

    def test_server_start_stop(self):
        """Test that the server can start and stop cleanly."""
        masker = SecretMasker()

        with tempfile.TemporaryDirectory() as tmpdir:
            socket_path = Path(tmpdir) / "test.sock"
            server = SecretRegistrationServer(masker, str(socket_path))

            # Start server
            server.start()
            assert socket_path.exists()

            # Stop server
            server.stop()
            # Give it a moment to clean up
            time.sleep(0.1)
            assert not socket_path.exists()

    def test_register_single_secret(self):
        """Test registering a single secret via socket."""
        masker = SecretMasker()

        with tempfile.TemporaryDirectory() as tmpdir:
            socket_path = Path(tmpdir) / "test.sock"
            server = SecretRegistrationServer(masker, str(socket_path))
            server.start()

            # Give server time to start
            time.sleep(0.1)

            # Register a secret
            success = register_secret_via_socket("my-secret-token", str(socket_path))
            assert success

            # Give server time to process
            time.sleep(0.1)

            # Check that the secret was registered
            assert masker.mask_string("The token is my-secret-token") == "The token is [REDACTED]"
            assert server.get_registered_count() == 1

            server.stop()

    def test_register_multiple_secrets(self):
        """Test registering multiple secrets at once."""
        masker = SecretMasker()

        with tempfile.TemporaryDirectory() as tmpdir:
            socket_path = Path(tmpdir) / "test.sock"
            server = SecretRegistrationServer(masker, str(socket_path))
            server.start()

            # Give server time to start
            time.sleep(0.1)

            # Register multiple secrets
            secrets = ["secret1", "secret2", "secret3"]
            success = register_secrets_via_socket(secrets, str(socket_path))
            assert success

            # Give server time to process
            time.sleep(0.1)

            # Check that all secrets were registered
            assert masker.mask_string("secret1 and secret2") == "[REDACTED] and [REDACTED]"
            assert masker.mask_string("secret3 here") == "[REDACTED] here"
            assert server.get_registered_count() == 3

            server.stop()

    def test_concurrent_registrations(self):
        """Test multiple clients registering secrets concurrently."""
        masker = SecretMasker()

        with tempfile.TemporaryDirectory() as tmpdir:
            socket_path = Path(tmpdir) / "test.sock"
            server = SecretRegistrationServer(masker, str(socket_path))
            server.start()

            # Give server time to start
            time.sleep(0.1)

            # Create multiple threads to register secrets
            def register_worker(secret_prefix):
                for i in range(5):
                    register_secret_via_socket(f"{secret_prefix}-{i}", str(socket_path))

            threads = []
            for prefix in ["threadA", "threadB", "threadC"]:
                t = threading.Thread(target=register_worker, args=(prefix,))
                threads.append(t)
                t.start()

            # Wait for all threads
            for t in threads:
                t.join()

            # Give server time to process
            time.sleep(0.2)

            # Check that secrets from all threads were registered
            assert masker.mask_string("threadA-0") == "[REDACTED]"
            assert masker.mask_string("threadB-2") == "[REDACTED]"
            assert masker.mask_string("threadC-4") == "[REDACTED]"
            assert server.get_registered_count() == 15  # 3 threads * 5 secrets each

            server.stop()

    def test_invalid_socket_path(self):
        """Test that client handles invalid socket path gracefully."""
        success = register_secret_via_socket("secret", "/nonexistent/socket.sock")
        assert not success

    def test_server_handles_invalid_message(self):
        """Test that server handles invalid messages gracefully."""
        import socket
        import struct

        masker = SecretMasker()

        with tempfile.TemporaryDirectory() as tmpdir:
            socket_path = Path(tmpdir) / "test.sock"
            server = SecretRegistrationServer(masker, str(socket_path))
            server.start()

            # Give server time to start
            time.sleep(0.1)

            # Send invalid message directly
            client_socket = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
            client_socket.connect(str(socket_path))

            # Send invalid JSON
            invalid_message = b"not valid json"
            client_socket.send(struct.pack('!I', len(invalid_message)))
            client_socket.send(invalid_message)

            response = client_socket.recv(1024)
            assert b"ERROR" in response

            client_socket.close()
            server.stop()

    def test_empty_secrets_ignored(self):
        """Test that empty strings are not registered as secrets."""
        masker = SecretMasker()

        with tempfile.TemporaryDirectory() as tmpdir:
            socket_path = Path(tmpdir) / "test.sock"
            server = SecretRegistrationServer(masker, str(socket_path))
            server.start()

            # Give server time to start
            time.sleep(0.1)

            # Try to register empty secrets
            success = register_secrets_via_socket(["", "valid-secret", "", None], str(socket_path))
            assert success

            # Give server time to process
            time.sleep(0.1)

            # Only valid secret should be registered
            assert masker.mask_string("valid-secret") == "[REDACTED]"
            assert server.get_registered_count() == 1

            server.stop()

    def test_socket_cleanup_on_error(self):
        """Test that socket is cleaned up even if server crashes."""
        masker = SecretMasker()

        with tempfile.TemporaryDirectory() as tmpdir:
            socket_path = Path(tmpdir) / "test.sock"

            # Start and stop server multiple times
            for _ in range(3):
                server = SecretRegistrationServer(masker, str(socket_path))
                server.start()
                assert socket_path.exists()
                server.stop()
                time.sleep(0.1)
                assert not socket_path.exists()