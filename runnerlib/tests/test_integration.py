"""Integration tests for runnerlib."""

import os
import tempfile
import shutil
from pathlib import Path
from unittest.mock import patch, MagicMock
import pytest

from src.config import get_config, get_environment_vars
from src.validation import validate_config
from src.source_prep import prepare_job_directory, get_code_directory_path
from src.container import run_container


class TestConfigurationIntegration:
    """Integration tests for configuration system."""

    def test_end_to_end_configuration_hierarchy(self):
        """Test complete configuration hierarchy from defaults to CLI overrides."""
        # Test with only environment variables
        env_vars = {
            'REACTORCIDE_CODE_DIR': '/job/custom-code',
            'REACTORCIDE_JOB_COMMAND': 'env-command',
            'REACTORCIDE_RUNNER_IMAGE': 'env:image'
        }
        
        with patch.dict(os.environ, env_vars, clear=True):
            # Get config with some CLI overrides
            config = get_config(
                job_command='cli-command',  # Should override env
                job_env='CLI_VAR=cli_value'  # New from CLI
            )
            
            # Check hierarchy worked correctly
            assert config.code_dir == '/job/custom-code'  # From env
            assert config.job_command == 'cli-command'  # CLI override
            assert config.runner_image == 'env:image'  # From env
            assert config.job_env == 'CLI_VAR=cli_value'  # From CLI
            
            # Get all environment variables
            env_vars = get_environment_vars(config)
            
            # Should include both REACTORCIDE_* and job-specific vars
            assert env_vars['REACTORCIDE_CODE_DIR'] == '/job/custom-code'
            assert env_vars['REACTORCIDE_JOB_COMMAND'] == 'cli-command'
            assert env_vars['CLI_VAR'] == 'cli_value'

    def test_configuration_validation_integration(self):
        """Test that configuration validation catches real issues."""
        # Test with invalid configuration
        invalid_config = get_config(
            code_dir='relative/path',  # Invalid - not absolute
            job_command='test',
            runner_image=''  # Invalid - empty
        )
        
        with patch('shutil.which', return_value=None):  # nerdctl not available
            result = validate_config(invalid_config, check_files=False)
            
            assert not result.is_valid
            assert len(result.errors) >= 2  # At least path and nerdctl errors
            
            # Check specific error types
            error_messages = [error.message for error in result.errors]
            assert any('absolute path' in msg for msg in error_messages)
            assert any('nerdctl' in msg for msg in error_messages)


class TestDirectoryManagementIntegration:
    """Integration tests for directory management."""

    def setup_method(self):
        """Set up test environment."""
        self.temp_dir = tempfile.mkdtemp()
        self.original_cwd = os.getcwd()
        os.chdir(self.temp_dir)

    def teardown_method(self):
        """Clean up test environment."""
        os.chdir(self.original_cwd)
        if os.path.exists(self.temp_dir):
            shutil.rmtree(self.temp_dir, ignore_errors=True)

    def test_job_directory_preparation_with_custom_paths(self):
        """Test directory preparation with custom configuration."""
        config = get_config(
            code_dir='/job/custom-src',
            job_dir='/job/custom-work',
            job_command='test',
            runner_image='test:image'
        )
        
        # Prepare directories
        job_path = prepare_job_directory(config)
        
        # Check that directories were created correctly
        assert job_path.exists()
        assert job_path.name == "job"
        
        # Check custom directories were created
        code_path = get_code_directory_path(config)
        assert code_path.exists()
        assert str(code_path).endswith("custom-src")

    def test_directory_validation_with_real_filesystem(self):
        """Test directory validation against real filesystem."""
        config = get_config(
            code_dir='/job/src',
            job_dir='/job/work',
            job_command='test',
            runner_image='test:image'
        )
        
        # Test validation without directories
        with patch('shutil.which', return_value="/usr/bin/nerdctl"):
            result = validate_config(config, check_files=True)
            
            # Should have warnings about missing directories
            assert result.has_warnings
            warning_messages = [w.message for w in result.warnings]
            assert any('does not exist' in msg for msg in warning_messages)
        
        # Create directories and test again
        prepare_job_directory(config)
        
        with patch('shutil.which', return_value="/usr/bin/nerdctl"):
            result = validate_config(config, check_files=True)
            
            # Should be valid now (may still have warnings about empty dirs)
            assert result.is_valid


class TestEnvironmentVariableIntegration:
    """Integration tests for environment variable handling."""

    def setup_method(self):
        """Set up test environment."""
        self.temp_dir = tempfile.mkdtemp()
        self.original_cwd = os.getcwd()
        os.chdir(self.temp_dir)
        self.job_dir = Path("./job")
        self.job_dir.mkdir()

    def teardown_method(self):
        """Clean up test environment."""
        os.chdir(self.original_cwd)
        if os.path.exists(self.temp_dir):
            shutil.rmtree(self.temp_dir, ignore_errors=True)

    def test_environment_file_integration(self):
        """Test environment file creation and parsing integration."""
        # Create environment file
        env_file = self.job_dir / "test.env"
        env_content = """# Test environment file
DATABASE_URL=postgresql://localhost/test
API_KEY=secret-key-123
DEBUG=true"""
        env_file.write_text(env_content)
        
        # Configure to use the environment file
        config = get_config(
            job_command='test',
            job_env="./job/test.env",
            runner_image='test:image'
        )
        
        # Validate configuration
        with patch('shutil.which', return_value="/usr/bin/nerdctl"):
            result = validate_config(config, check_files=True)
            assert result.is_valid
        
        # Get all environment variables
        env_vars = get_environment_vars(config)
        
        # Should include both REACTORCIDE_* and file variables
        assert env_vars['DATABASE_URL'] == 'postgresql://localhost/test'
        assert env_vars['API_KEY'] == 'secret-key-123'
        assert env_vars['DEBUG'] == 'true'
        assert env_vars['REACTORCIDE_JOB_ENV'] == "./job/test.env"

    def test_environment_security_validation(self):
        """Test security validation for environment files."""
        # Test with path traversal in job directory context
        config = get_config(
            job_command='test',
            job_env='./job/../../../etc/passwd',  # Path traversal attempt
            runner_image='test:image'
        )
        
        # Should fail validation
        result = validate_config(config, check_files=False)
        assert not result.is_valid
        
        error_messages = [error.message for error in result.errors]
        assert any('Path traversal' in msg for msg in error_messages)


class TestContainerExecutionIntegration:
    """Integration tests for container execution."""

    def test_container_command_building(self):
        """Test that container commands are built correctly."""
        config = get_config(
            code_dir='/job/src',
            job_dir='/job/work',
            job_command='npm test',
            runner_image='node:18',
            job_env='NODE_ENV=test\nDEBUG=true'
        )
        
        # Mock container execution to capture command
        with patch('subprocess.Popen') as mock_popen, \
             patch('shutil.which', return_value="/usr/bin/nerdctl"), \
             patch('src.source_prep.prepare_job_directory') as mock_prepare:
            
            mock_process = MagicMock()
            mock_process.poll.return_value = 0
            mock_process.communicate.return_value = ("", "")
            mock_popen.return_value = mock_process
            
            mock_prepare.return_value = Path("/tmp/job")
            
            # Run container
            exit_code = run_container(config, additional_args=["--verbose"])
            
            # Verify command was built correctly
            mock_popen.assert_called_once()
            call_args = mock_popen.call_args[0][0]  # First positional argument (the command)
            
            # Check command structure
            assert call_args[0] == "nerdctl"
            assert call_args[1] == "run"
            assert "--rm" in call_args
            assert "-v" in call_args
            assert "-w" in call_args
            assert "/job/work" in call_args  # Working directory
            assert "node:18" in call_args  # Image
            assert "npm test" in call_args  # Command
            assert "--verbose" in call_args  # Additional args
            
            # Check environment variables are included
            env_args = []
            for i, arg in enumerate(call_args):
                if arg == "-e" and i + 1 < len(call_args):
                    env_args.append(call_args[i + 1])
            
            # Should have REACTORCIDE_* and job-specific env vars
            env_dict = dict(env.split('=', 1) for env in env_args if '=' in env)
            assert 'REACTORCIDE_CODE_DIR' in env_dict
            assert 'REACTORCIDE_JOB_COMMAND' in env_dict
            assert 'NODE_ENV' in env_dict
            assert 'DEBUG' in env_dict

    def test_container_validation_before_execution(self):
        """Test that container execution validates configuration first."""
        # Create invalid configuration
        config = get_config(
            code_dir='invalid-path',  # Invalid path
            job_command='test',
            runner_image=''  # Empty image
        )
        
        # Container execution should handle validation gracefully
        # (since CLI layer usually validates first, but container should be defensive)
        with patch('shutil.which', return_value=None):  # nerdctl not available
            with pytest.raises(FileNotFoundError, match="nerdctl is not available"):
                run_container(config)


class TestDryRunIntegration:
    """Integration tests for dry-run functionality."""

    def test_dry_run_shows_complete_configuration(self):
        """Test that dry-run displays all configuration correctly."""
        config = get_config(
            code_dir='/job/custom-src',
            job_dir='/job/custom-work', 
            job_command='pytest tests/',
            runner_image='python:3.11',
            job_env='PYTEST_ARGS=--verbose'
        )
        
        # Test dry-run output (would be tested via CLI in real usage)
        # This tests the underlying data structures
        env_vars = get_environment_vars(config)
        
        # Should have complete environment
        assert len(env_vars) >= 6  # 5 REACTORCIDE_* + 1 job-specific
        assert env_vars['REACTORCIDE_CODE_DIR'] == '/job/custom-src'
        assert env_vars['REACTORCIDE_JOB_DIR'] == '/job/custom-work'
        assert env_vars['REACTORCIDE_JOB_COMMAND'] == 'pytest tests/'
        assert env_vars['PYTEST_ARGS'] == '--verbose'
        
        # Validate configuration
        with patch('shutil.which', return_value="/usr/bin/nerdctl"):
            result = validate_config(config, check_files=False)
            assert result.is_valid


class TestErrorHandlingIntegration:
    """Integration tests for error handling across components."""

    def test_cascading_validation_errors(self):
        """Test that validation errors cascade properly through system."""
        # Create configuration with multiple issues
        with patch.dict(os.environ, {}, clear=True):
            try:
                config = get_config(
                    code_dir='relative/path',  # Invalid
                    job_command='',  # Invalid
                    runner_image='bad image:latest',  # Warning
                    job_env='INVALID_FORMAT'  # Invalid
                )
                
                # Should have gotten this far (config creation may succeed)
                with patch('shutil.which', return_value=None):
                    result = validate_config(config, check_files=False)
                    
                    # Should have multiple errors
                    assert not result.is_valid
                    assert len(result.errors) >= 3  # Path, command, nerdctl, env format
                    
            except ValueError as e:
                # Or might fail at config creation level
                assert "Missing required configuration" in str(e)

    def test_recovery_from_validation_errors(self):
        """Test system can recover when validation issues are fixed."""
        # Start with invalid config
        invalid_config = get_config(
            code_dir='relative/path',
            job_command='test',
            runner_image='test:image'
        )
        
        with patch('shutil.which', return_value=None):
            result = validate_config(invalid_config, check_files=False)
            assert not result.is_valid
        
        # Fix the issues
        valid_config = get_config(
            code_dir='/job/src',  # Fixed path
            job_command='test',
            runner_image='test:image'
        )
        
        with patch('shutil.which', return_value="/usr/bin/nerdctl"):  # Fixed nerdctl
            result = validate_config(valid_config, check_files=False)
            assert result.is_valid