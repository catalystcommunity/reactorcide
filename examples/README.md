# Reactorcide API Client Examples

This directory contains example API clients demonstrating how to interact with the Reactorcide API and its Corndogs integration.

## Overview

The Reactorcide API provides endpoints for:
- Creating jobs that are automatically submitted to Corndogs for queuing
- Monitoring job status and progress
- Managing job lifecycle (list, cancel, delete)
- Retrieving job logs and artifacts (when object store is configured)

## Corndogs Integration Flow

When you create a job through the API:

1. **Job Creation**: The job is created in the database with initial status
2. **Corndogs Submission**: The job is automatically submitted to Corndogs queue
3. **Task ID Storage**: The Corndogs task ID is stored with the job for tracking
4. **Worker Processing**: Workers poll Corndogs for tasks and execute them using runnerlib
5. **Status Updates**: Job status is updated as it progresses through Corndogs states
6. **Result Storage**: Logs and artifacts are stored in the configured object store

## Examples

### Go Client (`api_client.go`)

A complete Go client implementation with:
- Health check
- Job creation with Corndogs queue configuration
- Job status monitoring
- Job management (list, cancel, delete)
- Polling with timeout for job completion

### Python Client (`api_client.py`)

A Python client implementation with:
- Class-based API wrapper
- Multiple example scenarios (git jobs, simple jobs)
- Error handling and timeout management
- Environment variable configuration

## Authentication

The API uses Bearer token authentication. To use these examples:

### For Testing (Development Only)

1. Create a test user in the database
2. Generate a token value (e.g., `test-api-token-{user_id}`)
3. Store the SHA256 hash of the token in the database
4. Use the original token value in the `Authorization: Bearer {token}` header

### For Production

1. Use the proper user registration flow
2. Generate secure API tokens through the API
3. Store tokens securely (never commit them to version control)

## Running the Examples

### Go Example

```bash
# Set environment variables
export REACTORCIDE_API_URL=http://localhost:8080
export REACTORCIDE_API_TOKEN=your-api-token

# Run the example
go run api_client.go
```

### Python Example

```bash
# Install dependencies
pip install requests

# Set environment variables
export REACTORCIDE_API_URL=http://localhost:8080
export REACTORCIDE_API_TOKEN=your-api-token

# Run the example
python api_client.py
```

## Job Configuration

When creating jobs, you can specify:

### Required Fields
- `name`: Job name
- `job_command`: Command to execute in the container
- `source_type`: Either "git" or "copy"

### Optional Fields
- `description`: Job description
- `git_url`: Git repository URL (for git source type)
- `git_ref`: Branch, tag, or commit SHA
- `source_path`: Local path (for copy source type)
- `runner_image`: Docker image (default: alpine:latest)
- `job_env_vars`: Environment variables as key-value pairs
- `queue_name`: Corndogs queue name (default: "default")
- `timeout_seconds`: Job timeout
- `priority`: Job priority (higher values = higher priority)

## Job Status Values

Jobs progress through these states:

- `submitted`: Initial state when submitted to Corndogs
- `queued`: Job is in the Corndogs queue
- `running`: Worker has claimed the job and is executing it
- `completed`: Job finished successfully
- `failed`: Job failed during execution
- `cancelled`: Job was cancelled by user
- `submit_failed`: Failed to submit to Corndogs (job remains in database)

## Error Handling

Both examples include error handling for:
- Network failures
- Authentication errors
- Invalid job configurations
- Timeout during job execution
- API errors with detailed messages

## Integration with Corndogs

The examples demonstrate how Reactorcide integrates with Corndogs:

1. **Queue Selection**: Specify `queue_name` to route jobs to specific Corndogs queues
2. **Priority**: Set job priority for Corndogs scheduling
3. **Timeout**: Configure job timeout that Corndogs will enforce
4. **Task Tracking**: The API stores Corndogs task IDs for correlation
5. **Status Sync**: Job status reflects the Corndogs task state

## Next Steps

For production use:
1. Implement proper authentication and token management
2. Add retry logic for transient failures
3. Implement log streaming from object store
4. Add webhook support for job completion notifications
5. Integrate with your CI/CD pipeline