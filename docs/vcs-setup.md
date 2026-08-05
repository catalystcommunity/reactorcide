# Connect a VCS Repository

GitHub webhook events are operational. The GitLab client is incomplete. It
parses payloads but does not map them to generic Reactorcide events. GitLab
webhooks do not start jobs.

Complete the GitLab adapter or add a different provider with a pull request.
See [Add a VCS Provider](#add-a-vcs-provider).

Project-scoped clients currently use the public GitHub and GitLab API
endpoints. GitHub Enterprise Server and self-managed GitLab need an adapter
change for a project-specific API base URL.

## Before You Start

You need:

- A Reactorcide coordinator with VCS integration enabled.
- An HTTPS URL that the VCS provider can reach.
- An API token for the coordinator.
- Administrator access to the GitHub repository webhook settings.
- A repository webhook secret.
- A VCS API token if you need private checkout, commit status, or
  pull-request comments.

Store secret values in files with mode `0600` or in a password manager. Do not
put values in repository files.

## Enable VCS Integration

### Kubernetes

Set these Helm values:

```yaml
vcs:
  enabled: true
  baseURL: "https://ci.example.com"
```

Run `helm upgrade` with your normal production values file.

### VM

The VM Compose file does not enable VCS by default. Create
`~/reactorcide/docker-compose.vcs.yml` on the VM:

```yaml
services:
  coordinator-api:
    environment:
      REACTORCIDE_VCS_ENABLED: "true"
      REACTORCIDE_VCS_BASE_URL: "https://ci.example.com"
```

Start the stack with all three files:

```bash
cd ~/reactorcide
source ./.docker-cmd
$DOCKER_CMD compose \
  -f docker-compose.prod.yml \
  -f docker-compose.override.yml \
  -f docker-compose.vcs.yml \
  up -d
```

The VM deployment job uses only the first two files. Run this command again
after each VM deployment until VCS settings become part of your deployment
automation.

## Create Secret Files

Create one webhook secret for the repository:

```bash
mkdir -p ~/.config/reactorcide
chmod 700 ~/.config/reactorcide
openssl rand -hex 32 > ~/.config/reactorcide/example-webhook-secret
chmod 600 ~/.config/reactorcide/example-webhook-secret
```

Create a VCS API token in GitHub. Save it in:

```text
~/.config/reactorcide/example-vcs-token
```

Set file mode `0600`.

The token needs only the operations that you use:

- Read repository content for private checkout.
- Read pull-request or merge-request data.
- Write commit status.
- Write pull-request or merge-request comments.

GitHub permission names can change. Check the current GitHub documentation
when you create the token.

## Store the Secrets in Reactorcide

Set the coordinator URL and token without terminal output:

```bash
export REACTORCIDE_API_URL="https://ci.example.com"
export REACTORCIDE_API_TOKEN="$(cat ~/.config/reactorcide/api-token)"
```

Store the webhook secret:

```bash
./coordinator_api/reactorcide secrets set --stdin \
  webhooks/example/repo secret \
  < ~/.config/reactorcide/example-webhook-secret
```

Store the VCS API token:

```bash
./coordinator_api/reactorcide secrets set --stdin \
  vcs/example/repo api_token \
  < ~/.config/reactorcide/example-vcs-token
```

The command uses the API because `REACTORCIDE_API_URL` and
`REACTORCIDE_API_TOKEN` are set.

## Create the Project

Use the canonical repository URL. It has no scheme and no `.git` suffix.

Write the project definition to a file. GitHub example:

```yaml
# example-repo.yaml
name: example-repo
repo_url: github.com/example/repo
enabled: true
target_branches:
  - main
allowed_event_types:
  - push
  - pull_request_opened
  - pull_request_updated
  - pull_request_merged
  - pull_request_closed
  - tag_created
vcs_token_secrets:
  github: "vcs/example/repo:api_token"
webhook_secrets:
  github: "webhooks/example/repo:secret"
```

The secret fields hold references in `path:key` format. They do not hold
secret values.

Create the project:

```bash
./coordinator_api/reactorcide projects create --file example-repo.yaml
```

For a project with no secret references, use flags instead of a file:

```bash
./coordinator_api/reactorcide projects create \
  --name example-repo \
  --repo-url github.com/example/repo \
  --enabled \
  --target-branch main
```

Add `default_ci_source_url` and `default_ci_source_ref` when CI definitions
are in a separate trusted repository:

```yaml
default_ci_source_type: git
default_ci_source_url: https://github.com/example/ci-definitions.git
default_ci_source_ref: main
```

For same-repository pull requests, Reactorcide uses trusted base content for
the CI definition. For a fork pull request, source points to the fork and CI
content remains on the upstream base commit.

## Add Repository Files

Add workflow files to `.reactorcide/workflows/`. Add reusable jobs to
`.reactorcide/jobs/`.

Example `.reactorcide/workflows/pr.yaml`:

```yaml
name: "Pull request checks"
on:
  events:
    - pull_request_opened
    - pull_request_updated
  branches:
    - main
jobs:
  test:
    image: "golang:1.26"
    command: "go test ./..."
```

See [Job Definition Reference](./job-definitions.md).

## Configure a GitHub Webhook

In the GitHub repository:

1. Open **Settings**, then **Webhooks**.
2. Select **Add webhook**.
3. Set the payload URL to
   `https://ci.example.com/api/v1/webhooks/github`.
4. Select `application/json`.
5. Copy the value from the webhook secret file into the secret field.
6. Enable SSL verification.
7. Select push and pull-request events.
8. Save the webhook.

GitHub sends a ping. Reactorcide returns `200` with a `pong` response.

## GitLab Status

The route `/api/v1/webhooks/gitlab` and the GitLab client exist. Do not use
them for CI triggers yet. `GitLabClient.ParseWebhook` must set
`WebhookEvent.GenericEvent` for push, tag, and merge-request actions. It must
also populate fork or cross-project head repository data.

Add tests for those behaviors before you document GitLab as supported.

## Verify the Integration

Push a branch or open a pull request.

Check recent jobs:

```bash
./coordinator_api/reactorcide jobs list --limit 10
```

Check these results:

- The webhook delivery has HTTP status `200`.
- Reactorcide creates an eval job.
- The eval job creates a named workflow.
- A matching worker runs each ready job.
- The VCS provider shows the workflow status.

## Troubleshooting

### The endpoint returns 500

Confirm that VCS integration is enabled. The provider clients do not exist
when `REACTORCIDE_VCS_ENABLED` is false.

### The endpoint returns 401

The webhook secret did not match. Confirm:

- The project `repo_url` matches the payload repository.
- The project secret reference is correct.
- The Reactorcide secret value matches the VCS webhook value.

### The endpoint returns 200 but no job starts

Confirm:

- The project is enabled.
- `allowed_event_types` contains the normalized event.
- `target_branches` contains the push branch or pull-request base branch.
- A workflow or job file matches the event.
- An eligible worker is online.

### Checkout of a private repository fails

Confirm that the project VCS token reference exists. Confirm that the token
can read the exact repository path.

### Status or comments are absent

Confirm that the VCS token can write commit status and comments. Confirm that
`vcs.baseURL` or `REACTORCIDE_VCS_BASE_URL` contains the public Reactorcide
URL.

## Add a VCS Provider

New provider support requires a pull request. Implement it in
`coordinator_api/internal/vcs/`.

The adapter must:

1. Add a `Provider` constant in `interface.go`.
2. Implement the `Client` interface.
3. Validate the provider webhook without logging the secret.
4. Parse push and pull-request-equivalent events.
5. Map events to the generic values in `event_types.go`.
6. Populate `HeadRepository` for a pull request from a fork.
7. Implement commit status and pull-request comment operations.
8. Add the provider to `NewClient` and `Manager`.
9. Add a webhook route in `internal/handlers/router.go`.
10. Add provider keys to credential and webhook secret resolution.
11. Add unit tests for signatures, payloads, URL normalization, forks, status,
    and comments.
12. Update this guide and the job event table.

Do not add provider-specific fields to the workflow engine when a generic
field is sufficient. Keep provider credentials in the system secret path.
