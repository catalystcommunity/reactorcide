"""Tests for config module."""

import os
import tempfile
import pytest
from pathlib import Path
from unittest.mock import patch

from src.config import ConfigManager, RunnerConfig, get_config, get_environment_vars


class TestConfigManager:
    """Test cases for ConfigManager class."""

    def setup_method(self):
        """Set up test environment."""
        self.config_manager = ConfigManager()

    def test_defaults(self):
        """Test that default values are properly set."""
        assert self.config_manager.DEFAULTS['code_dir'] == '/job/src'
        assert self.config_manager.DEFAULTS['runner_image'] == 'quay.io/catalystcommunity/reactorcide_runner'

    def test_env_var_mappings(self):
        """Test that environment variable mappings are correct."""
        expected_mappings = {
            'code_dir': 'REACTORCIDE_CODE_DIR',
            'job_dir': 'REACTORCIDE_JOB_DIR',
            'job_command': 'REACTORCIDE_JOB_COMMAND',
            'runner_image': 'REACTORCIDE_RUNNER_IMAGE',
            'job_env': 'REACTORCIDE_JOB_ENV'
        }
        assert self.config_manager.ENV_VARS == expected_mappings

    def test_get_config_with_defaults(self):
        """Test getting configuration with only defaults."""
        with patch.dict(os.environ, {}, clear=True):
            config = self.config_manager.get_config(job_command="test-command")
            
            assert config.code_dir == "/job/src"
            assert config.job_dir == "/job/src"  # Should default to code_dir
            assert config.job_command == "test-command"
            assert config.runner_image == "quay.io/catalystcommunity/reactorcide_runner"
            assert config.job_env is None

    def test_get_config_with_env_vars(self):
        """Test configuration hierarchy with environment variables."""
        env_vars = {
            'REACTORCIDE_CODE_DIR': '/job/custom-src',
            'REACTORCIDE_JOB_DIR': '/job/custom-job',
            'REACTORCIDE_JOB_COMMAND': 'env-command',
            'REACTORCIDE_RUNNER_IMAGE': 'custom:image',
            'REACTORCIDE_JOB_ENV': 'ENV_VAR=value'
        }
        
        with patch.dict(os.environ, env_vars, clear=True):
            config = self.config_manager.get_config()
            
            assert config.code_dir == "/job/custom-src"
            assert config.job_dir == "/job/custom-job"
            assert config.job_command == "env-command"
            assert config.runner_image == "custom:image"
            assert config.job_env == "ENV_VAR=value"

    def test_get_config_with_cli_overrides(self):
        """Test configuration hierarchy with CLI overrides."""
        env_vars = {
            'REACTORCIDE_CODE_DIR': '/job/env-src',
            'REACTORCIDE_JOB_COMMAND': 'env-command'
        }
        
        with patch.dict(os.environ, env_vars, clear=True):
            config = self.config_manager.get_config(
                code_dir='/job/cli-src',
                job_command='cli-command',
                runner_image='cli:image'
            )
            
            # CLI overrides should win
            assert config.code_dir == "/job/cli-src"
            assert config.job_command == "cli-command"
            assert config.runner_image == "cli:image"
            
            # job_dir should default to code_dir since not set
            assert config.job_dir == "/job/cli-src"

    def test_get_config_missing_required_field(self):
        """Test that missing required fields raise ValueError."""
        with patch.dict(os.environ, {}, clear=True):
            with pytest.raises(ValueError, match="Missing required configuration: job_command"):
                self.config_manager.get_config()

    def test_job_dir_defaults_to_code_dir(self):
        """Test that job_dir defaults to code_dir when not specified."""
        with patch.dict(os.environ, {}, clear=True):
            config = self.config_manager.get_config(
                code_dir='/job/custom',
                job_command='test'
            )
            assert config.job_dir == config.code_dir

    def test_parse_job_environment_inline(self):
        """Test parsing inline environment variables."""
        job_env = "KEY1=value1\nKEY2=value2\nKEY3=value with spaces"
        
        result = self.config_manager.parse_job_environment(job_env)
        
        expected = {
            'KEY1': 'value1',
            'KEY2': 'value2',
            'KEY3': 'value with spaces'
        }
        assert result == expected

    def test_parse_job_environment_with_comments(self):
        """Test parsing environment variables with comments."""
        job_env = """# This is a comment
KEY1=value1
# Another comment
KEY2=value2

KEY3=value3"""
        
        result = self.config_manager.parse_job_environment(job_env)
        
        expected = {
            'KEY1': 'value1',
            'KEY2': 'value2',
            'KEY3': 'value3'
        }
        assert result == expected

    def test_parse_job_environment_from_file(self):
        """Test parsing environment variables from a file."""
        with tempfile.NamedTemporaryFile(mode='w', suffix='.env', delete=False) as f:
            f.write("FILE_KEY1=file_value1\nFILE_KEY2=file_value2")
            temp_file = f.name
        
        try:
            # Move file to job directory structure
            job_dir = Path("./job")
            job_dir.mkdir(exist_ok=True)
            env_file = job_dir / "test.env"
            Path(temp_file).rename(env_file)
            
            result = self.config_manager.parse_job_environment(str(env_file))
            
            expected = {
                'FILE_KEY1': 'file_value1',
                'FILE_KEY2': 'file_value2'
            }
            assert result == expected
            
        finally:
            # Clean up
            if env_file.exists():
                env_file.unlink()
            if job_dir.exists():
                job_dir.rmdir()

    def test_parse_job_environment_invalid_format(self):
        """Test that invalid environment variable format raises ValueError."""
        job_env = "INVALID_LINE_WITHOUT_EQUALS"
        
        with pytest.raises(ValueError, match="Invalid environment variable format"):
            self.config_manager.parse_job_environment(job_env)

    def test_parse_job_environment_empty_key(self):
        """Test that empty key raises ValueError."""
        job_env = "=value_without_key"
        
        with pytest.raises(ValueError, match="Empty environment variable key"):
            self.config_manager.parse_job_environment(job_env)

    def test_parse_job_environment_file_not_found(self):
        """Test that non-existent file raises FileNotFoundError."""
        with pytest.raises(FileNotFoundError, match="Environment file not found"):
            self.config_manager.parse_job_environment("./job/nonexistent.env")

    def test_parse_job_environment_insecure_path(self):
        """Test that insecure file paths raise ValueError."""
        with pytest.raises(ValueError, match="Path traversal not allowed"):
            self.config_manager.parse_job_environment("../../../etc/passwd")
        
        with pytest.raises(ValueError, match="job_env file path must start with"):
            self.config_manager.parse_job_environment("/absolute/path/file.env")

    def test_get_all_environment_vars(self):
        """Test getting all environment variables for container."""
        config = RunnerConfig(
            code_dir="/job/src",
            job_dir="/job/work",
            job_command="test-cmd",
            runner_image="test:image",
            job_env="TEST_VAR=test_value"
        )
        
        env_vars = self.config_manager.get_all_environment_vars(config)
        
        # Should include REACTORCIDE_* vars
        assert env_vars['REACTORCIDE_CODE_DIR'] == "/job/src"
        assert env_vars['REACTORCIDE_JOB_DIR'] == "/job/work"
        assert env_vars['REACTORCIDE_JOB_COMMAND'] == "test-cmd"
        assert env_vars['REACTORCIDE_RUNNER_IMAGE'] == "test:image"
        assert env_vars['REACTORCIDE_JOB_ENV'] == "TEST_VAR=test_value"
        
        # Should include job-specific vars
        assert env_vars['TEST_VAR'] == "test_value"

    def test_get_all_environment_vars_no_job_env(self):
        """Test getting environment variables when job_env is None."""
        config = RunnerConfig(
            code_dir="/job/src",
            job_dir="/job/src",
            job_command="test-cmd",
            runner_image="test:image"
        )
        
        env_vars = self.config_manager.get_all_environment_vars(config)
        
        # Should only include REACTORCIDE_* vars
        assert env_vars['REACTORCIDE_CODE_DIR'] == "/job/src"
        assert env_vars['REACTORCIDE_JOB_DIR'] == "/job/src"
        assert env_vars['REACTORCIDE_JOB_COMMAND'] == "test-cmd"
        assert env_vars['REACTORCIDE_RUNNER_IMAGE'] == "test:image"
        assert 'REACTORCIDE_JOB_ENV' not in env_vars


class TestConvenienceFunctions:
    """Test cases for convenience functions."""

    def test_get_config_function(self):
        """Test the get_config convenience function."""
        with patch.dict(os.environ, {}, clear=True):
            config = get_config(job_command="test")
            assert isinstance(config, RunnerConfig)
            assert config.job_command == "test"

    def test_get_environment_vars_function(self):
        """Test the get_environment_vars convenience function."""
        config = RunnerConfig(
            code_dir="/job/src",
            job_dir="/job/src",
            job_command="test",
            runner_image="test:image"
        )
        
        env_vars = get_environment_vars(config)
        assert isinstance(env_vars, dict)
        assert 'REACTORCIDE_CODE_DIR' in env_vars


class TestRunnerConfig:
    """Test cases for RunnerConfig dataclass."""

    def test_runner_config_creation(self):
        """Test creating a RunnerConfig instance."""
        config = RunnerConfig(
            code_dir="/job/src",
            job_dir="/job/work",
            job_command="test-command",
            runner_image="test:image",
            job_env="KEY=value"
        )
        
        assert config.code_dir == "/job/src"
        assert config.job_dir == "/job/work"
        assert config.job_command == "test-command"
        assert config.runner_image == "test:image"
        assert config.job_env == "KEY=value"

    def test_runner_config_optional_job_env(self):
        """Test creating a RunnerConfig without job_env."""
        config = RunnerConfig(
            code_dir="/job/src",
            job_dir="/job/src",
            job_command="test-command",
            runner_image="test:image"
        )
        
        assert config.job_env is None