"""Tests for the zero-touch Kubernetes deployment sequence."""

from __future__ import annotations

import base64
import importlib.util
import subprocess
from pathlib import Path


REPOSITORY_ROOT = Path(__file__).resolve().parents[2]
DEPLOY_SCRIPT = REPOSITORY_ROOT / "jobs" / "scripts" / "deploy_k8s.py"


def _load_deploy_module():
    spec = importlib.util.spec_from_file_location("reactorcide_deploy_k8s", DEPLOY_SCRIPT)
    module = importlib.util.module_from_spec(spec)
    assert spec.loader is not None
    spec.loader.exec_module(module)
    return module


def _deployment_config():
    return {
        "namespace": "reactorcide",
        "release": "reactorcide",
        "db_uri": "postgresql://configured",
        "provision_postgres": False,
        "deploy_corndogs": False,
        "corndogs_url": "corndogs:5080",
        "gateway_enabled": False,
        "api_token_secret_name": "base-reactorcide-api-token",
    }


def test_registry_pull_secret_stays_out_of_process_arguments(monkeypatch):
    """Registry credentials go to kubectl through standard input."""
    deploy = _load_deploy_module()
    config = _deployment_config() | {
        "registry_server": "containers.example.com",
        "registry_pull_secret_name": "registry-pull",
        "registry_username": "pull-user",
        "registry_password": "pull-password",
    }
    calls = []

    def run_process(args, **kwargs):
        calls.append((list(args), kwargs))
        return subprocess.CompletedProcess(args=args, returncode=0)

    monkeypatch.setattr(deploy.subprocess, "run", run_process)
    monkeypatch.delenv("REACTORCIDE_SECRETS_SOCKET", raising=False)

    deploy.apply_registry_pull_secret(config)

    assert len(calls) == 1
    process_arguments = " ".join(calls[0][0])
    assert config["registry_username"] not in process_arguments
    assert config["registry_password"] not in process_arguments
    manifest = calls[0][1]["input"]
    assert config["registry_username"] not in manifest
    assert config["registry_password"] not in manifest


def test_registry_pull_secret_is_added_to_service_and_job_pods():
    """Helm attaches the registry Secret to service and job pods."""
    deploy = _load_deploy_module()
    config = _deployment_config() | {
        "helm_values": "",
        "image_tag": "",
        "registry_pull_secret_name": "registry-pull",
        "object_store_type": "memory",
        "vcs_enabled": False,
        "vcs_base_url": "",
        "default_org": "default",
        "worker_coordinator_url": "",
        "worker_enrollment_token_secret": "",
    }

    args = deploy.build_helm_values(config, "postgresql://configured", "corndogs:5080")

    assert "imagePullSecrets[0].name=registry-pull" in args
    assert "worker.jobImagePullSecrets[0]=registry-pull" in args


def test_registry_pull_secret_is_added_to_corndogs(monkeypatch):
    """The separate Corndogs release uses the registry pull Secret."""
    deploy = _load_deploy_module()
    commands = []
    config = _deployment_config() | {
        "registry_pull_secret_name": "registry-pull",
    }

    monkeypatch.setattr(
        deploy,
        "run_cmd",
        lambda command, **kwargs: commands.append(command),
    )

    deploy.deploy_corndogs(config)

    assert any("imagePullSecrets[0].name=registry-pull" in command for command in commands)


def test_deploy_bootstraps_token_before_web(monkeypatch):
    """A missing token causes a control-plane deploy before the web deploy."""
    deploy = _load_deploy_module()
    events = []
    helm_args = ["--set", "web.enabled=true"]

    monkeypatch.setattr(deploy, "run_cmd", lambda *args, **kwargs: None)
    monkeypatch.setattr(deploy, "setup_kubeconfig", lambda *args, **kwargs: None)
    monkeypatch.setattr(deploy, "verify_cluster", lambda **kwargs: None)
    monkeypatch.setattr(deploy, "create_namespace", lambda *args, **kwargs: None)
    monkeypatch.setattr(deploy, "build_helm_values", lambda *args: helm_args)
    monkeypatch.setattr(deploy, "api_token_exists", lambda *args, **kwargs: False)
    monkeypatch.setattr(
        deploy,
        "deploy_helm",
        lambda config, args, **kwargs: events.append(("helm", list(args))),
    )
    monkeypatch.setattr(
        deploy,
        "wait_for_rollout",
        lambda config, include_web, **kwargs: events.append(("wait", include_web)),
    )
    monkeypatch.setattr(
        deploy,
        "run_migrations",
        lambda *args, **kwargs: events.append(("migrate",)),
    )
    monkeypatch.setattr(
        deploy,
        "create_api_token",
        lambda *args, **kwargs: events.append(("token",)) or True,
    )
    monkeypatch.setattr(deploy, "show_status", lambda *args, **kwargs: None)

    assert deploy.deploy(_deployment_config()) == 0
    assert events == [
        ("helm", [*helm_args, "--set", "web.enabled=false"]),
        ("wait", False),
        ("migrate",),
        ("token",),
        ("helm", helm_args),
        ("wait", True),
    ]


def test_deploy_with_token_uses_one_helm_upgrade(monkeypatch):
    """An existing token lets the control plane and web deploy together."""
    deploy = _load_deploy_module()
    events = []
    helm_args = ["--set", "web.enabled=true"]

    monkeypatch.setattr(deploy, "run_cmd", lambda *args, **kwargs: None)
    monkeypatch.setattr(deploy, "setup_kubeconfig", lambda *args, **kwargs: None)
    monkeypatch.setattr(deploy, "verify_cluster", lambda **kwargs: None)
    monkeypatch.setattr(deploy, "create_namespace", lambda *args, **kwargs: None)
    monkeypatch.setattr(deploy, "build_helm_values", lambda *args: helm_args)
    monkeypatch.setattr(deploy, "api_token_exists", lambda *args, **kwargs: True)
    monkeypatch.setattr(
        deploy,
        "deploy_helm",
        lambda config, args, **kwargs: events.append(("helm", list(args))),
    )
    monkeypatch.setattr(
        deploy,
        "wait_for_rollout",
        lambda config, include_web, **kwargs: events.append(("wait", include_web)),
    )
    monkeypatch.setattr(deploy, "run_migrations", lambda *args, **kwargs: None)
    monkeypatch.setattr(deploy, "create_api_token", lambda *args, **kwargs: True)
    monkeypatch.setattr(deploy, "show_status", lambda *args, **kwargs: None)

    assert deploy.deploy(_deployment_config()) == 0
    assert events == [("helm", helm_args), ("wait", True)]


def test_create_api_token_keeps_token_out_of_process_arguments(monkeypatch):
    """The token is passed to kubectl through a protected patch file."""
    deploy = _load_deploy_module()
    token = "test-token-that-must-not-be-an-argument"
    token_id = "test-token-id"
    secret_checks = iter(["", base64.b64encode(token.encode()).decode()])

    def command_output(command, dry_run=False):
        if "jsonpath" in command:
            return next(secret_checks)
        if " token create " in command:
            return f"Token: {token}\nToken ID: {token_id}"
        raise AssertionError(f"unexpected command: {command}")

    calls = []

    def run_process(args, check):
        calls.append(list(args))
        patch_path = Path(args[-1])
        assert patch_path.stat().st_mode & 0o777 == 0o600
        assert base64.b64encode(token.encode()).decode() in patch_path.read_text()
        return subprocess.CompletedProcess(args=args, returncode=0)

    monkeypatch.setattr(deploy, "run_cmd_output", command_output)
    monkeypatch.setattr(deploy.subprocess, "run", run_process)
    monkeypatch.delenv("REACTORCIDE_SECRETS_SOCKET", raising=False)

    assert deploy.create_api_token(_deployment_config())
    assert len(calls) == 1
    process_arguments = " ".join(calls[0])
    assert token not in process_arguments
    assert base64.b64encode(token.encode()).decode() not in process_arguments
    assert not Path(calls[0][-1]).exists()
