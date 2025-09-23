#!/usr/bin/env python3
"""Command-line tool for registering secrets dynamically during job execution.

This tool can be used by jobs running in containers to register secrets
that should be masked in all subsequent output.

Usage:
    python -m src.register_secret "my-secret-value"
    python -m src.register_secret "secret1" "secret2" "secret3"
    echo "my-secret" | python -m src.register_secret -

Environment:
    REACTORCIDE_SECRETS_SOCKET: Path to the Unix domain socket for registration
"""

import sys
import os
import argparse

from src.secrets_server import register_secret_via_socket, register_secrets_via_socket


def main():
    parser = argparse.ArgumentParser(
        description="Register secrets to be masked in job output",
        epilog="Secrets are registered with the Reactorcide runner for masking in logs."
    )
    parser.add_argument(
        "secrets",
        nargs="+",
        help="Secret values to register (use '-' to read from stdin)"
    )
    parser.add_argument(
        "--socket",
        default=os.environ.get("REACTORCIDE_SECRETS_SOCKET"),
        help="Path to secrets socket (default: from REACTORCIDE_SECRETS_SOCKET env var)"
    )

    args = parser.parse_args()

    if not args.socket:
        print("Error: No socket path provided. Set REACTORCIDE_SECRETS_SOCKET or use --socket", file=sys.stderr)
        return 1

    # Collect secrets
    secrets = []
    for secret in args.secrets:
        if secret == "-":
            # Read from stdin
            for line in sys.stdin:
                line = line.strip()
                if line:
                    secrets.append(line)
        else:
            secrets.append(secret)

    if not secrets:
        print("Warning: No secrets provided", file=sys.stderr)
        return 0

    # Register secrets
    try:
        if len(secrets) == 1:
            success = register_secret_via_socket(secrets[0], args.socket)
        else:
            success = register_secrets_via_socket(secrets, args.socket)

        if success:
            print(f"Successfully registered {len(secrets)} secret(s)")
            return 0
        else:
            print("Failed to register secrets", file=sys.stderr)
            return 1

    except Exception as e:
        print(f"Error registering secrets: {e}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())