# GitHub App

A GitHub App is the recommended connection to GitHub. A deployment can hold
many Apps at the same time. Each App has its own rate limit, so many Apps
spread the load (plan item B6, decision D2).

## What the App Gives

- **Installation tokens.** Reactorcide gets short-lived tokens for each
  installation. You do not store a personal access token for a project.
- **Check runs.** A project in an App installation posts `Reactorcide Checks`
  and `Reactorcide Reviews` as check runs, not as commit statuses. The names
  do not change. A check run has:
  - a summary with one row for each workflow,
  - the failed jobs, their failure class, and the first error line,
  - the failed tests of the test reports,
  - line annotations for failed tests that give a file and a line,
  - a **Re-run** button on a finished pull request run.
- **Self-service registration.** When an account installs the App on a
  repository, Reactorcide makes a project for it.

A project with no App installation keeps its tokens and the Commit Status
API. Nothing changes for it.

For App projects, check runs supersede decision D4 of `EVENT_CI_PLAN.md`
(keep the Commit Status API). A workflow `status.context` stays a commit
status.

## Before You Start

You need:

- A coordinator with VCS integration enabled. See
  [Connect a VCS Repository](./vcs-setup.md).
- Master keys on the coordinator. Reactorcide encrypts the App private key
  and the App webhook secret with the master keys. Without master keys, the
  App routes return `503`.
- A global administrator. The admin API token is sufficient. You do not need
  a login.

## Create the App on GitHub

1. Open **Settings**, then **Developer settings**, then **GitHub Apps**.
   For an organization App, use the organization settings.
2. Select **New GitHub App**.
3. Set the webhook URL to
   `https://ci.example.com/api/v1/webhooks/github-app`.
4. Make a webhook secret:

   ```bash
   openssl rand -hex 32 > ~/.config/reactorcide/github-app-secret
   chmod 600 ~/.config/reactorcide/github-app-secret
   ```

5. Copy the value from the file into the webhook secret field.
6. Set the repository permissions:

   | Permission | Access |
   | --- | --- |
   | Checks | Read and write |
   | Commit statuses | Read and write |
   | Contents | Read |
   | Metadata | Read |
   | Pull requests | Read and write |

7. Set the organization permission **Members** to **Read**. Team sync of
   groups needs it ([Principals](./principals.md)).
8. Subscribe to these events:
   - Check run
   - Check suite
   - Issue comment
   - Pull request
   - Pull request review
   - Push
   - Membership
   - Team

   GitHub always sends installation events. You do not select them.
9. Save the App. Record the App ID.
10. Make a private key. Save the `.pem` file with mode `0600`.

The endpoint `/api/v1/webhooks/github` also accepts App deliveries. Each App
delivery has the App ID in the header `X-GitHub-Hook-Installation-Target-ID`.
Reactorcide verifies the delivery with the webhook secret of that App, not
with a project secret.

The App secret only proves that the delivery came from that App. Thus a
delivery about a repository continues only when all of these are true:

- the repository is bound to an active installation of that App,
- the GitHub repository ID of the payload is the ID of the binding,
- the project of the repository URL is the project of the binding, and its
  stored repository ID (`vcs_repository_id`) is the same or not known.

Else Reactorcide answers `200` with `{"status": "ignored", "reason": ...}`
and logs the reason. A delivery with no repository continues only for the
`team`, `membership`, and `organization` events (group sync). A
`check_run` or `check_suite` re-run request also needs the binding of that
App for the project.

## Register the App

```bash
export REACTORCIDE_API_URL="https://ci.example.com"
export REACTORCIDE_API_TOKEN="$(cat ~/.config/reactorcide/api-token)"

./coordinator_api/reactorcide github-apps add \
  --app-id 123456 \
  --private-key-file ~/.config/reactorcide/github-app.pem \
  --webhook-secret-file ~/.config/reactorcide/github-app-secret \
  --default-org example
```

The command reads the key and the secret from files. It does not print them.
No API answer contains them.

| Flag | Meaning |
| --- | --- |
| `--default-org ORG` | The organization of new projects when no account mapping applies. |
| `--open-registration` | Enable the new projects that go into `--default-org`. Without it, such a project is created disabled and waits for approval. Needs `--default-org`. |
| `--no-verify` | Do not check the key with GitHub. By default, Reactorcide calls `GET /app` and reads the slug, the name, and the client ID. |
| `--replace` | Replace the key and the secret of a registered App (rotation). |

Then install the App on an account. Use the `install_url` of
`github-apps show`.

If the App was installed before you registered it, read the installations
from GitHub:

```bash
./coordinator_api/reactorcide github-apps sync 123456
```

## Manage Apps

| Command | Purpose |
| --- | --- |
| `github-apps list` | List the Apps with headroom, installations, repositories, and the recommended App. |
| `github-apps show <app-id>` | Show one App. |
| `github-apps recommended` | Show the App that new installations must use. |
| `github-apps enable\|disable <app-id>` | Enable or disable an App. |
| `github-apps installations <app-id>` | List the installations and their repositories. |
| `github-apps sync <app-id>` | Read the installations and repositories from GitHub. Register projects. |
| `github-apps map-account --org ORG <account-login>` | Register new projects of a GitHub account in an organization. |
| `github-apps unmap-account <account-login>` | Remove an account mapping. |
| `github-apps accounts` | List the account mappings. |

A disabled App gives no tokens. Its projects use their tokens and commit
statuses again.

A global administrator can also use the GitHub Apps page of the admin UI.
The page shows the Apps, the installations, and the rate-limit headroom.

### REST API

Every route needs a global administrator. A `GET` route needs
`organizations:read` or `organizations:manage` with all organizations. A
change needs `organizations:manage` with all organizations.

| Route | Purpose |
| --- | --- |
| `GET /api/v1/github-apps` | List the Apps. |
| `POST /api/v1/github-apps` | Register an App (`app_id`, `private_key`, `webhook_secret`, `slug`, `name`, `client_id`, `default_org`, `open_registration`, `verify`, `replace`). `open_registration` needs `default_org`; on a replacement, an omitted `open_registration` does not change. |
| `GET /api/v1/github-apps/recommended` | The recommended App. |
| `GET /api/v1/github-apps/account-orgs` | List the account mappings. |
| `PUT /api/v1/github-apps/account-orgs/LOGIN` | Set a mapping (`{"org": "NAME"}`). |
| `DELETE /api/v1/github-apps/account-orgs/LOGIN` | Remove a mapping. |
| `GET /api/v1/github-apps/APP_ID` | Show an App. |
| `POST /api/v1/github-apps/APP_ID/enable` | Enable an App. |
| `POST /api/v1/github-apps/APP_ID/disable` | Disable an App. |
| `POST /api/v1/github-apps/APP_ID/sync` | Sync an App from GitHub. |
| `GET /api/v1/github-apps/APP_ID/installations` | List the installations and repositories. |

## Self-Service Registration

When a repository goes into an installation, Reactorcide binds the
repository to that installation.

- **No project exists.** Reactorcide makes a project with the default
  settings. The name is the repository name. `is_private` is the private
  flag of the repository. A public repository stays publicly visible. The
  project records the GitHub repository ID (`vcs_repository_id`). The
  project is enabled or pending approval (below). The default runner image
  of the project is the default runner image of the deployment
  (`REACTORCIDE_DEFAULT_RUNNER_IMAGE`). When that setting is empty, the
  project gets the default of the database.
- **A project exists.** Reactorcide binds the project. It does not change the
  project settings. A project with no repository ID gets the ID of the
  repository.
- **A project exists with another repository ID.** GitHub gave the name to
  a new repository (the old one was deleted or renamed). Reactorcide binds
  the repository, but does not link the project. The project keeps its token
  path, gets no App token, and gets no App delivery of the new repository.
  The audit event is `github_app.repository_id_mismatch`.
- **The repository leaves the installation.** Reactorcide removes the
  binding. A project that the App registered is disabled. Reactorcide does not
  delete it. A project from before the App goes back to its token path.
- **The repository comes back.** Reactorcide enables the project again when
  the removal disabled it.
- **The installation is deleted.** The same rules as a removal apply to each
  repository.
- **The installation is suspended.** The installation gives no tokens. Its
  projects use their token path until GitHub unsuspends it.

The organization of a new project is, in this order:

1. The account mapping (`github-apps map-account`).
2. The default organization of the App (`--default-org`).
3. The default organization of the deployment.

Anybody can install a public App on their own account. Thus a new project is
enabled only when an administrator chose it:

| Organization from | New project |
| --- | --- |
| The account mapping | Enabled. |
| The default organization of the App, with `--open-registration` | Enabled. The owner chose open registration for that App. |
| The default organization of the App, without `--open-registration` | Disabled, pending approval. |
| The default organization of the deployment | Disabled, pending approval. |

A pending project runs no jobs from webhooks, and the organization-wide
secret grants do not apply to it. `github-apps installations` shows the
repository with `(pending approval)`, and the GitHub Apps page of the admin
UI shows it the same way. The audit event is
`github_app.project_pending_approval`. To approve, enable the project.
`projects update` takes the project ID, not the name. Use `projects list` to
find the ID:

```bash
./coordinator_api/reactorcide projects update PROJECT_ID --enabled
```

To refuse, delete the project or leave it disabled.

## Many Apps and Rate Limits

Reactorcide records the `x-ratelimit-limit`, `x-ratelimit-remaining`,
`x-ratelimit-used`, and `x-ratelimit-reset` headers of each GitHub response.
It keeps one snapshot for each App (App calls) and one for each installation
(installation-token calls).

The headroom of an App is the lowest remaining fraction of the App snapshot
and of the snapshots of its active installations. A snapshot after its reset
time counts as full.

The recommended App is the enabled App with:

1. the most headroom,
2. then the fewest bound repositories,
3. then the lowest App ID.

Send new installations to the `install_url` of the recommended App. A new
installation belongs to the App that received its webhook.

One repository binds to one installation. When two Apps are on the same
account, the first active binding stays. Thus the check runs of a repository
come from one App.

## Tokens

- Reactorcide signs an App JWT with RS256 (go-jose). The issue time is 60
  seconds in the past. The expiry is 9 minutes after now.
- An installation token stays in a cache until 5 minutes before it expires.
- A token request asks only for permissions that the installation granted,
  from this set: `contents: read`, `metadata: read`, `checks: write`,
  `statuses: write`, `pull_requests: write`, `members: read`. GitHub refuses a
  request for more.
- A checkout token reads only one repository (`contents: read`,
  `metadata: read`). It goes only into the lease `vcs_auth`. It is never in
  the job environment.
- A checkout token is only for a job of the project of the binding. A job
  names its own source URL. When the URL is a repository of another project
  or another organization, the job gets no installation token and uses the
  token path of its own project. A job with no project never gets an App
  token. The trusted CI checkout (`ci_source_url`) gets no `vcs_auth`.
- A fork repository that no installation holds uses the token path of the
  project.
- An account token (team reads, `InstallationTokenForAccount`) is only for
  an organization that is entitled to the installation: the installation
  holds a repository of a project of that organization, or the account is
  in the VCS owner allowlist of the organization
  ([Principals](./principals.md)). Another organization gets no token.

Installation tokens are system credentials, the same as other VCS tokens. See
[VCS Credentials and Secret Grants](./vcs-credentials-and-secret-grants.md).

## The Re-run Button

A finished `Reactorcide Checks` run of a pull request has a **Re-run**
button. These deliveries ask for a re-run:

- `check_run` with action `rerequested`,
- `check_run` with action `requested_action` and identifier `rerun`,
- `check_suite` with action `rerequested`.

Reactorcide runs the `rerun` command of the GitHub sender, the same as a
`/reactorcide rerun` comment:

- The sender must be a run approver of the project (`run_approvers`).
- The head of the pull request must not have changed.
- A push commit has no pull request. It has no button, and a re-run request
  for it does nothing.

## Report Modes

The report mode of the project applies to check runs. In `silent` mode,
Reactorcide posts no check run. See
[Reporting and Ownership](./reporting-and-ownership.md).

## Move a Token-Based Project to the App

A project moves to the App when its repository is in an installation. You do
not change the project.

1. Install the App on the repository.
2. Remove the repository webhook. If you keep it, GitHub sends each event two
   times.
3. Change branch protection or the ruleset. Require the `Reactorcide Checks`
   check, and select the App as its source. A commit status and a check run
   with the same name are different sources.
4. If the project requires reviews, do the same for `Reactorcide Reviews`.

The project token stays in the project. Reactorcide uses it again when the
repository leaves the installation or the App is disabled.

## Configuration

| Variable | Default | Meaning |
| --- | --- | --- |
| `REACTORCIDE_GITHUB_APP_API_URL` | `https://api.github.com` | The GitHub API of App calls. Set it for GitHub Enterprise Server, for example `https://github.example.com/api/v3`. |

## Storage

Migration `000057` adds these tables:

| Table | Content |
| --- | --- |
| `github_apps` | The Apps, the encrypted key and webhook secret, the App rate-limit snapshot. |
| `github_app_installations` | The installations, granted permissions, suspended and deleted state, the installation rate-limit snapshot. |
| `github_app_installation_repositories` | One binding for each repository, with `repository_id` and `pending_approval`. |
| `github_account_orgs` | The account mappings. |

It also adds `github_apps.open_registration` (default `false`) and
`projects.vcs_repository_id` (the GitHub repository ID, `NULL` until a
binding records it).

## Audit

| Action | When |
| --- | --- |
| `github_app.registered` | An App is registered. |
| `github_app.credentials_replaced` | The key and secret are replaced. |
| `github_app.enabled`, `github_app.disabled` | An App is enabled or disabled. |
| `github_app.synced` | A sync ran. |
| `github_app.installation_created`, `github_app.installation_deleted` | An installation is made or deleted. |
| `github_app.installation_suspended`, `github_app.installation_unsuspended` | An installation is suspended or unsuspended. |
| `github_app.project_registered` | A project is made for a repository. The details have `org_source` and `enabled`. |
| `github_app.project_pending_approval` | A new project is disabled and waits for approval. |
| `github_app.repository_id_mismatch` | A project has another repository ID than the installed repository. |
| `github_app.project_enabled`, `github_app.project_disabled` | A repository comes back or leaves. |
| `github_app.account_mapped`, `github_app.account_unmapped` | An account mapping changes. |

## Troubleshooting

### The App webhook returns 401

- `Unknown GitHub App`: the App is not registered, or it is disabled.
  Run `github-apps list`.
- `Invalid webhook signature`: the secret in GitHub is not the registered
  secret. Run `github-apps add --replace` with the correct secret file.

### A delivery returns `ignored`

- `the repository is not in an active installation of this App`: install
  the App on the repository, or run `github-apps sync <app-id>`.
- `the project is not the project of the App binding`: the binding has no
  project or another project. Run `github-apps sync <app-id>`.
- `the project belongs to another repository with the same name`: the
  project was made for a repository that was deleted or renamed. Delete
  the project, or set its repository again.

### No check run shows

- Confirm that the App has the **Checks** read and write permission. After
  you change a permission, the account must accept it.
- Confirm that the repository is in an active installation:
  `github-apps installations <app-id>`.
- Confirm that the report mode is not `silent`.

### The Re-run button does nothing

- The sender is not a run approver of the project.
- The pull request has a newer head. Use the button of the newest run.
- The commit is a push, not a pull request.
