# VCS Provider Integration

This document describes the coordinator VCS adapter boundary. Operators must
use [Connect a VCS Repository](../../docs/vcs-setup.md).

## Provider Status

GitHub is operational.

The GitLab client can validate and parse webhooks and can call provider APIs.
It does not set `WebhookEvent.GenericEvent`. The webhook handler treats the
event as unknown and does not create a job. GitLab also does not populate
cross-project head repository data.

The coordinator registers provider clients only when
`REACTORCIDE_VCS_ENABLED=true`.

Registered webhook routes are:

```text
POST /api/v1/webhooks/github
POST /api/v1/webhooks/gitlab
```

The routes do not use API-token authentication. Provider webhook validation is
mandatory.

## Generic Events

Provider adapters map payloads to:

- `push`
- `pull_request_opened`
- `pull_request_updated`
- `pull_request_merged`
- `pull_request_closed`
- `tag_created`
- `ping`

Unknown events return success and do not create work. All current GitLab push
and merge-request events have this result because normalization is incomplete.

## Client Interface

A provider `Client` implements:

- `ParseWebhook`
- `ValidateWebhook`
- `UpdateCommitStatus`
- `UpdatePRComment`
- `UpsertPRCommentByMarker`
- `GetPRInfo`
- `GetProvider`

The common structures are in `internal/vcs/interface.go`.

## Webhook Processing

The handler:

1. Selects the provider client.
2. Reads the request body.
3. Returns a ping response when applicable.
4. Extracts and normalizes the repository URL.
5. Finds the project.
6. Resolves webhook-secret candidates.
7. Validates the request.
8. Parses the provider payload.
9. Filters the event by project event and target branch.
10. Creates an eval job.

Webhook secret candidates include active rotation entries, project
references, organization references, and deployment fallback values. Project
configuration uses `path:key` references. It does not contain values.

## Fork Pull Requests

An adapter must distinguish the base repository from the head repository.
Set `PullRequestInfo.HeadRepository` when the head branch is in a fork.

The handler then uses:

- Fork URL and head SHA for application source.
- Base repository URL and trusted base SHA for CI source.

Tests must cover a fork whose branch name does not exist in the base
repository.

## Status and Comments

The status updater uses project credentials when available. It falls back to
organization or deployment credentials.

Workflow status uses the workflow name as the status context. Pull-request
comments use a stable marker so Reactorcide can update one comment.

## Add a Provider

1. Add the provider constant to `internal/vcs/interface.go`.
2. Add a provider file under `internal/vcs/`.
3. Implement the complete `Client` interface.
4. Add event translation tests.
5. Add signature or token validation tests.
6. Add push, tag, pull-request, and fork payload tests.
7. Add status and comment tests with an HTTP test server.
8. Add URL normalization cases.
9. Register the provider in `NewClient`.
10. Register it in `Manager.initializeClients`.
11. Add the webhook route and handler method.
12. Add the provider to secret-reference lookup.
13. Update operator documentation.

Do not log payload credentials, webhook secrets, or API tokens. Do not pass a
VCS system credential to a normal job environment.
