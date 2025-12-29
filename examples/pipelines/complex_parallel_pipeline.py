#!/usr/bin/env python3
"""
Complex Pipeline Example: Parallel Tests, Build, and Conditional Deploy

This example demonstrates a sophisticated multi-step pipeline:
1. Run tests and lint in parallel (both must pass)
2. If both pass, trigger build job
3. Build job creates artifacts
4. If build succeeds and on main branch, conditionally deploy
5. Check if deploy is already running before triggering

Pipeline Flow:
    ┌─────────┐
    │  Ingest │
    └────┬────┘
         │
    ┌────┴────┐
    │         │
    ▼         ▼
  ┌────┐   ┌──────┐
  │Test│   │ Lint │
  └─┬──┘   └───┬──┘
    └──────┬───┘
           ▼
       ┌───────┐
       │ Build │
       └───┬───┘
           ▼
      ┌────────┐
      │ Deploy │ (conditional: main branch, no deploy running)
      └────────┘

Usage:
    python -m runnerlib.cli run \
        --source-type git \
        --source-url https://github.com/user/repo.git \
        --source-ref main \
        --ci-source-type git \
        --ci-source-url https://github.com/user/repo.git \
        --ci-source-ref main \
        --job-command "python /job/ci/examples/pipelines/complex_parallel_pipeline.py"
"""

import sys
import subprocess
from pathlib import Path

# Add runnerlib to path if running locally
sys.path.insert(0, str(Path(__file__).parent.parent.parent / "runnerlib" / "src"))

from workflow import (
    trigger_job,
    flush_triggers,
    git_info,
    changed_files,
    is_job_running,
    WorkflowContext,
)


def analyze_changes():
    """Analyze what files changed to determine what needs to run."""
    print("=" * 60)
    print("Analyzing changes...")
    print("=" * 60)

    try:
        files = changed_files("origin/main", "HEAD")
        print(f"Found {len(files)} changed file(s):")
        for f in files[:10]:  # Show first 10
            print(f"  - {f}")
        if len(files) > 10:
            print(f"  ... and {len(files) - 10} more")

        # Determine what needs to run based on changes
        needs_test = any(f.endswith(".py") for f in files)
        needs_lint = any(f.endswith((".py", ".js", ".ts")) for f in files)
        needs_docs = any("docs/" in f for f in files)

        print()
        print(f"Analysis:")
        print(f"  - Run tests: {needs_test}")
        print(f"  - Run linter: {needs_lint}")
        print(f"  - Update docs: {needs_docs}")

        return {
            "needs_test": needs_test,
            "needs_lint": needs_lint,
            "needs_docs": needs_docs,
            "changed_files": files,
        }
    except Exception as e:
        print(f"⚠ Could not analyze changes: {e}")
        # Default: run everything
        return {
            "needs_test": True,
            "needs_lint": True,
            "needs_docs": False,
            "changed_files": [],
        }


def main():
    """Main ingest pipeline logic."""
    print("Starting complex parallel pipeline (ingest phase)")
    print()

    # Get git information
    info = git_info()
    print(f"Repository: {info['remote_url']}")
    print(f"Branch: {info['branch']}")
    print(f"Commit: {info['commit']}")
    if info['tag']:
        print(f"Tag: {info['tag']}")
    print()

    # Analyze what changed
    analysis = analyze_changes()
    print()

    # Create workflow context
    ctx = WorkflowContext()

    # Trigger parallel test and lint jobs
    print("=" * 60)
    print("Scheduling parallel jobs...")
    print("=" * 60)

    jobs_to_trigger = []

    if analysis["needs_test"]:
        print("✓ Scheduling: test job")
        trigger_job(
            "test",
            env={
                "TEST_SUITE": "full",
                "COMMIT": info["commit"] or "unknown",
            },
            # Test job has no dependencies, runs immediately
        )
        jobs_to_trigger.append("test")

    if analysis["needs_lint"]:
        print("✓ Scheduling: lint job")
        trigger_job(
            "lint",
            env={
                "LINT_FILES": ",".join(analysis["changed_files"][:50]),  # Pass file list
                "COMMIT": info["commit"] or "unknown",
            },
            # Lint job has no dependencies, runs immediately (parallel with test)
        )
        jobs_to_trigger.append("lint")

    # Schedule build job (depends on test and lint)
    if jobs_to_trigger:
        print("✓ Scheduling: build job (waits for test + lint)")
        trigger_job(
            "build",
            env={
                "BUILD_TYPE": "release" if ctx.branch == "main" else "debug",
                "COMMIT": info["commit"] or "unknown",
                "TAG": info["tag"] or "",
            },
            depends_on=jobs_to_trigger,
            condition="all_success",  # Only build if both test and lint pass
            container_image="reactorcide/runner:latest",
            job_command="make build && make package",
        )

        # Schedule conditional deploy (depends on build, only on main)
        if ctx.branch == "main":
            # Check if there's already a deploy running
            deploy_already_running = is_job_running("deploy-production")

            if deploy_already_running:
                print("ℹ Deploy already running, skipping deploy trigger")
            else:
                print("✓ Scheduling: deploy job (waits for build, main branch only)")
                trigger_job(
                    "deploy-production",
                    env={
                        "DEPLOY_TARGET": "production",
                        "BUILD_COMMIT": info["commit"] or "unknown",
                        "BUILD_TAG": info["tag"] or "",
                    },
                    depends_on=["build"],
                    condition="all_success",
                    container_image="reactorcide/runner:latest",
                    job_command="python /job/ci/scripts/deploy.py",
                    priority=10,  # High priority for production deploys
                    timeout=1800,  # 30 minute timeout
                )
        else:
            print(f"ℹ Deploy skipped (not on main branch, current: {ctx.branch})")

    else:
        print("ℹ No jobs needed based on changes")

    print()
    print("=" * 60)
    print("Flushing triggers...")
    print("=" * 60)

    # Flush all triggers to file
    flush_triggers()

    print()
    print("✓ Ingest pipeline completed successfully!")
    print()
    print("Scheduled jobs will execute based on their dependencies:")
    if analysis["needs_test"] and analysis["needs_lint"]:
        print("  1. test + lint (parallel)")
        print("  2. build (waits for test + lint)")
        if ctx.branch == "main":
            print("  3. deploy-production (waits for build)")
    elif analysis["needs_test"]:
        print("  1. test")
        print("  2. build (waits for test)")
    else:
        print("  (No jobs scheduled)")

    return 0


if __name__ == "__main__":
    sys.exit(main())
