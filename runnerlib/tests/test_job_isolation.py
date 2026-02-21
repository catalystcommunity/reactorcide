"""Test job isolation with separate work directories."""

import os
import pytest
import tempfile
import shutil
from unittest.mock import patch, MagicMock

from src.config import RunnerConfig
from src.source_prep import prepare_job_directory
from src.container import run_container


class TestJobIsolation:
    """Test job isolation features."""

    def test_work_dir_isolation(self):
        """Test that jobs use separate work directories."""
        with tempfile.TemporaryDirectory() as temp_dir1:
            with tempfile.TemporaryDirectory() as temp_dir2:
                # Change to first temp directory
                original_cwd = os.getcwd()

                try:
                    # Test job 1 in temp_dir1
                    os.chdir(temp_dir1)
                    config1 = RunnerConfig(
                        code_dir="/job/src",
                        job_dir="/job/src",
                        job_command="echo 'job1'",
                        runner_image="alpine:latest"
                    )
                    job_path1 = prepare_job_directory(config1)
                    assert job_path1.exists()
                    assert str(job_path1).startswith(temp_dir1)

                    # Create a test file in job1's directory
                    test_file1 = job_path1 / "test1.txt"
                    test_file1.write_text("job1 data")

                    # Test job 2 in temp_dir2
                    os.chdir(temp_dir2)
                    config2 = RunnerConfig(
                        code_dir="/job/src",
                        job_dir="/job/src",
                        job_command="echo 'job2'",
                        runner_image="alpine:latest"
                    )
                    job_path2 = prepare_job_directory(config2)
                    assert job_path2.exists()
                    assert str(job_path2).startswith(temp_dir2)

                    # Create a test file in job2's directory
                    test_file2 = job_path2 / "test2.txt"
                    test_file2.write_text("job2 data")

                    # Verify isolation - each job has its own directory
                    assert job_path1 != job_path2
                    assert test_file1.exists()
                    assert test_file1.read_text() == "job1 data"
                    assert not (job_path2 / "test1.txt").exists()

                    assert test_file2.exists()
                    assert test_file2.read_text() == "job2 data"
                    assert not (job_path1 / "test2.txt").exists()

                finally:
                    os.chdir(original_cwd)

    def test_concurrent_job_isolation(self):
        """Test that concurrent jobs don't interfere with each other."""
        import threading
        import time

        results = {}
        errors = {}

        def run_job(job_id: str, work_dir: str):
            """Run a job in its own work directory."""
            try:
                original_cwd = os.getcwd()
                os.chdir(work_dir)

                config = RunnerConfig(
                    code_dir="/job/src",
                    job_dir="/job/src",
                    job_command=f"echo 'job-{job_id}'",
                    runner_image="alpine:latest"
                )

                job_path = prepare_job_directory(config)

                # Create a unique file for this job
                test_file = job_path / f"job-{job_id}.txt"
                test_file.write_text(f"Data for job {job_id}")

                # Simulate some work
                time.sleep(0.1)

                # Verify the file still exists and has correct content
                assert test_file.exists()
                assert test_file.read_text() == f"Data for job {job_id}"

                # Check no files from other jobs exist
                other_files = list(job_path.glob("job-*.txt"))
                assert len(other_files) == 1
                assert other_files[0].name == f"job-{job_id}.txt"

                results[job_id] = True

            except Exception as e:
                errors[job_id] = str(e)
            finally:
                os.chdir(original_cwd)

        # Create temporary directories for each job
        temp_dirs = []
        threads = []

        try:
            # Start multiple jobs concurrently
            for i in range(5):
                temp_dir = tempfile.mkdtemp(prefix=f"job-{i}-")
                temp_dirs.append(temp_dir)

                thread = threading.Thread(
                    target=run_job,
                    args=(str(i), temp_dir)
                )
                thread.start()
                threads.append(thread)

            # Wait for all jobs to complete
            for thread in threads:
                thread.join(timeout=5)

            # Verify all jobs succeeded
            assert len(errors) == 0, f"Jobs failed: {errors}"
            assert len(results) == 5
            assert all(results.values())

        finally:
            # Cleanup
            for temp_dir in temp_dirs:
                if os.path.exists(temp_dir):
                    shutil.rmtree(temp_dir)

    def test_socket_isolation(self):
        """Test that secret registration sockets are isolated per job."""
        from src.secrets_server import SecretRegistrationServer
        from src.secrets import SecretMasker

        # Create two secret servers with different PIDs simulated
        masker1 = SecretMasker()
        masker2 = SecretMasker()

        with tempfile.TemporaryDirectory() as temp_dir:
            # Server 1
            socket_path1 = f"{temp_dir}/secrets-job1.sock"
            server1 = SecretRegistrationServer(masker1, socket_path1)

            # Server 2
            socket_path2 = f"{temp_dir}/secrets-job2.sock"
            server2 = SecretRegistrationServer(masker2, socket_path2)

            # Verify different socket paths
            assert server1.socket_path != server2.socket_path
            assert socket_path1 == server1.socket_path
            assert socket_path2 == server2.socket_path

            # Start both servers
            server1.start()
            server2.start()

            try:
                # Register different secrets to each
                import socket
                import json

                # Register to server1
                client1 = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
                client1.connect(socket_path1)
                message1 = {"action": "register", "secrets": ["secret1", "password1"]}
                data1 = json.dumps(message1).encode('utf-8')
                import struct
                client1.sendall(struct.pack('!I', len(data1)))  # Network byte order
                client1.sendall(data1)
                client1.close()

                # Register to server2
                client2 = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
                client2.connect(socket_path2)
                message2 = {"action": "register", "secrets": ["secret2", "password2"]}
                data2 = json.dumps(message2).encode('utf-8')
                client2.sendall(struct.pack('!I', len(data2)))  # Network byte order
                client2.sendall(data2)
                client2.close()

                # Small delay for processing
                import time
                time.sleep(0.1)

                # Verify isolation - each masker only has its own secrets
                # Note: The mask_string method masks complete words that match secrets
                result1 = masker1.mask_string("secret1 password1")
                assert "secret1" not in result1  # secret1 should be masked
                assert "password1" not in result1  # password1 should be masked
                assert masker1.mask_string("secret2 password2") == "secret2 password2"  # Not masked

                result2 = masker2.mask_string("secret2 password2")
                assert "secret2" not in result2  # secret2 should be masked
                assert "password2" not in result2  # password2 should be masked
                assert masker2.mask_string("secret1 password1") == "secret1 password1"  # Not masked

            finally:
                server1.stop()
                server2.stop()

    @patch('shutil.which', return_value='/usr/bin/docker')
    @patch('subprocess.Popen')
    def test_container_mount_isolation(self, mock_popen, mock_which):
        """Test that containers mount only their job's directory."""
        # Mock the Popen object with proper behavior
        mock_process = MagicMock()
        mock_process.poll.side_effect = [None, None, 0]  # Running, running, then finished
        mock_process.returncode = 0
        mock_process.stdout.readline.return_value = ''  # No output (text mode)
        mock_process.stderr.readline.return_value = ''  # No errors
        mock_process.communicate.return_value = ('', '')  # Empty remaining output
        mock_popen.return_value = mock_process

        with tempfile.TemporaryDirectory() as temp_dir:
            # Save original cwd if possible
            try:
                original_cwd = os.getcwd()
            except FileNotFoundError:
                # If current dir doesn't exist, use temp dir as fallback
                original_cwd = temp_dir

            try:
                os.chdir(temp_dir)

                config = RunnerConfig(
                    code_dir="/job/src",
                    job_dir="/job/src",
                    job_command="echo test",
                    runner_image="alpine:latest"
                )

                # Prepare job directory
                job_path = prepare_job_directory(config)

                # Create a test file
                test_file = job_path / "test.txt"
                test_file.write_text("test data")

                # Run container
                run_container(config)

                # Verify the mount was for this specific directory
                mock_popen.assert_called_once()
                args = mock_popen.call_args[0][0]

                # Find the volume mount argument
                mount_arg = None
                for i, arg in enumerate(args):
                    if arg == '-v' and i + 1 < len(args):
                        mount_arg = args[i + 1]
                        break

                assert mount_arg is not None
                # Mount should be from the job_path to /job
                expected_mount = f"{job_path}:/job"
                assert mount_arg == expected_mount

            finally:
                os.chdir(original_cwd)


if __name__ == "__main__":
    pytest.main([__file__, "-v"])