# Inbound Federation

Inbound federation lets a workload of another system act as a service
principal with no static Reactorcide token. The workload has a token from its
own OIDC issuer, for example GitHub Actions, a Kubernetes service account, or
a cloud workload identity. It exchanges that token for a short-lived
Reactorcide token.

An org admin, or the admin token, sets up federation for the organization
(owner decision D24).

## How it works

1. An org admin registers a trusted issuer for the organization.
2. An org admin adds a claim mapping. The mapping names a service principal
   of the organization and the claim conditions that a token must match.
3. The workload sends its external token to
   `POST /api/v1/federation/token`.
4. The coordinator verifies the token and finds the one mapping that matches.
5. The coordinator issues a short-lived token for the service principal.
6. The workload uses that token as a normal bearer token.

```text
workload --(external JWT)--> POST /api/v1/federation/token
         <--(Reactorcide token, 15 min max by default)--
workload --(Bearer token)--> /api/v1/jobs, /api/v1/approvals, ...
```

## Verification

The coordinator refuses the token when one of these checks fails:

| Check | Rule |
| --- | --- |
| Issuer | The `iss` claim must be the exact URL of an active issuer of the organization. |
| Signature | A key of the issuer JWKS must verify the signature. go-jose does the cryptography. |
| Algorithm | The algorithm must be in the issuer list. Only `RS256`, `ES256`, and `EdDSA` are possible. `none` and HMAC (`HS*`) are never accepted. |
| Audience | The `aud` claim must contain one of the issuer audiences. |
| Expiry | The token must have `exp`. `exp` must be in the future (with the clock skew). |
| Lifetime | `exp` must be at most 24 hours from now. A longer token is a static credential. |
| Not before | `nbf`, when present, must be in the past (with the clock skew). |
| Issued at | The token must have `iat`. `iat` must not be in the future (with the clock skew). |
| Subject | The token must have `sub`. |
| Mapping | Exactly one active mapping of the issuer must match. No match or two matches is a refusal. |
| Principal | The principal must be an active service principal of the organization. |

### JWKS fetch and cache

- The coordinator reads `jwks_uri` from
  `ISSUER/.well-known/openid-configuration` when the issuer has no JWKS URL.
  The discovery document must name the same issuer.
- The coordinator keeps a JWKS for 10 minutes.
- A token with an unknown key ID makes one new fetch. A new fetch occurs at
  most one time in 30 seconds for each issuer. A random key ID thus cannot
  make a fetch for each request.
- If a fetch fails, the coordinator uses the last JWKS that it has. A key that
  is not in that JWKS is refused.
- The issuer URL and the JWKS URL must use HTTPS. For an isolated
  development network only, start the coordinator with
  `reactorcide serve --allow-insecure-transport`. No environment variable can
  turn on this exception.

## Claim mappings

A mapping has one or more conditions. Every condition must match.

| Operator | CLI form | Match |
| --- | --- | --- |
| `equals` | `CLAIM=VALUE` | The claim is exactly `VALUE`. |
| `glob` | `CLAIM~PATTERN` | `*` matches any run of characters, also `/` and `:`. `?` matches one character. No other character is special. |

Rules:

- A mapping must have a condition on `sub`. A `sub` condition that is only
  `*` (or `?`) is refused.
- A `sub` glob must fix the owner of the subject. The text before the first
  `*` or `?` must end with `/` or `:` and have at least two parts that are
  not empty, for example `repo:acme/` or `project_path:group/app:`. The
  coordinator refuses `repo:*` (every repository of every owner),
  `repo:acme*` (also matches `repo:acme-evil/app`), and `*:ref:...`. If the
  subject format of an issuer cannot fix the owner this way, add an `equals`
  condition on another claim that names the owner, for example
  `repository_owner_id=12345`. A `glob` on another claim does not count.
- For an issuer whose first two subject parts do not name an owner (for
  example `system:serviceaccount:NAMESPACE:NAME`), write the namespace too,
  or add an `equals` condition.
- A condition matches only a string, a number, or a boolean claim. A list or
  an object claim never matches.
- A mapping must give at least one capability. A mapping never gives every
  capability.
- The caller can only give capabilities that the caller has. The caller also
  needs `tokens:manage`, because a mapping issues tokens.
- The token lifetime is 60 to 3600 seconds. The default is 900 seconds. The
  issued token never outlives the external token.

Write narrow conditions. Include the repository and the ref in the subject
condition. For GitHub Actions, for example:

```text
sub~repo:acme/app:ref:refs/heads/main
sub~repo:acme/app:environment:production
```

Do not map `repo:acme/app:*` to a principal with write capabilities: a pull
request workflow of the same repository then matches too.

## Authority of the issued token

The issued token is an `api_tokens` row of the type `service_token`, bound to
the service principal (`intersect_roles` is true). At each use, its
capabilities are intersected with the roles and groups of the principal, the
same as a token from `reactorcide principals tokens create`:

- With no organization role, the token can do nothing.
- A `member` role keeps only the read capabilities.
- An `admin` role keeps the mapping capabilities.
- A disabled principal makes the token invalid at once.

A request can ask for fewer capabilities than the mapping gives
(`"capabilities": [...]`). It cannot ask for more.

Expired exchange tokens are removed from the database one hour after they
expire, at the next exchange.

## Identity of the subject

The exchange records the identity `federated:ISSUER|SUBJECT` on the principal
(`verified_by` = `federation`). The identity gives the subject
`identity:federated:ISSUER|SUBJECT` to the principal. Only federation makes a
`federated` identity: `reactorcide principals link` refuses this type.

When a mapping changes to another service principal of the same
organization, the next exchange moves the identity. The coordinator does not
move an identity that belongs to a principal of another organization. The
issued token is bound to the mapped principal in all cases.

## Decision: an exchange, not a bearer JWT

The coordinator accepts the external JWT only at the exchange route. It does
not accept an external JWT as a bearer token on other routes. Reasons:

- Every API route keeps one credential type and one check path.
- The JWKS fetch and mapping evaluation occur one time for each exchange, not
  one time for each API call.
- The issued token can be revoked, and it records `last_used_at`.

## Audit

Each exchange writes an audit record:

| Action | Details |
| --- | --- |
| `federation.exchange` | issuer, subject, `jti`, mapping, token ID, capabilities, expiry. The actor is the service principal. |
| `federation.exchange.denied` | The reason, the issuer, and the subject when known. |

The token itself is never in an audit record or a log. The administration
routes write `federation.issuer.create|update|delete` and
`federation.mapping.create|update|delete`.

## Set up federation

```bash
# 1. A service principal with a role.
reactorcide principals create --org acme deployer
reactorcide principals grant-role --org acme --role admin deployer

# 2. The trusted issuer.
reactorcide federation issuers create --org acme gha \
  --issuer-url https://token.actions.githubusercontent.com \
  --audience reactorcide --algorithm RS256

# 3. The mapping.
reactorcide federation mappings create --org acme --issuer gha main-deploy \
  --principal deployer \
  --condition 'sub~repo:acme/app:ref:refs/heads/main' \
  --condition 'repository=acme/app' \
  --capability jobs:submit --capability jobs:read --ttl 600
```

Other commands:

| Command | Action |
| --- | --- |
| `federation issuers list --org ORG` | List issuers |
| `federation issuers show --org ORG NAME` | Show an issuer |
| `federation issuers update --org ORG NAME [flags]` | Change an issuer. Omitted flags keep their value. `--status disabled` stops all exchanges. |
| `federation issuers delete --org ORG NAME` | Remove an issuer and its mappings |
| `federation mappings list --org ORG --issuer ISSUER` | List mappings |
| `federation mappings show\|update\|delete --org ORG --issuer ISSUER NAME` | Show, change, or remove a mapping. `--condition` on update replaces all conditions. |
| `federation exchange --org ORG [--token-file FILE] [--principal P] [--capability C]` | Exchange a token. It reads the token from stdin by default and prints only the issued token. |

The UI has the same functions on the **Principals** page, in the
**Federated issuers** card. It uses the CSIL operations
`list-federation-issuers`, `save-federation-issuer`,
`delete-federation-issuer`, `save-federation-mapping`, and
`delete-federation-mapping`.

## Exchange a token

Request:

```http
POST /api/v1/federation/token
Content-Type: application/json

{"organization": "acme", "token": "EXTERNAL_JWT",
 "principal": "deployer", "capabilities": ["jobs:submit"]}
```

`principal` and `capabilities` are optional. `principal` selects the mappings
of one principal, when the mappings of two principals can match.

Response:

```json
{"access_token": "rcf_...", "token_type": "Bearer", "expires_in": 600,
 "expires_at": "2026-11-16T12:10:00Z", "token_id": "...",
 "principal_id": "...", "principal": "deployer", "capabilities": ["jobs:submit"]}
```

A refusal is `401` with a reason. An unknown organization gets the same
answer as an unknown issuer.

In GitHub Actions:

```bash
ID_TOKEN="$(curl -sS -H "Authorization: bearer $ACTIONS_ID_TOKEN_REQUEST_TOKEN" \
  "$ACTIONS_ID_TOKEN_REQUEST_URL&audience=reactorcide" | jq -r .value)"
REACTORCIDE_API_TOKEN="$(printf '%s' "$ID_TOKEN" | \
  reactorcide federation exchange --api-url https://ci.example.com --org acme)"
export REACTORCIDE_API_TOKEN
```

## REST routes

All administration routes are under
`/api/v1/organizations/ORG/federation/issuers`. They need
`organizations:manage` for the organization (an org admin). Mapping changes
also need `tokens:manage`.

| Method and path | Action |
| --- | --- |
| `GET /` | List issuers |
| `POST /` | Create: `{"name", "issuer_url", "jwks_url", "audiences", "algorithms", "clock_skew_seconds", "status"}` |
| `GET`, `PUT`, `DELETE /{issuer}` | Show, change, or remove an issuer |
| `GET`, `POST /{issuer}/mappings` | List or create: `{"name", "principal", "conditions", "capabilities", "token_ttl_seconds", "status"}` |
| `GET`, `PUT`, `DELETE /{issuer}/mappings/{mapping}` | Show, change, or remove a mapping |

`POST /api/v1/federation/token` needs no Reactorcide credential.

## Database

Migration `000050_federation_and_oidc.sql` adds `federation_issuers`,
`federation_mappings`, and `federation_issued_tokens`. An organization with
no issuer accepts no federated token, so existing organizations do not
change.

## Source address behind a proxy

The source limit of the exchange route uses one bucket for each source
address. `REACTORCIDE_TRUSTED_PROXY_CIDRS` is a comma-separated list of the
CIDRs (or single addresses) of the proxies and ingresses in front of the
coordinator. For example: `10.0.0.0/8,fd00::/8`.

- Empty (the default): the source address is the TCP peer. The coordinator
  does not read `X-Forwarded-For`, because a caller can set it.
- Set: when the TCP peer is in the list, the source address is the
  right-most `X-Forwarded-For` address that is not in the list. When the TCP
  peer is not in the list, the coordinator does not read the header.
- An entry in `X-Forwarded-For` that is not an address stops the walk. Then
  the source address is the last trusted hop.
- If the list is not valid, the coordinator logs an error and uses the TCP
  peer.

Add only the addresses of your own proxies. If you add a network that
callers can use, a caller can select its bucket with a false header. Make
sure that the ingress adds the client address to `X-Forwarded-For` (most
ingress controllers do this by default).

## Known limits

- The exchange route has two rate limits
  ([Rate Limits](./events-and-schedules.md#rate-limits)): one for each source
  address, before any other work, and one for each issuer of an
  organization, after the external token is verified. Only a verified token
  counts toward the issuer limit, so a token with a false `iss` claim cannot
  use the limit of an issuer. Over a limit, the route returns HTTP 429 with
  `Retry-After`. The source address is the TCP peer by default. Behind an
  ingress or a proxy, all callers then have the address of the proxy. To
  limit each caller, set `REACTORCIDE_TRUSTED_PROXY_CIDRS` (see
  [Source address behind a proxy](#source-address-behind-a-proxy)).
- The coordinator does not keep a list of used `jti` values. A workload can
  exchange the same external token more than one time while it is valid. Each
  exchange is audited.
