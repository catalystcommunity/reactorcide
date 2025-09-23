"""Value-based secret masking for logs and output.

This module provides value-based secret masking, tracking actual secret
values and replacing them wherever they appear, regardless of key names.
"""

import re
from typing import Dict, List, Set, Any
from threading import Lock


class SecretMasker:
    """Handles masking of known secret values in logs and output.

    This is a value-based masking system - it tracks actual secret strings
    and replaces them wherever they appear, regardless of the key name.
    """

    def __init__(self, redaction_text: str = "[REDACTED]"):
        """Initialize the secret masker.

        Args:
            redaction_text: Text to replace secrets with (default: "[REDACTED]")
        """
        self._secrets: Set[str] = set()
        self._lock = Lock()
        self._redaction_text = redaction_text
        self._min_secret_length = 3  # Don't mask very short strings to avoid false positives

    def register_secret(self, value: Any) -> None:
        """Register a secret value that should be masked.

        Args:
            value: The secret value to register (will be converted to string)
        """
        if value is None:
            return

        str_value = str(value)
        if not str_value:
            return

        with self._lock:
            self._secrets.add(str_value)

    def register_secrets(self, values: List[Any]) -> None:
        """Register multiple secret values at once.

        Args:
            values: List of secret values to register
        """
        for value in values:
            self.register_secret(value)

    def register_env_vars(self, env_vars: Dict[str, Any]) -> None:
        """Register the VALUES of environment variables as secrets.

        This allows masking based on actual values, not key names.
        For example, if ZEALOUS="my-secret-123", we register "my-secret-123" as a secret.

        Args:
            env_vars: Dictionary of environment variables (values will be registered)
        """
        for value in env_vars.values():
            self.register_secret(value)

    def mask_string(self, text: str) -> str:
        """Replace all known secret values in a string with redaction text.

        This is the core masking function - it finds and replaces actual secret values.

        Args:
            text: The text to mask secrets in

        Returns:
            Text with all known secrets replaced with redaction text
        """
        if not text:
            return text

        with self._lock:
            masked = text
            for secret in self._secrets:
                # Only mask secrets that are reasonably long to avoid false positives
                if len(secret) >= self._min_secret_length:
                    # Use re.escape to handle special regex characters in secrets
                    pattern = re.escape(secret)
                    masked = re.sub(pattern, self._redaction_text, masked)
            return masked

    def mask_command_args(self, args: List[str]) -> List[str]:
        """Mask secret values in command arguments.

        Unlike key-based masking, this finds actual secret values in the args.

        Args:
            args: List of command arguments

        Returns:
            List of command arguments with secrets masked
        """
        return [self.mask_string(arg) for arg in args]

    def mask_dict_values(self, data: Dict[str, Any], mask_keys: bool = False) -> Dict[str, Any]:
        """Mask secret values in a dictionary.

        Args:
            data: Dictionary to mask values in
            mask_keys: If True, also mask secrets in keys (default: False)

        Returns:
            Dictionary with secret values masked
        """
        masked = {}
        for key, value in data.items():
            masked_key = self.mask_string(key) if mask_keys else key
            if isinstance(value, str):
                masked[masked_key] = self.mask_string(value)
            elif isinstance(value, dict):
                masked[masked_key] = self.mask_dict_values(value, mask_keys)
            elif isinstance(value, list):
                masked[masked_key] = [
                    self.mask_string(str(v)) if v is not None else v
                    for v in value
                ]
            else:
                # For non-string values, convert to string and mask
                masked[masked_key] = self.mask_string(str(value)) if value is not None else value
        return masked

    def clear(self) -> None:
        """Remove all registered secrets (useful for testing)."""
        with self._lock:
            self._secrets.clear()

    def size(self) -> int:
        """Return the number of registered secrets (useful for debugging)."""
        with self._lock:
            return len(self._secrets)

    def has_secret(self, value: str) -> bool:
        """Check if a value is registered as a secret.

        Args:
            value: Value to check

        Returns:
            True if the value is registered as a secret
        """
        with self._lock:
            return value in self._secrets


# Global masker instance that can be used throughout the application
_default_masker = SecretMasker()


def register_secret(value: Any) -> None:
    """Register a secret value with the default masker."""
    _default_masker.register_secret(value)


def register_secrets(values: List[Any]) -> None:
    """Register multiple secrets with the default masker."""
    _default_masker.register_secrets(values)


def register_env_vars(env_vars: Dict[str, Any]) -> None:
    """Register environment variable values with the default masker."""
    _default_masker.register_env_vars(env_vars)


def mask_string(text: str) -> str:
    """Mask secrets in a string using the default masker."""
    return _default_masker.mask_string(text)


def mask_command_args(args: List[str]) -> List[str]:
    """Mask secrets in command arguments using the default masker."""
    return _default_masker.mask_command_args(args)


def mask_dict_values(data: Dict[str, Any], mask_keys: bool = False) -> Dict[str, Any]:
    """Mask secrets in dictionary values using the default masker."""
    return _default_masker.mask_dict_values(data, mask_keys)


def clear_secrets() -> None:
    """Clear all registered secrets from the default masker."""
    _default_masker.clear()


def get_default_masker() -> SecretMasker:
    """Get the default masker instance (useful for testing)."""
    return _default_masker