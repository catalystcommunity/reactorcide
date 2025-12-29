"""Simple integration test for Docker container execution."""

import subprocess
import sys
import tempfile
from pathlib import Path


def test_basic_docker_execution():
    """Test that we can execute a simple container with Docker."""

    # Create a temporary working directory
    with tempfile.TemporaryDirectory() as tmpdir:
        work_dir = Path(tmpdir)

        # Create job directory structure
        job_dir = work_dir / "job"
        job_dir.mkdir()

        # Create a simple test script
        test_script = job_dir / "test.sh"
        test_script.write_text("""#!/bin/sh
echo "Hello from Docker container"
echo "Current directory: $(pwd)"
echo "Job directory contents:"
ls -la /job/
exit 0
""")
        test_script.chmod(0o755)

        # Run the container using runnerlib CLI
        result = subprocess.run(
            [
                sys.executable, "-m", "src.cli", "run",
                "--runner-image", "alpine:latest",
                "--job-command", "sh /job/test.sh",
                "--code-dir", "/job",
                "--job-dir", "/job",
            ],
            capture_output=True,
            text=True,
            cwd=work_dir,  # Run from the temp directory
            env={**subprocess.os.environ, "PYTHONPATH": str(Path(__file__).parent.parent)}
        )

        print("STDOUT:", result.stdout)
        print("STDERR:", result.stderr)
        print("Return code:", result.returncode)

        # Verify the execution
        assert result.returncode == 0, f"Container execution failed with code {result.returncode}"
        assert "Hello from Docker container" in result.stdout
        assert "Current directory: /job" in result.stdout
        assert "test.sh" in result.stdout


def test_docker_with_environment_variables():
    """Test Docker execution with environment variables."""

    with tempfile.TemporaryDirectory() as tmpdir:
        work_dir = Path(tmpdir)

        # Create job directory
        job_dir = work_dir / "job"
        job_dir.mkdir()

        # Create script that uses environment variables
        test_script = job_dir / "env_test.sh"
        test_script.write_text("""#!/bin/sh
echo "TEST_VAR=$TEST_VAR"
echo "CUSTOM_VAR=$CUSTOM_VAR"
if [ "$TEST_VAR" = "test_value" ]; then
    echo "Environment variables work!"
    exit 0
else
    echo "Environment variables failed"
    exit 1
fi
""")
        test_script.chmod(0o755)

        # Create env file (use relative path from working directory)
        env_file = job_dir / "test.env"
        env_file.write_text("""# Test environment
TEST_VAR=test_value
CUSTOM_VAR=custom_value
""")

        # Run with environment file - needs to be relative path starting with ./job/
        result = subprocess.run(
            [
                sys.executable, "-m", "src.cli", "run",
                "--runner-image", "alpine:latest",
                "--job-command", "sh /job/env_test.sh",
                "--code-dir", "/job",
                "--job-dir", "/job",
                "--job-env", "./job/test.env",
            ],
            capture_output=True,
            text=True,
            cwd=work_dir,
            env={**subprocess.os.environ, "PYTHONPATH": str(Path(__file__).parent.parent)}
        )

        print("ENV TEST STDOUT:", result.stdout)
        print("ENV TEST STDERR:", result.stderr)

        assert result.returncode == 0, f"Environment test failed with code {result.returncode}"
        # Values should be masked in output for security (non-REACTORCIDE vars are masked by default)
        assert "TEST_VAR=[REDACTED]" in result.stdout
        assert "CUSTOM_VAR=[REDACTED]" in result.stdout
        # The script logic should still work correctly with the actual values
        assert "Environment variables work!" in result.stdout


def test_docker_with_python():
    """Test running Python code in a container."""

    with tempfile.TemporaryDirectory() as tmpdir:
        work_dir = Path(tmpdir)

        # Create job directory
        job_dir = work_dir / "job"
        job_dir.mkdir()

        # Create Python script
        py_script = job_dir / "test.py"
        py_script.write_text("""
import sys
import os

print(f"Python version: {sys.version.split()[0]}")
print(f"Working directory: {os.getcwd()}")
print(f"Job files: {os.listdir('/job')}")

# Test that we can write output
with open('/job/output.txt', 'w') as f:
    f.write("Test output from Python container\\n")

print("Successfully wrote output file")
sys.exit(0)
""")

        # Run Python container
        result = subprocess.run(
            [
                sys.executable, "-m", "src.cli", "run",
                "--runner-image", "python:3.11-alpine",
                "--job-command", "python /job/test.py",
                "--code-dir", "/job",
                "--job-dir", "/job",
            ],
            capture_output=True,
            text=True,
            cwd=work_dir,
            env={**subprocess.os.environ, "PYTHONPATH": str(Path(__file__).parent.parent)}
        )

        print("PYTHON TEST STDOUT:", result.stdout)
        print("PYTHON TEST STDERR:", result.stderr)

        assert result.returncode == 0, f"Python container failed with code {result.returncode}"
        assert "Python version: 3.11" in result.stdout
        assert "Working directory: /job" in result.stdout
        assert "Successfully wrote output file" in result.stdout

        # Check that the output file was created
        output_file = job_dir / "output.txt"
        assert output_file.exists(), "Output file was not created"
        assert "Test output from Python container" in output_file.read_text()


def test_docker_failure_handling():
    """Test that container failures are properly reported."""

    with tempfile.TemporaryDirectory() as tmpdir:
        work_dir = Path(tmpdir)

        # Create job directory
        job_dir = work_dir / "job"
        job_dir.mkdir()

        # Create a script that fails
        fail_script = job_dir / "fail.sh"
        fail_script.write_text("""#!/bin/sh
echo "This script will fail"
echo "Error: Something went wrong" >&2
exit 42
""")
        fail_script.chmod(0o755)

        # Run container that should fail
        result = subprocess.run(
            [
                sys.executable, "-m", "src.cli", "run",
                "--runner-image", "alpine:latest",
                "--job-command", "sh /job/fail.sh",
                "--code-dir", "/job",
                "--job-dir", "/job",
            ],
            capture_output=True,
            text=True,
            cwd=work_dir,
            env={**subprocess.os.environ, "PYTHONPATH": str(Path(__file__).parent.parent)}
        )

        print("FAIL TEST STDOUT:", result.stdout)
        print("FAIL TEST STDERR:", result.stderr)
        print("FAIL TEST RETURN CODE:", result.returncode)

        # Should propagate the exit code
        assert result.returncode == 42, f"Expected exit code 42, got {result.returncode}"
        assert "This script will fail" in result.stdout
        # stderr might be mixed with stdout or separate depending on implementation


if __name__ == "__main__":
    # Run tests manually for debugging
    print("Testing basic Docker execution...")
    test_basic_docker_execution()
    print("✓ Basic execution passed\n")

    print("Testing environment variables...")
    test_docker_with_environment_variables()
    print("✓ Environment variables passed\n")

    print("Testing Python container...")
    test_docker_with_python()
    print("✓ Python container passed\n")

    print("Testing failure handling...")
    test_docker_failure_handling()
    print("✓ Failure handling passed\n")

    print("All tests passed!")


# Additional unique tests not covered above

def test_docker_available():
    """Test that Docker is available and working."""
    result = subprocess.run(
        ["docker", "version", "--format", "{{.Server.Version}}"],
        capture_output=True,
        text=True
    )
    assert result.returncode == 0, "Docker is not available"
    assert result.stdout.strip(), "Docker version not found"


def test_container_with_working_directory():
    """Test that working directory is set correctly in container."""
    with tempfile.TemporaryDirectory() as tmpdir:
        work_dir = Path(tmpdir)

        # Create job directory with subdirectory
        job_dir = work_dir / "job"
        job_dir.mkdir()
        sub_dir = job_dir / "subdir"
        sub_dir.mkdir()

        # Create test file in subdirectory
        test_file = sub_dir / "data.txt"
        test_file.write_text("test data")

        # Create script that checks working directory
        test_script = job_dir / "pwd_test.sh"
        test_script.write_text("""#!/bin/sh
echo "Current directory: $(pwd)"
echo "Directory contents:"
ls -la
echo "Subdir exists:"
ls -d subdir
exit 0
""")
        test_script.chmod(0o755)

        # Run with working directory set to /job
        result = subprocess.run(
            [
                sys.executable, "-m", "src.cli", "run",
                "--runner-image", "alpine:latest",
                "--job-command", "sh pwd_test.sh",  # Note: no /job/ prefix since we're in that dir
                "--code-dir", "/job",
                "--job-dir", "/job",
            ],
            capture_output=True,
            text=True,
            cwd=work_dir,
            env={**subprocess.os.environ, "PYTHONPATH": str(Path(__file__).parent.parent)}
        )

        print("WORKING DIR TEST:", result.stdout)
        print("STDERR:", result.stderr)

        assert result.returncode == 0, f"Container execution failed: {result.stderr}"
        assert "Current directory: /job" in result.stdout, "Working directory not set correctly"
        assert "subdir" in result.stdout, "Subdirectory not visible"
        assert "pwd_test.sh" in result.stdout, "Test script not visible"


def test_dry_run_mode():
    """Test dry-run mode doesn't actually execute container."""
    with tempfile.TemporaryDirectory() as tmpdir:
        work_dir = Path(tmpdir)

        # Create job directory
        job_dir = work_dir / "job"
        job_dir.mkdir()

        # Create a script that should NOT run
        test_script = job_dir / "should_not_run.sh"
        test_script.write_text("""#!/bin/sh
echo "ERROR: This should not execute in dry-run mode!"
exit 1
""")
        test_script.chmod(0o755)

        # Run in dry-run mode
        result = subprocess.run(
            [
                sys.executable, "-m", "src.cli", "run",
                "--runner-image", "alpine:latest",
                "--job-command", "sh /job/should_not_run.sh",
                "--code-dir", "/job",
                "--job-dir", "/job",
                "--dry-run",
            ],
            capture_output=True,
            text=True,
            cwd=work_dir,
            env={**subprocess.os.environ, "PYTHONPATH": str(Path(__file__).parent.parent)}
        )

        print("DRY RUN OUTPUT:", result.stdout)
        print("DRY RUN STDERR:", result.stderr)

        assert result.returncode == 0, f"Dry-run failed: {result.stderr}"
        assert "DRY RUN MODE" in result.stdout, "Dry run mode not indicated"
        assert "ERROR: This should not execute" not in result.stdout, "Script was executed in dry-run mode!"
        assert "docker run" in result.stdout, "Docker command not shown in dry-run"
        # Should show what WOULD be executed
        assert "alpine:latest" in result.stdout, "Image not shown in dry-run"


def test_node_container():
    """Test Node.js container execution."""
    with tempfile.TemporaryDirectory() as tmpdir:
        work_dir = Path(tmpdir)

        # Create job directory
        job_dir = work_dir / "job"
        job_dir.mkdir()

        # Create Node.js script
        js_script = job_dir / "test.js"
        js_script.write_text("""
console.log('Node version:', process.version);
console.log('Platform:', process.platform);
console.log('Working dir:', process.cwd());
process.exit(0);
""")

        # Run Node container
        result = subprocess.run(
            [
                sys.executable, "-m", "src.cli", "run",
                "--runner-image", "node:18-alpine",
                "--job-command", "node /job/test.js",
                "--code-dir", "/job",
                "--job-dir", "/job",
            ],
            capture_output=True,
            text=True,
            cwd=work_dir,
            env={**subprocess.os.environ, "PYTHONPATH": str(Path(__file__).parent.parent)}
        )

        print("NODE TEST OUTPUT:", result.stdout)
        print("NODE TEST STDERR:", result.stderr)

        assert result.returncode == 0, f"Node container failed: {result.stderr}"
        assert "Node version: v18" in result.stdout, "Node version not correct"
        assert "Platform: linux" in result.stdout, "Platform not correct"
        assert "Working dir: /job" in result.stdout, "Working directory not correct"


def test_container_with_multiple_env_vars():
    """Test passing multiple environment variables via CLI."""
    with tempfile.TemporaryDirectory() as tmpdir:
        work_dir = Path(tmpdir)

        # Create job directory
        job_dir = work_dir / "job"
        job_dir.mkdir()

        # Create test script
        test_script = job_dir / "multi_env.sh"
        test_script.write_text("""#!/bin/sh
echo "VAR1=$VAR1"
echo "VAR2=$VAR2"
echo "VAR3=$VAR3"
if [ "$VAR1" = "value1" ] && [ "$VAR2" = "value2" ] && [ "$VAR3" = "value3" ]; then
    echo "All environment variables set correctly!"
    exit 0
else
    echo "Environment variables not set correctly"
    exit 1
fi
""")
        test_script.chmod(0o755)

        # Run with multiple env vars in a single --job-env (newline separated)
        result = subprocess.run(
            [
                sys.executable, "-m", "src.cli", "run",
                "--runner-image", "alpine:latest",
                "--job-command", "sh /job/multi_env.sh",
                "--code-dir", "/job",
                "--job-dir", "/job",
                "--job-env", "VAR1=value1\nVAR2=value2\nVAR3=value3",
            ],
            capture_output=True,
            text=True,
            cwd=work_dir,
            env={**subprocess.os.environ, "PYTHONPATH": str(Path(__file__).parent.parent)}
        )

        assert result.returncode == 0, f"Multi-env test failed: {result.stderr}"
        # Values should be masked in output for security (non-REACTORCIDE vars are masked by default)
        assert "VAR1=[REDACTED]" in result.stdout
        assert "VAR2=[REDACTED]" in result.stdout
        assert "VAR3=[REDACTED]" in result.stdout
        assert "All environment variables set correctly!" in result.stdout


def test_selective_secret_masking():
    """Test selective masking of secrets using --secrets-list."""
    with tempfile.TemporaryDirectory() as tmpdir:
        work_dir = Path(tmpdir)

        # Create job directory
        job_dir = work_dir / "job"
        job_dir.mkdir()

        # Create test script that prints environment variables
        test_script = job_dir / "selective_test.sh"
        test_script.write_text("""#!/bin/sh
echo "API_KEY=$API_KEY"
echo "PUBLIC_VALUE=$PUBLIC_VALUE"
echo "SECRET_TOKEN=$SECRET_TOKEN"
echo "CONFIG_PATH=$CONFIG_PATH"
exit 0
""")
        test_script.chmod(0o755)

        # Run with environment vars and explicitly mark only some as secrets
        result = subprocess.run(
            [
                sys.executable, "-m", "src.cli", "run",
                "--runner-image", "alpine:latest",
                "--job-command", "sh /job/selective_test.sh",
                "--code-dir", "/job",
                "--job-dir", "/job",
                "--job-env", "API_KEY=my-secret-api-key-123\nPUBLIC_VALUE=not-a-secret\nSECRET_TOKEN=super-secret-token\nCONFIG_PATH=/etc/config",
                "--secrets-list", "my-secret-api-key-123,super-secret-token",  # Only mask these specific values
            ],
            capture_output=True,
            text=True,
            cwd=work_dir,
            env={**subprocess.os.environ, "PYTHONPATH": str(Path(__file__).parent.parent)}
        )

        assert result.returncode == 0, f"Selective masking test failed: {result.stderr}"

        # Only explicitly marked secrets should be masked
        assert "API_KEY=[REDACTED]" in result.stdout  # Marked as secret
        assert "SECRET_TOKEN=[REDACTED]" in result.stdout  # Marked as secret

        # These should NOT be masked since they weren't in the secrets list
        assert "PUBLIC_VALUE=not-a-secret" in result.stdout  # Not in secrets list
        assert "CONFIG_PATH=/etc/config" in result.stdout  # Not in secrets list


