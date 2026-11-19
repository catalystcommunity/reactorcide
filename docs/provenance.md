# Provenance

Reactorcide writes a signed provenance statement for each completed job that
made an artifact (plan item G2). The platform writes the provenance, not the
job. A job can report only what it made. All other facts come from
coordinator records, so a job cannot change them.

## What a job reports

- **Uploaded artifacts.** Each file that the job wrote under
  `REACTORCIDE_ARTIFACTS_DIR` is a subject. The coordinator computed its
  SHA-256 digest from the stored bytes (see
  [Artifacts and Test Results](./artifacts-and-tests.md)). Test reports are
  not subjects.
- **External digests (optional).** A job that pushes a file somewhere else,
  for example an image to a registry, declares its digest in the workflow
  output (`/job/workflow-output.json`):

  ```json
  {
    "provenance_subjects": [
      {"name": "registry.example/acme/app", "digest": {"sha256": "6b1f...e9"}}
    ]
  }
  ```

  A name has 1 to 512 characters. Only `sha256` digests (64 hex characters,
  an optional `sha256:` prefix) are accepted. A job has at most 200
  subjects. The coordinator drops an invalid entry and logs the reason.

A declared digest is a claim of the job: the coordinator did not see those
bytes. The statement binds the claim to the facts of the job that made it.
A consumer that trusts the job (by its ref class, CI origin, and profile)
can trust the claim.

## When the coordinator writes it

When a job completes with the status `completed` and has at least one
subject, the coordinator:

1. builds an in-toto Statement v1 with a SLSA Provenance v1 predicate,
2. signs it as a DSSE envelope with its provenance key,
3. stores the envelope as an artifact of the kind `provenance`
   (`provenance.intoto.dsse.json`).

A failed, cancelled, or timed-out job gets no provenance.

## Statement

| Field | Value |
| --- | --- |
| `_type` | `https://in-toto.io/Statement/v1` |
| `subject` | The artifacts and the declared subjects, with `digest.sha256` and the annotation `reactorcide.subject_source` (below) |
| `predicateType` | `https://slsa.dev/provenance/v1` |
| `buildDefinition.buildType` | `https://github.com/catalystcommunity/reactorcide/blob/main/docs/provenance.md#build-type-job-v1` |
| `buildDefinition.externalParameters` | `workflow` (`name`, `security_id`, `run_id`), `node`, `job`, `ref`, `submitted_ref` (API submits only), `event` (`type`, `id`, `pr_number`, `head_ref`, `base_ref`) |
| `buildDefinition.internalParameters` | `organization`, `project`, `project_id`, `repository`, `ci_origin`, `execution_profile`, `policy_revision`, `policy_rule`, `approval_id`, `worker_class`, `ref_classes`, `actor_principal_id`, `actor_kind`, `owner_group` |
| `buildDefinition.resolvedDependencies` | `source` (the source repository and commit, `digest.gitCommit`) and `trusted_ci` (the trusted CI repository and commit) |
| `runDetails.builder.id` | The builder ID (below) |
| `runDetails.metadata` | `invocationId` (the job ID), `startedOn`, `finishedOn` |

### Subject source

Each subject has the annotation `reactorcide.subject_source`:

| Value | Meaning |
| --- | --- |
| `coordinator_hashed` | An artifact that the worker uploaded. The coordinator computed the digest from the bytes that it stored. |
| `declared` | An entry of `provenance_subjects` in the workflow output. The job stated the digest. The coordinator did not see the bytes. |

A job can declare any digest. When an uploaded artifact and a declared
subject have the same name and digest, the statement keeps only the
`coordinator_hashed` subject.

### Build type `job` v1

The build type `job` v1 is one Reactorcide job. The job ran the command of
its trusted CI definition (`resolvedDependencies` `trusted_ci`) against the
source commit (`source`), in an isolated container, with the
`externalParameters` and `internalParameters` above. The facts have the same
meaning as the claims of [OIDC Tokens for Jobs](./oidc.md#claims), with the
same refs: a pull request job always has the ref `refs/pull/N/head`.

## Signing keys

The provenance keys use the same key management as the OIDC issuer: the
coordinator makes the keys, stores each private key encrypted under the
primary master key, and signs with ES256. The keys are in a separate table
(`provenance_signing_keys`). A provenance key never signs an OIDC token, and
an OIDC key never signs provenance.

- The first key is made at the first signature or key request.
- A key rotates after one year. A retired key stays in the JWKS for one year
  after the next key starts to sign, so old statements still verify.

| Variable (coordinator) | Default | Meaning |
| --- | --- | --- |
| `REACTORCIDE_PROVENANCE_BUILDER_ID` | `REACTORCIDE_OIDC_ISSUER_URL` | The builder ID in each statement. With neither value, provenance is off. |
| `REACTORCIDE_PROVENANCE_ENABLED` | `true` | `false` turns provenance off. |

The coordinator also needs a master key. Without a master key, provenance is
off.

### Public keys

`GET /api/v1/provenance/keys` is public. It returns the builder ID and the
public keys as a JWKS:

```json
{"builder_id": "https://ci.example.com/api/v1/oidc", "keys": [{"kty": "EC", "crv": "P-256", "kid": "...", "alg": "ES256", "use": "sig", "x": "...", "y": "..."}]}
```

## Get and verify

| Channel | Operation |
| --- | --- |
| UI | The job page "Artifacts" card shows whether the provenance signature verifies, and the statement. |
| REST | `GET /api/v1/jobs/JOB_ID/provenance` returns the DSSE envelope (`jobs:read`). |
| CSIL | `get-job-provenance` returns the envelope and the verified statement. |
| CLI | `reactorcide provenance get JOB_ID [-o FILE]`, `reactorcide provenance verify FILE\|--job JOB_ID` |

```bash
reactorcide provenance get "$JOB_ID" -o app.provenance.json
curl -s https://ci.example.com/api/v1/provenance/keys > provenance-keys.json

reactorcide provenance verify app.provenance.json --keys provenance-keys.json \
  --require-project "$PROJECT_ID" --require-workflow deploy \
  --require-ref-class production --require-ci-origin base \
  --require-builder https://ci.example.com/api/v1/oidc \
  --require-coordinator-hashed --subject sha256:6b1f...e9
```

`verify` checks the DSSE signature with the keys (`--keys FILE`, or the
public keys route of `--api-url`), then prints the facts. It exits with an
error when the signature does not verify or when a `--require-*` or
`--subject` condition is not met.

| Flag | Requirement |
| --- | --- |
| `--require-ref-class C` | The ref classes contain `C` (repeatable). |
| `--require-ci-origin O` | The CI origin is `O` (`base` or `head`). |
| `--require-builder ID` | The builder ID is `ID`. |
| `--require-repository R` | The repository is `R`, as the `verify` output shows it (for example `acme/app`). The compare ignores the case, a scheme, and `.git`. |
| `--require-project P` | The project ID is `P`. A project name is also accepted, but only with `--require-org`: a project name is unique only in one organization. |
| `--require-org O` | The organization name is `O`. |
| `--require-workflow S` | The workflow security ID is `S`. |
| `--subject sha256:HEX` | A subject has this digest. |
| `--require-coordinator-hashed` | The `--subject` digest is `coordinator_hashed`. With no `--subject`, every subject must be `coordinator_hashed`. |
| `--allow-any-repository` | Lets `--subject` pass with no `--require-repository` and no `--require-project`. |

`--subject` without `--require-repository` or `--require-project` fails.
Every project on the coordinator can get a signed statement for any digest
that it declares. Thus a digest and a valid signature alone do not tell
which project built the artifact. Give `--allow-any-repository` only when
every project on the coordinator is trusted to publish to the target.

Any DSSE verifier can check the envelope. The signature is ECDSA P-256 with
SHA-256 over the DSSE pre-authentication encoding
(`DSSEv1 <len(type)> <type> <len(payload)> <payload>`), in ASN.1 DER form.

## Admission at deploy time

A deploy-time admission policy can admit only artifacts that a trusted job
made. A policy engine in the deploy target gets the artifact digest (for
example the image digest) and the provenance envelope, then:

1. Verifies the DSSE signature with the Reactorcide provenance JWKS. Keep a
   copy of the JWKS in the policy configuration, or fetch it from
   `/api/v1/provenance/keys` over HTTPS.
2. Requires `runDetails.builder.id` to be the builder ID of your
   coordinator.
3. Requires a subject whose `digest.sha256` is the digest of the artifact.
   For a file artifact, also require the subject annotation
   `reactorcide.subject_source` to be `coordinator_hashed`. A pushed image
   is a `declared` subject: then the next step is the only protection, so
   do not skip it.
4. Requires `internalParameters.project_id` to be the ID of the project
   that may publish to this target (or `repository` to be its repository).
   Without this step, any project on the coordinator can attest your
   artifact digest.
5. Requires `predicate.buildDefinition.internalParameters.ref_classes` to
   contain `production` (or the class that your ref class rules give to
   protected refs, see [Ref Classes](./ref-classes.md)).
6. Requires `internalParameters.ci_origin` to be `base`, so the CI
   definition came from the trusted base and not from a pull request head.
7. Requires `externalParameters.workflow.security_id` to be your deploy
   workflow.
8. Optionally requires `execution_profile` (your trusted profile),
   and an `event.type` of `push` or `tag_created`.

Refuse a statement with `ref_classes` that contains `pr` or `fork`, or with
an `externalParameters.ref` that starts with `refs/pull/`. Do not trust
`event.type` `api` or `manual` for production unless only trusted principals
can submit.

A safe admission recipe with the CLI does steps 1 to 7:

```bash
reactorcide provenance verify app.provenance.json --keys provenance-keys.json \
  --require-builder https://ci.example.com/api/v1/oidc \
  --require-project "$PROJECT_ID" \
  --require-workflow deploy \
  --require-ref-class production \
  --require-ci-origin base \
  --require-coordinator-hashed \
  --subject "sha256:$ARTIFACT_DIGEST"
```

For a declared subject (an image digest from `provenance_subjects`),
remove `--require-coordinator-hashed` and keep `--require-project` and
`--require-workflow`. Run the command in a deploy job or in an admission
webhook.

## Database

Migration `000061_artifacts_tests_provenance.sql` adds
`provenance_signing_keys`. Production does not change until a builder ID and
a master key are available and a job publishes an artifact.
