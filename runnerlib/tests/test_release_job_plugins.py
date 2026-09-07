"""Tests for the repository release lifecycle plugin and workflow."""

import importlib.util
import subprocess
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import Mock

import pytest

from src.eval import evaluate_workflows, load_workflow_definitions
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


def test_tag_release_reuses_draft_and_pushes_planned_tag(
    monkeypatch,
    tmp_path,
):
    plugin = _load_release_plugin()
    monkeypatch.setenv("REACTORCIDE_REPO", "owner/repository")
    monkeypatch.setattr(plugin, "_source_sha", Mock(return_value="b" * 40))
    monkeypatch.setattr(
        plugin,
        "_find_draft_release",
        Mock(return_value={"id": 8, "tag_name": "v1.2.3"}),
    )
    release_metadata = Mock(
        side_effect=[
            {"tag": "v1.2.3", "notes": "fix: release"},
            {"tag": "v1.2.3", "notes": "fix: release"},
        ]
    )
    monkeypatch.setattr(plugin, "_release_metadata", release_metadata)

    plugin.tag_release(tmp_path)

    assert release_metadata.call_args_list[0].kwargs == {}
    assert release_metadata.call_args_list[1].kwargs == {"push": True}


def test_tag_release_creates_draft_before_push(monkeypatch, tmp_path):
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
        Mock(
            side_effect=[
                {"tag": "v1.2.3", "notes": "fix: release"},
                {"tag": "v1.2.3", "notes": "fix: release"},
            ]
        ),
    )
    github_request = Mock(return_value={"id": 9, "tag_name": "v1.2.3"})
    monkeypatch.setattr(plugin, "_github_request", github_request)

    plugin.tag_release(tmp_path)

    _, path = github_request.call_args.args
    payload = github_request.call_args.kwargs["payload"]
    assert path == "/repos/owner/repository/releases"
    assert payload["tag_name"] == "v1.2.3"
    assert payload["target_commitish"] == source_sha
    assert payload["draft"] is True
    assert plugin._release_marker(source_sha) in payload["body"]


def test_prepare_verifies_matching_ci_draft_and_tag(monkeypatch, tmp_path):
    plugin = _load_release_plugin()
    source_sha = "d" * 40
    monkeypatch.setenv("REACTORCIDE_REPO", "owner/repository")
    monkeypatch.setenv("REACTORCIDE_EVENT_TYPE", "tag_created")
    monkeypatch.setenv("REACTORCIDE_BRANCH", "v1.2.3")
    monkeypatch.setattr(plugin, "_source_sha", Mock(return_value=source_sha))
    monkeypatch.setattr(
        plugin,
        "_release_for_source",
        Mock(return_value={"id": 10, "tag_name": "v1.2.3"}),
    )
    monkeypatch.setattr(
        plugin,
        "_run",
        Mock(
            return_value=subprocess.CompletedProcess(
                args=[],
                returncode=0,
                stdout=source_sha + "\n",
                stderr="",
            )
        ),
    )

    plugin.prepare_release(tmp_path)


def test_prepare_rejects_tag_without_ci_draft(monkeypatch, tmp_path):
    plugin = _load_release_plugin()
    monkeypatch.setenv("REACTORCIDE_REPO", "owner/repository")
    monkeypatch.setenv("REACTORCIDE_EVENT_TYPE", "tag_created")
    monkeypatch.setenv("REACTORCIDE_BRANCH", "v1.2.3")
    monkeypatch.setattr(plugin, "_source_sha", Mock(return_value="e" * 40))
    monkeypatch.setattr(plugin, "_release_for_source", Mock(return_value=None))

    with pytest.raises(RuntimeError, match="No CI-created draft"):
        plugin.prepare_release(tmp_path)


def test_reads_semver_release_metadata_in_dry_run(monkeypatch, tmp_path):
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
    command = plugin._run.call_args.args[0]
    assert command[-1] == "--dry_run"


def test_semver_push_uses_git_push_environment(monkeypatch, tmp_path):
    plugin = _load_release_plugin()
    monkeypatch.setattr(
        plugin,
        "_install_semver_tags",
        Mock(return_value=Path("/tmp/semver-tags")),
    )
    push_environment = {"GIT_ASKPASS": "/tmp/askpass"}
    monkeypatch.setattr(
        plugin,
        "_git_push_environment",
        Mock(return_value=push_environment),
    )
    monkeypatch.setattr(
        plugin,
        "_run",
        Mock(
            return_value=subprocess.CompletedProcess(
                args=[],
                returncode=0,
                stdout=(
                    '{"New_release_published":"true",'
                    '"New_release_git_tag":"v1.2.3",'
                    '"New_release_notes":"fix: release"}\n'
                ),
                stderr="",
            )
        ),
    )

    metadata = plugin._release_metadata(tmp_path, push=True)

    assert metadata["tag"] == "v1.2.3"
    command = plugin._run.call_args.args[0]
    assert "--dry_run" not in command
    assert plugin._run.call_args.kwargs["env"] is push_environment


def test_git_push_environment_uses_askpass_without_embedding_token(
    monkeypatch,
    tmp_path,
):
    plugin = _load_release_plugin()
    token = "github-secret-value"
    monkeypatch.setenv("REACTORCIDE_REPO", "owner/repository")
    monkeypatch.setenv("GITHUB_PAT", token)
    monkeypatch.setenv("GIT_CONFIG_GLOBAL", "/job/vcs-auth/gitconfig")
    monkeypatch.setattr(
        plugin,
        "_run",
        Mock(
            return_value=subprocess.CompletedProcess(
                args=[],
                returncode=0,
                stdout="https://github.com/owner/repository.git\n",
                stderr="",
            )
        ),
    )

    environment = plugin._git_push_environment(tmp_path)

    assert "GITHUB_PAT" not in environment
    assert "GIT_CONFIG_GLOBAL" not in environment
    assert environment["GIT_CONFIG_KEY_0"] == "credential.helper"
    assert environment["GIT_CONFIG_VALUE_0"] == ""
    assert environment["REACTORCIDE_GIT_PASSWORD"] == token
    askpass = Path(environment["GIT_ASKPASS"])
    assert token not in askpass.read_text(encoding="utf-8")


def test_git_push_environment_rejects_a_different_origin(
    monkeypatch,
    tmp_path,
):
    plugin = _load_release_plugin()
    monkeypatch.setenv("REACTORCIDE_REPO", "owner/repository")
    monkeypatch.setenv("GITHUB_PAT", "github-secret-value")
    monkeypatch.setattr(
        plugin,
        "_run",
        Mock(
            return_value=subprocess.CompletedProcess(
                args=[],
                returncode=0,
                stdout="https://github.com/other/repository.git\n",
                stderr="",
            )
        ),
    )

    with pytest.raises(RuntimeError, match="configured GitHub repository"):
        plugin._git_push_environment(tmp_path)


def test_semver_failure_masks_github_token(monkeypatch):
    plugin = _load_release_plugin()
    token = "github-secret-value"
    monkeypatch.setenv("GITHUB_PAT", token)
    error = subprocess.CalledProcessError(
        1,
        ["semver-tags"],
        output=f"push failed for {token}",
        stderr="",
    )

    safe_error = plugin._semver_failure(error)

    assert token not in str(safe_error)
    assert "***" in str(safe_error)


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
    release_tag = next(
        workflow
        for workflow in workflows
        if workflow.name == "Reactorcide Release Tag"
    )
    release = next(
        workflow
        for workflow in workflows
        if workflow.name == "Reactorcide Release"
    )
    assert release_tag.triggers.events == ["pull_request_merged"]
    assert release_tag.triggers.branches == ["main"]
    assert [job.name for job in release_tag.jobs] == ["tag"]
    assert (
        release_tag.jobs[0].environment["REACTORCIDE_RELEASE_JOB"]
        == "tag"
    )
    assert release.triggers.events == ["tag_created"]
    assert release.triggers.branches == ["v*"]
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


def test_release_workflows_match_only_merge_and_release_tag_events():
    workflows = load_workflow_definitions(REPOSITORY_ROOT)

    merged = evaluate_workflows(
        workflows,
        "pull_request_merged",
        "main",
    )
    assert [workflow.name for workflow in merged] == [
        "Reactorcide Release Tag"
    ]

    tagged = evaluate_workflows(workflows, "tag_created", "v1.2.3")
    assert [workflow.name for workflow in tagged] == [
        "Reactorcide Release"
    ]

    assert evaluate_workflows(
        workflows,
        "tag_created",
        "not-a-release",
    ) == []
    assert evaluate_workflows(workflows, "push", "main") == []


# --- Release version stamping ------------------------------------------------
#
# A binary that reports the wrong version, or no version, still builds and runs.
# Nothing notices until somebody asks a shipped binary what it is, which is why
# the release job asserts on it rather than trusting the build.


@pytest.mark.parametrize(
    ("tag", "expected"),
    [
        ("v1.2.3", "1.2.3"),
        ("v0.0.1", "0.0.1"),
        ("v1.2.3-rc.1", "1.2.3-rc.1"),
        ("v10.20.30", "10.20.30"),
    ],
)
def test_release_version_strips_the_tag_prefix(tag, expected):
    plugin = _load_release_plugin()

    assert plugin._release_version(tag) == expected


@pytest.mark.parametrize(
    "tag",
    [
        "latest",
        "1.2.3",
        # The version is spliced into a linker flag. _run never uses a shell,
        # so there is no shell injection here, but whitespace would still split
        # into extra -ldflags tokens and hand the linker arguments nobody wrote.
        "v1.2.3 -X main.Version=evil",
        "v1.2.3\t-s",
    ],
)
def test_release_version_rejects_anything_that_is_not_a_release_tag(tag):
    plugin = _load_release_plugin()

    with pytest.raises(RuntimeError, match="Not a release tag"):
        plugin._release_version(tag)


def test_version_ldflags_stamps_the_version_and_keeps_the_strip_flags():
    plugin = _load_release_plugin()

    assert plugin._version_ldflags("1.2.3") == "-ldflags=-X main.Version=1.2.3 -w -s"


@pytest.mark.parametrize(
    ("setting", "expected"),
    [
        # go version -m quotes the whole value. Without unwrapping it, shlex
        # returns the entire flag string as one token and every comparison
        # against it fails -- on correct builds.
        ('build\t-ldflags="-X main.Version=1.2.3 -w -s"', ["main.Version=1.2.3"]),
        ('build\t-ldflags="-X=main.Version=1.2.3"', ["main.Version=1.2.3"]),
        ('build\t-ldflags="-Xmain.Version=1.2.3"', ["main.Version=1.2.3"]),
        ('build\t-ldflags="-w -s"', []),
        # -trimpath makes Go omit -ldflags from the build settings entirely,
        # because linker flags can contain local paths. That is absence of
        # evidence, not a failure, so it is reported as None.
        ("build\t-trimpath=true", None),
        ("", None),
    ],
)
def test_linked_version_assignments_reads_every_x_form(setting, expected):
    plugin = _load_release_plugin()

    assert plugin._linked_version_assignments(f"\t{setting}\n") == expected


def _stub_go_version(plugin, monkeypatch, *, stdout, on_execute=None):
    """Replace _run so the version check sees a chosen `go version -m` result.

    Executing the binary returns on_execute, defaulting to the correct output.
    """

    def fake_run(command, *, cwd, env=None, capture_output=False):
        if command[:3] == ["go", "version", "-m"]:
            return subprocess.CompletedProcess(command, 0, stdout=stdout, stderr="")
        if command[0] == "go" and command[1] == "build":
            return subprocess.CompletedProcess(command, 0, stdout="", stderr="")
        return subprocess.CompletedProcess(
            command,
            0,
            stdout=on_execute if on_execute is not None else "reactorcide 1.2.3\n",
            stderr="",
        )

    monkeypatch.setattr(plugin, "_run", fake_run)


def test_verify_executes_a_native_binary_and_compares_exactly(
    monkeypatch,
    tmp_path,
):
    plugin = _load_release_plugin()
    monkeypatch.setattr(plugin, "_host_platform", lambda: ("linux", "amd64"))
    _stub_go_version(plugin, monkeypatch, stdout="\tbuild\t-trimpath=true\n")
    binary = tmp_path / "reactorcide"
    binary.write_bytes(b"")

    plugin._verify_binary_version(
        binary,
        "1.2.3",
        "linux",
        "amd64",
        module_dir=tmp_path,
        build_args=("-buildvcs=false", "-trimpath"),
    )


@pytest.mark.parametrize(
    "printed",
    [
        # The ldflag never reached the build.
        "reactorcide dev\n",
        # 1.2.30 must not satisfy 1.2.3: the comparison is exact, not a prefix.
        "reactorcide 1.2.30\n",
        # The right version from the wrong binary.
        "reactorcide-web 1.2.3\n",
        # urfave's default printer, which this CLI deliberately replaces.
        "reactorcide version 1.2.3\n",
    ],
)
def test_verify_rejects_a_native_binary_that_reports_something_else(
    monkeypatch,
    tmp_path,
    printed,
):
    plugin = _load_release_plugin()
    monkeypatch.setattr(plugin, "_host_platform", lambda: ("linux", "amd64"))
    _stub_go_version(
        plugin,
        monkeypatch,
        stdout="\tbuild\t-trimpath=true\n",
        on_execute=printed,
    )
    binary = tmp_path / "reactorcide"
    binary.write_bytes(b"")

    with pytest.raises(RuntimeError, match="--version printed"):
        plugin._verify_binary_version(
            binary,
            "1.2.3",
            "linux",
            "amd64",
            module_dir=tmp_path,
            build_args=("-buildvcs=false", "-trimpath"),
        )


@pytest.mark.parametrize(
    "recorded",
    [
        '\tbuild\t-ldflags="-X main.Version=9.9.9 -w -s"\n',
        '\tbuild\t-ldflags="-X main.Version=1.2.30 -w -s"\n',
        '\tbuild\t-ldflags="-w -s"\n',
    ],
)
def test_verify_rejects_an_artifact_whose_recorded_ldflags_are_wrong(
    monkeypatch,
    tmp_path,
    recorded,
):
    plugin = _load_release_plugin()
    monkeypatch.setattr(plugin, "_host_platform", lambda: ("linux", "amd64"))
    _stub_go_version(plugin, monkeypatch, stdout=recorded)
    binary = tmp_path / "reactorcide"
    binary.write_bytes(b"")

    with pytest.raises(RuntimeError, match="was not linked with"):
        plugin._verify_binary_version(
            binary,
            "1.2.3",
            "linux",
            "amd64",
            module_dir=tmp_path,
            build_args=("-buildvcs=false",),
        )


def test_verify_builds_a_host_twin_for_a_target_it_cannot_run(
    monkeypatch,
    tmp_path,
):
    """A cross-compiled artifact cannot be executed, so its flags are.

    An earlier version searched the binary for the version string instead. That
    check does not work: a binary stamped 9.9.9 still contains the bytes of
    1.2.3 somewhere -- one dependency pinned at that version is enough -- so it
    reported success for an artifact carrying the wrong version.
    """
    plugin = _load_release_plugin()
    monkeypatch.setattr(plugin, "_host_platform", lambda: ("linux", "amd64"))
    commands = []

    def fake_run(command, *, cwd, env=None, capture_output=False):
        commands.append((list(command), Path(cwd), dict(env or {})))
        if command[:3] == ["go", "version", "-m"]:
            return subprocess.CompletedProcess(
                command, 0, stdout="\tbuild\t-trimpath=true\n", stderr=""
            )
        return subprocess.CompletedProcess(
            command, 0, stdout="reactorcide 1.2.3\n", stderr=""
        )

    monkeypatch.setattr(plugin, "_run", fake_run)
    binary = tmp_path / "reactorcide.exe"
    binary.write_bytes(b"")

    plugin._verify_binary_version(
        binary,
        "1.2.3",
        "windows",
        "arm64",
        module_dir=tmp_path / "coordinator_api",
        build_args=("-buildvcs=false", "-trimpath"),
    )

    build, _, build_env = next(
        entry for entry in commands if entry[0][:2] == ["go", "build"]
    )
    assert "-ldflags=-X main.Version=1.2.3 -w -s" in build
    assert "-trimpath" in build
    # The twin has to run on THIS machine, so it must NOT inherit the
    # artifact's GOOS/GOARCH -- a windows/arm64 twin could not be executed.
    assert "GOOS" not in build_env
    assert "GOARCH" not in build_env
    twin = next(entry[0] for entry in commands if entry[0][0].endswith("-hostcheck"))
    assert twin[1] == "--version"


def test_cli_asset_build_stamps_and_verifies_the_version(monkeypatch, tmp_path):
    plugin = _load_release_plugin()
    commands = []

    def fake_run(command, *, cwd, env=None, capture_output=False):
        commands.append(list(command))
        if command[:2] == ["go", "build"]:
            Path(command[command.index("-o") + 1]).write_bytes(b"binary")
        return subprocess.CompletedProcess(command, 0, stdout="", stderr="")

    monkeypatch.setattr(plugin, "_run", fake_run)
    verify = Mock()
    monkeypatch.setattr(plugin, "_verify_binary_version", verify)

    plugin._release_asset_path(tmp_path, "1.2.3", "darwin", "arm64")

    build = next(c for c in commands if c[:2] == ["go", "build"])
    assert "-ldflags=-X main.Version=1.2.3 -w -s" in build
    verify.assert_called_once()
    assert verify.call_args.args[1:] == ("1.2.3", "darwin", "arm64")


@pytest.mark.parametrize(
    ("target", "module", "command_name"),
    [
        ("coordinator-worker", "coordinator_api", "reactorcide"),
        ("web", "webapp", "reactorcide-web"),
    ],
)
def test_image_builds_stamp_and_verify_the_version(
    monkeypatch,
    tmp_path,
    target,
    module,
    command_name,
):
    plugin = _load_release_plugin()
    monkeypatch.setenv("REACTORCIDE_RELEASE_IMAGE", target)
    monkeypatch.setenv("REGISTRY_INTERNAL", "registry.example")
    monkeypatch.setattr(
        plugin,
        "_release_for_source",
        Mock(return_value={"id": 1, "tag_name": "v1.2.3"}),
    )
    monkeypatch.setattr(plugin, "_registry_environment", Mock(return_value={}))
    monkeypatch.setattr(plugin, "_install_buildctl", Mock(return_value="buildctl"))
    monkeypatch.setattr(plugin, "_wait_for_buildkit", Mock())
    monkeypatch.setattr(plugin, "_build_and_push", Mock())
    monkeypatch.setattr(plugin, "_build_web_ui", Mock())
    verify = Mock()
    monkeypatch.setattr(plugin, "_verify_binary_version", verify)
    (tmp_path / "deployment").mkdir()
    commands = []

    def fake_run(command, *, cwd, env=None, capture_output=False):
        commands.append((list(command), Path(cwd)))
        if command[:2] == ["go", "build"]:
            Path(command[command.index("-o") + 1]).write_bytes(b"binary")
        return subprocess.CompletedProcess(command, 0, stdout="", stderr="")

    monkeypatch.setattr(plugin, "_run", fake_run)

    plugin.build_release_images(tmp_path)

    build, cwd = next((c, d) for c, d in commands if c[:2] == ["go", "build"])
    assert "-ldflags=-X main.Version=1.2.3 -w -s" in build
    assert cwd == tmp_path / module
    verify.assert_called_once()
    assert verify.call_args.args[1:] == ("1.2.3", "linux", "amd64")
    assert verify.call_args.kwargs["module_dir"] == tmp_path / module
    assert verify.call_args.kwargs.get("command_name", "reactorcide") == command_name
