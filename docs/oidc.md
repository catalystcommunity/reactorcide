# OIDC Tokens for Jobs

Reactorcide can be a generic OIDC issuer for its jobs (owner decision D14). A
running job asks the coordinator for a token for one audience. The
coordinator signs a JWT with claims that describe the job. A consumer, for
example a cloud provider, a secret store, or a remote cache, verifies the
token with the public JWKS of the issuer. The job then needs no static
secret for that consumer.

The job cannot choose its claims. It only names the audience. The
coordinator reads every claim from its own records.

## Turn on the issuer

The issuer is off until you set the issuer URL. With no URL, the coordinator
serves no discovery document, no JWKS, and no job tokens.

| Variable | Default | Meaning |
| --- | --- | --- |
| `REACTORCIDE_OIDC_ISSUER_URL` | empty (off) | The `iss` value, for example `https://ci.example.com/api/v1/oidc`. It must use HTTPS. |
| `REACTORCIDE_OIDC_SIGNING_ALGORITHM` | `ES256` | The algorithm of new keys: `ES256` or `RS256`. |
| `REACTORCIDE_OIDC_KEY_ROTATION_DAYS` | `90` | Start a rotation when the newest key is older than this. `0` turns off automatic rotation. |

The coordinator needs a master key (`REACTORCIDE_MASTER_KEYS`, or the keys in
the database). Without a master key, the issuer stays off.

The coordinator serves these public routes under the path of the issuer URL:

| Route | Content |
| --- | --- |
| `ISSUER_PATH/.well-known/openid-configuration` | The discovery document. `Cache-Control: max-age=3600`. |
| `ISSUER_PATH/.well-known/jwks.json` | The public keys. `Cache-Control: max-age=300`. |

For `https://ci.example.com/api/v1/oidc`, the routes are
`/api/v1/oidc/.well-known/openid-configuration` and
`/api/v1/oidc/.well-known/jwks.json`. Route that path of the public host to
the coordinator. The webapp does not serve these routes, and it holds no
coordinator credential.

For an isolated development network only, `reactorcide serve
--allow-insecure-transport` accepts an `http` issuer URL.

### Settings for an organization or a project

The issuer is on for every organization and project when the URL is set. An
org admin can turn it off, and can add claims:

```bash
reactorcide oidc settings show --org acme
reactorcide oidc settings set --org acme --issuer deny
reactorcide oidc settings set --org acme --project app --issuer allow
reactorcide oidc settings set --org acme --claims-file claims.json
```

| Setting | Values | Meaning |
| --- | --- | --- |
| `issuer` (organization) | `allow` (default), `deny` | Jobs of the organization can or cannot get tokens. |
| `issuer` (project) | `inherit` (default), `allow`, `deny` | The project value replaces the organization value, unless it is `inherit`. |
| `audience_claims` | `{"AUDIENCE": {"CLAIM": "VALUE"}, "*": {...}}` | Operator-defined claims. `*` applies to every audience. Project claims replace organization claims with the same name. |

An extra claim name has lowercase letters, digits, and underscores. A
standard claim name (below) is refused, so an extra claim cannot replace
`sub`, `ref`, or another standard claim.

REST: `GET` and `PUT /api/v1/organizations/ORG/oidc-settings` and
`/api/v1/organizations/ORG/oidc-settings/projects/PROJECT`. `GET` needs
`organizations:read`. `PUT` needs `organizations:manage`.

## Get a token in a job

A job that a coordinator runs has a job token (`REACTORCIDE_API_TOKEN`) that
is bound to the job. Use the runnerlib helper:

```bash
# Print the token to stdout, for a pipe.
runnerlib oidc-token --audience sts.amazonaws.com > /tmp/web-identity-token

# Or write it to a file with mode 0600.
runnerlib oidc-token --audience sts.amazonaws.com --output /tmp/web-identity-token
export AWS_WEB_IDENTITY_TOKEN_FILE=/tmp/web-identity-token
```

| Option | Meaning |
| --- | --- |
| `--audience`, `-a` | The `aud` value. Required. |
| `--ttl` | The lifetime in seconds, 60 to 3600. The default is 600. |
| `--output`, `-o` | Write the token to this file with mode 0600, not to stdout. |
| `--allow-insecure-transport` | Permit an `http` coordinator URL (isolated development network only). A worker in its insecure development mode (`--allow-insecure-transport`) sets `REACTORCIDE_ALLOW_INSECURE_TRANSPORT=true` in the job environment, and the helper reads it as the default of this option. A job script then needs no change in development. |

The helper never writes the token to stderr or to a log. When the runnerlib
masker runs, the helper registers the token, so a token that a job prints by
accident is masked in the job log.

Python:

```python
from src.oidc import request_token

token = request_token("sts.amazonaws.com", ttl_seconds=300)
```

REST: `POST /api/v1/oidc/token` with the job token as the bearer token and
`{"audience": "...", "ttl_seconds": 600}`. Only a job token can call it. The
job must be `scheduling` or `running`.

## Lifetime

- The default lifetime is 10 minutes. The longest is 1 hour.
- A token never outlives the job: `exp` is at most the start of the job plus
  its timeout. A job that is past its timeout gets no token.
- `iat` and `nbf` are the time of issue.

## Claims

| Claim | Value |
| --- | --- |
| `iss` | The issuer URL, without a trailing slash. |
| `sub` | `org:ORG:project:PROJECT:workflow:SECURITY_ID:node:NODE:ref:REF` (see below). |
| `aud` | The requested audience (one string). |
| `exp`, `iat`, `nbf` | Times in seconds. |
| `jti` | A random token ID. The audit record has the same ID. |
| `org`, `org_id` | The organization name and UUID. |
| `project`, `project_id` | The project name and UUID. Empty for a job with no project. |
| `repository` | The repository, for example `acme/app`. |
| `workflow_security_id` | The stable security ID of the workflow. |
| `workflow_name`, `workflow_id` | The workflow name and the workflow run UUID. |
| `node` | The workflow node name. |
| `job_id` | The job UUID. |
| `ref` | The ref of the event. See "Refs" below. |
| `head_ref`, `base_ref` | For a pull request: the head branch name and the base branch name. Empty otherwise. |
| `submitted_ref` | The ref that a submitter stated, not verified and not normalized. Only on a manual eval submit or an API submit. Its `ref` is then empty. Never trust it. |
| `sha` | The commit SHA that the job tests. |
| `event_type` | `push`, `tag_created`, `pull_request_opened`, `pull_request_updated`, `pull_request_merged`, `pull_request_closed`, `schedule`, `manual` (a manual event: an API eval submit or a manual run), or `api` (a job that no CI event started). |
| `event_id` | The CI event UUID. |
| `pr_number` | The pull request number. Only on a pull request job. |
| `ref_classes` | The ref classes of the job, sorted (see [Ref Classes](./ref-classes.md)). |
| `actor_principal_id`, `actor_kind` | The principal that started the run, and its kind (`human`, `service`, `system`). |
| `ci_origin` | `base` or `head`: where the CI definition of the job came from. |
| `execution_profile` | The execution profile of the job, for example `standard` or `pr-untrusted`. |
| `policy_revision` | The CI policy revision that admitted the job. Empty without a stored policy. |
| `owner_group` | The owner group of the workflow, from the trusted owner snapshot of the run (a group name, or `vcs_team:ORG/SLUG`). Empty when the workflow has no owner or the owner is unknown. A pull request cannot change it. See [Owners and Contact](./workflow-design.md#owners-and-contact). |
| extra claims | The operator-defined claims for the audience. |

### Subject format

```text
org:ORG:project:PROJECT:workflow:SECURITY_ID:node:NODE:ref:REF
```

- The parts are always in this order. An unknown part is empty, for example
  `workflow::node:` for a job that is not in a workflow.
- A `:` in a value becomes `%3A`, and `%` becomes `%25`. Thus a node name
  cannot make a false `ref:` part.

Example:

```text
org:acme:project:app:workflow:deploy:node:release:ref:refs/heads/main
```

### Refs

| Event | `ref` |
| --- | --- |
| Push | The pushed ref, for example `refs/heads/main` or `refs/tags/v1.2.0`. |
| Pull request (any action) | `refs/pull/NUMBER/head`. The head branch name goes only to `head_ref`. |
| Schedule | `refs/heads/BRANCH`. |
| Manual run (`POST /api/v1/projects/{id}/runs`) | The ref that the coordinator resolved from the VCS, as for a push or a pull request. `event_type` is `manual`. |
| Manual eval submit (`REACTORCIDE_JOB_KIND=eval`) | Empty. The stated branch goes only to `submitted_ref`. `event_type` is `manual`. |
| API submit with no event | Empty. The source ref goes only to `submitted_ref`, not normalized. `event_type` is `api`. |

A fork can name its branch `main`. Its pull request job still has
`ref` = `refs/pull/N/head`, never `refs/heads/main`.

A submitter states the branch and the event type of a manual eval submit
(`REACTORCIDE_BRANCH`, `REACTORCIDE_EVENT_TYPE`). The coordinator never puts
them in `ref`, `sub`, or `event_type`. Thus a manual submit that states
`main` and `push` gets an empty `ref`, `sub` = `...:ref:`, and
`event_type` = `manual`. It cannot match a consumer rule for
`refs/heads/main`.

A manual run that a principal starts on a ref has the real ref of that ref,
because the coordinator resolves the commit from the VCS and uses the CI
from the default branch. Its subject is the same as the subject of a push
to that ref. Any principal with the run permission can start one. If a
consumer must refuse manual runs, require `event_type` = `push` (or
`tag_created`), or require that `ref_classes` does not contain `manual`.

## Safe trust conditions for consumers

A token proves which job asked for it. It does not prove that the job ran
trusted code. A pull request from a fork runs code that anybody can write.
Configure each consumer to accept only the jobs that must have its access.

Do these things:

1. Always check `iss` and `aud`. Use an audience that is unique to the
   consumer.
2. Match `sub` exactly, or with a pattern that fixes the organization, the
   project, the workflow, and the ref:
   `org:acme:project:app:workflow:deploy:node:*:ref:refs/heads/main`.
3. For production access, also require:
   - `event_type` = `push` (or `tag_created` for a release). This refuses
     `manual` and `api` jobs,
   - `ci_origin` = `base`,
   - `execution_profile` = your trusted profile,
   - a ref class that only protected refs get, for example
     `ref_classes` contains `production`.
4. Never trust a token with `pr_number`, with `ref` `refs/pull/...`, or with
   `ref_classes` that contains `fork` or `pr`, for write access.
5. Never trust `head_ref` or `base_ref` alone. A fork chooses its branch
   names.
6. Do not trust `event_type` = `api` or `manual` for production. Any
   principal with `jobs:submit` can submit a job and state any ref. Never
   trust `submitted_ref`.
7. Use a short session at the consumer. The token lives at most 1 hour.

Do not do these things:

- Do not match `sub` with `org:acme:*` or a pattern that has `*` in the `ref`
  part.
- Do not trust `repository` alone. Every pull request job of the repository
  has the same `repository`.
- Do not trust extra claims for security decisions unless only you can
  change the settings that make them.

Example: AWS IAM trust policy conditions.

```json
"Condition": {
  "StringEquals": {
    "ci.example.com/api/v1/oidc:aud": "sts.amazonaws.com",
    "ci.example.com/api/v1/oidc:sub": "org:acme:project:app:workflow:deploy:node:release:ref:refs/heads/main"
  }
}
```

AWS can match only some claims. If a consumer can match only `sub` and `aud`,
put the production workflow in its own workflow security ID and match the
full subject. A fork pull request job never has a `refs/heads/...` ref. An
API submit and a manual eval submit have an empty ref part, so they never
match. A manual run of the branch has the same subject as a push; if the
consumer must refuse it, give the run permission of the project only to
principals that may deploy.

## Signing keys and rotation

- The coordinator makes its signing keys. It stores each private key as a
  PKCS#8 key, encrypted under the primary master key, in
  `oidc_signing_keys`. A private key never leaves the coordinator.
- The first key is made at the first JWKS or token request. It signs at once.
- A rotation adds a new key. The JWKS publishes it at once, but the issuer
  signs with it only after one hour (the publish lead). A consumer that keeps
  the JWKS for up to an hour thus has the key before it sees a token signed
  with it.
- The keys before the new key stay in the JWKS for 24 hours after the new key
  starts to sign. This overlap is longer than the longest token lifetime.
- The coordinator starts a rotation automatically when the newest key is
  older than `REACTORCIDE_OIDC_KEY_ROTATION_DAYS`.
- Replicas share the keys through the database. A database lock stops two
  replicas from making two keys at once.

Commands (global authority with `secrets:manage`, for example the admin
token). A key list (`GET`) also accepts a global admin with
`organizations:read` or `organizations:manage`:

| Command | Action |
| --- | --- |
| `reactorcide oidc keys list` | List the keys with their state: `signing`, `pending`, `published`, `retired` |
| `reactorcide oidc keys rotate` | Add a key that signs after the publish lead |
| `reactorcide oidc keys rotate --immediate` | Add a key that signs at once (for a compromised key) |
| `reactorcide oidc keys retire KID` | Remove a key from the JWKS at once |

For a compromised key: run `rotate --immediate`, then `retire KID`. Tokens
that the old key signed then fail. A consumer that keeps an old JWKS refuses
new tokens until it gets the JWKS again.

REST: `GET /api/v1/oidc/keys`, `POST /api/v1/oidc/keys/rotate`
(`{"immediate": true}`), and `POST /api/v1/oidc/keys/KID/retire`.

UI: the "OIDC signing keys" card on the Admin page (global admin) lists,
rotates and retires keys (CSIL `list-oidc-keys`, `rotate-oidc-key`,
`retire-oidc-key`). The "OIDC tokens" card on the organization CI trust page
and on the project Execution page shows and changes the issuer setting and
the extra claims of each audience (CSIL `get-oidc-settings`,
`put-oidc-settings`; an org admin changes them).

A rotation of the master key does not encrypt the signing keys again. Keep
the old master key until a new signing key replaces each key that it
encrypts.

## Audit

Each token writes an `oidc.token` audit record with the job, the audience,
the subject, the `jti`, the key ID, the expiry, `ci_origin`, the profile, and
the ref classes. The token itself is never in an audit record or a log. Key
changes write `oidc.key.rotate` and `oidc.key.retire`. Setting changes write
`oidc.settings.update`.

## Database

Migration `000050_federation_and_oidc.sql` adds `oidc_signing_keys` and
`oidc_settings`. With no setting row, the default applies. Production does
not change until an operator sets `REACTORCIDE_OIDC_ISSUER_URL`.
