#!/usr/bin/env python3
"""
Reactorcide API Client Example

This example demonstrates how to interact with the Reactorcide API to:
- Create jobs that are submitted to Corndogs
- Monitor job status
- List and manage jobs

Requirements:
    pip install requests
"""

import os
import sys
import time
import json
from typing import Dict, List, Optional, Any
import requests


class ReactorcideClient:
    """Simple client for the Reactorcide API"""

    def __init__(self, base_url: str, token: str):
        """
        Initialize the Reactorcide client.

        Args:
            base_url: The base URL of the Reactorcide API (e.g., http://localhost:8080)
            token: The API authentication token
        """
        self.base_url = base_url.rstrip('/')
        self.token = token
        self.session = requests.Session()
        self.session.headers.update({
            'Authorization': f'Bearer {token}',
            'Content-Type': 'application/json'
        })

    def check_health(self) -> Dict[str, Any]:
        """Check the API health status."""
        response = self.session.get(f'{self.base_url}/api/v1/health')
        response.raise_for_status()
        return response.json()

    def create_job(self,
                   name: str,
                   job_command: str,
                   source_type: str = 'git',
                   git_url: Optional[str] = None,
                   git_ref: Optional[str] = None,
                   source_path: Optional[str] = None,
                   description: Optional[str] = None,
                   runner_image: str = 'alpine:latest',
                   job_env_vars: Optional[Dict[str, str]] = None,
                   queue_name: str = 'default',
                   timeout_seconds: Optional[int] = None,
                   priority: Optional[int] = None) -> Dict[str, Any]:
        """
        Create a new job that will be submitted to Corndogs.

        Args:
            name: The name of the job
            job_command: The command to execute
            source_type: Type of source ('git' or 'copy')
            git_url: Git repository URL (for git source type)
            git_ref: Git reference (branch/tag/commit)
            source_path: Path to source (for copy source type)
            description: Job description
            runner_image: Docker image to use for execution
            job_env_vars: Environment variables for the job
            queue_name: Corndogs queue name
            timeout_seconds: Job timeout in seconds
            priority: Job priority (higher = more important)

        Returns:
            The created job object
        """
        payload = {
            'name': name,
            'job_command': job_command,
            'source_type': source_type,
            'runner_image': runner_image,
            'queue_name': queue_name
        }

        if description:
            payload['description'] = description
        if git_url:
            payload['git_url'] = git_url
        if git_ref:
            payload['git_ref'] = git_ref
        if source_path:
            payload['source_path'] = source_path
        if job_env_vars:
            payload['job_env_vars'] = job_env_vars
        if timeout_seconds:
            payload['timeout_seconds'] = timeout_seconds
        if priority:
            payload['priority'] = priority

        response = self.session.post(
            f'{self.base_url}/api/v1/jobs',
            json=payload
        )
        response.raise_for_status()
        return response.json()

    def get_job(self, job_id: str) -> Dict[str, Any]:
        """Get a job by ID."""
        response = self.session.get(f'{self.base_url}/api/v1/jobs/{job_id}')
        response.raise_for_status()
        return response.json()

    def list_jobs(self, limit: int = 100, offset: int = 0) -> List[Dict[str, Any]]:
        """List all jobs."""
        response = self.session.get(
            f'{self.base_url}/api/v1/jobs',
            params={'limit': limit, 'offset': offset}
        )
        response.raise_for_status()
        result = response.json()
        return result.get('jobs', [])

    def cancel_job(self, job_id: str) -> None:
        """Cancel a running job."""
        response = self.session.put(f'{self.base_url}/api/v1/jobs/{job_id}/cancel')
        response.raise_for_status()

    def delete_job(self, job_id: str) -> None:
        """Delete a job."""
        response = self.session.delete(f'{self.base_url}/api/v1/jobs/{job_id}')
        response.raise_for_status()

    def wait_for_job(self, job_id: str, timeout: int = 300, poll_interval: int = 2) -> Dict[str, Any]:
        """
        Wait for a job to complete.

        Args:
            job_id: The job ID to wait for
            timeout: Maximum time to wait in seconds
            poll_interval: Time between status checks in seconds

        Returns:
            The final job object

        Raises:
            TimeoutError: If the job doesn't complete within the timeout
        """
        start_time = time.time()
        terminal_states = {'completed', 'failed', 'cancelled'}

        while time.time() - start_time < timeout:
            job = self.get_job(job_id)
            if job['status'] in terminal_states:
                return job
            time.sleep(poll_interval)

        raise TimeoutError(f"Job {job_id} did not complete within {timeout} seconds")


def example_git_job(client: ReactorcideClient):
    """Example: Create and monitor a job that clones a git repository"""
    print("\n=== Git Repository Job Example ===")

    job = client.create_job(
        name="Git Clone and Build",
        description="Clone a repository and run build commands",
        source_type="git",
        git_url="https://github.com/example/project.git",
        git_ref="main",
        job_command="make build && make test",
        runner_image="golang:1.21",
        job_env_vars={
            "GO111MODULE": "on",
            "CGO_ENABLED": "0"
        },
        queue_name="build-queue",
        timeout_seconds=600,
        priority=5
    )

    print(f"Created job: {job['job_id']}")
    print(f"Status: {job['status']}")
    print(f"Job submitted to Corndogs queue: {job.get('queue_name', 'default')}")

    # Monitor job progress
    print("\nMonitoring job progress...")
    try:
        completed_job = client.wait_for_job(job['job_id'], timeout=120)
        print(f"Job completed with status: {completed_job['status']}")

        if completed_job.get('logs_object_key'):
            print(f"Logs available at: {completed_job['logs_object_key']}")
        if completed_job.get('artifacts_object_key'):
            print(f"Artifacts available at: {completed_job['artifacts_object_key']}")

    except TimeoutError as e:
        print(f"Job monitoring timed out: {e}")
        # Optionally cancel the job
        client.cancel_job(job['job_id'])
        print("Job cancelled due to timeout")


def example_simple_job(client: ReactorcideClient):
    """Example: Create a simple echo job"""
    print("\n=== Simple Echo Job Example ===")

    job = client.create_job(
        name="Echo Test",
        description="A simple test job",
        source_type="copy",
        job_command="echo 'Hello from Reactorcide!' && ls -la",
        runner_image="alpine:latest",
        job_env_vars={
            "TEST_VAR": "test_value"
        }
    )

    print(f"Created job: {job['job_id']}")
    print(f"Status: {job['status']}")
    print("Job has been submitted to Corndogs for processing")

    return job


def example_list_jobs(client: ReactorcideClient):
    """Example: List all jobs"""
    print("\n=== Listing Jobs ===")

    jobs = client.list_jobs(limit=10)

    if not jobs:
        print("No jobs found")
        return

    for job in jobs:
        print(f"- {job['job_id']}: {job['name']}")
        print(f"  Status: {job['status']}")
        print(f"  Created: {job['created_at']}")
        print(f"  Queue: {job.get('queue_name', 'default')}")
        print()


def main():
    # Get configuration from environment variables
    api_url = os.getenv('REACTORCIDE_API_URL', 'http://localhost:8080')
    api_token = os.getenv('REACTORCIDE_API_TOKEN')

    if not api_token:
        print("Error: REACTORCIDE_API_TOKEN environment variable not set")
        print("\nTo create a test token:")
        print("1. Check coordinator_api/test/jobs_test.go for examples")
        print("2. Use the pattern: token_value = 'test-api-token-{user_id}'")
        print("3. Store SHA256 hash of token_value in database")
        print("4. Use original token_value as REACTORCIDE_API_TOKEN")
        sys.exit(1)

    # Create client
    client = ReactorcideClient(api_url, api_token)

    try:
        # Check API health
        print("Checking API health...")
        health = client.check_health()
        print(f"API Status: {health['status']}")
        print(f"Verification: {health.get('verification', {})}")

        # Run examples
        simple_job = example_simple_job(client)
        example_git_job(client)
        example_list_jobs(client)

        # Clean up - delete the simple job
        print(f"\nCleaning up - deleting job {simple_job['job_id']}...")
        client.delete_job(simple_job['job_id'])
        print("Job deleted successfully")

    except requests.HTTPError as e:
        print(f"HTTP Error: {e}")
        print(f"Response: {e.response.text}")
    except Exception as e:
        print(f"Error: {e}")


if __name__ == '__main__':
    main()