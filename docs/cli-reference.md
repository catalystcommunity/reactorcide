# CLI Reference

The `reactorcide` binary runs the coordinator, runs jobs, and operates the
coordinator API. Build it first:

```bash
cd coordinator_api && go build -o reactorcide .
```

## Connect to a Coordinator

Commands that use the API read the coordinator URL and the API token from
these environment variables:

- `REACTORCIDE_API_URL`
- `REACTORCIDE_API_TOKEN`

Use `--api-url` and `--token` to override them. If you give no token, the
command prompts for one. The prompt hides the value.

You can put a flag before or after a positional argument. These two commands
are the same:

```bash
reactorcide jobs get <job-id> --format json
reactorcide jobs --format json get <job-id>
```

## Output Formats

Commands that show records accept `--format`:

- `table` (default): columns for a terminal
- `json`: the full record
- `yaml`: the full record

## Server Commands

| Command | Purpose |
| --- | --- |
| `serve` | Run the coordinator API. |
| `migrate` | Run database migrations. |
| `worker` | Run a job worker. |
| `healthcheck` | Check the API health endpoint. Use it for container health checks. |

## Job Commands

| Command | Purpose |
| --- | --- |
| `run-local <job-file>` | Run a job in a container on this machine. |
| `submit <job-file>` | Submit a job to a coordinator. |
| `jobs list` | List jobs. |
| `jobs get <job-id>` | Show one job. |
| `jobs cancel <job-id>` | Cancel a job. Cleanup hooks run. |
| `jobs kill <job-id>` | Stop a job immediately. Cleanup hooks do not run. Admin only. |
| `jobs retry <job-id>` | Run the job again as a new job. |
| `jobs delete <job-id>` | Delete a job and its telemetry. |
| `jobs metrics <job-id>` | Show CPU, memory, and storage metrics for a job. |
| `logs <job-id>` | Get the logs of a job. |

Filter `jobs list` with `--status`, `--queue-name`, `--source-type`,
`--project-id`, `--workflow-id`, and `--user-id`. Page with `--limit` and
`--offset`.

```bash
reactorcide jobs list --status failed --limit 20
reactorcide jobs get 019fc939-397e-7974-a099-6e8d29c760b8 --format yaml
reactorcide jobs cancel 019fc939-397e-7974-a099-6e8d29c760b8
```

## Workflow Commands

| Command | Purpose |
| --- | --- |
| `workflows list` | List workflows. |
| `workflows get <workflow-id>` | Show one workflow and its job counts. |
| `workflows cancel <workflow-id>` | Cancel a workflow and its unfinished jobs. |
| `workflows retry <workflow-id>` | Run the workflow again as a new instance. |
| `workflows retry-unsuccessful <workflow-id>` | Run the failed and cancelled jobs again in the same instance. |

Filter `workflows list` with `--status`, `--project-id`, and `--user-id`.

`workflows retry` keeps the original instance for history. `workflows
retry-unsuccessful` changes the original instance.

```bash
reactorcide workflows list --status failed
reactorcide workflows retry-unsuccessful 019fc939-397e-7974-a099-6e8d29c760b8
```

## Project Commands

A project connects a repository to Reactorcide.

| Command | Purpose |
| --- | --- |
| `projects list` | List projects. |
| `projects get <project-id>` | Show one project. |
| `projects create` | Create a project. |
| `projects update <project-id>` | Change the given fields of a project. |
| `projects delete <project-id>` | Delete a project. |

Give the fields with flags, or with `--file` and a YAML or JSON document.
The document uses the same field names as the API. Flags win over the file.
Use `--file` for the secret reference maps `vcs_token_secrets` and
`webhook_secrets`, which have no flags.

```bash
reactorcide projects create --file example-repo.yaml
reactorcide projects update <project-id> --enabled --target-branch main
```

See [Connect a VCS Repository](./vcs-setup.md) for a full example.

`update` changes only the fields you give. It cannot set a text field back to
empty.

## Token Commands

| Command | Purpose |
| --- | --- |
| `token create` | Create a token. Writes to the database. |
| `token list` | List your tokens. |
| `token delete <token-id>` | Delete a token. |

`token create` needs database access, not an API token, so you can use it to
make the first token. Give it `--db-uri` or set `REACTORCIDE_DB_URI`. The
token value prints one time.

## Secret Commands

| Command | Purpose |
| --- | --- |
| `secrets init` | Prepare secret storage. |
| `secrets set <path> <key>` | Set a secret value. |
| `secrets get <path> <key>` | Print a secret value. |
| `secrets delete <path> <key>` | Delete a secret. |
| `secrets list <path>` | List the keys at a path. |
| `secrets list-paths` | List all paths. |
| `secrets get-multi <path:key>...` | Get more than one secret in one operation. |
| `secrets master-keys ...` | Administer master keys. Admin only. |

`secrets` uses the coordinator API when `REACTORCIDE_API_URL` is set.
Otherwise it uses local file storage and asks for
`REACTORCIDE_SECRETS_PASSWORD`.

See [Secrets](./secrets.md).

## Secret Grant Commands

| Command | Purpose |
| --- | --- |
| `secret-grants list` | List grants. |
| `secret-grants get <name-or-id>` | Show one grant. |
| `secret-grants set <name>` | Create or change a grant. |
| `secret-grants delete <name-or-id>` | Delete a grant. |
| `secret-grants apply --file <file>` | Apply a set of grants from a file. |

See [VCS Credentials and Secret Grants](./vcs-credentials-and-secret-grants.md).

## VM Image Commands

| Command | Purpose |
| --- | --- |
| `vm-image build` | Build a VM image. |
| `vm-image publish` | Push an image to a registry. |
| `vm-image pull` | Pull an image to the local cache. |
| `vm-image cache prune` | Remove unused cached images. |
| `vm-image registry login` | Store registry credentials. |
| `vm-image registry logout` | Remove registry credentials. |

See [VM Image Operations](./vm-images.md).
