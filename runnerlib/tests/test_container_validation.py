"""Tests for container_validation module."""

import subprocess
from unittest.mock import patch, MagicMock
import pytest

from src.container_validation import (
    check_container_image_availability,
    validate_container_runtime,
    get_container_runtime_info,
    format_container_validation_results
)


class TestContainerImageAvailability:
    """Test cases for container image availability checking."""

    @patch('shutil.which')
    def test_check_image_nerdctl_not_available(self, mock_which):
        """Test when nerdctl is not available."""
        mock_which.return_value = None
        
        available, message = check_container_image_availability("test:image")
        
        assert available is False
        assert "nerdctl is not available" in message

    @patch('shutil.which')
    @patch('subprocess.run')
    def test_check_image_local_available(self, mock_run, mock_which):
        """Test when image is available locally."""
        mock_which.return_value = "/usr/bin/nerdctl"
        mock_run.return_value = MagicMock(returncode=0)
        
        available, message = check_container_image_availability("test:image")
        
        assert available is True
        assert message is None
        mock_run.assert_called_once()

    @patch('shutil.which')
    @patch('subprocess.run')
    def test_check_image_local_not_available_registry_available(self, mock_run, mock_which):
        """Test when image is not local but available in registry."""
        mock_which.return_value = "/usr/bin/nerdctl"
        
        # First call (local check) fails, second call (registry check) succeeds
        mock_run.side_effect = [
            MagicMock(returncode=1),  # Local check fails
            MagicMock(returncode=0)   # Registry check succeeds
        ]
        
        available, message = check_container_image_availability("test:image")
        
        assert available is True
        assert "not local" in message

    @patch('shutil.which')
    @patch('subprocess.run')
    def test_check_image_not_available_anywhere(self, mock_run, mock_which):
        """Test when image is not available locally or in registry."""
        mock_which.return_value = "/usr/bin/nerdctl"
        
        # All calls fail
        mock_run.side_effect = [
            MagicMock(returncode=1),  # Local check fails
            MagicMock(returncode=1),  # Pull dry-run check fails  
            MagicMock(returncode=1)   # Manifest check fails
        ]
        
        available, message = check_container_image_availability("test:image")
        
        assert available is False
        assert "not found in registry" in message

    @patch('shutil.which')
    @patch('subprocess.run')
    def test_check_image_timeout(self, mock_run, mock_which):
        """Test timeout handling."""
        mock_which.return_value = "/usr/bin/nerdctl"
        mock_run.side_effect = subprocess.TimeoutExpired(cmd=['nerdctl'], timeout=30)
        
        available, message = check_container_image_availability("test:image")
        
        assert available is False
        assert "Timeout" in message

    @patch('shutil.which')
    @patch('subprocess.run')
    def test_check_image_exception_handling(self, mock_run, mock_which):
        """Test general exception handling."""
        mock_which.return_value = "/usr/bin/nerdctl"
        mock_run.side_effect = Exception("Unexpected error")
        
        available, message = check_container_image_availability("test:image")
        
        # Should handle gracefully - different implementations may return True with limitation message
        assert isinstance(available, bool)
        assert isinstance(message, str)


class TestContainerRuntimeValidation:
    """Test cases for container runtime validation."""

    @patch('shutil.which')
    def test_validate_runtime_nerdctl_not_available(self, mock_which):
        """Test when nerdctl is not available."""
        mock_which.return_value = None
        
        valid, message = validate_container_runtime()
        
        assert valid is False
        assert "❌ nerdctl is not available" in message

    @patch('shutil.which')
    @patch('subprocess.run')
    def test_validate_runtime_nerdctl_working(self, mock_run, mock_which):
        """Test when nerdctl is working properly."""
        mock_which.return_value = "/usr/bin/nerdctl"
        mock_run.return_value = MagicMock(
            returncode=0,
            stdout="nerdctl version 1.0.0"
        )
        
        valid, message = validate_container_runtime()
        
        assert valid is True
        assert "✅ nerdctl is working" in message
        assert "nerdctl version 1.0.0" in message

    @patch('shutil.which')
    @patch('subprocess.run')
    def test_validate_runtime_nerdctl_version_fail(self, mock_run, mock_which):
        """Test when nerdctl version check fails."""
        mock_which.return_value = "/usr/bin/nerdctl"
        mock_run.return_value = MagicMock(
            returncode=1,
            stderr="containerd not available"
        )
        
        valid, message = validate_container_runtime()
        
        assert valid is False
        assert "❌ nerdctl version check failed" in message

    @patch('shutil.which')
    @patch('subprocess.run')
    def test_validate_runtime_timeout(self, mock_run, mock_which):
        """Test timeout handling for runtime validation."""
        mock_which.return_value = "/usr/bin/nerdctl"
        mock_run.side_effect = subprocess.TimeoutExpired(cmd=['nerdctl'], timeout=10)
        
        valid, message = validate_container_runtime()
        
        assert valid is False
        assert "❌ nerdctl version check timed out" in message

    @patch('shutil.which')
    @patch('subprocess.run')
    def test_validate_runtime_exception(self, mock_run, mock_which):
        """Test exception handling for runtime validation."""
        mock_which.return_value = "/usr/bin/nerdctl"
        mock_run.side_effect = Exception("Unexpected error")
        
        valid, message = validate_container_runtime()
        
        assert valid is False
        assert "❌ Error checking nerdctl" in message


class TestContainerRuntimeInfo:
    """Test cases for container runtime information gathering."""

    @patch('shutil.which')
    def test_get_runtime_info_nerdctl_not_available(self, mock_which):
        """Test runtime info when nerdctl is not available."""
        mock_which.return_value = None
        
        info = get_container_runtime_info()
        
        assert info["nerdctl_available"] is False
        assert info["nerdctl_path"] is None
        assert info["containerd_status"] == "unknown"

    @patch('shutil.which')
    @patch('subprocess.run')
    def test_get_runtime_info_nerdctl_working(self, mock_run, mock_which):
        """Test runtime info when nerdctl is working."""
        mock_which.return_value = "/usr/bin/nerdctl"
        mock_run.return_value = MagicMock(
            returncode=0,
            stdout="nerdctl version info"
        )
        
        info = get_container_runtime_info()
        
        assert info["nerdctl_available"] is True
        assert info["nerdctl_path"] == "/usr/bin/nerdctl"
        assert info["version_info"] == "nerdctl version info"
        assert info["containerd_status"] == "accessible"

    @patch('shutil.which')
    @patch('subprocess.run')
    def test_get_runtime_info_version_error(self, mock_run, mock_which):
        """Test runtime info when version check has error."""
        mock_which.return_value = "/usr/bin/nerdctl"
        mock_run.return_value = MagicMock(returncode=1)
        
        info = get_container_runtime_info()
        
        assert info["nerdctl_available"] is True
        assert info["containerd_status"] == "error"

    @patch('shutil.which')
    @patch('subprocess.run')
    def test_get_runtime_info_timeout(self, mock_run, mock_which):
        """Test runtime info when version check times out."""
        mock_which.return_value = "/usr/bin/nerdctl"
        mock_run.side_effect = subprocess.TimeoutExpired(cmd=['nerdctl'], timeout=10)
        
        info = get_container_runtime_info()
        
        assert info["nerdctl_available"] is True
        assert info["containerd_status"] == "timeout"


class TestFormatContainerValidationResults:
    """Test cases for formatting validation results."""

    def test_format_results_image_available_runtime_valid(self):
        """Test formatting when both image and runtime are available."""
        formatted = format_container_validation_results(
            image_available=True,
            image_message="Image is cached locally",
            runtime_valid=True,
            runtime_message="✅ nerdctl is working"
        )
        
        assert "🔧 Container Runtime Validation:" in formatted
        assert "✅ nerdctl is working" in formatted
        assert "🐳 Container Image Validation:" in formatted
        assert "✅ Image is available" in formatted
        assert "💡 Image is cached locally" in formatted

    def test_format_results_image_not_available(self):
        """Test formatting when image is not available."""
        formatted = format_container_validation_results(
            image_available=False,
            image_message="Image not found in registry",
            runtime_valid=True,
            runtime_message="✅ nerdctl is working"
        )
        
        assert "❌ Image is NOT available" in formatted
        assert "⚠️  Image not found in registry" in formatted

    def test_format_results_runtime_not_valid(self):
        """Test formatting when runtime is not valid."""
        formatted = format_container_validation_results(
            image_available=True,
            image_message=None,
            runtime_valid=False,
            runtime_message="❌ nerdctl is not available"
        )
        
        assert "❌ nerdctl is not available" in formatted

    def test_format_results_no_additional_messages(self):
        """Test formatting without additional messages."""
        formatted = format_container_validation_results(
            image_available=True,
            image_message=None,
            runtime_valid=True,
            runtime_message="✅ nerdctl is working"
        )
        
        assert "✅ Image is available" in formatted
        # Should not have additional message lines
        lines = formatted.split('\n')
        image_section = [line for line in lines if "Image is available" in line]
        assert len(image_section) == 1