# API Secrets Management

## Overview

Reactorcide provides encrypted, per-organization secret storage through the Coordinator API. Secrets are:

- **Encrypted at rest** with per-organization keys, themselves encrypted by master keys
- **Automatically masked** in job logs — any output matching a secret value is replaced with `***`
- **Injected into jobs** via `${secret:path:key}` references in environment variables

This document covers the HTTP API for managing secrets. For CLI usage, run `reactorcide secrets --help`.

## Prerequisites

1. **Authentication token** — All endpoints require a valid API token via `Authorization: Bearer <token>`
2. **Master keys configured** — The server must have `REACTORCIDE_MASTER_KEYS` set or auto-generated keys present. If master keys are not configured, the secrets endpoints are disabled entirely.
3. **Secrets initialized** — Before storing secrets, your organization must be initialized (see below). Most endpoints return `412 Precondition Failed` if secrets are not initialized.

## Initializing Secrets

Initialize secret storage for your organization. This is a one-time operation that generates your organization's encryption key.

```bash
curl -X POST https://reactorcide.example.com/api/v1/secrets/init \
  -H "Authorization: Bearer $API_TOKEN"
```

**Response** (201 Created):
```json
{
  "status": "initialized",
  "org_id": "01HXYZ..."
}
```

If already initialized, returns `409 Conflict`.

## Managing Secrets

### Set a Secret

Create or update a secret value at a given path and key.

```bash
curl -X PUT "https://reactorcide.example.com/api/v1/secrets/value?path=prod/db&key=password" \
  -H "Authorization: Bearer $API_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"value": "s3cret!"}'
```

**Response** (200 OK):
```json
{"status": "ok"}
```

### Get a Secret

```bash
curl "https://reactorcide.example.com/api/v1/secrets/value?path=prod/db&key=password" \
  -H "Authorization: Bearer $API_TOKEN"
```

**Response** (200 OK):
```json
{"value": "s3cret!"}
```

Returns `404 Not Found` if the path/key does not exist.

### Delete a Secret

```bash
curl -X DELETE "https://reactorcide.example.com/api/v1/secrets/value?path=prod/db&key=password" \
  -H "Authorization: Bearer $API_TOKEN"
```

**Response** (200 OK):
```json
{"status": "deleted"}
```

Returns `404 Not Found` if the path/key does not exist.

### List Keys at a Path

```bash
curl "https://reactorcide.example.com/api/v1/secrets?path=prod/db" \
  -H "Authorization: Bearer $API_TOKEN"
```

**Response** (200 OK):
```json
{"keys": ["password", "username", "connection_string"]}
```

### List All Paths

```bash
curl "https://reactorcide.example.com/api/v1/secrets/paths" \
  -H "Authorization: Bearer $API_TOKEN"
```

**Response** (200 OK):
```json
{"paths": ["prod/db", "prod/aws", "staging/db"]}
```

## Batch Operations

### Batch Get

Retrieve multiple secrets in a single request.

```bash
curl -X POST "https://reactorcide.example.com/api/v1/secrets/batch/get" \
  -H "Authorization: Bearer $API_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "refs": [
      {"path": "prod/db", "key": "password"},
      {"path": "prod/aws", "key": "access_key"}
    ]
  }'
```

**Response** (200 OK):
```json
{
  "secrets": {
    "prod/db:password": "s3cret!",
    "prod/aws:access_key": "AKIA..."
  }
}
```

Missing secrets are omitted from the response (no error).

### Batch Set

Create or update multiple secrets in a single request.

```bash
curl -X POST "https://reactorcide.example.com/api/v1/secrets/batch/set" \
  -H "Authorization: Bearer $API_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "secrets": [
      {"path": "prod/db", "key": "password", "value": "s3cret!"},
      {"path": "prod/db", "key": "username", "value": "admin"},
      {"path": "prod/aws", "key": "access_key", "value": "AKIA..."}
    ]
  }'
```

**Response** (200 OK):
```json
{"status": "ok"}
```

## Using Secrets in Jobs

Reference secrets in job environment variables with the `${secret:path:key}` syntax. The worker resolves these references before passing environment variables to the container.

```yaml
environment:
  DB_PASSWORD: "${secret:prod/db:password}"
  AWS_ACCESS_KEY: "${secret:prod/aws:access_key}"
```

References can appear inline alongside other text:

```yaml
environment:
  DATABASE_URL: "postgres://admin:${secret:prod/db:password}@db.example.com/mydb"
```

All resolved secret values are automatically registered for log masking — any occurrence in stdout or stderr is replaced with `***`.

## Path and Key Naming

| Rule | Path | Key |
|------|------|-----|
| Allowed characters | `a-z A-Z 0-9 / _ -` | `a-z A-Z 0-9 _ -` |
| Slashes | Allowed (for hierarchy) | Not allowed |
| Regex | `^[a-zA-Z0-9/_-]+$` | `^[a-zA-Z0-9_-]+$` |
| Empty | Not allowed | Not allowed |

Examples of valid paths: `prod`, `prod/db`, `aws/us-east-1/credentials`

Examples of valid keys: `password`, `api_key`, `db-password`

## Master Key Administration

These endpoints require the `admin` role.

### Environment Variable Format

Set `REACTORCIDE_MASTER_KEYS` with one or more base64-encoded 32-byte keys:

```bash
export REACTORCIDE_MASTER_KEYS="primary-key:BASE64_KEY_1,backup-key:BASE64_KEY_2"
```

Format: `name:base64key,name:base64key,...`

The **first** key listed is the primary key, used for all new encryptions. Additional keys are available for decryption during rotation.

If no environment variable is set and no keys exist in the database, three keys are auto-generated on first startup.

### Register a Master Key

Register an environment-provided key in the database for tracking and rotation management.

```bash
curl -X POST "https://reactorcide.example.com/api/v1/admin/secrets/master-keys" \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name": "primary-key", "description": "Production primary key"}'
```

**Response** (201 Created):
```json
{
  "key_id": "01HXYZ...",
  "name": "primary-key",
  "is_active": true,
  "is_primary": true,
  "description": "Production primary key",
  "created_at": "2025-01-15T10:30:00Z"
}
```

The key must already exist in the `REACTORCIDE_MASTER_KEYS` environment variable. Returns `409 Conflict` if the name is already registered or the key is not in the environment.

### List Master Keys

```bash
curl "https://reactorcide.example.com/api/v1/admin/secrets/master-keys" \
  -H "Authorization: Bearer $ADMIN_TOKEN"
```

**Response** (200 OK):
```json
{
  "master_keys": [
    {
      "key_id": "01HXYZ...",
      "name": "primary-key",
      "is_active": true,
      "is_primary": true,
      "description": "Production primary key",
      "created_at": "2025-01-15T10:30:00Z"
    }
  ]
}
```

### Rotate to a Key

Re-encrypt all organization keys with a different master key. Use this when transitioning to a new primary key.

```bash
curl -X POST "https://reactorcide.example.com/api/v1/admin/secrets/master-keys/new-key/rotate" \
  -H "Authorization: Bearer $ADMIN_TOKEN"
```

**Response** (200 OK):
```json
{"status": "rotated", "key_name": "new-key"}
```

The target key must be registered and present in the environment. Returns `404 Not Found` if the key name is not registered.

### Decommission a Key

Mark a key as inactive after rotation. This removes its associated organization encryption key entries.

```bash
curl -X DELETE "https://reactorcide.example.com/api/v1/admin/secrets/master-keys/old-key" \
  -H "Authorization: Bearer $ADMIN_TOKEN"
```

**Response** (200 OK):
```json
{"status": "decommissioned", "key_name": "old-key"}
```

You **cannot** decommission the primary key — rotate to a different key first. Returns `400 Bad Request` if you attempt to decommission the primary key.

### Sync Primary from Environment

Update the database to match the current `REACTORCIDE_MASTER_KEYS` environment variable (first key becomes primary).

```bash
curl -X POST "https://reactorcide.example.com/api/v1/admin/secrets/sync-primary" \
  -H "Authorization: Bearer $ADMIN_TOKEN"
```

**Response** (200 OK):
```json
{"status": "synced", "primary": "new-primary-key"}
```

### Key Rotation Workflow

1. Add the new key to `REACTORCIDE_MASTER_KEYS` (keep old key listed too)
2. Restart the Coordinator API
3. Register the new key: `POST /api/v1/admin/secrets/master-keys`
4. Rotate all orgs to the new key: `POST /api/v1/admin/secrets/master-keys/{new}/rotate`
5. Sync primary if the new key should be primary: `POST /api/v1/admin/secrets/sync-primary`
6. Decommission the old key: `DELETE /api/v1/admin/secrets/master-keys/{old}`
7. Remove the old key from `REACTORCIDE_MASTER_KEYS` and restart

## Troubleshooting

| HTTP Status | Error | Cause |
|-------------|-------|-------|
| 400 | `invalid_input` | Missing or invalid `path`/`key` parameter, or malformed request body |
| 403 | `forbidden` | Token does not have access to this organization's secrets |
| 404 | `not_found` | Secret at the given path/key does not exist |
| 409 | `already_exists` | Secrets already initialized, or master key name already registered |
| 412 | `not_initialized` | Call `POST /api/v1/secrets/init` first |
| 500 | `internal_error` | Database or encryption failure — check server logs |

## References

- [Security Model](security-model.md) — CI/CD security architecture and secret isolation
- [DESIGN.md](../DESIGN.md) — System architecture and component interactions
