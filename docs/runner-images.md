# Runner Images

A job can use any image that obeys the contract on this page. The project
image `runnerbase` (`runnerlib/Dockerfile.runner`) obeys it and adds common
tools.

## Image Contract

| Item | Contract |
| --- | --- |
| User | Worker jobs run as uid `1001` by default, or as `run_as.user`. The image must work as uid 1001. `runnerbase` has the user `runner` with uid 1001. |
| `HOME` | `/home/runner`. The runner makes it writable in all modes (worker and `run-local`). Do not keep files in `/home/runner` in the image: `run-local` mounts an empty directory over it. |
| Working directory | `/job`. The source is at `/job/src`. Trusted CI is at `/job/ci` when the job has a separate CI source. |
| Entrypoint | The worker clears the entrypoint. The job `command` is the full command. |
| Shell | Each job command runs through `sh -c` by default (PowerShell for `os: windows`), also a single-line command, so the image needs `/bin/sh`. An image without a shell (for example distroless) must set `REACTORCIDE_JOB_SHELLCMD: none` (or `command_prefix: none`), or a prefix for a shell that the image has. See [Command Execution](./job-definitions.md#command-execution). |
| runnerlib | Optional. Use it for source preparation, plugins, and triggers. A raw-command job does not need it. |
| `webi` | `runnerbase` has `webi` in `/usr/local/bin`. A job installs a missing toolchain at a pinned version, as the runner user: `webi node@22.23.2`. `~/.local/bin` and `~/.local/opt/<name>/bin` are on the PATH. |
| Root | Capabilities (`docker`, `builder`) do not change the user. Use `run_as.user: root` when the job needs root. |
| Network | The worker can send all egress through the egress proxy ([Egress Proxy](./egress-proxy.md)). A job must then use `HTTP_PROXY` and `HTTPS_PROXY` and trust the CA in `REACTORCIDE_EGRESS_CA_FILE`, or be transparent-proxy compatible. |

The job environment has the reserved values of the coordinator
(`REACTORCIDE_*`, `RC_WF_*`). See [Runtime Behavior](./runtime-behavior.md)
and [Builds, Caches, and Job Settings](./builds-and-caches.md).

## Pinned Versions

Every version in `runnerlib/Dockerfile.runner` is pinned:

| Part | Pin |
| --- | --- |
| Base image | `python:3.14.10-slim-trixie@sha256:...` (tag and digest) |
| uv | `ghcr.io/astral-sh/uv:0.13.8@sha256:...` (tag and digest) |
| Go | `GO_VERSION` and the sha256 of each release tarball |
| Rust | `RUST_VERSION` (rustup toolchain version) |

Known items that are not pinned:

- Debian packages (`apt-get install`). The base image digest selects the
  Debian release. A rebuild can get newer point-release packages.
- The `rustup-init` and `webi` install scripts. They are downloaded at build
  time over HTTPS. The installed Rust toolchain is pinned.

### Update a pin

1. Find the new tag.
2. Get the digest of the multi-platform index, for example with
   `crane digest ghcr.io/astral-sh/uv:0.13.9` or
   `docker buildx imagetools inspect ghcr.io/astral-sh/uv:0.13.9`.
3. Change the tag and the digest together.
4. For Go, change `GO_VERSION` and both sha256 values (from
   `https://go.dev/dl/?mode=json`).
5. Build the image and run a job with it (`./tools` has a build command).

An organization can require digest-pinned job images with the image policy
([Builds, Caches, and Job Settings](./builds-and-caches.md#image-policy)).
