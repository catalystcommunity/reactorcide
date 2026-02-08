# GitHub Webhook Setup Guide

This guide walks through connecting a GitHub repository to Reactorcide so that pushes, pull requests, and tags automatically trigger CI/CD jobs.

## Prerequisites

- A running Reactorcide instance with the Coordinator API accessible from the internet (or from GitHub's webhook IPs)
- An API authentication token (created via `POST /api/v1/tokens` or the CLI)
- A repository on GitHub where you have admin access (to configure webhooks)

## Step 1: Create a Project

A **Project** links a GitHub repository to Reactorcide. It defines which events to process, which branches to watch, and default job settings.

```bash
curl -s -X POST "https://your-instance.com/api/v1/projects" \
  -H "Authorization: Bearer $API_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "my-project",
    "description": "CI for my-org/my-repo",
    "repo_url": "github.com/my-org/my-repo",
    "enabled": true,
    "target_branches": ["main", "develop"],
    "allowed_event_types": [
      "push",
      "pull_request_opened",
      "pull_request_updated",
      "tag_created"
    ],
    "default_runner_image": "alpine:latest",
    "default_timeout_seconds": 3600
  }'
```

**Key fields:**

| Field | Description | Default |
|---|---|---|
| `name` | Human-readable project name | (required) |
| `repo_url` | Repository identifier in `github.com/org/repo` format (no protocol, no `.git`) | (required) |
| `enabled` | Whether to process webhooks for this project | `true` |
| `target_branches` | Branches that trigger jobs (empty = all) | `["main", "master", "develop"]` |
| `allowed_event_types` | Which event types to process | `["push", "pull_request_opened", "pull_request_updated", "tag_created"]` |
| `default_ci_source_url` | URL of a separate repo containing job definitions (optional, defaults to source repo) | `""` |
| `default_ci_source_ref` | Branch/ref to use for CI source repo | `"main"` |
| `default_runner_image` | Container image for eval jobs | `"quay.io/catalystcommunity/reactorcide_runner"` |
| `default_job_command` | Override the eval command (usually left empty to use default) | `"runnerlib eval ..."` |
| `default_timeout_seconds` | Job timeout | `3600` |
| `default_queue_name` | Corndogs queue name | `"reactorcide-jobs"` |

Save the returned `project_id` for reference.

### Same-Repo vs Separate CI Source

By default, Reactorcide looks for job definitions (`.reactorcide/jobs/*.yaml`) in the **source repository** itself. This is the simplest setup.

For additional security, you can point `default_ci_source_url` to a **separate trusted repository** that contains your job definitions. This prevents PR authors from modifying which jobs run.

## Step 2: Configure the GitHub Webhook

1. Go to your GitHub repository **Settings > Webhooks > Add webhook**
2. Configure:

| Setting | Value |
|---|---|
| **Payload URL** | `https://your-instance.com/api/v1/webhooks/github` |
| **Content type** | `application/json` |
| **Secret** | A shared secret (must match `VCS_GITHUB_SECRET` or `VCS_WEBHOOK_SECRET` on the Reactorcide instance) |
| **SSL verification** | Enable (recommended) |
| **Events** | Select "Let me select individual events" and check **Pull requests** and **Pushes** |

3. Click **Add webhook**

GitHub will send a ping event to verify the endpoint is reachable.

### Webhook Secret Configuration

The Reactorcide Coordinator API validates webhook signatures using HMAC-SHA256. Set the secret on the server using one of these environment variables:

- `VCS_GITHUB_SECRET` - GitHub-specific secret
- `VCS_WEBHOOK_SECRET` - Shared secret used for any provider

The secret configured in GitHub's webhook settings must match exactly.

## Step 3: Create Job Definitions

Job definitions tell Reactorcide what to run when events occur. Create them in your repository (or CI source repository) under `.reactorcide/jobs/`.

Example `.reactorcide/jobs/test.yaml`:

```yaml
name: test
description: "Run tests on pull requests"
triggers:
  events:
    - pull_request_opened
    - pull_request_updated
  branches:
    - main
    - "feature/*"
job:
  image: python:3.12
  command: "pip install -r requirements.txt && pytest"
  timeout: 1800
environment:
  CI: "true"
```

See the [Job Definition Reference](job-definitions.md) for the full YAML schema, event types, branch glob syntax, and more examples.

## Step 4: Verify

1. **Push a commit** or **open a pull request** against a watched branch
2. Check that GitHub shows a successful webhook delivery (Settings > Webhooks > Recent Deliveries)
3. Verify an eval job was created via the API:
   ```bash
   curl -s "https://your-instance.com/api/v1/jobs?limit=5" \
     -H "Authorization: Bearer $API_TOKEN" | jq .
   ```
4. The eval job runs the `runnerlib eval` command, which reads your job definitions and creates child jobs for any matching triggers
5. GitHub commit status will update from "pending" to "success" or "failure"

## Event Flow

```
GitHub Webhook
  -> Coordinator validates signature
  -> Translates to generic event type (e.g., pull_request_opened)
  -> Looks up Project by repo URL
  -> Checks event type is allowed & branch is watched
  -> Creates eval job and submits to task queue
       -> Worker picks up eval job
       -> Checks out source code and CI source
       -> Runs: runnerlib eval --event-type <type> --branch <branch>
       -> Reads .reactorcide/jobs/*.yaml, matches against event
       -> Writes triggers.json with matched jobs
       -> Worker reads triggers.json, creates and submits child jobs
       -> Child jobs run your actual CI commands
```

## Environment Variables

Jobs triggered by webhooks have these environment variables available:

| Variable | Description | Example |
|---|---|---|
| `REACTORCIDE_CI` | Always `"true"` in CI context | `true` |
| `REACTORCIDE_PROVIDER` | VCS provider | `github` |
| `REACTORCIDE_EVENT_TYPE` | Generic event type | `pull_request_opened` |
| `REACTORCIDE_REPO` | Repository full name | `my-org/my-repo` |
| `REACTORCIDE_SOURCE_URL` | Clone URL for the source repo | `https://github.com/my-org/my-repo.git` |
| `REACTORCIDE_SHA` | Commit SHA | `abc123def456` |
| `REACTORCIDE_BRANCH` | Target branch (push) or base branch (PR) | `main` |
| `REACTORCIDE_PR_NUMBER` | Pull request number (PR events only) | `42` |
| `REACTORCIDE_PR_REF` | PR head branch (PR events only) | `feature/my-change` |
| `REACTORCIDE_PR_BASE_REF` | PR base branch (PR events only) | `main` |
| `REACTORCIDE_CI_SOURCE_URL` | CI source repo URL (if separate) | `https://github.com/my-org/ci-config.git` |
| `REACTORCIDE_CI_SOURCE_REF` | CI source ref (if separate) | `main` |

## Troubleshooting

### Webhook returns 401 Unauthorized
- The webhook secret doesn't match. Verify `VCS_GITHUB_SECRET` (or `VCS_WEBHOOK_SECRET`) on the server matches the secret in GitHub's webhook settings exactly.

### Webhook returns 200 but no job is created
- Check that a Project exists with a `repo_url` matching the webhook's repository. The format must be `github.com/org/repo` (no protocol prefix, no `.git` suffix).
- Verify the project is `enabled`.
- Check `allowed_event_types` includes the event you're sending.
- Check `target_branches` includes the branch you're pushing to / targeting with a PR.

### Eval job runs but no child jobs are created
- Verify `.reactorcide/jobs/*.yaml` files exist in the CI source (your repo or the separate CI source repo).
- Check that job definitions have `triggers.events` matching the event type.
- Check that `triggers.branches` matches the branch (or is omitted to match all branches).
- Review the eval job's logs for parsing errors.

### GitHub commit status stays "pending"
- The eval job may still be running or queued. Check its status via the jobs API.
- If the job failed, check its logs for errors. The commit status is updated to "failure" or "error" on job completion.

### Project API returns 401
- Ensure you're including the `Authorization: Bearer <token>` header.
- Verify the token is valid and hasn't been deleted.
