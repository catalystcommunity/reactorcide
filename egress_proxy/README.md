# egress_proxy

The Reactorcide egress proxy. When egress control is on for a job, all
network traffic of the job goes through this proxy. The proxy is written
in Rust. It started as a copy of the Magi egress proxy. The design is in
`docs/egress-proxy.md`.

The directory has these parts:

| Path | Content |
| --- | --- |
| `src/` | The proxy (`reactorcide-egress`). |
| `wit/egress.wit` | The plugin interface (world `filter`). |
| `plugins/` | The test plugin (`test-actions`). Operators can add their own plugins. |
| `netd/` | The network helper of the job (`reactorcide-egress-net`): firewall redirect and fake DNS. |
| `tests/` | End-to-end tests. |

## What the proxy does

For each request and each response, in this order:

1. **Job.** The client must send Basic proxy credentials: the user is the
   job id, the password is the hex HMAC-SHA256 of
   `"reactorcide-egress-job:" + job id` with the job key. The job must be
   registered (see "Control API"). Else the proxy refuses the request.
2. **Destination.** The global policy, the job destinations, or a grant
   host must permit the host and port. Else the request gets 403.
3. **Protocol.** In a CONNECT tunnel, the first bytes give the protocol.
   TLS ends here with the proxy CA, with a certificate for each host. The
   host certificates have the authority key identifier of the CA (Python
   3.13 and later need it).
4. **Decode.** The proxy removes `gzip` and `deflate`. It asks the
   upstream for `identity`.
5. **Secret scan.** The proxy removes each operator secret and each grant
   value of the job from the URL, the headers, and the body, in both
   directions: raw, base64, base64url, hex, and percent encoded. A
   removed value becomes `[redacted:<id>]`.
6. **Decode depth.** A request with more nested encodings than
   `decode_depth` (default 2) stops.
7. **Plugins** (WASM components), in the order of their grants.
8. **Review.** A `review` action from a plugin stops the request at once
   (403, `x-reactorcide-egress: review`). The flow record keeps the
   reason.
9. **Grants.** For each grant of the job whose hosts permit the
   destination, the real value goes in place of the placeholder in the
   grant header only. With `format`, the full header value becomes the
   format with the value. A `Basic` credential is decoded, changed, and
   encoded again (git over HTTPS).
10. **Placeholders.** A placeholder (`reactorcide-secret-...`) that stays
    in the URL or a header stops the request. A placeholder in the body
    passes; it is not a secret.
11. **Send and record.** The record has no body and no secret value.

A step that fails stops the flow. The client gets 403 (or 502 for an
upstream failure), the header `x-reactorcide-egress` with the outcome, and
a text body with the reason.

## Control API

The worker calls the control API with `Authorization: Bearer <control
token>`. JSON bodies.

| Call | Result |
| --- | --- |
| `GET /healthz` | `ok` (no token) |
| `PUT /v1/jobs/{job_id}` | Registers the job policy: `{"job_id","grants"}`. |
| `DELETE /v1/jobs/{job_id}` | Removes the job: `{"job_id","flows","blocked","secrets_used"}`. |
| `GET /v1/flows?job=ID&limit=N` | The flow records of the job, newest first. |

The job policy:

```json
{
  "job_id": "01J...",
  "identity": {"org_id": "", "project_id": "", "workflow_id": "", "workflow_security_id": "",
               "node": "", "actor_principal_id": "", "actor_identity": "",
               "execution_profile": "", "ci_origin": "", "ref_classes": []},
  "destinations": [{"host": "coordinator.example", "ports": [8080]}],
  "grants": [{"id": "grant-1", "secret_ref": "path:key",
              "placeholder": "reactorcide-secret-<32 hex>", "value": "<real value>",
              "hosts": ["api.github.com"], "header": "authorization", "format": "Bearer {}"}],
  "expires_at_ms": 0
}
```

- `identity` accepts more keys, so a newer coordinator can send more facts.
- `header` defaults to `authorization`. `format` must contain `{}` one time.
- A grant host permits ports 80 and 443 for the job.
- A job is no longer registered after `expires_at_ms` (0: no expiry).

## Global policy

`REACTORCIDE_EGRESS_POLICY` is a JSON file with no secret values. It is
the same format as the Magi proxy: `destinations`, `passthrough` (layer 4
only), `secrets` (hosts of operator secrets), `decode_depth`,
`decode_exceptions`, `stream_hosts`, `event_stream_hosts`, `max_body`,
`ssh_hosts`, and `plugins`. In `destinations`, `environments` is a list
of job id patterns. A plugin grant lists `ask_ops`, the operations of the
optional ask service that the plugin can call.

## Configuration

| Flag | Variable | Default |
| --- | --- | --- |
| `--listen` | `REACTORCIDE_EGRESS_LISTEN` | `:8080` (control API) |
| `--proxy-listen` | `REACTORCIDE_EGRESS_PROXY_LISTEN` | `:3128` |
| `--control-token` | `REACTORCIDE_EGRESS_CONTROL_TOKEN_FILE` | required (at least 32 bytes) |
| `--policy` | `REACTORCIDE_EGRESS_POLICY` | none: an empty policy |
| `--ca-cert`, `--ca-key` | `REACTORCIDE_EGRESS_CA_CERT`, `REACTORCIDE_EGRESS_CA_KEY` | required |
| `--job-key` | `REACTORCIDE_EGRESS_JOB_KEY` | required (at least 32 bytes) |
| `--secrets-dir` | `REACTORCIDE_EGRESS_SECRETS_DIR` | none |
| `--upstream-ca` | `REACTORCIDE_EGRESS_UPSTREAM_CA` | none: the public web roots only |
| `--ask-url`, `--ask-token` | `REACTORCIDE_EGRESS_ASK_URL`, `REACTORCIDE_EGRESS_ASK_TOKEN_FILE` | none |
| `--ssh-host-key` | `REACTORCIDE_EGRESS_SSH_HOST_KEY` | none: SSH is not ended |
| `--allow-unregistered` | `REACTORCIDE_EGRESS_ALLOW_UNREGISTERED` | `false` |
| `--control-tls-name` | `REACTORCIDE_EGRESS_CONTROL_TLS_NAME` | none: the control API is plain HTTP. Set: TLS with a certificate for this name from the proxy CA (the worker uses an `https` control URL and trusts only the CA) |
| | `REACTORCIDE_EGRESS_DNS` | the first nameserver in `/etc/resolv.conf` |

Commands that make the files (mode 600; an existing file is not
replaced):

```sh
reactorcide-egress ca --out DIR [--name NAME]
reactorcide-egress key --out FILE              # the job key
reactorcide-egress control-token --out FILE
reactorcide-egress ssh-key --out FILE          # prints the public key line
reactorcide-egress credential --job-key FILE --job JOB --out FILE
```

## Tests

```sh
cargo test
```

The tests build the test plugin (they need the `wasm32-wasip2` target)
and use `curl` as the client.

## Known limits

- HTTP/1.1 only. HTTP/2, WebSocket, and other upgrades stop.
- Flow records are in memory. A restart loses them.
- A placeholder in an encoding other than Basic credentials is not
  replaced.
- Non-HTTP protocols and clients that pin certificates need a
  `passthrough` destination (layer 4, not inspected).
- A streamed response needs `event_stream_hosts` or `stream_hosts`.
