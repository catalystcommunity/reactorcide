"""Tests for validation module."""

import shutil
from pathlib import Path
from unittest.mock import patch

from src.validation import (
    ValidationError, ValidationResult, ConfigValidator, 
    validate_config, format_validation_result
)
from src.config import RunnerConfig


class TestValidationError:
    """Test cases for ValidationError dataclass."""

    def test_validation_error_creation(self):
        """Test creating a ValidationError."""
        error = ValidationError(
            field="test_field",
            message="Test message",
            suggestion="Test suggestion"
        )
        
        assert error.field == "test_field"
        assert error.message == "Test message"
        assert error.suggestion == "Test suggestion"

    def test_validation_error_without_suggestion(self):
        """Test creating a ValidationError without suggestion."""
        error = ValidationError(
            field="test_field",
            message="Test message"
        )
        
        assert error.field == "test_field"
        assert error.message == "Test message"
        assert error.suggestion is None


class TestValidationResult:
    """Test cases for ValidationResult dataclass."""

    def test_validation_result_valid(self):
        """Test ValidationResult for valid configuration."""
        result = ValidationResult(
            is_valid=True,
            errors=[],
            warnings=[]
        )
        
        assert result.is_valid is True
        assert not result.has_errors
        assert not result.has_warnings

    def test_validation_result_with_errors(self):
        """Test ValidationResult with errors."""
        error = ValidationError("field", "message")
        result = ValidationResult(
            is_valid=False,
            errors=[error],
            warnings=[]
        )
        
        assert result.is_valid is False
        assert result.has_errors is True
        assert not result.has_warnings

    def test_validation_result_with_warnings(self):
        """Test ValidationResult with warnings."""
        warning = ValidationError("field", "warning message")
        result = ValidationResult(
            is_valid=True,
            errors=[],
            warnings=[warning]
        )
        
        assert result.is_valid is True
        assert not result.has_errors
        assert result.has_warnings is True


class TestConfigValidator:
    """Test cases for ConfigValidator class."""

    def setup_method(self):
        """Set up test environment."""
        self.validator = ConfigValidator()
        self.valid_config = RunnerConfig(
            code_dir="/job/src",
            job_dir="/job/src",
            job_command="test-command",
            runner_image="test:image"
        )

    def test_validate_required_fields_valid(self):
        """Test validation passes for valid required fields."""
        errors = self.validator._validate_required_fields(self.valid_config)
        assert len(errors) == 0

    def test_validate_required_fields_missing_job_command(self):
        """Test validation fails for missing job_command."""
        config = RunnerConfig(
            code_dir="/job/src",
            job_dir="/job/src",
            job_command="",  # Empty string
            runner_image="test:image"
        )
        
        errors = self.validator._validate_required_fields(config)
        assert len(errors) == 1
        assert errors[0].field == "job_command"
        assert "Job command is required" in errors[0].message

    def test_validate_required_fields_missing_runner_image(self):
        """Test validation fails for missing runner_image."""
        config = RunnerConfig(
            code_dir="/job/src",
            job_dir="/job/src",
            job_command="test",
            runner_image=""  # Empty string
        )
        
        errors = self.validator._validate_required_fields(config)
        assert len(errors) == 1
        assert errors[0].field == "runner_image"

    def test_validate_directory_paths_valid(self):
        """Test validation passes for valid directory paths."""
        errors = self.validator._validate_directory_paths(self.valid_config)
        assert len(errors) == 0

    def test_validate_directory_paths_relative_code_dir(self):
        """Test validation fails for relative code_dir."""
        config = RunnerConfig(
            code_dir="relative/path",  # Relative path
            job_dir="/job/work",
            job_command="test",
            runner_image="test:image"
        )
        
        errors = self.validator._validate_directory_paths(config)
        assert len(errors) >= 1
        assert any("absolute path" in error.message for error in errors)

    def test_validate_directory_paths_outside_job_mount(self):
        """Test validation fails for paths outside /job."""
        config = RunnerConfig(
            code_dir="/outside/job",  # Outside /job
            job_dir="/job/work",
            job_command="test",
            runner_image="test:image"
        )
        
        errors = self.validator._validate_directory_paths(config)
        assert len(errors) >= 1
        assert any("within /job mount point" in error.message for error in errors)

    def test_validate_job_environment_valid_inline(self):
        """Test validation passes for valid inline job environment."""
        config = RunnerConfig(
            code_dir="/job/src",
            job_dir="/job/src",
            job_command="test",
            runner_image="test:image",
            job_env="KEY1=value1\nKEY2=value2"
        )
        
        errors, warnings = self.validator._validate_job_environment(config, check_files=False)
        assert len(errors) == 0

    def test_validate_job_environment_invalid_format(self):
        """Test validation fails for invalid job environment format."""
        config = RunnerConfig(
            code_dir="/job/src",
            job_dir="/job/src", 
            job_command="test",
            runner_image="test:image",
            job_env="INVALID_LINE_WITHOUT_EQUALS"
        )
        
        errors, warnings = self.validator._validate_job_environment(config, check_files=False)
        assert len(errors) >= 1
        assert any("Invalid environment variable format" in error.message for error in errors)

    def test_validate_job_environment_system_var_warning(self):
        """Test warnings for overriding system environment variables."""
        config = RunnerConfig(
            code_dir="/job/src",
            job_dir="/job/src",
            job_command="test", 
            runner_image="test:image",
            job_env="PATH=/custom/path"
        )
        
        errors, warnings = self.validator._validate_job_environment(config, check_files=False)
        assert len(warnings) >= 1
        assert any("system environment variable" in warning.message for warning in warnings)

    def test_validate_job_environment_long_value_warning(self):
        """Test warnings for very long environment variable values."""
        long_value = "x" * 1500  # Very long value
        config = RunnerConfig(
            code_dir="/job/src",
            job_dir="/job/src",
            job_command="test",
            runner_image="test:image", 
            job_env=f"LONG_VAR={long_value}"
        )
        
        errors, warnings = self.validator._validate_job_environment(config, check_files=False)
        assert len(warnings) >= 1
        assert any("very long value" in warning.message for warning in warnings)

    def test_validate_container_image_warnings(self):
        """Test warnings for container image issues."""
        # Test image with spaces
        config = RunnerConfig(
            code_dir="/job/src",
            job_dir="/job/src",
            job_command="test",
            runner_image="bad image name"  # Contains spaces
        )
        
        warnings = self.validator._validate_container_image(config)
        assert len(warnings) >= 1
        assert any("contains spaces" in warning.message for warning in warnings)

    def test_validate_container_image_latest_tag_warning(self):
        """Test warnings for latest tag usage."""
        config = RunnerConfig(
            code_dir="/job/src",
            job_dir="/job/src",
            job_command="test",
            runner_image="ubuntu:latest"  # Uses latest tag
        )
        
        warnings = self.validator._validate_container_image(config)
        assert len(warnings) >= 1
        assert any("latest" in warning.message for warning in warnings)

    @patch('shutil.which')
    def test_validate_external_dependencies_docker_missing(self, mock_which):
        """Test validation fails when docker is missing and container mode is required."""
        mock_which.return_value = None  # docker not found

        # With require_container_runtime=True, docker check should fail
        errors = self.validator._validate_external_dependencies(require_container_runtime=True)
        assert len(errors) == 1
        assert errors[0].field == "system"
        assert "docker is not available" in errors[0].message

    @patch('shutil.which')
    def test_validate_external_dependencies_docker_available(self, mock_which):
        """Test validation passes when docker is available."""
        mock_which.return_value = "/usr/bin/docker"  # docker found

        errors = self.validator._validate_external_dependencies(require_container_runtime=True)
        assert len(errors) == 0

    @patch('shutil.which')
    def test_validate_external_dependencies_local_mode_no_docker_required(self, mock_which):
        """Test validation passes in local mode even without docker."""
        mock_which.return_value = None  # docker not found

        # With require_container_runtime=False (default), docker check is skipped
        errors = self.validator._validate_external_dependencies(require_container_runtime=False)
        assert len(errors) == 0

    def test_validate_config_integration_valid(self):
        """Test full config validation for valid configuration."""
        with patch('shutil.which', return_value="/usr/bin/docker"):
            result = self.validator.validate_config(self.valid_config, check_files=False)
            
            assert result.is_valid is True
            assert len(result.errors) == 0

    def test_validate_config_integration_invalid(self):
        """Test full config validation for invalid configuration."""
        invalid_config = RunnerConfig(
            code_dir="relative/path",  # Invalid - not absolute
            job_dir="/job/work",
            job_command="",  # Invalid - empty
            runner_image=""  # Invalid - empty
        )
        
        with patch('shutil.which', return_value=None):  # docker not available
            result = self.validator.validate_config(invalid_config, check_files=False)
            
            assert result.is_valid is False
            assert len(result.errors) > 0

    def test_validate_file_system_job_directory_missing(self):
        """Test file system validation when job directory is missing."""
        # Ensure ./job doesn't exist
        job_path = Path("./job")
        if job_path.exists():
            shutil.rmtree(job_path)
        
        try:
            errors, warnings = self.validator._validate_file_system(self.valid_config)
            
            # Should have warning about missing directory
            assert len(warnings) >= 1
            assert any("does not exist" in warning.message for warning in warnings)
        finally:
            # Clean up
            if job_path.exists():
                shutil.rmtree(job_path)


class TestConvenienceFunctions:
    """Test cases for convenience functions."""

    def test_validate_config_function(self):
        """Test the validate_config convenience function."""
        config = RunnerConfig(
            code_dir="/job/src",
            job_dir="/job/src",
            job_command="test",
            runner_image="test:image"
        )
        
        with patch('shutil.which', return_value="/usr/bin/docker"):
            result = validate_config(config, check_files=False)
            assert isinstance(result, ValidationResult)

    def test_format_validation_result_valid(self):
        """Test formatting a valid validation result."""
        result = ValidationResult(
            is_valid=True,
            errors=[],
            warnings=[]
        )
        
        formatted = format_validation_result(result)
        assert "✅ Configuration is valid" in formatted

    def test_format_validation_result_with_errors(self):
        """Test formatting validation result with errors."""
        error = ValidationError(
            field="test_field",
            message="Test error message",
            suggestion="Test suggestion"
        )
        result = ValidationResult(
            is_valid=False,
            errors=[error],
            warnings=[]
        )
        
        formatted = format_validation_result(result)
        assert "❌ Configuration has errors:" in formatted
        assert "test_field: Test error message" in formatted
        assert "💡 Test suggestion" in formatted

    def test_format_validation_result_with_warnings(self):
        """Test formatting validation result with warnings."""
        warning = ValidationError(
            field="test_field",
            message="Test warning message"
        )
        result = ValidationResult(
            is_valid=True,
            errors=[],
            warnings=[warning]
        )
        
        formatted = format_validation_result(result)
        assert "⚠️  Configuration warnings:" in formatted
        assert "test_field: Test warning message" in formatted
        assert "✅ Configuration is valid (with warnings)" in formatted