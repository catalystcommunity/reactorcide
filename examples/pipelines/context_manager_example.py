#!/usr/bin/env python3
"""
Context Manager Example: Automatic Trigger Flushing

This example demonstrates using the workflow_context context manager
for automatic trigger flushing and cleaner code.

The context manager ensures triggers are flushed even if exceptions occur,
and provides a cleaner API for accessing workflow context.

Usage:
    python -m runnerlib.cli run \
        --source-type git \
        --source-url https://github.com/user/repo.git \
        --source-ref main \
        --job-command "python /job/ci/examples/pipelines/context_manager_example.py"
"""

import sys
import subprocess
from pathlib import Path

# Add runnerlib to path if running locally
sys.path.insert(0, str(Path(__file__).parent.parent.parent / "runnerlib" / "src"))

from workflow import workflow_context, git_info


def run_security_scan() -> bool:
    """Run security vulnerability scan."""
    print("Running security scan...")

    try:
        # Example: run a security scanner like bandit
        result = subprocess.run(
            ["bandit", "-r", ".", "-f", "json", "-o", "/job/security-report.json"],
            cwd="/job/src",
            capture_output=True,
            text=True
        )

        # Bandit returns non-zero if issues found
        if result.returncode != 0:
            print("⚠ Security issues found!")
            return False

        print("✓ Security scan passed")
        return True

    except FileNotFoundError:
        print("ℹ bandit not found, skipping security scan")
        return True  # Don't fail if tool not available


def run_license_check() -> bool:
    """Check for license compliance."""
    print("Checking license compliance...")

    # This is a placeholder - in reality you'd use a tool like
    # license-checker, fossology, or similar
    print("✓ License check passed")
    return True


def main():
    """Main pipeline using context manager."""
    print("Starting security and compliance pipeline")
    print()

    # Get git info
    info = git_info()
    print(f"Analyzing commit: {info['short_commit']}")
    print()

    # Use context manager for automatic trigger flushing
    with workflow_context() as ctx:
        print("=" * 60)
        print("Running security checks...")
        print("=" * 60)

        security_passed = run_security_scan()
        license_passed = run_license_check()

        print()

        if not security_passed or not license_passed:
            print("✗ Security/compliance checks failed")
            # Triggers are NOT flushed on exception/failure
            sys.exit(1)

        print("✓ All security checks passed")
        print()

        # Trigger follow-up jobs based on context
        print("=" * 60)
        print("Scheduling follow-up jobs...")
        print("=" * 60)

        # Always run tests after security checks pass
        ctx.trigger_job(
            "test",
            env={
                "SECURITY_SCAN_PASSED": "true",
                "COMMIT": info["commit"] or "unknown",
            },
        )

        # If on a release branch or tag, trigger additional checks
        if ctx.branch and ctx.branch.startswith("release/"):
            print("✓ Release branch detected - scheduling release validation")
            ctx.trigger_job(
                "validate-release",
                env={
                    "RELEASE_BRANCH": ctx.branch,
                    "COMMIT": info["commit"] or "unknown",
                },
                depends_on=["test"],
            )

        if info["tag"]:
            print(f"✓ Tag detected ({info['tag']}) - scheduling artifact signing")
            ctx.trigger_job(
                "sign-artifacts",
                env={
                    "TAG": info["tag"],
                    "COMMIT": info["commit"] or "unknown",
                },
                depends_on=["test"],
            )

        # If this is a PR to main, trigger full integration tests
        target_branch = ctx.branch
        if target_branch == "main" or (target_branch and "pr-" in target_branch):
            print("✓ Main branch or PR - scheduling integration tests")
            ctx.trigger_job(
                "integration-test",
                env={
                    "TEST_SUITE": "integration",
                    "COMMIT": info["commit"] or "unknown",
                },
                depends_on=["test"],
                timeout=3600,  # 1 hour timeout for integration tests
            )

        print()
        print("✓ All jobs scheduled")

        # Triggers are automatically flushed when exiting the context manager
        # (no need to call flush_triggers() explicitly)

    print()
    print("Pipeline completed successfully!")
    return 0


if __name__ == "__main__":
    sys.exit(main())
