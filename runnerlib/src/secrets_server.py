"""Socket server for dynamic secret registration during job execution.

This module provides a Unix domain socket server that allows running jobs
to register secrets dynamically by connecting to the socket and sending values.
"""

import socket
import os
import threading
import json
from pathlib import Path
from typing import Optional
import select
import struct

from src.logging import StructuredLogger

# Create component-specific logger
logger = StructuredLogger("secrets_server")


class SecretRegistrationServer:
    """Unix domain socket server for dynamic secret registration.

    Jobs can connect to this server and register secrets that should be
    masked in all subsequent output.
    """

    def __init__(self, masker, socket_path: Optional[str] = None):
        """Initialize the secret registration server.

        Args:
            masker: SecretMasker instance to register secrets with
            socket_path: Path for Unix domain socket (default: auto-generate in /tmp)
        """
        self.masker = masker
        self.socket_path = socket_path or f"/tmp/reactorcide-secrets-{os.getpid()}.sock"
        self.server_socket = None
        self.server_thread = None
        self.running = False
        self._lock = threading.Lock()
        self._registered_count = 0

    def start(self):
        """Start the socket server in a background thread."""
        if self.running:
            return

        # Ensure socket doesn't already exist
        socket_path = Path(self.socket_path)
        if socket_path.exists():
            socket_path.unlink()

        # Create Unix domain socket
        self.server_socket = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        self.server_socket.bind(self.socket_path)
        self.server_socket.listen(5)
        self.server_socket.setblocking(False)

        # Make socket accessible to job (container may run as different user)
        os.chmod(self.socket_path, 0o666)

        self.running = True
        self.server_thread = threading.Thread(target=self._serve, daemon=True)
        self.server_thread.start()

        logger.info("Secret registration server started", fields={"socket_path": self.socket_path})

    def stop(self):
        """Stop the socket server and clean up."""
        if not self.running:
            return

        self.running = False

        if self.server_socket:
            try:
                self.server_socket.close()
            except Exception:
                pass

        if self.server_thread:
            self.server_thread.join(timeout=1.0)

        # Clean up socket file
        socket_path = Path(self.socket_path)
        if socket_path.exists():
            try:
                socket_path.unlink()
            except Exception:
                pass

        logger.info(
            "Secret registration server stopped",
            fields={"registered_count": self._registered_count}
        )

    def _serve(self):
        """Main server loop - handles incoming connections."""
        while self.running:
            try:
                # Use select with timeout to allow checking self.running
                readable, _, _ = select.select([self.server_socket], [], [], 0.5)

                if not readable:
                    continue

                try:
                    client_socket, _ = self.server_socket.accept()
                    # Handle client in same thread (simple and sufficient for our use case)
                    self._handle_client(client_socket)
                except socket.error:
                    continue

            except Exception as e:
                if self.running:
                    logger.error("Error in secret server", error=e)

    def _handle_client(self, client_socket):
        """Handle a client connection."""
        try:
            client_socket.settimeout(5.0)  # 5 second timeout for client operations

            # Read message length (4 bytes, network byte order)
            length_data = client_socket.recv(4)
            if len(length_data) != 4:
                return

            message_length = struct.unpack('!I', length_data)[0]

            # Sanity check on message length (max 1MB)
            if message_length > 1024 * 1024:
                logger.warning(
                    "Rejecting oversized message",
                    fields={"message_length": message_length, "max_length": self.MAX_MESSAGE_SIZE}
                )
                return

            # Read the message
            message_data = b''
            while len(message_data) < message_length:
                chunk = client_socket.recv(min(4096, message_length - len(message_data)))
                if not chunk:
                    break
                message_data += chunk

            if len(message_data) != message_length:
                logger.warning(
                    "Incomplete message received",
                    fields={"received": len(message_data), "expected": message_length}
                )
                return

            # Parse JSON message
            try:
                message = json.loads(message_data.decode('utf-8'))
            except (json.JSONDecodeError, UnicodeDecodeError) as e:
                logger.warning("Invalid message format", fields={"error": str(e)})
                client_socket.send(b'ERROR: Invalid JSON\n')
                return

            # Handle the registration request
            if message.get('action') == 'register':
                secrets = message.get('secrets', [])
                if isinstance(secrets, str):
                    secrets = [secrets]

                count = 0
                for secret in secrets:
                    if secret and isinstance(secret, str):
                        self.masker.register_secret(secret)
                        count += 1
                        with self._lock:
                            self._registered_count += 1

                response = {'status': 'ok', 'registered': count}
                client_socket.send(json.dumps(response).encode('utf-8') + b'\n')

                logger.debug("Registered secrets from client", fields={"count": count})

            else:
                client_socket.send(b'ERROR: Unknown action\n')

        except socket.timeout:
            logger.warning("Client connection timed out")
        except Exception as e:
            logger.error("Error handling client", error=e)
        finally:
            try:
                client_socket.close()
            except Exception:
                pass

    def get_socket_path(self) -> str:
        """Get the path to the Unix domain socket."""
        return self.socket_path

    def get_registered_count(self) -> int:
        """Get the total number of secrets registered via this server."""
        with self._lock:
            return self._registered_count


def register_secret_via_socket(secret: str, socket_path: str) -> bool:
    """Client function to register a secret with the server.

    Args:
        secret: The secret value to register
        socket_path: Path to the Unix domain socket

    Returns:
        True if successful, False otherwise
    """
    try:
        client_socket = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        client_socket.settimeout(5.0)
        client_socket.connect(socket_path)

        # Prepare message
        message = json.dumps({'action': 'register', 'secrets': [secret]})
        message_bytes = message.encode('utf-8')

        # Send length prefix (4 bytes, network byte order)
        client_socket.send(struct.pack('!I', len(message_bytes)))

        # Send message
        client_socket.send(message_bytes)

        # Read response
        response_data = client_socket.recv(4096)
        response = json.loads(response_data.decode('utf-8').strip())

        client_socket.close()

        return response.get('status') == 'ok'

    except Exception as e:
        logger.error("Failed to register secret", error=e)
        return False


def register_secrets_via_socket(secrets: list, socket_path: str) -> bool:
    """Client function to register multiple secrets with the server.

    Args:
        secrets: List of secret values to register
        socket_path: Path to the Unix domain socket

    Returns:
        True if successful, False otherwise
    """
    try:
        client_socket = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        client_socket.settimeout(5.0)
        client_socket.connect(socket_path)

        # Prepare message
        message = json.dumps({'action': 'register', 'secrets': secrets})
        message_bytes = message.encode('utf-8')

        # Send length prefix (4 bytes, network byte order)
        client_socket.send(struct.pack('!I', len(message_bytes)))

        # Send message
        client_socket.send(message_bytes)

        # Read response
        response_data = client_socket.recv(4096)
        response = json.loads(response_data.decode('utf-8').strip())

        client_socket.close()

        return response.get('status') == 'ok'

    except Exception as e:
        logger.error("Failed to register secrets", error=e)
        return False