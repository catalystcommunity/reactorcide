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

Grant matching includes a secret path matcher plus an optional job-name
matcher. Grants are named so they can be managed idempotently by CLI/API.

Supported secret path matchers are `exact`, `prefix`, `glob`, and `regex`.
Supported job name matchers are `any`, `exact`, `prefix`, `glob`, and `regex`.
Use scoped job names or prefixes when the same workflow/job name appears in
more than one context.

## Project Secret Grant API

Org/global grants live at `/api/v1/secret-grants`. Project-scoped grants can
be addressed either by query parameter or the project route:

- `/api/v1/secret-grants?project=<project_id_or_name_or_repo_url>`
- `/api/v1/projects/<project_id>/secret-grants`

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
    "name": "deploy-production",
    "secret_path_match": "prefix",
    "secret_path_pattern": "deploy/production",
    "job_name_match": "prefix",
    "job_name_pattern": "github.com/example/repo:deploy",
    "description": "Allow the deploy job to read production deploy secrets"
  }'
```

Update a grant by name or id:

```bash
curl -X PATCH \
  -H "Authorization: Bearer <token>" \
  -H "Content-Type: application/json" \
  https://reactorcide.example.com/api/v1/secret-grants/deploy-production?project=<project_id> \
  -d '{"job_name_match":"glob","job_name_pattern":"github.com/example/repo:deploy*"}'
```

Delete a grant:

```bash
curl -X DELETE \
  -H "Authorization: Bearer <token>" \
  https://reactorcide.example.com/api/v1/projects/<project_id>/secret-grants/deploy-production
```

Grant APIs return metadata only. They never return secret values.

## Secret Grant CLI

The CLI uses `REACTORCIDE_API_URL` and `REACTORCIDE_API_TOKEN`, or matching
`--api-url` and `--token` flags.

```bash
reactorcide secret-grants list --project github.com/example/repo

reactorcide secret-grants set deploy-production \
  --project github.com/example/repo \
  --secret-path deploy/production \
  --secret-match prefix \
  --job-name "github.com/example/repo:deploy" \
  --job-match exact

reactorcide secret-grants delete deploy-production --project github.com/example/repo
```

Bulk apply accepts a CSIL-style YAML file:

```yaml
apiVersion: reactorcide.catalystcommunity.io/v1alpha1
kind: SecretGrantList
items:
  - metadata:
      name: deploy-production
    spec:
      scope:
        project: github.com/example/repo
      secret:
        path: deploy/production
        match: prefix
      subject:
        jobName:
          value: github.com/example/repo:deploy
          match: exact
      description: Allow deploy jobs to read production deploy secrets
```

Apply it:

```bash
reactorcide secret-grants apply --file secret-grants.yaml --dry-run
reactorcide secret-grants apply --file secret-grants.yaml
```

Set `spec.state: absent` on an item to delete it. Use `--prune` only when the
file is the complete desired grant list for every scope it mentions.

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
