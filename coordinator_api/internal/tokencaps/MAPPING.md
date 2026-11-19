# Token Capability Mapping

Token capability strings are the authorization source of truth. The REST
handler checks the capability before it reads or changes a resource. The
`authz.Caps` structure remains the UI wire format.

| Token capability | UI capability or enforcement point |
| --- | --- |
| `organizations:read` | Organization list and get handlers |
| `organizations:manage` | `GlobalAdmin` and organization mutation handlers |
| `projects:read` | `ViewPrivate` and project visibility checks |
| `projects:create` | `CreateProject` |
| `projects:manage` | `DeleteProject` and `ProjectSettings` |
| `jobs:submit` | Job and trigger submission handlers |
| `jobs:read` | Job visibility checks |
| `jobs:cancel` | `Cancel` and `Kill` |
| `jobs:retry` | `Retry` |
| `logs:read` | Job log handlers |
| `workflows:read` | Workflow visibility checks |
| `workflows:control` | `Cancel` and `Retry` for workflows |
| `secrets:manage` | `ManageSecrets`, `ManageWebhookSecrets`, and `ManageVCSCredentials` |
| `workers:manage` | `ManageWorkers` and `ViewWorkers` |
| `workers:read` | `ViewWorkers` (list workers, pools, queues, worker classes) and queue backlog read (`GET /api/v1/backlog`); `workers:manage` also gives it |
| `policies:manage` | Policy, profile, and approval management handlers |
| `tokens:manage` | Token creation, inspection, and revocation handlers |
| `audit:read` | Audit export handler |
| `diagnosis:write` | Diagnosis entry posts (`PUT .../diagnosis`, CSIL `put-diagnosis`), with a role in the scope of the job (`authz.Resolver.CanPostDiagnosis`). An org `member` role of a service principal keeps it. |


## UI service (CSIL-RPC) operations

The UI service applies the same capabilities. A token gets the role of its
owner, limited to the organizations and the capabilities of the token. See
the full table in `docs/ui-auth.md` ("Sessions and API tokens").

| Token capability | UI service operations |
| --- | --- |
| `organizations:read` | Reads of groups, group members, group VCS links, role assignments, principals (UI service and REST); `list-users`; `get-shed-state`; OIDC settings read; image alias read (`list-image-aliases`, `get-image-alias`, `get-image-rollout-status`); `get-image-policy`; organization member data of `list-orgs`; global admin reads with all organizations (global settings, trusted identities and domain patterns, GitHub Apps, VCS owner allowlist, OIDC keys, VCS identity links) |
| `organizations:manage` | Every `organizations:read` operation, and the changes of groups, group members, group VCS links, role assignments, principals; `delete-org`; shed changes; OIDC settings change; organization alert rules; organization subscriptions; global admin changes (with all organizations) |
| `projects:read` | Schedules read, ref class and priority lane read, required set read, organization alert rule list |
| `projects:manage` | `update-project`, `delete-project`, schedule changes, project subscriptions, project alert rules |
| `policies:manage` | Image alias changes, ref class and priority lane changes, execution profiles, image policy, CI trust settings, required set changes, group priority shift |
| `tokens:manage` | `issue-principal-token`, `revoke-principal-token` (with `organizations:manage`) |
| `workers:read` | `list-workers`, `list-pools`, `list-queues`, `list-worker-classes`, `get-worker-image-state` |
| `workers:manage` | Worker, pool, queue, worker class, and enrollment token changes, `list-enrollment-tokens` |
| `jobs:read`, `workflows:read` | `list-jobs`, `list-workflows`, and reads of private jobs and workflows, in the organizations of the token |
| `jobs:cancel` | `cancel-job`, `kill-job`; `workflows:control` also gives `cancel-job` and `cancel-workflow` |
| `jobs:retry` | `retry-job`, `retry-workflow`, `retry-unsuccessful-jobs`, `rerun-workflow`, and `start-run` (with `jobs:submit`); `workflows:control` also gives them |
| `workflows:control` | `pr-command` (as REST `POST /api/v1/approvals`), and the cancel and retry operations above |
| `policies:manage` (more) | CI policy (`get-ci-policy`, `put-ci-policy`, `delete-ci-policy`), reporting and path acceptance changes, required set approvals |
| `projects:read` (more) | Required set, reporting, and flaky test reads |
| `projects:manage` (more) | `quarantine-test`, `unquarantine-test` |
| `diagnosis:write` | `put-diagnosis` |
| `jobs:read`, `logs:read`, `workflows:read`, `projects:read` | UI event stream (`/api/v1/ui/stream`): job and metrics events, log events, workflow events, project events, in the organizations of the token |

No capability was added for these operations. The full list, with the
operations that check no token capability, is in `docs/ui-auth.md`.
