"""Advanced tests for container module to improve coverage."""

import subprocess
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch, MagicMock, mock_open, call

from src.container import build_docker_command, run_container, validate_container_config
from src.config import RunnerConfig


class TestContainerAdvanced(unittest.TestCase):
    """Advanced tests for container module."""

    def setUp(self):
        """Set up test fixtures."""
        self.config = RunnerConfig(
            runner_image="python:3.11",
            job_command="python test.py",
            code_dir="/job",
            job_dir="/job",
            job_env=""
        )

    def test_build_docker_command_with_resource_limits(self):
        """Test building docker command with memory and CPU limits."""
        resource_limits = {"memory": "512m", "cpus": "2"}

        with patch("src.container.Path") as mock_path_class:
            mock_path = MagicMock()
            mock_path.exists.return_value = True
            mock_path_class.return_value = mock_path

            cmd = build_docker_command(
                self.config,
                "/tmp/test/job",
                {},
                additional_args=None,
                resource_limits=resource_limits
            )

        # Check that resource limits are in the command
        self.assertIn("--memory", cmd)
        self.assertIn("512m", cmd)
        self.assertIn("--cpus", cmd)
        self.assertIn("2", cmd)

    def test_build_docker_command_with_additional_args(self):
        """Test building docker command with additional arguments."""
        with patch("src.container.Path") as mock_path_class:
            mock_path = MagicMock()
            mock_path.exists.return_value = True
            mock_path_class.return_value = mock_path

            cmd = build_docker_command(
                self.config,
                "/tmp/test/job",
                {},
                additional_args=["--verbose", "--debug"]
            )

        # Check that additional args are at the end
        self.assertEqual(cmd[-2:], ["--verbose", "--debug"])

    def test_build_docker_command_with_secrets_socket(self):
        """Test building docker command with secrets socket."""
        env_vars = {"REACTORCIDE_SECRETS_SOCKET": "/tmp/secrets.sock"}

        with patch("src.container.Path") as mock_path_class:
            mock_path = MagicMock()
            mock_path.exists.return_value = True
            mock_path_class.return_value = mock_path

            cmd = build_docker_command(self.config, "/tmp/test/job", env_vars)

        # Check that /tmp is mounted when socket exists
        self.assertIn("-v", cmd)
        self.assertIn("/tmp:/tmp", cmd)

    def test_validate_container_config_missing_job_command(self):
        """Test validation fails when job_command is missing."""
        config = RunnerConfig(
            runner_image="python:3.11",
            job_command="",
            code_dir="/job",
            job_dir="/job",
            job_env=""
        )

        with self.assertRaises(ValueError) as ctx:
            validate_container_config(config)
        self.assertIn("job_command is required", str(ctx.exception))

    def test_validate_container_config_missing_runner_image(self):
        """Test validation fails when runner_image is missing."""
        config = RunnerConfig(
            runner_image="",
            job_command="python test.py",
            code_dir="/job",
            job_dir="/job",
            job_env=""
        )

        with self.assertRaises(ValueError) as ctx:
            validate_container_config(config)
        self.assertIn("runner_image is required", str(ctx.exception))

    def test_validate_container_config_missing_code_dir(self):
        """Test validation fails when code_dir is missing."""
        config = RunnerConfig(
            runner_image="python:3.11",
            job_command="python test.py",
            code_dir="",
            job_dir="/job",
            job_env=""
        )

        with self.assertRaises(ValueError) as ctx:
            validate_container_config(config)
        self.assertIn("code_dir is required", str(ctx.exception))

    def test_validate_container_config_missing_job_dir(self):
        """Test validation fails when job_dir is missing."""
        config = RunnerConfig(
            runner_image="python:3.11",
            job_command="python test.py",
            code_dir="/job",
            job_dir="",
            job_env=""
        )

        with self.assertRaises(ValueError) as ctx:
            validate_container_config(config)
        self.assertIn("job_dir is required", str(ctx.exception))

    def test_validate_container_config_relative_code_dir(self):
        """Test validation fails when code_dir is not absolute."""
        config = RunnerConfig(
            runner_image="python:3.11",
            job_command="python test.py",
            code_dir="job",  # Relative path
            job_dir="/job",
            job_env=""
        )

        with self.assertRaises(ValueError) as ctx:
            validate_container_config(config)
        self.assertIn("code_dir must be an absolute path", str(ctx.exception))

    def test_validate_container_config_relative_job_dir(self):
        """Test validation fails when job_dir is not absolute."""
        config = RunnerConfig(
            runner_image="python:3.11",
            job_command="python test.py",
            code_dir="/job",
            job_dir="job",  # Relative path
            job_env=""
        )

        with self.assertRaises(ValueError) as ctx:
            validate_container_config(config)
        self.assertIn("job_dir must be an absolute path", str(ctx.exception))



if __name__ == "__main__":
    unittest.main()