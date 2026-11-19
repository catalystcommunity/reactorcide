# Egress Proxy and Proxy-Delivered Secrets

Status: built (2026-11-16, WP18). The feature is off by default. Production
does not change until an operator deploys the proxy and turns on egress
control or a proxy secret grant.

This document is the design. The proxy program is `egress_proxy/` (Rust).
The job network helper is `egress_proxy/netd/` (Go). The code is a copy of
the Magi egress proxy (`primitives/egress`) and of the Magi `envd` network
parts (`podnet`, `netns`, the transparent route, and the fake DNS). See
`egress_proxy/README.md` for the flags and the control API.

## 1. Purpose

A job that has a secret value can use it or disclose it. Log masking does
not stop this. The egress proxy gives two controls:

1. **Proxy-delivered secrets.** The job environment has a placeholder, not
   the secret. The proxy puts the real value in one request header, and only
   for the hosts that the secret grant names. The real value never goes into
   the job container.
2. **Egress control.** All network traffic of the job goes through the
   proxy. The proxy permits only the destinations in its policy. It scans
   each request and each response for secret values in raw and encoded
   forms, and removes them.

## 2. Parts

| Part | Where | What it does |
| --- | --- | --- |
| Proxy | `egress_proxy/src` | HTTP proxy on port 3128. It ends TLS with its own CA (a certificate for each host, with the Authority Key Identifier). It checks the destination, scans for secrets, runs WASM filter plugins, replaces placeholders, and records each flow. |
| Control API | `egress_proxy/src/server.rs` | Port 8080. Workers register the policy of each job (`PUT /v1/jobs/{job_id}`) and remove it (`DELETE`). Bearer control token. |
| Network helper | `egress_proxy/netd` | `reactorcide-egress-net`. `podnet` installs the nftables redirect and the guard. `serve` holds the local proxy port, the transparent route, and the fake DNS. |
| Coordinator | `internal/workerapi/egress.go` | Decides egress control for a lease, issues placeholders, and puts the proxy grants in `Lease.egress`. |
| Worker | `internal/coordinatorworker/egress.go` | Registers the lease with the proxy before the job starts. Removes it after the job ends. |
| Runners | `internal/worker/*_egress.go` | Add the network plumbing for Kubernetes, Docker, containerd, and VM jobs. |

One image (`reactorcide/egress`) holds the proxy and the network helper.

## 3. Control plane

```text
coordinator (RequestJob)                       worker                  egress proxy
------------------------                       ------                  ------------
resolve ${secret:path:key}
  grant delivery "env"   -> value in env
  grant delivery "proxy" -> placeholder in env,
                            real value in Lease.egress.grants
job created: egress control on (org, project,
  or profile)?           -> job requires egress_proxy=true (queue routing)
egress control on? (org, project, profile,
  or a proxy grant)      -> Lease.egress
worker has egress_proxy=true? else fail job
                         ---- Lease (CSIL-RPC, authenticated) ---->
                                                register job  ---- PUT /v1/jobs/{id} -->
                                                start job with
                                                  HTTPS_PROXY=job:credential
                                                job ends, remove job -- DELETE ------->
```

Decisions:

- **The coordinator computes the grants when it makes the lease.** It
  resolves the secret, checks the grant, and makes a new random placeholder
  (`reactorcide-secret-` and 32 hex digits) for each grant and secret
  reference. The real value goes only in `Lease.egress.grants`. The lease
  goes to the worker on the authenticated worker channel, as job secrets
  already do.
- **The worker gives the policy to the proxy.** The worker calls the
  control API with the control token. The token is a file that only the
  worker and the proxy have. The registration has the secret values, so
  protect the control port: with `--control-tls-name` the proxy serves the
  control API over TLS with a certificate from its CA, and a worker with an
  `https` control URL trusts only that CA. The Helm chart turns on TLS
  (`egressProxy.controlTLS.enabled`) and the NetworkPolicy (the control
  port only from worker pods) by default. Without TLS, the proxy and the
  worker log a warning. In Docker and containerd, a job cannot reach the
  control port: the guard permits only the proxy port.
- **Per-job credential.** The job authenticates to the proxy with Basic
  proxy authentication: user = job ID, password = hex HMAC-SHA256 of
  `reactorcide-egress-job:` + job ID with the job key. The proxy refuses a
  valid credential of a job that is not registered. Thus the credential
  stops when the worker removes the job, or when the registration expires
  (job timeout and one hour, or one day without a timeout).
- **Policy facts.** The registration has the job identity: organization,
  project, workflow, workflow security ID, node, actor principal and
  identity, execution profile, CI origin, and the ref classes of the job
  (`ref_classes`, see [Ref classes](./ref-classes.md); empty when the job
  has none). The flow records show the identity.
- **Old workers.** An older worker ignores `Lease.egress`. The coordinator
  therefore gives a job with egress control only to a worker with the
  characteristic `egress_proxy=true`. A worker adds it when it has egress
  settings. Another worker that claims the job fails it with the reason
  "egress policy denied".
- **Automatic routing.** When the coordinator creates a job (a webhook, a
  trigger, a schedule, a REST submit, or a retry) and egress control is on
  for the organization, the project, or the execution profile of the job, it
  adds the characteristic `egress_proxy=true` to the job. The queue of the
  job then requires it, so only a worker with an egress proxy takes the job.
  The job waits until such a worker is available; it does not fail on
  another worker. If you turn egress control on, start egress workers before
  new jobs arrive, or the jobs wait.
- A job that has egress control only because of a proxy-delivered secret
  grant does not get the characteristic automatically. Give such a job a
  queue that requires `egress_proxy=true` (for example
  `characteristics: {egress_proxy: true}` in the job), or turn egress control
  on for its project or profile.

## 4. Secret delivery mode

A secret grant has these fields (migration `000063`):

| Field | Default | Meaning |
| --- | --- | --- |
| `delivery` | `env` | `env`: the value goes into the job environment (the behavior before this feature). `proxy`: the job gets a placeholder. |
| `proxy_hosts` | empty | Host patterns: `api.github.com` (that name) or `*.example.com` (each name below it). Necessary for `proxy`. Not permitted: `*` alone, `localhost` (and names below it), and a public suffix or a wildcard of one (`co.uk`, `*.co.uk`, `*.github.io`, `*.herokuapp.com`, `*.githubusercontent.com`), because each name below a public suffix can have a different owner. Name each host instead (`raw.githubusercontent.com`). The check uses the Public Suffix List, private section included. |
| `proxy_header` | `authorization` | The request header where the proxy puts the value. |
| `proxy_header_format` | empty | The full header value, with `{}` for the value, for example `Bearer {}`. Empty: the proxy replaces only the placeholder in the header value. |

Rules:

- When two grants match one secret path and one has `delivery: proxy`, the
  proxy grant wins. An administrator who binds a secret to hosts does not
  want the value in a job environment.
- A job-scoped secret (`jobs/{id}`) has no grant and is always `env`.
- A placeholder in an environment value with other text works:
  `AUTH="Bearer ${secret:org/gh:token}"` becomes `Bearer reactorcide-secret-…`.
- The worker masks the real value, the placeholder-bearing values, and the
  job credential in the logs. The coordinator masking cache holds the real
  values.
- A job with a proxy grant always runs with egress control. A placeholder
  works only through the proxy.
- A proxy grant value must have at least 8 bytes (the shortest value that
  the proxy scanner finds). The coordinator refuses the claim of a job with
  a shorter value, and the proxy refuses the registration.

CLI:

```bash
reactorcide secret-grants set github-api \
  --project github.com/example/repo \
  --secret-path org/github --secret-match prefix \
  --delivery proxy --proxy-host api.github.com \
  --proxy-header-format 'Bearer {}'
```

REST: the same fields on `POST`/`PATCH /api/v1/secret-grants` (`delivery`,
`proxy_hosts`, `proxy_header`, `proxy_header_format`).

## 5. Proxy order of work

For each request:

1. **Credential.** The job credential must be valid, and the job must be
   registered.
2. **Destination.** The host and port must be in the global policy
   destinations, in the job destinations, or a grant host (ports 80 and 443).
   The coordinator adds its job API (the coordinator setting
   `REACTORCIDE_JOB_API_URL`, which is `REACTORCIDE_COORDINATOR_URL` in
   the job) to the job destinations, so that a job can create triggers
   and get OIDC tokens. The value never comes from the job environment.
   Else the request gets 403.
3. **Protocol.** TLS ends at the proxy. HTTP is read. SSH ends at the proxy
   for `ssh_hosts`. Other protocols stop, unless the destination is a
   `passthrough` (layer 4) destination.
4. **Decode and scan.** The proxy removes the global secrets and the job's
   grant values from the URL, the headers, and the body: raw, base64,
   base64url, hex, and percent encoding. A request with more than
   `decode_depth` (default 2) nested encodings stops.
5. **Plugins** (WASM filters), in the policy order.
6. **Grant substitution.** Only when the proxy talks TLS to the upstream
   (`https`). For each grant whose hosts match the destination: in the
   named header, the proxy puts the value in place of the placeholder, or
   sets the full value from the format. A `Basic` credential is decoded,
   changed, and encoded again (git over HTTPS). On plain HTTP (an `http://`
   URL, or plain HTTP in a `CONNECT` tunnel), a grant placeholder in the
   URL or a header stops the request (403, "a secret goes only over
   TLS"). A plugin secret also goes only over TLS.
7. **Leftover check.** A placeholder that is still in the URL or in a
   header stops the request (403): the grant does not permit that host. A
   placeholder in the body is not a secret. It passes and is never replaced.
8. **Send, scan the response, record.** The upstream gets
   `accept-encoding: identity`, so that the response can be scanned. The
   response is scanned for the same secrets.

Each response from the proxy has the header `x-reactorcide-egress` with the
outcome (`blocked`, `denied`, `review`, `failed`) when it stops a request.
A plugin `review` action blocks at once. Reactorcide has no review store;
the flow record keeps the reason.

## 6. Egress control setting

Egress control is on for a job when the organization, the project, or the
execution profile of the job has `egress_control: true`, or when the job has
a proxy grant. One setting cannot turn off another; the strictest one wins.
All are `false` after the migration.

- Organization: `PUT /api/v1/organizations/{name}` with
  `{"egress_control": true, "display_name": "...", "is_private": ...}`.
  This endpoint also writes `display_name` and `is_private`, so send their
  current values.
- Project: `PUT /api/v1/projects/{id}` with `{"egress_control": true}`.
- Execution profile: `PUT /api/v1/organizations/{name}/profiles/{profile}`
  with `egress_control` in the profile JSON. Send `false` to turn it off.

UI: the "Egress control" column of the organization event defaults on the
Admin page (global admin), the project Execution page (a project owner), and
the execution profile form (`/org/profiles`, an org admin). The secret grant
form on the Secrets page sets and edits the delivery mode, the proxy hosts,
the header, and the header format (`{}` is the value).

When egress control is on, all egress of the job goes through the proxy,
also for a job with no proxy-delivered secret. This also enforces
dependency mirrors (C4): permit only the mirror hosts in the policy.

## 7. Routing for each backend

| Backend | How the proxy is the only route |
| --- | --- |
| Kubernetes | Init container `egress-net` (root, all capabilities dropped except `NET_ADMIN`) runs `podnet`: the nat redirect (DNS to port 15053, other TCP to port 15001, loopback and the proxy IP excluded), the guard filter (drop all other output, for example UDP and QUIC; an established connection passes only in the direction that the job started), the input filter (accept loopback, replies of connections that the job started, and IPv6 neighbor discovery; drop all new inbound connections), and the CA file in an emptyDir. It is the first init container. The native sidecar `egress-forward` (uid 65532, no capability) runs `serve`. A startup probe holds the job container until the local proxy port answers. The job container drops all capabilities, has `allowPrivilegeEscalation: false` and the `RuntimeDefault` seccomp profile, so it cannot change the rules or send raw packets. The pod has the label `reactorcide.io/egress=proxy` for the chart NetworkPolicy (egress only to the proxy port, no ingress). |
| Docker, containerd | A helper container (`reactorcide-egress-<job>`, uid 65532, no capability) runs `serve` and owns the network namespace. A setup container joins that namespace with `NET_ADMIN` only, runs `podnet`, and exits. The job starts only after the setup exited with status 0, and joins the namespace (`container:<helper>`) with all capabilities dropped and `no-new-privileges`. The helper DNS server is `192.0.2.53`, so each query goes through the redirect to the fake DNS. The CA is a read-only bind mount of a file in the workspace. |
| VM (macOS, Windows) | The guest has no NIC. Each guest TCP flow comes to the host forwarder as `CONNECT`. With `REACTORCIDE_VM_EGRESS_PROXY` and a job key (`REACTORCIDE_VM_EGRESS_PROXY_KEY_FILE`, or the worker `REACTORCIDE_EGRESS_JOB_KEY_FILE`), the forwarder adds the job credential to each `CONNECT`. The guest never sees it. A job with egress control fails at start on a VM worker without these settings. |

The job container gets these variables (they replace job values with the
same names):

- `HTTP_PROXY`, `HTTPS_PROXY`, `http_proxy`, `https_proxy`:
  `http://<job>:<credential>@127.0.0.1:3128` (the local port of the helper).
- `NO_PROXY`, `no_proxy`: `localhost,127.0.0.1,::1`.
- `REACTORCIDE_EGRESS`: `proxy`.
- The CA file `/etc/reactorcide/egress/ca.crt` in `SSL_CERT_FILE`,
  `NODE_EXTRA_CA_CERTS`, `REQUESTS_CA_BUNDLE`, `CURL_CA_BUNDLE`,
  `GIT_SSL_CAINFO`, `PIP_CERT`, `CARGO_HTTP_CAINFO`, `AWS_CA_BUNDLE`,
  `DENO_CERT`, `BUNDLE_SSL_CA_CERT`, and `REACTORCIDE_EGRESS_CA_FILE`.

A tool that does not read the proxy variables also goes through the proxy:
the redirect sends each TCP connection to the transparent route, which finds
the destination name from the fake DNS and sends `CONNECT name:port`.

Fake DNS trap (from Magi): a Kubernetes pod has search domains and
`ndots:5`, so the first query is for example
`pypi.org.<ns>.svc.cluster.local`. The fake DNS answers NXDOMAIN for each
name under a search domain, so the resolver then asks for `pypi.org`.

## 8. Worker settings

| Variable | Meaning |
| --- | --- |
| `REACTORCIDE_EGRESS_CONTROL_URL` | Proxy control API. Empty: no egress proxy (the default). |
| `REACTORCIDE_EGRESS_PROXY_ADDR` | Proxy port as the job network reaches it (`host:port`). The worker resolves the host. |
| `REACTORCIDE_EGRESS_CONTROL_TOKEN_FILE` | Control token file (at least 32 bytes). |
| `REACTORCIDE_EGRESS_JOB_KEY_FILE` | Job key file, the same as the proxy `REACTORCIDE_EGRESS_JOB_KEY`. |
| `REACTORCIDE_EGRESS_CA_FILE` | Proxy CA certificate (public). |
| `REACTORCIDE_EGRESS_NET_IMAGE` | Image of the network helper. Default: the `reactorcide/egress` image. |
| `REACTORCIDE_EGRESS_NETWORK` | Docker or nerdctl network of the helper, from which the proxy is reachable. Default `bridge`. |

Helm: `egressProxy.enabled` (default `false`), `egressProxy.secretName` (a
Secret with `ca.crt`, `ca.key`, `job.key`, `control.token`),
`egressProxy.policy`, `egressProxy.controlTLS.enabled` (default `true`:
the control API uses TLS and the worker URL is `https`),
`egressProxy.networkPolicy.enabled` (default `true`: job pods get egress
only to the proxy port and no ingress; the proxy accepts the control port
only from worker pods and the proxy port only from egress job pods), and
`egressProxy.networkPolicy.proxyClients` (more peers of the proxy port,
for example VM worker hosts), `egressProxy.secretsDirSecret` (a Secret with
operator secrets for WASM plugins and the SSH proxy), `egressProxy.allowUnregistered`
(default `false`), `egressProxy.netImage` (empty: the proxy image), and
`egressProxy.service.proxyPort` / `.controlPort` (`3128` / `8080`). The proxy probes are exec probes, so they
need no ingress from the node. The worker mounts `ca.crt`, `job.key`, and
`control.token` only, not `ca.key`.

An `https` `REACTORCIDE_EGRESS_CONTROL_URL` makes the worker trust only
the proxy CA (`REACTORCIDE_EGRESS_CA_FILE`) for the control API. Set the
proxy `REACTORCIDE_EGRESS_CONTROL_TLS_NAME` to the host name of that URL.

Compose: the `egress-proxy` service has the profile `egress` (off by
default). Make the keys with `scripts/egress-dev-keys.sh`.

## 9. Known limits

- **Git over HTTPS with Basic auth.** Git sends `user:password` in base64.
  The proxy decodes a `Basic` value in the grant header and replaces the
  placeholder in it. Other encodings of a placeholder are not found: the
  placeholder then goes to the host as text, which is not a secret.
- **Protocols that are not HTTP, and pinned certificates.** The proxy ends
  TLS with its own CA. A client that pins a certificate or a key (for
  example linkkeys) fails. Use a `passthrough` destination (layer 4, not
  inspected, no secrets) or another path. SSH ends at the proxy only for
  `ssh_hosts`.
- **Streamed responses.** The proxy reads a full response before it sends
  it, to scan it. A streamed response (`text/event-stream`) comes only at
  the end, unless its host is in `event_stream_hosts` (each event is scanned;
  a secret split over two events is not found). A response larger than
  `max_body` stops, unless its host is in `stream_hosts` (not inspected).
- **HTTP/1.1 only.** HTTP/2 and WebSocket upgrades stop.
- **Capabilities, root, and sudo.** A job with `docker` (privileged) could
  remove the redirect, so the coordinator refuses the claim and the worker
  refuses the job with egress control. The `builder` sidecar does not trust
  the proxy CA yet, so it is refused too. A job with `run_as: root` is
  refused: root, or any `CAP_NET_RAW`, can send AF_PACKET frames, which
  nftables does not see. The job container of an egress job drops all
  capabilities and has no-new-privileges (Kubernetes:
  `allowPrivilegeEscalation: false`). Thus `sudo` does not work in an egress
  job (the runnerbase `sudo apt-get` line stays for other jobs). Install
  tools in the image, or use a per-user installer such as webi.
- **Inbound connections.** The input filter drops each new inbound
  connection to the job, and the chart NetworkPolicy denies ingress to
  egress job pods. A job cannot serve a port to other pods or containers.
- **VCS checkout credential.** The checkout credential (`gitconfig` and
  `credentials` in the job) is in the job container. It is not a proxy
  grant, and the proxy scanner does not know its value. A job can send it
  to any permitted destination. Use a read-only, short-lived credential.
- **`stream_hosts`.** A response from a host in `stream_hosts` that is
  larger than `max_body` is not scanned. Permit only hosts that you trust
  not to echo secret values (for example artifact downloads).
- **Java.** No trust store is made. Java does not read `SSL_CERT_FILE`. A
  Java job needs a trust store with the CA (for example
  `JAVA_TOOL_OPTIONS=-Djavax.net.ssl.trustStore=...`).
- **VM guests** do not get the CA file. A guest image must trust the proxy
  CA.
- **Cluster names.** The fake DNS answers NXDOMAIN for each name under a
  search domain. A cluster name such as `svc.ns.svc.cluster.local` works
  only through the proxy variables, not through the transparent route.
- **Flow records** are in the proxy memory. A restart loses them. The worker
  logs a summary of each job (flows, blocked flows, grants used).
- **Plugins.** The image has no default WASM plugin. The `ask` host function
  calls an optional external service (`REACTORCIDE_EGRESS_ASK_URL`).
- **Test coverage.** A Docker end-to-end test runs a real job with the
  helper, the redirect, the guard, the fake DNS, and the real proxy
  (`REACTORCIDE_EGRESS_DOCKER_E2E=1 go test ./internal/worker/ -run
  TestDockerEgressEndToEnd`, see the test for the image). Kubernetes and
  containerd have unit tests of the pod spec and of the nerdctl arguments
  only; they have not run on a real cluster or containerd host yet. The
  Docker test also checks that the job has no capability, no-new-privileges,
  no AF_PACKET socket, and that an inbound connection carries no data out.
  `go test ./...` in `egress_proxy/netd` installs the real rules in an
  unprivileged user and network namespace with a veth link
  (`TestNamespaceInboundBlocked`; skipped without unprivileged user
  namespaces).
