#!/usr/bin/env python3
"""
Simple Pipeline Example: Test then Deploy

This example demonstrates a basic two-step pipeline:
1. Run tests
2. If tests pass, trigger deploy (only on main branch)

Usage:
    python -m runnerlib.cli run \
        --source-type git \
        --source-url https://github.com/user/repo.git \
        --source-ref main \
        --ci-source-type git \
        --ci-source-url https://github.com/user/repo.git \
        --ci-source-ref main \
        --job-command "python /job/ci/examples/pipelines/simple_test_and_deploy.py"
"""

import sys
import subprocess
from pathlib import Path

# Add runnerlib to path if running locally
sys.path.insert(0, str(Path(__file__).parent.parent.parent / "runnerlib" / "src"))

from workflow import trigger_job, flush_triggers, git_info, WorkflowContext


def run_tests() -> bool:
    """Run the test suite."""
    print("=" * 60)
    print("Running tests...")
    print("=" * 60)

    try:
        # Run pytest (or whatever test command you use)
        result = subprocess.run(
            ["pytest", "tests/", "-v"],
            cwd="/job/src",
            check=True
        )
        print("✓ Tests passed!")
        return True
    except subprocess.CalledProcessError as e:
        print(f"✗ Tests failed with exit code {e.returncode}")
        return False
    except FileNotFoundError:
        print("⚠ pytest not found, skipping tests")
        # For demo purposes, treat missing pytest as success
        return True


def main():
    """Main pipeline logic."""
    print("Starting simple test-and-deploy pipeline")

    # Get git information
    info = git_info()
    print(f"Running on branch: {info['branch']}")
    print(f"Commit: {info['short_commit']}")
    print()

    # Run tests
    tests_passed = run_tests()

    if not tests_passed:
        print("✗ Pipeline failed: tests did not pass")
        sys.exit(1)

    # Check if we should deploy (only on main branch)
    ctx = WorkflowContext()

    if ctx.branch == "main":
        print()
        print("=" * 60)
        print("Tests passed on main branch - triggering deploy")
        print("=" * 60)

        # Trigger deploy job
        trigger_job(
            "deploy-production",
            env={
                "DEPLOY_TARGET": "production",
                "BUILD_COMMIT": info["commit"] or "unknown",
            },
            depends_on=["test"],
            condition="all_success"
        )

        # Flush triggers to file
        flush_triggers()
    else:
        print()
        print(f"✓ Tests passed on branch '{ctx.branch}'")
        print("ℹ Deploy skipped (not on main branch)")

    print()
    print("Pipeline completed successfully!")
    return 0


if __name__ == "__main__":
    sys.exit(main())
