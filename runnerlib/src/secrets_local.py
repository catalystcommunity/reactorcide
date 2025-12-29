"""Local encrypted secrets storage - functional implementation.

This module provides local password-based encrypted secrets storage with:
- scrypt key derivation (N=2^18, ~256MB memory) for brute force resistance
- Fernet encryption (AES-128-CBC + HMAC)
- XDG-compliant storage path
- Path/key validation
"""

import os
import re
import json
import base64
from pathlib import Path
from typing import Optional, List

from cryptography.fernet import Fernet, InvalidToken
from cryptography.hazmat.primitives.kdf.scrypt import Scrypt

# High cost parameters - intentionally slow for brute force resistance
SCRYPT_N = 2**18  # ~256MB memory
SCRYPT_R = 8
SCRYPT_P = 1
SALT_SIZE = 32

# Path validation: alphanumeric, dash, underscore, forward slash
PATH_PATTERN = re.compile(r'^[a-zA-Z0-9/_-]+$')
KEY_PATTERN = re.compile(r'^[a-zA-Z0-9_-]+$')


def get_default_base_path() -> Path:
    """Get the default secrets storage path (XDG compliant)."""
    xdg_config = os.environ.get("XDG_CONFIG_HOME", str(Path.home() / ".config"))
    return Path(xdg_config) / "reactorcide" / "secrets"


def get_or_create_salt(base_path: Path) -> bytes:
    """Get existing salt or create a new one."""
    salt_file = base_path / ".salt"
    if salt_file.exists():
        return salt_file.read_bytes()
    base_path.mkdir(parents=True, exist_ok=True)
    salt = os.urandom(SALT_SIZE)
    salt_file.write_bytes(salt)
    salt_file.chmod(0o600)
    return salt


def derive_key(password: str, base_path: Path) -> bytes:
    """Derive encryption key from password using scrypt (expensive)."""
    salt = get_or_create_salt(base_path)
    kdf = Scrypt(salt=salt, length=32, n=SCRYPT_N, r=SCRYPT_R, p=SCRYPT_P)
    return base64.urlsafe_b64encode(kdf.derive(password.encode()))


def get_fernet(password: str, base_path: Path) -> Fernet:
    """Create Fernet cipher from password."""
    key = derive_key(password, base_path)
    return Fernet(key)


def validate_path(path: str) -> None:
    """Validate a secret path (allows slashes)."""
    if not path or not PATH_PATTERN.match(path):
        raise ValueError(f"Invalid path: {path}. Use alphanumeric, dash, underscore, or slash.")


def validate_key(key: str) -> None:
    """Validate a secret key (no slashes)."""
    if not key or not KEY_PATTERN.match(key):
        raise ValueError(f"Invalid key: {key}. Use alphanumeric, dash, or underscore.")


def secrets_file(base_path: Path) -> Path:
    """Get the path to the encrypted secrets file."""
    return base_path / "secrets.enc"


def load_all(password: str, base_path: Path) -> dict:
    """Load and decrypt all secrets. Returns {path: {key: value}}."""
    sf = secrets_file(base_path)
    if not sf.exists():
        return {}
    try:
        fernet = get_fernet(password, base_path)
        encrypted = sf.read_bytes()
        decrypted = fernet.decrypt(encrypted)
        return json.loads(decrypted.decode())
    except InvalidToken:
        raise ValueError("Invalid password or corrupted secrets file")


def save_all(data: dict, password: str, base_path: Path) -> None:
    """Encrypt and save all secrets."""
    base_path.mkdir(parents=True, exist_ok=True)
    fernet = get_fernet(password, base_path)
    plaintext = json.dumps(data, indent=2).encode()
    encrypted = fernet.encrypt(plaintext)
    sf = secrets_file(base_path)
    sf.write_bytes(encrypted)
    sf.chmod(0o600)


# --- Public API functions ---

def secret_get(path: str, key: str, password: str, base_path: Optional[Path] = None) -> Optional[str]:
    """Get a single secret value. Returns None if not found."""
    validate_path(path)
    validate_key(key)
    bp = base_path or get_default_base_path()
    data = load_all(password, bp)
    return data.get(path, {}).get(key)


def secret_set(path: str, key: str, value: str, password: str, base_path: Optional[Path] = None) -> None:
    """Set a secret value."""
    validate_path(path)
    validate_key(key)
    bp = base_path or get_default_base_path()
    data = load_all(password, bp)
    if path not in data:
        data[path] = {}
    data[path][key] = value
    save_all(data, password, bp)


def secret_delete(path: str, key: str, password: str, base_path: Optional[Path] = None) -> bool:
    """Delete a secret. Returns True if it existed."""
    validate_path(path)
    validate_key(key)
    bp = base_path or get_default_base_path()
    data = load_all(password, bp)
    if path in data and key in data[path]:
        del data[path][key]
        if not data[path]:  # Remove empty path
            del data[path]
        save_all(data, password, bp)
        return True
    return False


def secret_list_keys(path: str, password: str, base_path: Optional[Path] = None) -> List[str]:
    """List all keys in a path (NOT values)."""
    validate_path(path)
    bp = base_path or get_default_base_path()
    data = load_all(password, bp)
    return list(data.get(path, {}).keys())


def secret_list_paths(password: str, base_path: Optional[Path] = None) -> List[str]:
    """List all paths that have secrets."""
    bp = base_path or get_default_base_path()
    data = load_all(password, bp)
    return list(data.keys())


def secrets_init(password: str, base_path: Optional[Path] = None, force: bool = False) -> None:
    """Initialize secrets storage. Creates salt and empty secrets file."""
    bp = base_path or get_default_base_path()
    sf = secrets_file(bp)
    if sf.exists() and not force:
        raise ValueError("Secrets already initialized. Use force=True to reinitialize.")
    bp.mkdir(parents=True, exist_ok=True)
    get_or_create_salt(bp)  # Ensure salt exists
    save_all({}, password, bp)


def is_initialized(base_path: Optional[Path] = None) -> bool:
    """Check if secrets storage is initialized."""
    bp = base_path or get_default_base_path()
    return secrets_file(bp).exists()
