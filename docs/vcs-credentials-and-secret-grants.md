# VCS Credentials and Secret Grants

Reactorcide treats VCS credentials as system credentials, not job credentials.
They are secret-store references used by the coordinator and workers for
webhook validation, commit statuses, PR comments, and trusted checkout work.
Jobs should not receive those values as environment variables.

## Credential Resolution

For provider-specific VCS operations, Reactorcide resolves secret references in
this order:

1. Project provider map, such as `vcs_token_secrets.github`.
2. Project legacy field, such as `vcs_token_secret`.
3. Project owner/org provider map, such as `vcs_token_secrets.github`.
4. Global deployment configuration.

Webhook secrets use the same shape with `webhook_secrets` and the legacy
`webhook_secret` field. Global webhook fallback uses:

- `REACTORCIDE_VCS_GITHUB_SECRET`
- `REACTORCIDE_VCS_GITLAB_SECRET`
- `REACTORCIDE_VCS_WEBHOOK_SECRET`

Provider maps accept the provider name (`github`, `gitlab`), `default`, or `*`.
Values are secret references in `path:key` form, not plaintext.

Example project payload:

```json
{
  "name": "example",
  "repo_url": "github.com/example/repo",
  "vcs_token_secrets": {
    "github": "vcs/example/repo:github_pat"
  },
  "webhook_secrets": {
    "github": "webhooks/example/repo:secret"
  }
}
```

## Job Secret Access

Jobs can reference secrets in environment values with `${secret:path:key}`.
Worker execution allows:

- Job-owned secrets under `jobs/<job_id>`.
- Project job-file secrets under `projects/<project_id>/jobs/<job_file>`.
- Additional project/org paths only when a matching secret grant exists.

Grant matching includes the secret path prefix plus optional job identity
constraints. Job identity is both `job_name` and `job_file`; use `job_file`
when the same job name can appear in more than one context.

## Project Secret Grant API

List grants:

```bash
curl -H "Authorization: Bearer <token>" \
  https://reactorcide.example.com/api/v1/projects/<project_id>/secret-grants
```

Create a grant:

```bash
curl -X POST \
  -H "Authorization: Bearer <token>" \
  -H "Content-Type: application/json" \
  https://reactorcide.example.com/api/v1/projects/<project_id>/secret-grants \
  -d '{
    "secret_path_prefix": "deploy/production",
    "job_name": "deploy",
    "job_file": ".reactorcide/jobs/deploy.yaml",
    "description": "Allow the deploy job to read production deploy secrets"
  }'
```

Delete a grant:

```bash
curl -X DELETE \
  -H "Authorization: Bearer <token>" \
  https://reactorcide.example.com/api/v1/projects/<project_id>/secret-grants/<grant_id>
```

Grant APIs return metadata only. They never return secret values.

## Audit Behavior

Workers log whether secret access was allowed or denied, including the job id,
job name, job file, secret path, key, and grant id when applicable. VCS
credential selection logs the scope used (`project`, `org`, or `global`) and
provider. Logs must not include secret values.

## Private Git Checkout Auth

For Git sources, workers can use the resolved VCS credential for checkout
without placing the token value in job environment variables.

The worker creates per-job Git credential material:

- `gitconfig`
- `credentials`

The files are exposed at:

```text
/job/.reactorcide/vcs-auth
```

The job environment receives only path/config values:

- `REACTORCIDE_VCS_AUTH_DIR`
- `REACTORCIDE_VCS_AUTH_CLEANUP=true`
- `GIT_CONFIG_GLOBAL=/job/.reactorcide/vcs-auth/gitconfig`
- `GIT_TERMINAL_PROMPT=0`

The credential file uses Git's `credential.helper = store --file ...` with
`credential.useHttpPath = true`, so credentials are scoped to the repository
paths involved in the job checkout. The worker registers the token value with
its log masker, but does not pass the token itself as an env var.

Runtime behavior by runner:

- Docker and nerdctl/containerd write the files under the existing `/job` bind
  mount before the container starts.
- Kubernetes creates a short-lived Secret, copies it into an emptyDir with an
  init container, and mounts that writable emptyDir at the same auth path.

Runnerlib removes the auth directory immediately after CI/source preparation
in the normal `runnerlib run` and `runnerlib checkout` paths. Pre-checkout
hooks or explicit checkout commands can intentionally use
`REACTORCIDE_VCS_AUTH_DIR`; later job steps should not rely on the files being
present unless a future job option explicitly persists checkout credentials.
