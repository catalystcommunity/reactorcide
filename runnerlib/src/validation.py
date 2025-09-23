"""Configuration validation utilities for runnerlib."""

import os
import shutil
from pathlib import Path
from typing import List, Optional
from dataclasses import dataclass

from src.config import RunnerConfig, config_manager
from src.source_prep import get_code_directory_path


@dataclass
class ValidationError:
    """Represents a configuration validation error."""
    field: str
    message: str
    suggestion: Optional[str] = None


@dataclass
class ValidationResult:
    """Result of configuration validation."""
    is_valid: bool
    errors: List[ValidationError]
    warnings: List[ValidationError]
    
    @property
    def has_errors(self) -> bool:
        """Check if there are any validation errors."""
        return len(self.errors) > 0
    
    @property
    def has_warnings(self) -> bool:
        """Check if there are any validation warnings."""
        return len(self.warnings) > 0


class ConfigValidator:
    """Validates runner configuration and environment."""
    
    def validate_config(self, config: RunnerConfig, check_files: bool = True) -> ValidationResult:
        """Validate a runner configuration.
        
        Args:
            config: Configuration to validate
            check_files: Whether to check file and directory existence
            
        Returns:
            ValidationResult with errors and warnings
        """
        errors = []
        warnings = []
        
        # Validate required fields
        errors.extend(self._validate_required_fields(config))
        
        # Validate directory paths
        errors.extend(self._validate_directory_paths(config))
        
        # Validate job environment
        env_errors, env_warnings = self._validate_job_environment(config, check_files)
        errors.extend(env_errors)
        warnings.extend(env_warnings)
        
        # Validate container image
        warnings.extend(self._validate_container_image(config))
        
        # Check external dependencies
        errors.extend(self._validate_external_dependencies())
        
        # Check file and directory existence if requested
        if check_files:
            file_errors, file_warnings = self._validate_file_system(config)
            errors.extend(file_errors)
            warnings.extend(file_warnings)
        
        return ValidationResult(
            is_valid=len(errors) == 0,
            errors=errors,
            warnings=warnings
        )
    
    def _validate_required_fields(self, config: RunnerConfig) -> List[ValidationError]:
        """Validate required configuration fields."""
        errors = []
        
        if not config.job_command:
            errors.append(ValidationError(
                field="job_command",
                message="Job command is required",
                suggestion="Set REACTORCIDE_JOB_COMMAND environment variable or use --job-command flag"
            ))
        
        if not config.runner_image:
            errors.append(ValidationError(
                field="runner_image",
                message="Runner image is required",
                suggestion="Set REACTORCIDE_RUNNER_IMAGE environment variable or use --runner-image flag"
            ))
        
        if not config.code_dir:
            errors.append(ValidationError(
                field="code_dir",
                message="Code directory is required",
                suggestion="Set REACTORCIDE_CODE_DIR environment variable or use --code-dir flag"
            ))
        
        if not config.job_dir:
            errors.append(ValidationError(
                field="job_dir",
                message="Job directory is required",
                suggestion="Set REACTORCIDE_JOB_DIR environment variable or use --job-dir flag"
            ))
        
        return errors
    
    def _validate_directory_paths(self, config: RunnerConfig) -> List[ValidationError]:
        """Validate directory path formats."""
        errors = []
        
        # Validate code_dir
        if config.code_dir and not config.code_dir.startswith('/'):
            errors.append(ValidationError(
                field="code_dir",
                message=f"Code directory must be an absolute path: {config.code_dir}",
                suggestion="Use paths like '/job/src' or '/job/code'"
            ))
        
        # Validate job_dir
        if config.job_dir and not config.job_dir.startswith('/'):
            errors.append(ValidationError(
                field="job_dir",
                message=f"Job directory must be an absolute path: {config.job_dir}",
                suggestion="Use paths like '/job/src' or '/job'"
            ))
        
        # Check if paths are within /job mount point
        if config.code_dir and not config.code_dir.startswith('/job'):
            errors.append(ValidationError(
                field="code_dir",
                message=f"Code directory must be within /job mount point: {config.code_dir}",
                suggestion="Use paths starting with '/job/'"
            ))
        
        if config.job_dir and not config.job_dir.startswith('/job'):
            errors.append(ValidationError(
                field="job_dir",
                message=f"Job directory must be within /job mount point: {config.job_dir}",
                suggestion="Use paths starting with '/job/'"
            ))
        
        return errors
    
    def _validate_job_environment(self, config: RunnerConfig, check_files: bool) -> tuple[List[ValidationError], List[ValidationError]]:
        """Validate job environment configuration."""
        errors = []
        warnings = []
        
        if not config.job_env:
            return errors, warnings
        
        try:
            # Test parsing the job environment
            env_vars = config_manager.parse_job_environment(config.job_env)
            
            # Check for potentially problematic environment variables
            for key, value in env_vars.items():
                if not key:
                    errors.append(ValidationError(
                        field="job_env",
                        message="Empty environment variable key found",
                        suggestion="Ensure all environment variables have non-empty keys"
                    ))
                
                if key in ['PATH', 'HOME', 'USER']:
                    warnings.append(ValidationError(
                        field="job_env",
                        message=f"Overriding system environment variable: {key}",
                        suggestion="Consider using a different variable name to avoid conflicts"
                    ))
                
                if len(value) > 1000:
                    warnings.append(ValidationError(
                        field="job_env",
                        message=f"Environment variable {key} has very long value ({len(value)} chars)",
                        suggestion="Consider using a file or shortening the value"
                    ))
        
        except FileNotFoundError as e:
            if check_files:
                errors.append(ValidationError(
                    field="job_env",
                    message=f"Environment file not found: {e}",
                    suggestion="Ensure the file exists and the path starts with './job/'"
                ))
        except ValueError as e:
            errors.append(ValidationError(
                field="job_env",
                message=f"Invalid environment variable format: {e}",
                suggestion="Use 'KEY=value' format or ensure file contains valid key=value pairs"
            ))
        
        return errors, warnings
    
    def _validate_container_image(self, config: RunnerConfig) -> List[ValidationError]:
        """Validate container image configuration."""
        warnings = []
        
        if not config.runner_image:
            return warnings
        
        # Check for common image format issues
        if ' ' in config.runner_image:
            warnings.append(ValidationError(
                field="runner_image",
                message=f"Container image name contains spaces: {config.runner_image}",
                suggestion="Ensure image name is properly formatted"
            ))
        
        # Warn about using latest tag
        if config.runner_image.endswith(':latest') or ':' not in config.runner_image:
            warnings.append(ValidationError(
                field="runner_image",
                message="Using 'latest' tag or no tag specified",
                suggestion="Consider using a specific version tag for reproducible builds"
            ))
        
        return warnings
    
    def _validate_external_dependencies(self) -> List[ValidationError]:
        """Validate external tool dependencies."""
        errors = []
        
        # Check for docker
        if not shutil.which("docker"):
            errors.append(ValidationError(
                field="system",
                message="docker is not available in PATH",
                suggestion="Install docker: https://docs.docker.com/get-docker/"
            ))
        
        return errors
    
    def _validate_file_system(self, config: RunnerConfig) -> tuple[List[ValidationError], List[ValidationError]]:
        """Validate file system state."""
        errors = []
        warnings = []
        
        try:
            # Check if job directory exists and is accessible
            job_base_path = Path("./job")
            if not job_base_path.exists():
                warnings.append(ValidationError(
                    field="filesystem",
                    message="Job directory ./job does not exist",
                    suggestion="It will be created automatically, but you may want to prepare it first"
                ))
            elif not job_base_path.is_dir():
                errors.append(ValidationError(
                    field="filesystem",
                    message="./job exists but is not a directory",
                    suggestion="Remove the file './job' and let the system create the directory"
                ))
            
            # Check code directory if it should exist
            try:
                code_path = get_code_directory_path(config)
                if code_path.exists() and not code_path.is_dir():
                    errors.append(ValidationError(
                        field="filesystem",
                        message=f"Code path exists but is not a directory: {code_path}",
                        suggestion="Remove the file and let the system create the directory"
                    ))
                elif code_path.exists() and not os.access(code_path, os.R_OK):
                    errors.append(ValidationError(
                        field="filesystem",
                        message=f"Code directory is not readable: {code_path}",
                        suggestion="Check file permissions on the directory"
                    ))
            except Exception:
                # Ignore path resolution errors here
                pass
        
        except Exception as e:
            warnings.append(ValidationError(
                field="filesystem",
                message=f"Could not validate filesystem: {e}",
                suggestion="Check file system permissions and disk space"
            ))
        
        return errors, warnings


# Global validator instance
validator = ConfigValidator()


def validate_config(config: RunnerConfig, check_files: bool = True) -> ValidationResult:
    """Convenience function to validate configuration."""
    return validator.validate_config(config, check_files)


def format_validation_result(result: ValidationResult) -> str:
    """Format validation result for display to user."""
    lines = []
    
    if result.is_valid and not result.has_warnings:
        lines.append("✅ Configuration is valid")
        return "\n".join(lines)
    
    if result.has_errors:
        lines.append("❌ Configuration has errors:")
        for error in result.errors:
            lines.append(f"  • {error.field}: {error.message}")
            if error.suggestion:
                lines.append(f"    💡 {error.suggestion}")
        lines.append("")
    
    if result.has_warnings:
        lines.append("⚠️  Configuration warnings:")
        for warning in result.warnings:
            lines.append(f"  • {warning.field}: {warning.message}")
            if warning.suggestion:
                lines.append(f"    💡 {warning.suggestion}")
        lines.append("")
    
    if result.is_valid:
        lines.append("✅ Configuration is valid (with warnings)")
    
    return "\n".join(lines)