"""Tests for the OIDC token helper (runnerlib oidc-token)."""

import json
import os
import stat
import threading
from http.server import BaseHTTPRequestHandler, HTTPServer

import pytest
from typer.testing import CliRunner

from src import oidc
from src.cli import app

runner = CliRunner()
FAKE_TOKEN = "eyJhbGciOiJFUzI1NiJ9.eyJzdWIiOiJ4In0.c2ln"


class _Handler(BaseHTTPRequestHandler):
    requests = []
    status = 200
    location = ""

    def do_POST(self):  # noqa: N802 - http.server API
        length = int(self.headers.get("Content-Length", "0"))
        body = json.loads(self.rfile.read(length))
        _Handler.requests.append({"path": self.path, "auth": self.headers.get("Authorization"), "body": body})
        if _Handler.status in (301, 302, 303, 307, 308):
            self.send_response(_Handler.status)
            self.send_header("Location", _Handler.location)
            self.end_headers()
            return
        if _Handler.status != 200:
            self.send_response(_Handler.status)
            self.end_headers()
            self.wfile.write(json.dumps({"error": "forbidden", "message": "the job is not running"}).encode())
            return
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(json.dumps({"token": FAKE_TOKEN, "audience": body["audience"]}).encode())

    def log_message(self, *args):
        pass


@pytest.fixture
def coordinator(monkeypatch):
    _Handler.requests = []
    _Handler.status = 200
    server = HTTPServer(("127.0.0.1", 0), _Handler)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    url = f"http://127.0.0.1:{server.server_address[1]}"
    monkeypatch.setenv("REACTORCIDE_COORDINATOR_URL", url)
    monkeypatch.setenv("REACTORCIDE_API_TOKEN", "job-token-value")
    monkeypatch.delenv("REACTORCIDE_SECRETS_SOCKET", raising=False)
    yield url
    server.shutdown()


def test_request_token_sends_audience_and_job_token(coordinator):
    token = oidc.request_token("sts.example.com", ttl_seconds=300, allow_insecure_transport=True)
    assert token == FAKE_TOKEN
    request = _Handler.requests[0]
    assert request["path"] == "/api/v1/oidc/token"
    assert request["auth"] == "Bearer job-token-value"
    assert request["body"] == {"audience": "sts.example.com", "ttl_seconds": 300}


def test_request_token_requires_tls(coordinator):
    with pytest.raises(oidc.OIDCTokenError, match="requires TLS"):
        oidc.request_token("aud")
    assert _Handler.requests == []


def test_request_token_needs_job_environment(monkeypatch):
    monkeypatch.delenv("REACTORCIDE_COORDINATOR_URL", raising=False)
    monkeypatch.delenv("REACTORCIDE_API_TOKEN", raising=False)
    with pytest.raises(oidc.OIDCTokenError, match="only available to a job"):
        oidc.request_token("aud")


def test_request_token_error_has_no_secret(coordinator):
    _Handler.status = 403
    with pytest.raises(oidc.OIDCTokenError) as info:
        oidc.request_token("aud", allow_insecure_transport=True)
    assert "status 403" in str(info.value)
    assert "job-token-value" not in str(info.value)


def test_cli_prints_only_the_token(coordinator):
    result = runner.invoke(app, ["oidc-token", "--audience", "sts.example.com", "--allow-insecure-transport"])
    assert result.exit_code == 0, result.output
    assert result.stdout == FAKE_TOKEN + "\n"


def test_cli_reads_insecure_transport_from_worker_env(coordinator, monkeypatch):
    # A worker in its development mode sets the value; the job script does
    # not need the flag.
    monkeypatch.setenv("REACTORCIDE_ALLOW_INSECURE_TRANSPORT", "true")
    result = runner.invoke(app, ["oidc-token", "--audience", "sts.example.com"])
    assert result.exit_code == 0, result.output
    assert result.stdout == FAKE_TOKEN + "\n"


def test_cli_requires_tls_without_worker_env(coordinator, monkeypatch):
    monkeypatch.delenv("REACTORCIDE_ALLOW_INSECURE_TRANSPORT", raising=False)
    result = runner.invoke(app, ["oidc-token", "--audience", "sts.example.com"])
    assert result.exit_code == 1
    assert "requires TLS" in result.output


def test_cli_writes_a_private_file(coordinator, tmp_path):
    out = tmp_path / "token"
    result = runner.invoke(app, ["oidc-token", "-a", "aud", "-o", str(out), "--allow-insecure-transport"])
    assert result.exit_code == 0, result.output
    assert out.read_text() == FAKE_TOKEN
    assert stat.S_IMODE(os.stat(out).st_mode) == 0o600
    assert FAKE_TOKEN not in result.stdout


def test_cli_failure_goes_to_stderr(coordinator):
    _Handler.status = 403
    result = runner.invoke(app, ["oidc-token", "--audience", "aud", "--allow-insecure-transport"])
    assert result.exit_code == 1
    assert FAKE_TOKEN not in result.output
    assert "status 403" in result.output


def test_token_is_registered_for_masking(coordinator, monkeypatch):
    seen = []
    monkeypatch.setenv("REACTORCIDE_SECRETS_SOCKET", "/tmp/mask.sock")
    import src.secrets_server as server

    monkeypatch.setattr(server, "register_secret_via_socket", lambda secret, path: seen.append((secret, path)) or True)
    oidc.request_token("aud", allow_insecure_transport=True)
    assert seen == [(FAKE_TOKEN, "/tmp/mask.sock")]


class _CaptureHandler(BaseHTTPRequestHandler):
    seen = []

    def _record(self):
        _CaptureHandler.seen.append(self.headers.get("Authorization"))
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(json.dumps({"token": FAKE_TOKEN}).encode())

    do_GET = _record  # noqa: N815 - http.server API
    do_POST = _record  # noqa: N815 - http.server API

    def log_message(self, *args):
        pass


@pytest.mark.parametrize("code", [301, 302, 303, 307, 308])
def test_request_token_refuses_redirects(coordinator, code):
    """A redirect is never followed: urllib would send the job token to the
    redirect target, also to another host."""
    _CaptureHandler.seen = []
    other = HTTPServer(("127.0.0.1", 0), _CaptureHandler)
    thread = threading.Thread(target=other.serve_forever, daemon=True)
    thread.start()
    try:
        _Handler.status = code
        _Handler.location = f"http://127.0.0.1:{other.server_address[1]}/steal"
        with pytest.raises(oidc.OIDCTokenError) as excinfo:
            oidc.request_token("sts.example.com", allow_insecure_transport=True)
        assert "job-token-value" not in str(excinfo.value)
        assert _CaptureHandler.seen == []
    finally:
        other.shutdown()
