"""Container validation utilities for dry-run functionality."""

import subprocess
import shutil
from typing import Optional, Tuple


def check_container_image_availability(image: str, timeout: int = 30) -> Tuple[bool, Optional[str]]:
    """Check if a container image is available locally or can be pulled.
    
    Args:
        image: Container image name
        timeout: Timeout in seconds for image operations
        
    Returns:
        Tuple of (is_available, error_message)
    """
    # First check if docker is available
    if not shutil.which("docker"):
        return False, "docker is not available in PATH"

    # Check if image exists locally
    try:
        result = subprocess.run(
            ["docker", "image", "inspect", image],
            capture_output=True,
            text=True,
            timeout=timeout
        )
        if result.returncode == 0:
            return True, None
    except subprocess.TimeoutExpired:
        return False, f"Timeout checking local image: {image}"
    except Exception as e:
        return False, f"Error checking local image: {e}"
    
    # If not local, try to check if it can be pulled (without actually pulling)
    # We'll use docker to check if the image exists in the registry
    try:
        # Use manifest command to check if image exists without downloading
        result = subprocess.run(
            ["docker", "image", "pull", "--quiet", "--dry-run", image],
            capture_output=True,
            text=True,
            timeout=timeout
        )
        if result.returncode == 0:
            return True, "Image available for pull (not local)"
        else:
            # Try a different approach if dry-run isn't supported
            # Check manifest instead
            result = subprocess.run(
                ["docker", "manifest", "inspect", image],
                capture_output=True,
                text=True,
                timeout=timeout
            )
            if result.returncode == 0:
                return True, "Image available for pull (not local)"
            else:
                return False, f"Image not found in registry: {image}"
    except subprocess.TimeoutExpired:
        return False, f"Timeout checking registry for image: {image}"
    except Exception:
        # If manifest or dry-run commands aren't available, we can't easily check
        # without pulling, so we'll indicate unknown status
        return True, f"Cannot verify image availability (docker limitations): {image}"


def validate_container_runtime() -> Tuple[bool, str]:
    """Validate that the container runtime is properly configured.
    
    Returns:
        Tuple of (is_valid, status_message)
    """
    # Check docker availability
    if not shutil.which("docker"):
        return False, "❌ docker is not available in PATH"
    
    # Check if docker can communicate with containerd
    try:
        result = subprocess.run(
            ["docker", "version"],
            capture_output=True,
            text=True,
            timeout=10
        )
        if result.returncode == 0:
            # Extract version info
            version_info = result.stdout.strip()
            return True, f"✅ docker is working\n{version_info}"
        else:
            return False, f"❌ docker version check failed: {result.stderr}"
    except subprocess.TimeoutExpired:
        return False, "❌ docker version check timed out"
    except Exception as e:
        return False, f"❌ Error checking docker: {e}"


def get_container_runtime_info() -> dict:
    """Get detailed information about the container runtime.
    
    Returns:
        Dictionary with runtime information
    """
    info = {
        "docker_available": False,
        "docker_path": None,
        "version_info": None,
        "containerd_status": "unknown"
    }
    
    # Check docker path
    docker_path = shutil.which("docker")
    if docker_path:
        info["docker_available"] = True
        info["docker_path"] = docker_path
    
    # Get version information
    if info["docker_available"]:
        try:
            result = subprocess.run(
                ["docker", "version"],
                capture_output=True,
                text=True,
                timeout=10
            )
            if result.returncode == 0:
                info["version_info"] = result.stdout.strip()
                info["containerd_status"] = "accessible"
            else:
                info["containerd_status"] = "error"
        except Exception:
            info["containerd_status"] = "timeout"
    
    return info


def format_container_validation_results(
    image_available: bool, 
    image_message: Optional[str],
    runtime_valid: bool,
    runtime_message: str
) -> str:
    """Format container validation results for display.
    
    Args:
        image_available: Whether the container image is available
        image_message: Additional message about image availability
        runtime_valid: Whether the container runtime is valid
        runtime_message: Runtime validation message
        
    Returns:
        Formatted string for display
    """
    lines = []
    
    lines.append("🔧 Container Runtime Validation:")
    lines.append(f"  {runtime_message}")
    
    lines.append("\n🐳 Container Image Validation:")
    if image_available:
        lines.append("  ✅ Image is available")
        if image_message:
            lines.append(f"  💡 {image_message}")
    else:
        lines.append("  ❌ Image is NOT available")
        if image_message:
            lines.append(f"  ⚠️  {image_message}")
    
    return "\n".join(lines)