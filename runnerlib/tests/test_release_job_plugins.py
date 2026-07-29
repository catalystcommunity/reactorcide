"""Tests for the repository release lifecycle plugin and workflow."""

import importlib.util
import subprocess
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import Mock

import pytest

from src.eval import load_workflow_definitions
from src.plugins import PluginContext, PluginPhase


REPOSITORY_ROOT = Path(__file__).resolve().parents[2]


def _load_release_plugin():
    path = (
        REPOSITORY_ROOT
        / ".reactorcide"
        / "plugins"
        / "plugin_release_jobs.py"
    )
    spec = importlib.util.spec_from_file_location(path.stem, path)
    module = importlib.util.module_from_spec(spec)
    assert spec.loader is not None
    spec.loader.exec_module(module)
    return module


def test_finds_draft_release_for_source(monkeypatch):
    plugin = _load_release_plugin()
    source_sha = "a" * 40
    matching_release = {
        "id": 7,
        "draft": True,
        "body": plugin._release_marker(source_sha),
    }
    monkeypatch.setattr(
        plugin,
        "_github_request",
        Mock(
            return_value=[
                {"id": 6, "draft": False, "body": ""},
                matching_release,
            ]
        ),
    )

    result = plugin._find_draft_release("owner/repository", source_sha)

    assert result == matching_release


def test_prepare_reuses_existing_draft(monkeypatch, tmp_path):
    plugin = _load_release_plugin()
    monkeypatch.setenv("REACTORCIDE_REPO", "owner/repository")
    monkeypatch.setattr(plugin, "_source_sha", Mock(return_value="b" * 40))
    monkeypatch.setattr(
        plugin,
        "_find_draft_release",
        Mock(return_value={"id": 8, "tag_name": "v1.2.3"}),
    )
    release_metadata = Mock()
    monkeypatch.setattr(plugin, "_release_metadata", release_metadata)

    plugin.prepare_release(tmp_path)

    release_metadata.assert_not_called()


def test_prepare_creates_draft_release(monkeypatch, tmp_path):
    plugin = _load_release_plugin()
    source_sha = "c" * 40
    monkeypatch.setenv("REACTORCIDE_REPO", "owner/repository")
    monkeypatch.setattr(plugin, "_source_sha", Mock(return_value=source_sha))
    monkeypatch.setattr(
        plugin,
        "_find_draft_release",
        Mock(return_value=None),
    )
    monkeypatch.setattr(
        plugin,
        "_release_metadata",
        Mock(return_value={"tag": "v1.2.3", "notes": "fix: release"}),
    )
    github_request = Mock(return_value={"id": 9, "tag_name": "v1.2.3"})
    monkeypatch.setattr(plugin, "_github_request", github_request)

    plugin.prepare_release(tmp_path)

    _, path = github_request.call_args.args
    payload = github_request.call_args.kwargs["payload"]
    assert path == "/repos/owner/repository/releases"
    assert payload["tag_name"] == "v1.2.3"
    assert payload["target_commitish"] == source_sha
    assert payload["draft"] is True
    assert plugin._release_marker(source_sha) in payload["body"]


def test_reads_semver_release_metadata(monkeypatch, tmp_path):
    plugin = _load_release_plugin()
    monkeypatch.setattr(
        plugin,
        "_install_semver_tags",
        Mock(return_value=Path("/tmp/semver-tags")),
    )
    monkeypatch.setattr(
        plugin,
        "_run",
        Mock(
            return_value=subprocess.CompletedProcess(
                args=[],
                returncode=0,
                stdout=(
                    'status message\n'
                    '{"New_release_published":"true",'
                    '"New_release_git_tag":"v1.2.3",'
                    '"New_release_notes":"fix: release"}\n'
                ),
                stderr="",
            )
        ),
    )

    metadata = plugin._release_metadata(tmp_path)

    assert metadata == {"tag": "v1.2.3", "notes": "fix: release"}


def test_go_environment_sets_writable_cross_compile_cache(monkeypatch):
    plugin = _load_release_plugin()
    monkeypatch.setenv("HOME", "/home/runner")
    monkeypatch.setenv("GITHUB_PAT", "github-secret")
    monkeypatch.setenv("REGISTRY_USER", "registry-user")
    monkeypatch.setenv("REGISTRY_PASSWORD", "registry-secret")

    environment = plugin._go_environment("windows", "arm64")

    assert environment["GOPATH"] == "/home/runner/.cache/go"
    assert environment["GOCACHE"] == "/home/runner/.cache/go-build"
    assert environment["GOMODCACHE"] == "/home/runner/.cache/go-mod"
    assert environment["CGO_ENABLED"] == "0"
    assert environment["GOOS"] == "windows"
    assert environment["GOARCH"] == "arm64"
    assert "GITHUB_PAT" not in environment
    assert "REGISTRY_USER" not in environment
    assert "REGISTRY_PASSWORD" not in environment


def test_publish_requires_all_platform_assets(monkeypatch, tmp_path):
    plugin = _load_release_plugin()
    monkeypatch.setenv("REACTORCIDE_REPO", "owner/repository")
    monkeypatch.setattr(
        plugin,
        "_release_for_source",
        Mock(return_value={"id": 10, "tag_name": "v1.2.3"}),
    )
    monkeypatch.setattr(plugin, "_github_request", Mock(return_value=[]))

    with pytest.raises(RuntimeError, match="missing required assets"):
        plugin.publish_release(tmp_path)


def test_publish_updates_complete_draft(monkeypatch, tmp_path):
    plugin = _load_release_plugin()
    monkeypatch.setenv("REACTORCIDE_REPO", "owner/repository")
    release = {"id": 11, "tag_name": "v1.2.3"}
    monkeypatch.setattr(
        plugin,
        "_release_for_source",
        Mock(return_value=release),
    )
    assets = [
        {"name": name}
        for name in plugin._expected_asset_names("v1.2.3")
    ]
    github_request = Mock(
        side_effect=[assets, {"id": 11, "tag_name": "v1.2.3"}]
    )
    monkeypatch.setattr(plugin, "_github_request", github_request)

    plugin.publish_release(tmp_path)

    method, path = github_request.call_args.args
    assert method == "PATCH"
    assert path == "/repos/owner/repository/releases/11"
    assert github_request.call_args.kwargs["payload"] == {"draft": False}


@pytest.mark.parametrize(
    "value",
    [
        "linux/386",
        "freebsd/amd64",
        "linux",
        "../linux/amd64",
    ],
)
def test_rejects_unsupported_release_platform(monkeypatch, value):
    plugin = _load_release_plugin()
    monkeypatch.setenv("REACTORCIDE_RELEASE_PLATFORM", value)

    with pytest.raises(RuntimeError, match="Unsupported release platform"):
        plugin._platform()


def test_release_plugin_dispatches_selected_job(monkeypatch, tmp_path):
    plugin = _load_release_plugin()
    selected_job = Mock()
    monkeypatch.setitem(plugin.RELEASE_JOBS, "prepare", selected_job)
    monkeypatch.setenv("REACTORCIDE_RELEASE_JOB", "prepare")
    context = PluginContext(
        config=SimpleNamespace(code_dir=str(tmp_path)),
        phase=PluginPhase.POST_SOURCE_PREP,
    )

    plugin.ReactorcideReleaseJobsPlugin().execute(context)

    selected_job.assert_called_once_with(tmp_path)


def test_release_workflow_fans_out_and_joins():
    workflows = load_workflow_definitions(REPOSITORY_ROOT)
    release = next(
        workflow
        for workflow in workflows
        if workflow.name == "Reactorcide Release"
    )
    jobs = {job.name: job for job in release.jobs}
    fanout_names = {
        "image-runnerbase",
        "images-coordinator-worker",
        "image-web",
        "cli-linux-amd64",
        "cli-linux-arm64",
        "cli-darwin-amd64",
        "cli-darwin-arm64",
        "cli-windows-amd64",
        "cli-windows-arm64",
    }

    assert set(jobs) == {"prepare", "publish", *fanout_names}
    for name in fanout_names:
        assert jobs[name].job.depends_on == ["prepare"]
    assert set(jobs["publish"].job.depends_on) == fanout_names
    assert jobs["prepare"].environment["REACTORCIDE_RELEASE_JOB"] == "prepare"
    assert jobs["publish"].environment["REACTORCIDE_RELEASE_JOB"] == "publish"
    assert (
        jobs["cli-darwin-arm64"].environment[
            "REACTORCIDE_RELEASE_PLATFORM"
        ]
        == "darwin/arm64"
    )
    assert jobs["image-runnerbase"].job.capabilities == ["builder"]
