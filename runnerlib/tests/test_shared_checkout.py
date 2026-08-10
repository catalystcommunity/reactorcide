import subprocess
from pathlib import Path

from src.config import RunnerConfig
from src.source_prep import prepare_shared_eval_sources
from src.workflow import changed_files


def _git(path: Path, *args: str) -> str:
    result = subprocess.run(
        ["git", *args], cwd=path, check=True, capture_output=True, text=True
    )
    return result.stdout.strip()


def _commit(path: Path, message: str) -> str:
    _git(path, "add", ".")
    _git(path, "commit", "-m", message)
    return _git(path, "rev-parse", "HEAD")


def test_shared_checkout_uses_one_object_store_and_small_ci_views(tmp_path, monkeypatch):
    base_repo = tmp_path / "base-repo"
    base_repo.mkdir()
    _git(base_repo, "init")
    _git(base_repo, "config", "user.email", "test@example.invalid")
    _git(base_repo, "config", "user.name", "Test User")
    policy = base_repo / ".reactorcide" / "policy.yaml"
    workflow = base_repo / ".reactorcide" / "workflows" / "test.yaml"
    workflow.parent.mkdir(parents=True)
    policy.write_text("version: 1\ndefaults:\n  ci_source: base\n  profile: standard\n")
    workflow.write_text("id: tests\nname: Base tests\n")
    (base_repo / "large-source.bin").write_bytes(b"x" * 1024 * 1024)
    base_sha = _commit(base_repo, "base")

    head_repo = tmp_path / "head-repo"
    _git(tmp_path, "clone", str(base_repo), str(head_repo))
    _git(head_repo, "config", "user.email", "test@example.invalid")
    _git(head_repo, "config", "user.name", "Test User")
    (head_repo / ".reactorcide" / "workflows" / "test.yaml").write_text(
        "id: tests\nname: Head tests\n"
    )
    (head_repo / "application.txt").write_text("head source\n")
    head_sha = _commit(head_repo, "head")

    monkeypatch.chdir(tmp_path)
    monkeypatch.setenv("REACTORCIDE_IN_CONTAINER", "false")
    monkeypatch.setenv("REACTORCIDE_PR_NUMBER", "7")
    monkeypatch.setenv("REACTORCIDE_JOB_KIND", "eval")
    config = RunnerConfig(
        code_dir="/job/src",
        job_dir="/job/src",
        job_command="runnerlib eval",
        runner_image="runner",
        source_type="git",
        source_url=str(head_repo),
        source_ref=head_sha,
        ci_source_type="git",
        ci_source_url=str(base_repo),
        ci_source_ref=base_sha,
        checkout_mode="shared",
    )

    prepared = prepare_shared_eval_sources(config)
    assert prepared is not None
    source_path, base_path = prepared
    head_path = tmp_path / "job" / "ci" / "head"

    assert (source_path / ".git").is_dir()
    assert _git(source_path, "rev-parse", "--is-shallow-repository") == "false"
    assert not (source_path / "large-source.bin").exists()
    assert not (base_path / ".git").exists()
    assert not (head_path / ".git").exists()
    assert "Base tests" in (base_path / ".reactorcide" / "workflows" / "test.yaml").read_text()
    assert "Head tests" in (head_path / ".reactorcide" / "workflows" / "test.yaml").read_text()
    assert "application.txt" in changed_files(base_sha, head_sha, str(source_path))
    assert len(list((tmp_path / "job").rglob(".git"))) == 1
