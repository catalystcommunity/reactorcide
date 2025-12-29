"""Secret reference resolution - matches foundry's ${secret:path:key} pattern.

This module provides secret reference parsing and resolution for the
${secret:path/with/slashes:key} syntax used in environment variables
and configuration files.
"""

import re
from typing import Dict, Optional, List, Callable
from dataclasses import dataclass

# Pattern: ${secret:path/to/secret:key}
# Path can contain: alphanumeric, slash, dash, underscore
# Key can contain: alphanumeric, dash, underscore (no slashes)
SECRET_REF_PATTERN = re.compile(r'\$\{secret:([a-zA-Z0-9/_-]+):([a-zA-Z0-9_-]+)\}')


@dataclass(frozen=True)
class SecretRef:
    """A parsed secret reference."""
    path: str   # e.g., "npm-publishing/tokens" or "myproject/prod"
    key: str    # e.g., "NPM_TOKEN"
    raw: str    # Original "${secret:...}" string


def parse_secret_ref(s: str) -> Optional[SecretRef]:
    """Parse a secret reference string. Returns None if not a valid reference."""
    match = SECRET_REF_PATTERN.fullmatch(s)
    if not match:
        return None
    return SecretRef(
        path=match.group(1),
        key=match.group(2),
        raw=s
    )


def find_secret_refs(text: str) -> List[SecretRef]:
    """Find all secret references in a string."""
    return [
        SecretRef(path=m.group(1), key=m.group(2), raw=m.group(0))
        for m in SECRET_REF_PATTERN.finditer(text)
    ]


def is_secret_ref(s: str) -> bool:
    """Check if a string is a secret reference."""
    return SECRET_REF_PATTERN.fullmatch(s) is not None


def has_secret_refs(text: str) -> bool:
    """Check if a string contains any secret references."""
    return SECRET_REF_PATTERN.search(text) is not None


# Type for the getter function: (path, key) -> Optional[str]
SecretGetter = Callable[[str, str], Optional[str]]


def resolve_secrets_in_string(
    text: str,
    get_secret: SecretGetter,
    missing_ok: bool = False
) -> str:
    """Replace all ${secret:path:key} references with actual values.

    Args:
        text: String potentially containing secret references
        get_secret: Function (path, key) -> value that retrieves secrets
        missing_ok: If True, leave unreplaced refs; if False, raise on missing
    """
    def replacer(match):
        path = match.group(1)
        key = match.group(2)
        value = get_secret(path, key)
        if value is None:
            if missing_ok:
                return match.group(0)  # Leave unreplaced
            raise ValueError(f"Secret not found: {path}:{key}")
        return value

    return SECRET_REF_PATTERN.sub(replacer, text)


def resolve_secrets_in_dict(
    data: Dict,
    get_secret: SecretGetter,
    missing_ok: bool = False
) -> Dict:
    """Recursively resolve secret references in a dictionary."""
    result = {}
    for k, v in data.items():
        if isinstance(v, str):
            result[k] = resolve_secrets_in_string(v, get_secret, missing_ok)
        elif isinstance(v, dict):
            result[k] = resolve_secrets_in_dict(v, get_secret, missing_ok)
        elif isinstance(v, list):
            result[k] = [
                resolve_secrets_in_string(item, get_secret, missing_ok)
                if isinstance(item, str) else item
                for item in v
            ]
        else:
            result[k] = v
    return result


def collect_secret_refs(data: Dict) -> List[SecretRef]:
    """Collect all secret references from a dictionary (for validation)."""
    refs = []
    for v in data.values():
        if isinstance(v, str):
            refs.extend(find_secret_refs(v))
        elif isinstance(v, dict):
            refs.extend(collect_secret_refs(v))
        elif isinstance(v, list):
            for item in v:
                if isinstance(item, str):
                    refs.extend(find_secret_refs(item))
    return refs
