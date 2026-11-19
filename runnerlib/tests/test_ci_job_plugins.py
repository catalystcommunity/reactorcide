"""Tests for repository CI lifecycle plugins and plugin discovery."""

import importlib.util
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import Mock

import pytest

from src.cli import discover_run_plugin_directories
from src.plugins import PluginContext, PluginPhase


REPOSITORY_ROOT = Path(__file__).resolve().parents[2]


def _load_repository_plugin(name: str):
    path = REPOSITORY_ROOT / ".reactorcide" / "plugins" / name
    spec = importlib.util.spec_from_file_location(path.stem, path)
    module = importlib.util.module_from_spec(spec)
    assert spec.loader is not None
    spec.loader.exec_module(module)
    return module


@pytest.fixture(autouse=True)
def _clean_plugin_trust_env(monkeypatch):
    for name in (
        "REACTORCIDE_CI_SOURCE_DIR",
        "REACTORCIDE_CI_SOURCE_TYPE",
        "REACTORCIDE_IS_FORK_PR",
        "REACTORCIDE_PLUGIN_DIR",
    ):
        monkeypatch.delenv(name, raising=False)


def _make_plugins(root: Path) -> Path:
    plugin_dir = root / ".reactorcide" / "plugins"
    plugin_dir.mkdir(parents=True)
    return plugin_dir


def test_fork_pr_with_trusted_plugins_loads_only_trusted(tmp_path, monkeypatch):
    ci_plugins = _make_plugins(tmp_path / "ci")
    source_plugins = _make_plugins(tmp_path / "source")
    monkeypatch.setenv("REACTORCIDE_IS_FORK_PR", "true")
    monkeypatch.setenv("REACTORCIDE_CI_SOURCE_DIR", str(tmp_path / "ci"))
    config = SimpleNamespace(code_dir=str(tmp_path / "source"))

    result = discover_run_plugin_directories(
        config, ci_source_path=tmp_path / "ci", source_path=tmp_path / "source"
    )

    assert result == [str(ci_plugins)]
    assert str(source_plugins) not in result


def test_fork_pr_without_trusted_plugins_loads_no_plugins(tmp_path, monkeypatch):
    (tmp_path / "ci").mkdir()
    _make_plugins(tmp_path / "source")
    monkeypatch.setenv("REACTORCIDE_IS_FORK_PR", "true")
    monkeypatch.setenv("REACTORCIDE_CI_SOURCE_DIR", str(tmp_path / "ci"))
    config = SimpleNamespace(code_dir=str(tmp_path / "source"))

    result = discover_run_plugin_directories(
        config, ci_source_path=tmp_path / "ci", source_path=tmp_path / "source"
    )

    assert result == []


def test_separate_ci_tree_without_plugins_never_falls_back_to_source(tmp_path):
    (tmp_path / "ci").mkdir()
    _make_plugins(tmp_path / "source")
    config = SimpleNamespace(code_dir=str(tmp_path / "source"))

    result = discover_run_plugin_directories(
        config, ci_source_path=tmp_path / "ci", source_path=tmp_path / "source"
    )

    assert result == []


def test_ci_source_dir_env_without_plugins_never_falls_back(tmp_path, monkeypatch):
    # Pre-mounted layout (worker or run-local): no prepared CI checkout, but
    # the job has a CI tree at REACTORCIDE_CI_SOURCE_DIR.
    (tmp_path / "ci").mkdir()
    _make_plugins(tmp_path / "source")
    monkeypatch.setenv("REACTORCIDE_CI_SOURCE_DIR", str(tmp_path / "ci"))
    config = SimpleNamespace(code_dir=str(tmp_path / "source"))

    assert discover_run_plugin_directories(config) == []


def test_fork_pr_without_ci_tree_loads_no_source_plugins(tmp_path, monkeypatch):
    _make_plugins(tmp_path / "source")
    monkeypatch.setenv("REACTORCIDE_IS_FORK_PR", "true")
    config = SimpleNamespace(code_dir=str(tmp_path / "source"))

    assert discover_run_plugin_directories(config) == []


def test_untrusted_run_refuses_plugin_dir_inside_source(tmp_path, monkeypatch):
    (tmp_path / "ci").mkdir()
    source_plugins = _make_plugins(tmp_path / "source")
    monkeypatch.setenv("REACTORCIDE_PLUGIN_DIR", str(source_plugins))
    config = SimpleNamespace(code_dir=str(tmp_path / "source"))

    result = discover_run_plugin_directories(
        config,
        ci_source_path=tmp_path / "ci",
        source_path=tmp_path / "source",
        explicit_plugin_dir=str(source_plugins),
    )

    assert result == []


def test_untrusted_run_keeps_plugin_dir_outside_source(tmp_path):
    (tmp_path / "ci").mkdir()
    _make_plugins(tmp_path / "source")
    other = tmp_path / "image-plugins"
    other.mkdir()
    config = SimpleNamespace(code_dir=str(tmp_path / "source"))

    result = discover_run_plugin_directories(
        config,
        ci_source_path=tmp_path / "ci",
        source_path=tmp_path / "source",
        explicit_plugin_dir=str(other),
    )

    assert result == [str(other)]


def test_discovers_plugins_from_pre_mounted_code_directory(tmp_path):
    code_dir = tmp_path / "source"
    plugin_dir = code_dir / ".reactorcide" / "plugins"
    plugin_dir.mkdir(parents=True)
    config = SimpleNamespace(code_dir=str(code_dir))

    result = discover_run_plugin_directories(config)

    assert result == [str(plugin_dir)]


def test_trusted_ci_plugins_replace_application_plugins(tmp_path):
    ci_dir = tmp_path / "ci"
    ci_plugin_dir = ci_dir / ".reactorcide" / "plugins"
    ci_plugin_dir.mkdir(parents=True)
    source_dir = tmp_path / "source"
    source_plugin_dir = source_dir / ".reactorcide" / "plugins"
    source_plugin_dir.mkdir(parents=True)
    config = SimpleNamespace(code_dir=str(source_dir))

    result = discover_run_plugin_directories(
        config,
        ci_source_path=ci_dir,
        source_path=source_dir,
    )

    assert result == [str(ci_plugin_dir)]
    assert str(source_plugin_dir) not in result


@pytest.mark.parametrize(
    "subject",
    [
        "feat: add lifecycle CI jobs",
        "fix(runnerlib): load mounted plugins",
        "docs!: replace obsolete setup",
        "refactor(worker)!: change the protocol",
    ],
)
def test_conventional_commit_pattern_accepts_valid_subjects(subject):
    plugin = _load_repository_plugin("plugin_ci_jobs.py")

    assert plugin.CONVENTIONAL_COMMIT_PATTERN.fullmatch(subject)


@pytest.mark.parametrize(
    "subject",
    [
        "Add lifecycle CI jobs",
        "feature: use an unsupported type",
        "fix missing separator",
        "fix:",
    ],
)
def test_conventional_commit_pattern_rejects_invalid_subjects(subject):
    plugin = _load_repository_plugin("plugin_ci_jobs.py")

    assert not plugin.CONVENTIONAL_COMMIT_PATTERN.fullmatch(subject)


def test_ci_plugin_dispatches_selected_job(monkeypatch, tmp_path):
    plugin_module = _load_repository_plugin("plugin_ci_jobs.py")
    selected_job = Mock()
    monkeypatch.setitem(plugin_module.CI_JOBS, "test-go", selected_job)
    monkeypatch.setenv("REACTORCIDE_CI_JOB", "test-go")
    context = PluginContext(
        config=SimpleNamespace(code_dir=str(tmp_path)),
        phase=PluginPhase.POST_SOURCE_PREP,
    )

    plugin_module.ReactorcideCIJobsPlugin().execute(context)

    selected_job.assert_called_once_with(tmp_path)


def test_ci_plugin_is_inactive_without_job_selector(monkeypatch, tmp_path):
    plugin_module = _load_repository_plugin("plugin_ci_jobs.py")
    monkeypatch.delenv("REACTORCIDE_CI_JOB", raising=False)
    context = PluginContext(
        config=SimpleNamespace(code_dir=str(tmp_path)),
        phase=PluginPhase.POST_SOURCE_PREP,
    )

    plugin_module.ReactorcideCIJobsPlugin().execute(context)


def test_k8s_tools_plugin_requires_explicit_request(monkeypatch):
    plugin_module = _load_repository_plugin("plugin_k8s_tools.py")
    install_tools = Mock()
    monkeypatch.setattr(plugin_module, "install_tools", install_tools)
    monkeypatch.delenv("REACTORCIDE_INSTALL_K8S_TOOLS", raising=False)
    context = PluginContext(
        config=SimpleNamespace(code_dir="/job/src"),
        phase=PluginPhase.POST_SOURCE_PREP,
    )
    plugin = plugin_module.K8sToolsPlugin()

    plugin.execute(context)
    install_tools.assert_not_called()

    monkeypatch.setenv("REACTORCIDE_INSTALL_K8S_TOOLS", "true")
    plugin.execute(context)
    install_tools.assert_called_once_with()


def _write_generate_scripts(code_dir, worker_release, ui_release):
    scripts = code_dir / "scripts"
    scripts.mkdir(parents=True)
    (scripts / "generate-csil-worker.sh").write_text(
        f'#!/usr/bin/env bash\nCSILGEN_RELEASE="{worker_release}"\n'
    )
    (scripts / "generate-csil-ui.sh").write_text(
        f'#!/usr/bin/env bash\nCSILGEN_RELEASE="{ui_release}"\n'
    )


def test_csilgen_release_reads_the_shared_pin(tmp_path):
    module = _load_repository_plugin("plugin_ci_jobs.py")
    _write_generate_scripts(tmp_path, "csilgen/v0.2.11", "csilgen/v0.2.11")

    assert module._csilgen_release(tmp_path) == "csilgen/v0.2.11"
    assert module.CI_JOBS["csil-gen-check"] is module.csil_gen_check


def test_csilgen_release_rejects_scripts_that_disagree(tmp_path):
    module = _load_repository_plugin("plugin_ci_jobs.py")
    _write_generate_scripts(tmp_path, "csilgen/v0.2.11", "csilgen/v0.2.9")

    with pytest.raises(RuntimeError, match="different CSILGEN_RELEASE"):
        module._csilgen_release(tmp_path)


def test_repository_generate_scripts_share_one_pin():
    module = _load_repository_plugin("plugin_ci_jobs.py")

    assert module._csilgen_release(REPOSITORY_ROOT).startswith("csilgen/v")


def _egress_plugin(monkeypatch, rustc_output):
    module = _load_repository_plugin("plugin_ci_jobs.py")
    commands = []

    def fake_run(command, *, cwd, capture_output=False, env=None):
        commands.append((list(command), Path(cwd)))
        if command[:2] == ["rustc", "--version"]:
            if rustc_output is None:
                raise FileNotFoundError("rustc")
            return SimpleNamespace(stdout=rustc_output, returncode=0)
        return SimpleNamespace(stdout="", returncode=0)

    monkeypatch.setattr(module, "_run", fake_run)
    return module, commands


def _egress_tree(tmp_path):
    (tmp_path / "egress_proxy" / "netd").mkdir(parents=True)
    (tmp_path / "egress_proxy" / "Cargo.toml").write_text("[package]\n")
    return tmp_path


def test_egress_job_runs_netd_and_cargo_tests(monkeypatch, tmp_path):
    module, commands = _egress_plugin(monkeypatch, "rustc 1.100.0 (0ef1bc386 2026-11-09)\n")
    code_dir = _egress_tree(tmp_path)

    module.test_egress(code_dir)

    assert module.CI_JOBS["test-egress"] is module.test_egress
    assert (["go", "test", "./...", "-count=1"], code_dir / "egress_proxy" / "netd") in commands
    assert (["cargo", "test", "--locked"], code_dir / "egress_proxy") in commands
    assert (["rustup", "target", "add", "wasm32-wasip2"], code_dir / "egress_proxy") in commands
    assert not any(command[:2] == ["rustup", "toolchain"] for command, _ in commands)


@pytest.mark.parametrize("rustc_output", ["rustc 1.90.0 (abc 2025-09-01)\n", None])
def test_egress_job_installs_pinned_rust_when_image_rust_is_old(monkeypatch, tmp_path, rustc_output):
    module, commands = _egress_plugin(monkeypatch, rustc_output)
    code_dir = _egress_tree(tmp_path)

    module.test_egress(code_dir)

    pinned = module.EGRESS_RUST_TOOLCHAIN
    assert ["rustup", "toolchain", "install", pinned, "--profile", "minimal"] in [c for c, _ in commands]
    assert (["cargo", f"+{pinned}", "test", "--locked"], code_dir / "egress_proxy") in commands
    assert ["rustup", "target", "add", "wasm32-wasip2", "--toolchain", pinned] in [c for c, _ in commands]


def test_egress_job_fails_without_the_crate(monkeypatch, tmp_path):
    module, _ = _egress_plugin(monkeypatch, "rustc 1.100.0 (x 2026-11-09)\n")

    with pytest.raises(RuntimeError, match="Cargo.toml"):
        module.test_egress(tmp_path)


def test_egress_rust_pin_meets_the_crate_minimum():
    module = _load_repository_plugin("plugin_ci_jobs.py")
    major, minor, _ = (int(part) for part in module.EGRESS_RUST_TOOLCHAIN.split("."))
    assert (major, minor) >= module.EGRESS_RUST_MINIMUM
    dockerfile = (REPOSITORY_ROOT / "egress_proxy" / "Dockerfile").read_text()
    assert f"FROM rust:{module.EGRESS_RUST_TOOLCHAIN}-" in dockerfile


def test_egress_dockerfile_pins_every_base_image():
    dockerfile = (REPOSITORY_ROOT / "egress_proxy" / "Dockerfile").read_text()
    bases = [line.split()[1] for line in dockerfile.splitlines() if line.startswith("FROM ")]
    assert bases
    for base in bases:
        assert "@sha256:" in base, f"{base} has no digest"
        assert len(base.split("@sha256:", 1)[1]) == 64


def test_egress_job_limits_cargo_memory(monkeypatch, tmp_path):
    module = _load_repository_plugin("plugin_ci_jobs.py")
    environments = []

    def fake_run(command, *, cwd, capture_output=False, env=None):
        if command[:2] == ["rustc", "--version"]:
            return SimpleNamespace(stdout="rustc 1.100.0 (x 2026-11-09)\n", returncode=0)
        if command[:2] == ["cargo", "test"]:
            environments.append(env)
        return SimpleNamespace(stdout="", returncode=0)

    monkeypatch.setattr(module, "_run", fake_run)
    monkeypatch.delenv("CARGO_BUILD_JOBS", raising=False)
    monkeypatch.setenv("CARGO_TARGET_DIR", "/elsewhere")
    module.test_egress(_egress_tree(tmp_path))

    assert environments[0]["CARGO_PROFILE_DEV_DEBUG"] == "0"
    assert environments[0]["CARGO_BUILD_JOBS"] == module.EGRESS_CARGO_JOBS
    assert "CARGO_TARGET_DIR" not in environments[0]
