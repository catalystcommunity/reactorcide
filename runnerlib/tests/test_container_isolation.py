"""Test container isolation features."""

import os
import pytest
import tempfile
from pathlib import Path

from src.config import RunnerConfig
from src.container import build_docker_command
from src.source_prep import prepare_job_directory


class TestContainerIsolation:
    """Test container isolation and command building."""

    def test_build_docker_command_with_job_isolation(self):
        """Test that docker command correctly mounts job directory."""
        config = RunnerConfig(
            code_dir="/job/src",
            job_dir="/job/src",
            job_command="echo test",
            runner_image="alpine:latest"
        )

        job_path = Path("/tmp/job-123")
        env_vars = {
            "REACTORCIDE_CODE_DIR": "/job/src",
            "REACTORCIDE_JOB_DIR": "/job/src"
        }

        cmd = build_docker_command(config, job_path, env_vars)

        # Check that the command includes the correct mount
        assert "-v" in cmd
        mount_idx = cmd.index("-v")
        mount_value = cmd[mount_idx + 1]
        assert mount_value == f"{job_path}:/job"

        # Check working directory
        assert "-w" in cmd
        work_idx = cmd.index("-w")
        work_value = cmd[work_idx + 1]
        assert work_value == "/job/src"

        # Check image
        assert "alpine:latest" in cmd

        # Check command
        assert "echo" in cmd
        assert "test" in cmd

    def test_build_docker_command_with_socket_mount(self):
        """Test that docker command mounts /tmp when socket is present."""
        config = RunnerConfig(
            code_dir="/job/src",
            job_dir="/job/src",
            job_command="echo test",
            runner_image="alpine:latest"
        )

        job_path = Path("/tmp/job-456")

        # Create a temporary socket file
        with tempfile.NamedTemporaryFile(suffix=".sock", delete=False) as tmp:
            socket_path = tmp.name

        try:
            env_vars = {
                "REACTORCIDE_CODE_DIR": "/job/src",
                "REACTORCIDE_JOB_DIR": "/job/src",
                "REACTORCIDE_SECRETS_SOCKET": socket_path
            }

            cmd = build_docker_command(config, job_path, env_vars)

            # Check that /tmp is mounted
            mount_args = []
            for i, arg in enumerate(cmd):
                if arg == "-v" and i + 1 < len(cmd):
                    mount_args.append(cmd[i + 1])

            # Should have both job mount and tmp mount
            assert f"{job_path}:/job" in mount_args
            assert "/tmp:/tmp" in mount_args

            # Check socket env var is passed
            env_args = []
            for i, arg in enumerate(cmd):
                if arg == "-e" and i + 1 < len(cmd):
                    env_args.append(cmd[i + 1])

            assert f"REACTORCIDE_SECRETS_SOCKET={socket_path}" in env_args

        finally:
            # Clean up
            if os.path.exists(socket_path):
                os.unlink(socket_path)

    def test_different_jobs_get_different_paths(self):
        """Test that different jobs use different work directories."""
        with tempfile.TemporaryDirectory() as work_dir1:
            with tempfile.TemporaryDirectory() as work_dir2:
                config = RunnerConfig(
                    code_dir="/job/src",
                    job_dir="/job/src",
                    job_command="echo test",
                    runner_image="alpine:latest"
                )

                # Job 1
                job_path1 = Path(work_dir1) / "job"
                job_path1.mkdir(parents=True, exist_ok=True)
                env_vars1 = {"JOB_ID": "job1"}

                cmd1 = build_docker_command(config, job_path1, env_vars1)

                # Job 2
                job_path2 = Path(work_dir2) / "job"
                job_path2.mkdir(parents=True, exist_ok=True)
                env_vars2 = {"JOB_ID": "job2"}

                cmd2 = build_docker_command(config, job_path2, env_vars2)

                # Extract mount paths
                def get_mount_path(cmd):
                    for i, arg in enumerate(cmd):
                        if arg == "-v" and i + 1 < len(cmd):
                            mount = cmd[i + 1]
                            if ":/job" in mount:
                                return mount.split(":/job")[0]
                    return None

                mount1 = get_mount_path(cmd1)
                mount2 = get_mount_path(cmd2)

                # Different jobs should have different mount paths
                assert mount1 != mount2
                assert str(job_path1) == mount1
                assert str(job_path2) == mount2

    def test_work_directory_isolation_with_prepare(self):
        """Test that prepare_job_directory respects work directory changes."""
        with tempfile.TemporaryDirectory() as work_dir1:
            with tempfile.TemporaryDirectory() as work_dir2:
                original_cwd = os.getcwd()

                try:
                    # Prepare job 1
                    os.chdir(work_dir1)
                    config1 = RunnerConfig(
                        code_dir="/job/src",
                        job_dir="/job/src",
                        job_command="echo job1",
                        runner_image="alpine:latest"
                    )
                    job_path1 = prepare_job_directory(config1)
                    assert job_path1.exists()
                    assert str(job_path1).startswith(work_dir1)

                    # Create a test file
                    (job_path1 / "job1.txt").write_text("job1 data")

                    # Prepare job 2
                    os.chdir(work_dir2)
                    config2 = RunnerConfig(
                        code_dir="/job/src",
                        job_dir="/job/src",
                        job_command="echo job2",
                        runner_image="alpine:latest"
                    )
                    job_path2 = prepare_job_directory(config2)
                    assert job_path2.exists()
                    assert str(job_path2).startswith(work_dir2)

                    # Create a test file
                    (job_path2 / "job2.txt").write_text("job2 data")

                    # Verify isolation
                    assert job_path1 != job_path2
                    assert (job_path1 / "job1.txt").exists()
                    assert not (job_path1 / "job2.txt").exists()
                    assert (job_path2 / "job2.txt").exists()
                    assert not (job_path2 / "job1.txt").exists()

                finally:
                    os.chdir(original_cwd)


if __name__ == "__main__":
    pytest.main([__file__, "-v"])