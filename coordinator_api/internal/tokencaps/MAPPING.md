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
| `workers:manage` | `ManageWorkers` |
| `policies:manage` | Policy, profile, and approval management handlers |
| `tokens:manage` | Token creation, inspection, and revocation handlers |
| `audit:read` | Audit export handler |

