# Failure Diagnosis by Service Principals

Reactorcide gives primitives for an external diagnosis tool (plan item F6).
Reactorcide itself does not diagnose a failure. It has no model and no
language model code. The tool is a program that you operate. It can use any
method.

The tool is a service principal ([Principals](./principals.md)). It:

1. Gets a `node.post_complete` event of a failed job from an outbound
   subscription ([Outbound Events](./outbound-events.md)).
2. Reads the job, the logs, and the artifacts with its own grants.
3. Posts one diagnosis entry for the job, the workflow node, or the workflow.
4. Optionally retries the job with shifts (CPU, memory, storage, timeout).

Reactorcide shows each entry on the job page, on the workflow page, and as a
section in the rolling pull request comment. Each entry shows the display
name of its author and the kind of the author (`service` or `human`).

Every step works with no logins: the admin API token can do all of it
through the CLI and the REST API.

## Set Up a Diagnosis Tool

1. Make a service principal and give it a role in the organization:

   ```bash
   reactorcide principals create --org acme diag-bot --display-name "Diag Bot"
   reactorcide principals grant-role --org acme --role member diag-bot
   ```

2. Issue a token for the principal. Keep it in a secret store at once:

   ```bash
   reactorcide principals tokens create --org acme diag-bot \
     --capability jobs:read --capability logs:read \
     --capability workflows:read --capability diagnosis:write
   ```

3. Make the subscription with one command:

   ```bash
   reactorcide diagnosis setup --org acme --principal diag-bot \
     --url https://diag.example.com/reactorcide
   ```

   The command makes an org subscription that the principal owns. Its
   filters are the event type `node.post_complete` and the failure classes
   `job`, `oom`, and `timeout`. Use `--scope project --project ID` for one
   project, `--failure-class` (repeat) for other classes, and `--name` for a
   different name (the default is `diagnosis-PRINCIPAL`). The command shows
   the signing secret one time. Use `--secret-file` to give your own secret.

The default failure classes are the classes that the pipeline owner owns
([Failure Classes](./runtime-behavior.md#failure-classes)). An `infra.*`
failure is for the CI owner, and some of them retry automatically.

## Grants

The authority of the tool comes from its roles and its token, the same as
for any principal (owner decision D4).

| Action | Needs |
| --- | --- |
| Read a job and its workflow | `jobs:read` (and `workflows:read` for a workflow), and a role that can view the job |
| Read the logs | `logs:read`, and a role that can view the job |
| Read the artifacts and test results | `jobs:read`, and a role that can view the job (`GET /api/v1/jobs/JOB/artifacts`, `/tests`) |
| Post a diagnosis entry | `diagnosis:write`, and a role in the scope of the job (see below) |
| Retry with shifts | `jobs:retry` (or `workflows:control`), and the retry permission. Shifts need no other permission (owner decision D5). |

A token of a service principal intersects its capabilities with the roles
of the principal:

- An organization `member` role keeps the read capabilities and
  `diagnosis:write`. A diagnosis entry only adds text that is labelled with
  its author. It changes no run.
- An organization `admin` role keeps every issued capability. A
  `member` role does not keep `jobs:retry`. Thus, a tool that must retry
  needs an `admin` role, or a person retries from the suggested shifts.
- A principal with no organization role has no scope. Its token can do
  nothing.

A post needs one of these roles in the scope of the job: a global admin
role, any organization role in the organization of the job, or any project
role in the project of the job. A user token and a UI session follow the
same rule. A token with no live roles (the admin token, or a service token
made before principals) has only its capabilities. A job token can never
post. An anonymous caller can never post.

Public repositories stay publicly visible: anybody can read the logs of a
public job in the UI. A post always needs a principal and the capability.

## Masking Caution

The worker masks secret values in the logs before it stores them. The REST
API, the CSIL operations, and the UI serve only the stored, masked logs. Thus
the masking applies to every caller, also to a diagnosis tool.

The masking replaces literal values only. It cannot find a secret that a job
prints in another form, for example in base64, in hexadecimal, URL-encoded,
split over two lines, or changed in any other way. Such a value reaches every
reader of the logs, also the diagnosis tool, and from there possibly an
external service of the tool.

Recommendations:

- For a job that a diagnosis tool reads, deliver secrets through the egress
  proxy (`delivery: proxy`), not in the job environment. Then the job never
  has the real value and cannot print it. See
  [Egress Proxy and Proxy-Delivered Secrets](./egress-proxy.md).
- Turn on egress control for such jobs. The proxy also removes secret
  values in raw and encoded forms from the network traffic of the job.
- Give the tool access only to the projects that it must diagnose (a
  project subscription, and the narrowest roles).
- Treat the output of the tool as untrusted text. Reactorcide sanitizes it
  (see below), but it does not check its content.

## Diagnosis Entries

An entry has:

| Field | Rules |
| --- | --- |
| `title` | Necessary. 1 to 200 characters. One line. |
| `body` | Optional Markdown. At most 16384 bytes, also after sanitization. |
| `confidence` | Optional: `low`, `medium`, or `high`. |
| `category` | Optional: 1 to 64 lowercase letters, digits, dots, underscores, or hyphens. For example `dependency`, `flaky-test`, `oom`. |
| `links` | Optional. At most 10 objects `{"name", "url"}`. A name has 1 to 100 characters. A URL has at most 2048 characters. It must be absolute `https` or `http`, with a host and no user information. |
| `suggested_shifts` | Optional. The shifts of a retry: `cpu`, `cpu_request`, `memory`, `storage`, `storage_request`, `timeout`. The coordinator validates them with the retry parser. A key that is not a shift (for example `image`) is refused. |

Reactorcide records the author principal, its kind, and its display name.
One principal has at most one entry for each target. A new post of the same
principal replaces its entry and increments `revision`. A post is thus
idempotent: send the full entry each time.

Targets:

- A job: the entry is about one job.
- A workflow node: Reactorcide finds the job of the node. The entry is a job
  entry with the node name.
- A workflow: the entry is about the whole workflow. The list of a workflow
  also has the entries of the jobs of its nodes.

A suggested shift is a suggestion only. Reactorcide does not apply it. The
tool or a person retries with it.

### Sanitization

The UI shows the title and the body as plain text, never as HTML or as
rendered Markdown.

The pull request comment shows a sanitized form:

- No HTML: each `<` outside code becomes `&lt;`. Thus no tag, no HTML
  comment, and no `<img>` is possible.
- No character references: each `&` outside code becomes `&amp;`. Thus
  `&#64;admin` or `&commat;admin` cannot make an `@`, and `&#60;` cannot
  make a `<`.
- No images: `![text](url)` becomes the link `[text](url)`.
- No mentions that notify: every `@` gets a zero-width space after it, also
  after `_`, `*`, a backtick, or a letter. Thus `_@org/team_` and
  `dev@example.com` do not notify. The text looks the same.
- No issue references: `#123` and `GH-123` get a zero-width space before the
  digits. Thus a diagnosis does not add a cross-reference to another issue
  or pull request.
- No report markers: `-->` and `<!--` cannot close or open a section of the
  comment, also in code.
- No control characters and no bidirectional override characters.
- The title, the author name, and the link names are one line, with no
  Markdown link or emphasis syntax.
- The comment shows at most 4000 characters of the body. A longer body
  ends with a link to the job or workflow page.
- An open fenced code block is always closed at the end of the body, also
  when the body is not cut. Thus a body cannot put the next section of the
  comment into a code block.

The sanitizer finds fenced code blocks with the CommonMark rules:

- A fence has 0 to 3 spaces of indentation. A line with 4 or more spaces, or
  with a tab in the indentation, does not open or close a fence.
- A fence has 3 or more backticks or tildes. The info string after a
  backtick fence cannot contain a backtick: "```x`y" is text, not a fence.
- A fence closes with the same character, at least as many characters as the
  opening fence, and only spaces or tabs after them.
- An inline code span starts with a run of backticks and ends with a run of
  the same length.

Code keeps its visible text. In code, the sanitizer adds only zero-width
spaces: after each `@`, `<`, and `&`, and before the digits of `#123`.
GitHub can see code where the sanitizer does not (in a list item or a block
quote), and the opposite. With these invisible changes, text that GitHub
shows as normal text is safe also when the sanitizer saw it as code. A copy
of code from the comment can contain the zero-width spaces. The job page
shows the original body.

### Pull Request Comment

When the job or the workflow has a pull request, the entry is a section of
the rolling pull request comment (the shared report of `internal/vcsreport`).
The section has the heading `Diagnosis of SUBJECT: TITLE — by NAME (KIND)`,
for example `Diagnosis of build: Out of memory — by Diag Bot (service)`. The
section shows the confidence, the category, the body, the suggested shifts,
the links, and the line "Posted by the service principal NAME. Reactorcide
does not check the content of a diagnosis."

Each (target, author) has its own section. A new post of the same author
replaces the section. A new commit of the pull request starts a new report
generation; the sections of the old commit then go out of the comment, and
the entries stay on the job and workflow pages.

### Rate Limit

Each principal can post 30 entries a minute, with a burst of 10. Over the
limit, REST answers `429` with `Retry-After`, and CSIL answers
`rate_limited`. Each coordinator replica has its own buckets in memory.

| Setting | Default | Meaning |
| --- | --- | --- |
| `REACTORCIDE_DIAGNOSIS_RATE_LIMIT_PER_MINUTE` | `30` | Posts a minute for each principal. `0` turns the limit off. |
| `REACTORCIDE_DIAGNOSIS_RATE_LIMIT_BURST` | `10` | Posts at the same time before the rate applies. |

The metric `reactorcide_rate_limited_total{kind="diagnosis"}` counts the
refused REST posts.

## End-to-End Flow

1. A job fails. The coordinator sends `node.post_complete` with
   `failure_class` `job`, `oom`, or `timeout` to the subscription. Verify the
   signature ([Outbound Events](./outbound-events.md#verify-the-signature))
   and drop a duplicate event ID.

2. Read the job and the logs with the token of the principal:

   ```bash
   curl -H "Authorization: Bearer $TOKEN" \
     https://ci.example.com/api/v1/jobs/JOB_ID
   curl -H "Authorization: Bearer $TOKEN" \
     "https://ci.example.com/api/v1/jobs/JOB_ID/logs?stream=combined"
   ```

3. Post the entry:

   ```bash
   curl -X PUT -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
     https://ci.example.com/api/v1/jobs/JOB_ID/diagnosis \
     -d '{"title": "Out of memory in the test step",
          "body": "The test process used more than 4Gi. See the heap profile.",
          "confidence": "high", "category": "oom",
          "links": [{"name": "Heap profile", "url": "https://diag.example.com/r/123"}],
          "suggested_shifts": {"memory": "8Gi"}}'
   ```

   Or with the CLI:

   ```bash
   reactorcide diagnosis post --job JOB_ID --title "Out of memory in the test step" \
     --body-file diagnosis.md --confidence high --category oom \
     --link "Heap profile=https://diag.example.com/r/123" --shift memory=8Gi
   ```

4. Optionally retry with the suggested shifts. The answer of a post has
   `suggested_shifts` with the keys of a retry body:

   ```bash
   curl -X POST -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
     https://ci.example.com/api/v1/jobs/JOB_ID/retry \
     -d '{"shifts": {"memory": "8Gi"}}'
   ```

   The retry keeps the authority of the original run (owner decision D18).
   A retry that changes anything other than shifts is refused (D25). The
   token needs `jobs:retry` (see [Grants](#grants)). The retry bounds are
   the `rerun_shift_limits` setting.

## REST API

| Method and path | Action |
| --- | --- |
| `GET /api/v1/jobs/JOB/diagnosis` | List the entries of a job. |
| `PUT /api/v1/jobs/JOB/diagnosis` | Post or replace the entry of the caller for a job. |
| `GET /api/v1/workflows/WORKFLOW/diagnosis` | List the entries of a workflow and of the jobs of its nodes. |
| `PUT /api/v1/workflows/WORKFLOW/diagnosis` | Post or replace the entry of the caller for a workflow. |
| `GET`, `PUT /api/v1/workflows/WORKFLOW/nodes/NODE/diagnosis` | The same for the job of a node. |
| `DELETE` on each of these paths | Remove the entry of the caller for the target, and its pull request section. |

A principal can remove only its own entry: the entry of a target is the
entry of its author. The caller must see the target. `diagnosis:write` is not
necessary, so a tool can remove its entry after it lost the capability. The
answer is `200` with `deleted` and `diagnosis_id`, or `404` when the caller
has no entry for the target. The coordinator writes the audit record
`diagnosis.delete`. The pull request comment loses the section at its next
render. CLI: `reactorcide diagnosis delete --job JOB` (or `--workflow` and
`--node`).

A `PUT` body has the fields of [Diagnosis Entries](#diagnosis-entries). An
unknown field is refused. The answer is `200` for a new and for a replaced
entry. The field `created` tells which one. Errors:

| Status | Cause |
| --- | --- |
| `400` | An input is not valid. |
| `403` | The caller has no `diagnosis:write`, no role in the scope, or no principal. |
| `404` | The target does not exist, or the caller cannot view it. |
| `429` | The principal is over the rate limit. |

An entry in an answer:

```json
{"diagnosis_id": "…", "target_type": "job", "job_id": "…", "workflow_id": "…", "node_name": "build",
 "author": {"principal_id": "…", "kind": "service", "display_name": "Diag Bot"},
 "title": "Out of memory in the test step", "body": "…", "confidence": "high", "category": "oom",
 "links": [{"name": "Heap profile", "url": "https://diag.example.com/r/123"}],
 "suggested_shifts": {"memory": "8Gi"}, "revision": 1,
 "created_at": "2026-11-16T10:00:00Z", "updated_at": "2026-11-16T10:00:00Z", "created": true}
```

## CSIL and UI

The UI uses the CSIL operations `list-diagnoses`, `put-diagnosis`, and
`delete-diagnosis`. They apply the same rules as REST. The job page and the
workflow page show a **Diagnosis** card when there are entries. The card
loads again when the status of the job or the workflow changes. The card
shows a delete control on the entries of the signed-in person.

The **Event subscriptions** page has a **Failure classes** filter.

## CLI

```bash
reactorcide diagnosis setup --org ORG --principal NAME --url URL [--scope org|project] [--project ID] [--failure-class C ...] [--name N] [--secret-file F]
reactorcide diagnosis post (--job JOB | --workflow WF [--node NODE]) --title T [--body B | --body-file F|-] [--confidence C] [--category C] [--link NAME=URL ...] [--shift KEY=VALUE ...]
reactorcide diagnosis list (--job JOB | --workflow WF [--node NODE])
reactorcide diagnosis delete (--job JOB | --workflow WF [--node NODE])
```

`subscriptions create` and `subscriptions update` also accept
`--failure-class` (repeat). `subscriptions update --clear failure-class`
removes the filter.

## Database

Migration `000065_diagnosis_reports.sql`:

- Adds the table `diagnosis_reports`. A unique index permits one entry for
  each (job, author) and each (workflow, author). The body has a size CHECK.
- Adds `event_subscriptions.failure_classes` (`text[]`, empty by default).
  An empty list matches every event, as before.

It changes no existing row. The `-- +goose Down` section removes both.

Audit records: `diagnosis.post` for each post, with the target, the author,
the revision, and the suggested shifts. The record has no body text.
