# Artifacts and Test Results

This page tells how a job publishes files (artifacts), how Reactorcide reads
JUnit test reports, how it finds flaky tests, and how a test quarantine
works.

Reactorcide knows no build tool and no test tool (owner decision D6). A job
writes plain files. JUnit XML is a generic report format that most test tools
can write.

## Publish artifacts

A job publishes a file when it writes the file under its artifacts
directory:

| Variable | Value |
| --- | --- |
| `REACTORCIDE_ARTIFACTS_DIR` | `/job/artifacts` |

```sh
mkdir -p "$REACTORCIDE_ARTIFACTS_DIR/dist"
cp build/app.tar.gz "$REACTORCIDE_ARTIFACTS_DIR/dist/"
go test -json ./... | go-junit-report -parser gojson > "$REACTORCIDE_ARTIFACTS_DIR/junit.xml"
```

No job YAML key is necessary. The worker sets the variable only when the
coordinator accepts artifacts. A job that must also run on an older worker
can test the variable:

```sh
if [ -n "${REACTORCIDE_ARTIFACTS_DIR:-}" ]; then cp report.xml "$REACTORCIDE_ARTIFACTS_DIR/junit.xml"; fi
```

### How the upload works

1. The job ends (any status).
2. The worker reads the regular files under the artifacts directory, in
   path order. It does not follow a symbolic link, and it skips devices and
   sockets. A link could point at a file of the worker host.
3. The worker sends each file to the coordinator in chunks
   (`upload-artifact` on the authenticated worker connection). The worker
   never connects to the object store (see the trust boundaries in
   [DESIGN.md](../DESIGN.md#trust-boundaries)).
4. The coordinator writes each chunk to the object store under
   `artifacts/<artifact namespace>/<job id>/<artifact id>/<chunk>`. It
   computes the size and the SHA-256 digest from the bytes that it stored.
   The worker cannot set them.
5. The worker sends the job result. The coordinator closes the artifacts: an
   upload that did not complete becomes `failed`.

The artifact namespace is the namespace of the cache decision of the job
(`REACTORCIDE_ARTIFACT_NAMESPACE`, see
[Builds, Caches, and Job Settings](./builds-and-caches.md)). Thus a pull
request job and a production job write under different prefixes when the
profile splits the namespace by ref class.

The coordinator records the prefix of the job in
`jobs.artifacts_object_key`.

### Runners

| Runner | Artifacts | Quarantined tests file |
| --- | --- | --- |
| Docker, containerd | Yes. The worker reads the workspace that it mounts at `/job`. | Yes |
| Kubernetes | Yes. The worker reads `/job/artifacts` as a tar stream through the workflow output reader container, then deletes the pod. A job that is not in a workflow gets the reader container when its lease has artifacts. | Yes. The `prepare-workspace` init container writes it (at most 256 KiB). |
| VM (macOS, Windows) | No. The guest does not see the worker workspace. | No |
| `run-local` | No upload. The files stay in the local workspace. | No |

### Limits

The coordinator checks the limits on each chunk. The worker uses the same
limits to skip a file early. A file over a limit fails; the other files of the
job still upload. A refused artifact never fails the job.

| Variable (coordinator) | Default | Meaning |
| --- | --- | --- |
| `REACTORCIDE_ARTIFACTS_ENABLED` | `true` | `false` turns artifact uploads off. |
| `REACTORCIDE_ARTIFACT_MAX_FILE_BYTES` | 256 MiB | The largest file. |
| `REACTORCIDE_ARTIFACT_MAX_JOB_BYTES` | 1 GiB | All files of one job. |
| `REACTORCIDE_ARTIFACT_MAX_FILES` | 1000 | The number of files of one job. |
| `REACTORCIDE_ARTIFACT_CHUNK_BYTES` | 4 MiB | The chunk size. At most 8 MiB. |

A path is relative to the artifacts directory. It has at most 512 bytes and
no `.` or `..` part.

The job byte limit is atomic. The coordinator locks the job row, adds the
sizes of all files of the job, and records the chunk in one transaction. Two
uploads of one job at the same time cannot both pass the limit.

### Masking

The worker masks a test report with the secret values of the job before the
upload, the same as the job logs. It does not mask other files. When the
coordinator reads a test report, it masks the suite, the class, the name,
the file, and the message of each test with the secret values of the job
again. The test ID is a hash of the masked names. Do not write
a secret to an artifact. A public repository has public artifacts.

### Retention

Artifacts follow the telemetry retention
(`REACTORCIDE_TELEMETRY_RETENTION_DAYS`). With the default value `0`, the
coordinator keeps artifacts. When a job is older than the retention, the
coordinator deletes its artifact objects and records. The test results stay,
because the flakiness history uses them.

### Read artifacts

Access follows the visibility of the job. In the UI, the jobs of a public
project are public. A private job is "not found" to a caller with no role.

| Channel | Operation |
| --- | --- |
| UI | The job page has an "Artifacts" card with a download button for each file. The button shows the progress in percent. |
| REST | `GET /api/v1/jobs/JOB_ID/artifacts` lists them (`jobs:read`). `GET /api/v1/jobs/JOB_ID/artifacts/ARTIFACT_ID` downloads one. |
| CSIL | `list-job-artifacts`, `read-job-artifact` (at most 4 MiB for each call, from an offset) |
| CLI | `reactorcide artifacts list JOB_ID`, `reactorcide artifacts download JOB_ID PATH_OR_ID [-o FILE]` |

A REST download has `Content-Type: application/octet-stream`,
`X-Content-Type-Options: nosniff`, and `Content-Disposition: attachment`, so
that an HTML artifact cannot run in the API origin. The
`X-Reactorcide-Artifact-SHA256` header has the digest. The CLI compares the
digest of the download with the recorded digest.

The UI reads an artifact through `read-job-artifact` in parts of at most
4 MiB. One CSIL-RPC response is one envelope, so the parts are the stream:

- In a browser with the File System Access API (Chromium), the UI asks for a
  file first and writes each part to it when the part arrives. Memory holds
  one part at a time.
- In a browser without this API (Firefox, Safari), the UI collects the parts
  and saves them as one file. The full artifact is then in browser memory
  one time. For a very large artifact, use the CLI or REST.

## Test reports

### Which files are test reports

A file is a test report when one of these is true:

- Its file name contains `junit` and ends with `.xml`, in any directory of
  the artifacts directory. For example `junit.xml`,
  `reports/junit-unit.xml`, `TEST-junit-api.xml`.
- The workflow output declares it:

  ```json
  {"test_reports": ["results/unit.xml", "results/api.xml"]}
  ```

  The paths are relative to the artifacts directory. The workflow output
  file is `/job/workflow-output.json` (workflow jobs).

### What the coordinator reads

When the job result arrives, the coordinator parses each report. The parser:

- streams the XML,
- reads at most 32 MiB and 20000 test cases of each report, and at most
  50000 test cases of each job,
- refuses a DTD that declares entities, and never reads an external entity
  (no XXE),
- accepts UTF-8 only,
- limits the element depth to 64 and each message to 4096 bytes.

A report that is not valid XML, or that is too large, gives a parse error in
the test summary. The cases before the error stay.

The parser reads `testsuites`, `testsuite` (nested suites too), and
`testcase` with `name`, `classname`, `time`, `file`, and `line`. A
`failure` makes the test `fail`, an `error` makes it `error`, and `skipped`
makes it `skip`. Otherwise the test is `pass`. The message is the `message`
attribute, then the element text. `system-out` and `system-err` are not
stored.

### Test ID

Each test has a stable test ID: a hash of the project, the suite path, the
class name, and the test name (32 hex characters). The suite path is the
names of the nested `testsuite` elements, joined with `/`. The same test in
another project has another ID.

### Where the results show

| Place | Content |
| --- | --- |
| PR comment | For each workflow node with reports: the counts, then at most 5 failing tests with the first 3 lines of each message. Open failures come before quarantined failures. |
| UI job page | The "Tests" card: the counts, the failing tests with their first lines, the file and line, and the flags `quarantined` and `flaky`. |
| REST | `GET /api/v1/jobs/JOB_ID/tests[?status=fail,error&limit=N]` |
| CSIL | `get-job-test-results` |
| CLI | `reactorcide tests results JOB_ID [--failed]` |

A quarantined failure does not make the test summary fail: the PR comment
shows 🟨, not ❌, when every failure is quarantined.

## Flaky tests

A test is flaky on a commit when it passed and failed (or had an error) on
that commit, also across retries and re-runs, within the detection window.
The coordinator counts each flaky commit one time.

| Variable (coordinator) | Default | Meaning |
| --- | --- | --- |
| `REACTORCIDE_FLAKY_WINDOW_DAYS` | 30 | The detection window. |

The coordinator records for each test of a project: the number of flaky
commits, the number of runs (pass, fail, or error), the last flaky time, and
the last flaky commit.

| Channel | Operation |
| --- | --- |
| UI | The project page has a "Tests" card with the flaky tests. The job page flags flaky tests. |
| REST | `GET /api/v1/projects/PROJECT_ID/tests/flaky[?days=N]` |
| CSIL | `list-flaky-tests` |
| CLI | `reactorcide tests flaky --project PROJECT_ID [--days N]` |

## Test quarantine

A test quarantine (plan item A4) removes one test from the failure counts
until an expiry. A quarantine has:

- an owner principal (required; the principal of the caller by default),
- a reason (required, at most 1000 characters),
- an expiry (required, at most the organization maximum, 30 days by default:
  `max_quarantine_days` of the required set settings).

At the expiry the quarantine ends with no action. A new quarantine of the
same test replaces the current one. Each change writes an audit record
(`test.quarantine`, `test.unquarantine`).

### Who can quarantine

- An org admin of the project organization.
- A principal with the project role `owner` or `admin`.
- An owner of the workflow that the test ran in (a workflow role holder or a
  member of the owner group, see [Principals](./principals.md)), when the
  request names the workflow security ID.
- An instance or service token with no roles (for example the admin token of
  a deployment with no logins) that has `projects:manage` for the
  organization. It must name the owner, because it has no principal.

Any caller that can view the project can list the quarantines.

| Channel | Operation |
| --- | --- |
| UI | The job page "Tests" card has a "Quarantine" button for each failing test. The project page "Tests" card lists the quarantines and can end one. |
| REST | `GET /api/v1/projects/PROJECT_ID/test-quarantines[?all=true]`, `POST` (body below), `DELETE /api/v1/projects/PROJECT_ID/test-quarantines/QUARANTINE_ID` |
| CSIL | `list-test-quarantines`, `quarantine-test`, `unquarantine-test` |
| CLI | `reactorcide tests quarantine add\|list\|remove` |

```json
{
  "test_id": "3f2a...",
  "reason": "fails on arm64 runners, issue 812",
  "duration": "7d",
  "owner_principal_id": "PRINCIPAL_ID",
  "workflow_security_id": "unit-tests"
}
```

Give `test_id`, or `suite`, `classname`, and `name`. Give `until` (an RFC
3339 time) or `duration` (`72h`, `7d`).

```bash
reactorcide tests quarantine add --project "$PROJECT_ID" \
  --suite unit --class pkg.Client --name TestRetry \
  --reason "fails on arm64 runners" --for 7d --owner "$PRINCIPAL_ID"
```

### What a quarantine changes

The exit code of a job is the job's own. Reactorcide does not change it. A
quarantine acts in two places:

1. **The job can read the quarantined tests.** The worker writes the active
   quarantines of the project to a file before the job starts:

   | Variable | Value |
   | --- | --- |
   | `REACTORCIDE_QUARANTINED_TESTS_FILE` | `/job/quarantined-tests.json` |

   ```json
   {"version": 1, "tests": [
     {"test_id": "3f2a...", "suite": "unit", "classname": "pkg.Client", "name": "TestRetry", "expires_at": "2026-11-23T10:00:00Z"}
   ]}
   ```

   A test command can skip these tests or ignore their result. For example:

   ```sh
   skip=$(jq -r '.tests[].name' "$REACTORCIDE_QUARANTINED_TESTS_FILE" | paste -sd'|' -)
   go test ./... ${skip:+-skip "^(${skip})$"}
   ```

   Then the job does not fail for a quarantined test.

2. **The workflow node can ignore quarantined failures.** A job that runs
   the quarantined tests and fails because of them stays `failed`. Its
   workflow node fails too, unless the job definition opts in:

   ```yaml
   job:
     image: golang:1.26
     command: |
       go test -json ./... > test.json
       status=$?
       go-junit-report -parser gojson < test.json > "$REACTORCIDE_ARTIFACTS_DIR/junit.xml"
       # The exit status came from the test runner.
       echo '{"test_exit": true}' > /job/workflow-output.json
       exit $status
     quarantine_policy: ignore_quarantined
   ```

   | `quarantine_policy` | Result of a failed job |
   | --- | --- |
   | `report` (default) | The node fails. The PR comment shows the quarantined failures as quarantined. |
   | `ignore_quarantined` | The node succeeds when all of these are true: the workflow output of the job has `"test_exit": true`, the job failed with the failure class `job` (its own non-zero exit), its reports have at least one failed test, every failed test is quarantined, and every report was read completely. The job status stays `failed`; the node reason says why the node succeeded. |

   `test_exit: true` in the workflow output (`/job/workflow-output.json`)
   is the statement of the job that its non-zero exit came from the test
   runner. Without it, the node fails, also when every failed test is
   quarantined. A JSON `true` is necessary; a string does not count.

   The policy trusts the report of the job. Reactorcide cannot see why a
   process exited. A job that writes `test_exit: true` and then fails in a
   later step (a lint, a build, a push) hides that failure. Thus the
   override is an opt-in of the workflow owner, who writes the trusted CI
   definition. Write `test_exit` only with the exit status of the test
   command, as in the example. A timeout, an OOM kill, an infrastructure
   failure, or a report that was not read completely is never ignored.

## Database

Migration `000061_artifacts_tests_provenance.sql` adds `job_artifacts`,
`test_results`, `job_test_summaries`, `test_flakiness`,
`test_flaky_commits`, `test_quarantines`, and `provenance_signing_keys`. No
existing row changes. A job that writes no artifacts has no rows.

See also [Provenance](./provenance.md).
