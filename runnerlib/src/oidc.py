"""Request an OIDC token for the running job (docs/oidc.md).

The coordinator signs the token with claims that describe the job. The job
cannot choose its claims: it only names the audience. Use the token with a
consumer that trusts the Reactorcide issuer, for example a cloud provider's
workload identity federation.

The token is a credential. This module never logs it and never writes it to
stderr. ``runnerlib oidc-token`` prints it only to stdout, for piping.
"""

import json
import os
import urllib.error
import urllib.parse
import urllib.request
from typing import Optional


class OIDCTokenError(RuntimeError):
    """The coordinator did not issue a token. The message holds no token."""


class _NoRedirectHandler(urllib.request.HTTPRedirectHandler):
    """Refuse every redirect.

    urllib copies the Authorization header to the redirect target, also to
    another host. The token request carries the job token, so a redirect
    (from a proxy, a wrong URL, or an attacker) must never be followed.
    """

    def redirect_request(self, req, fp, code, msg, headers, newurl):
        raise urllib.error.URLError("the coordinator answered with a redirect; redirects are refused")


def request_token(
    audience: str,
    ttl_seconds: Optional[int] = None,
    allow_insecure_transport: bool = False,
    coordinator_url: Optional[str] = None,
    api_token: Optional[str] = None,
    timeout: float = 30.0,
) -> str:
    """Return a signed OIDC token for ``audience``.

    The job token (``REACTORCIDE_API_TOKEN``) and the coordinator URL
    (``REACTORCIDE_COORDINATOR_URL``) come from the job environment unless
    given. HTTPS is required unless ``allow_insecure_transport`` is true
    (an isolated development network only).
    """
    if not audience or not audience.strip():
        raise OIDCTokenError("an audience is required")
    coordinator_url = coordinator_url or os.getenv("REACTORCIDE_COORDINATOR_URL")
    api_token = api_token or os.getenv("REACTORCIDE_API_TOKEN")
    if not coordinator_url or not api_token:
        raise OIDCTokenError(
            "REACTORCIDE_COORDINATOR_URL and REACTORCIDE_API_TOKEN are required; "
            "an OIDC token is only available to a job that a coordinator runs"
        )
    url = coordinator_url.rstrip("/") + "/api/v1/oidc/token"
    scheme = urllib.parse.urlparse(url).scheme.lower()
    if scheme not in {"http", "https"}:
        raise OIDCTokenError("the coordinator URL uses an unsupported transport")
    if scheme != "https" and not allow_insecure_transport:
        raise OIDCTokenError(
            "an OIDC token request requires TLS; use --allow-insecure-transport "
            "only on an isolated development network"
        )
    body = {"audience": audience.strip()}
    if ttl_seconds is not None:
        body["ttl_seconds"] = int(ttl_seconds)
    req = urllib.request.Request(
        url,
        data=json.dumps(body).encode("utf-8"),
        headers={"Content-Type": "application/json", "Authorization": f"Bearer {api_token}"},
        method="POST",
    )
    opener = urllib.request.build_opener(_NoRedirectHandler())
    try:
        with opener.open(req, timeout=timeout) as resp:
            payload = json.loads(resp.read().decode("utf-8"))
    except urllib.error.HTTPError as exc:
        message = ""
        try:
            message = json.loads(exc.read().decode("utf-8")).get("message", "")
        except Exception:  # noqa: BLE001 - the body is only a hint
            message = ""
        detail = f": {message}" if message else ""
        raise OIDCTokenError(f"the coordinator refused the OIDC token request (status {exc.code}){detail}") from None
    except (urllib.error.URLError, OSError, ValueError) as exc:
        raise OIDCTokenError(f"the OIDC token request failed: {type(exc).__name__}") from None
    token = payload.get("token") if isinstance(payload, dict) else None
    if not isinstance(token, str) or not token:
        raise OIDCTokenError("the coordinator response has no token")
    _register_mask(token)
    return token


def _register_mask(token: str) -> None:
    """Register the token with the runnerlib masker, when it runs, so that a
    token that a job prints by accident is masked in the job log."""
    socket_path = os.getenv("REACTORCIDE_SECRETS_SOCKET")
    if not socket_path:
        return
    try:
        from src.secrets_server import register_secret_via_socket

        register_secret_via_socket(token, socket_path)
    except Exception:  # noqa: BLE001 - masking is best effort
        pass


def write_token_file(path: str, token: str) -> None:
    """Write ``token`` to ``path`` with mode 0600 (owner read and write only)."""
    flags = os.O_WRONLY | os.O_CREAT | os.O_TRUNC
    fd = os.open(path, flags, 0o600)
    try:
        os.fchmod(fd, 0o600)
        os.write(fd, token.encode("utf-8"))
    finally:
        os.close(fd)
