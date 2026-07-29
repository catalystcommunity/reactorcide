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
