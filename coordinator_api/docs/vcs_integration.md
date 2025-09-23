# VCS Integration Documentation

## Overview

Reactorcide provides comprehensive Version Control System (VCS) integration to enable automated CI/CD workflows triggered by Git events. The system supports both GitHub and GitLab, with features including webhook processing, commit status updates, pull request comments, and branch protection enforcement.

## Features

### 1. Webhook Receivers
- **GitHub Webhooks**: Receives and processes GitHub webhook events at `/api/v1/webhooks/github`
- **GitLab Webhooks**: Receives and processes GitLab webhook events at `/api/v1/webhooks/gitlab`
- **Event Types Supported**:
  - Pull Request/Merge Request events (opened, closed, synchronized)
  - Push events (commits to branches)
  - Ping events (webhook verification)
- **Security**: Validates webhook signatures using HMAC-SHA256 (GitHub) or token validation (GitLab)

### 2. Automatic Job Creation
When webhook events are received, Reactorcide automatically:
- Creates CI jobs for pull requests and pushes
- Sets appropriate priorities (PRs get higher priority)
- Configures environment variables with CI context
- Stores VCS metadata for status updates

### 3. Commit Status Updates
- Updates commit statuses in real-time as jobs progress through states:
  - `pending`: Job submitted or queued
  - `running`: Job executing
  - `success`: Job completed successfully
  - `failure`: Job failed
  - `error`: Job encountered an error or timed out
  - `cancelled`: Job was cancelled
- Provides direct links to job logs and details

### 4. Pull Request Comments
- Automatically comments on pull requests when CI builds complete
- Includes build status, duration, exit code, and link to full logs
- Uses emoji indicators for quick visual feedback (✅ ❌ ⚠️ ⏱️)

### 5. Branch Protection Integration
- Enforces branch protection rules before allowing merges
- Supports required status checks
- Validates that PR branches are up-to-date with base branch
- Configurable protection patterns (exact match or wildcard)

## Configuration

### Environment Variables

```bash
# Enable VCS integration
VCS_ENABLED=true

# GitHub Configuration
VCS_GITHUB_TOKEN=ghp_xxxxxxxxxxxx  # GitHub personal access token
VCS_GITHUB_SECRET=webhook_secret    # GitHub webhook secret (optional)

# GitLab Configuration
VCS_GITLAB_TOKEN=glpat-xxxxxxxxxxxx # GitLab personal access token
VCS_GITLAB_SECRET=webhook_secret    # GitLab webhook secret (optional)

# Shared Configuration
VCS_WEBHOOK_SECRET=shared_secret    # Shared secret for all providers (optional)
VCS_BASE_URL=https://ci.example.com # Base URL for status links
```

### Required Permissions

#### GitHub Token Permissions
- `repo:status` - Update commit statuses
- `repo` - Read repository information and create PR comments
- `write:discussion` - Comment on pull requests

#### GitLab Token Permissions
- `api` - Full API access
- `read_repository` - Read repository information
- `write_repository` - Update commit statuses

## Setup Guide

### 1. GitHub Webhook Setup

1. Navigate to your repository's Settings → Webhooks
2. Click "Add webhook"
3. Configure:
   - **Payload URL**: `https://your-reactorcide-instance.com/api/v1/webhooks/github`
   - **Content type**: `application/json`
   - **Secret**: Your configured `VCS_GITHUB_SECRET` or `VCS_WEBHOOK_SECRET`
   - **Events**: Select "Pull requests" and "Pushes" (or "Send me everything")
4. Click "Add webhook"

### 2. GitLab Webhook Setup

1. Navigate to your project's Settings → Webhooks
2. Configure:
   - **URL**: `https://your-reactorcide-instance.com/api/v1/webhooks/gitlab`
   - **Secret token**: Your configured `VCS_GITLAB_SECRET` or `VCS_WEBHOOK_SECRET`
   - **Trigger events**: Select "Push events" and "Merge request events"
3. Click "Add webhook"

### 3. Branch Protection Setup

Configure branch protection rules in your job configuration or via API:

```json
{
  "branch_protection": {
    "pattern": "main",
    "require_status_checks": true,
    "required_status_checks": ["continuous-integration/reactorcide"],
    "require_pr_reviews": true,
    "required_review_count": 1,
    "require_up_to_date": true
  }
}
```

## API Endpoints

### Webhook Endpoints

#### `POST /api/v1/webhooks/github`
Receives GitHub webhook events. No authentication required (validated via signature).

#### `POST /api/v1/webhooks/gitlab`
Receives GitLab webhook events. No authentication required (validated via token).

## Job Metadata

Jobs created from VCS events include metadata in the `notes` field:

```json
{
  "vcs_provider": "github",
  "repo": "owner/repository",
  "pr_number": 123,
  "branch": "feature-branch",
  "commit_sha": "abc123def456"
}
```

This metadata is used to:
- Update commit statuses as job state changes
- Post comments to pull requests
- Link back to the VCS from job details

## CI Environment Variables

Jobs triggered by VCS events receive these environment variables:

### Common Variables
- `CI=true` - Indicates CI environment
- `CI_PROVIDER` - VCS provider (github/gitlab)
- `CI_REPO` - Repository full name
- `CI_SHA` - Commit SHA

### Pull Request Variables
- `CI_PR_NUMBER` - Pull request number
- `CI_PR_SHA` - Pull request head SHA
- `CI_PR_REF` - Pull request source branch
- `CI_PR_BASE_REF` - Pull request target branch

### Push Variables
- `CI_BRANCH` - Branch name
- `CI_COMMIT_MESSAGE` - First commit message in push

## Default CI Commands

When no specific CI configuration is provided, Reactorcide attempts to detect and run appropriate tests:

1. **Makefile**: Runs `make test`
2. **Node.js** (package.json): Runs `npm install && npm test`
3. **Go** (go.mod): Runs `go test ./...`
4. **Python** (requirements.txt): Runs `pip install -r requirements.txt && python -m pytest`

## Security Considerations

1. **Webhook Validation**: Always configure webhook secrets to prevent unauthorized job creation
2. **Token Security**: Store VCS tokens as environment variables, never in code
3. **Network Security**: Use HTTPS for all webhook endpoints
4. **Permission Scope**: Use minimal token permissions required for functionality
5. **Branch Protection**: Configure branch protection to prevent unauthorized code execution

## Troubleshooting

### Webhooks Not Triggering
- Verify webhook URL is accessible from the internet
- Check webhook secret configuration matches
- Review webhook delivery logs in GitHub/GitLab
- Check Reactorcide logs for webhook processing errors

### Status Updates Not Appearing
- Verify VCS token has correct permissions
- Check job metadata includes VCS information
- Review worker logs for status update errors
- Ensure VCS_ENABLED is set to true

### PR Comments Not Posted
- Verify token has permission to comment on PRs
- Check that job has PR metadata (pr_number)
- Ensure job reaches a terminal state (completed/failed/cancelled)

## Architecture

The VCS integration consists of several components:

1. **Webhook Handlers** (`webhook_handler.go`): Receive and process webhook events
2. **VCS Clients** (`github.go`, `gitlab.go`): Provider-specific implementations
3. **Status Updater** (`status_updater.go`): Updates commit statuses based on job state
4. **Branch Protection** (`branch_protection.go`): Enforces merge requirements
5. **VCS Manager** (`manager.go`): Coordinates VCS operations and client initialization

## Best Practices

1. **Use Separate Queues**: Configure different queues for PR builds vs. branch pushes
2. **Set Appropriate Priorities**: Give PR builds higher priority for faster feedback
3. **Configure Timeouts**: Set reasonable timeouts to prevent hanging builds
4. **Monitor Webhook Failures**: Set up alerting for webhook processing errors
5. **Rotate Tokens Regularly**: Implement token rotation for security
6. **Test Webhook Locally**: Use tools like ngrok for local webhook testing

## Future Enhancements

- Support for additional VCS providers (Bitbucket, Gitea)
- Advanced branch protection rules (required checks per file pattern)
- Integration with code review tools
- Automatic retry of failed status updates
- Webhook event replay functionality
- Custom CI configuration per repository