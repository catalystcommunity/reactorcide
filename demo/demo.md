# Reactorcide Demo

This demo walks through Reactorcide's core features: encrypted secrets, local job execution, remote job submission, and container image builds. By the end you'll have built a static site, pushed it to a registry, and served it locally.

## Prerequisites

1. Build the CLI (one time):
   ```bash
   cd coordinator_api && go build -o reactorcide . && cd ..
   ```

2. Install host dependencies (Arch example):
   ```bash
   sudo pacman -S nerdctl cni-plugins
   sudo modprobe bridge br_netfilter
   ```

3. Set your secrets password (never goes in a file):
   ```bash
   export REACTORCIDE_SECRETS_PASSWORD="pick-a-password"
   ```

4. Create your config:
   ```bash
   cp demo/demo-config.example.yaml demo/demo-config.yaml
   # Edit demo-config.yaml with your registry, API URL, etc.
   ```

5. Have your API token ready (either `export REACTORCIDE_API_TOKEN=...` or paste interactively during setup).

## Demo Steps

Run `./demo.sh <step>` for each step. Run `./demo.sh` with no arguments to see all available steps.

| # | Command | What it does | What to look for |
|---|---------|-------------|-----------------|
| 1 | `./demo.sh validate` | Checks all required tools (CLI, git, nerdctl/docker, curl), shows resolved config values and detected backend. | All green, backend detected, config values populated. |
| 2 | `./demo.sh setup` | Initializes encrypted secrets storage, stores your API token at `demo/api:api_token`, creates a test secret for masking demos. | "Secrets storage ready", "API token stored", "Test secret stored". |
| 3 | `./demo.sh setup-registry` | Prompts for your container registry password, stores it encrypted at `demo/registry:password`. | "Registry password stored". |
| 4 | `./demo.sh setup-remote` | Syncs local demo secrets (test secret, registry password) to the coordinator API so remote jobs can resolve `${secret:...}` references. | "Synced demo/test:secret", "Synced demo/registry:password". |
| 5 | `./demo.sh hello-local` | Runs a simple Alpine container locally via `run-local`. Prints a greeting and echoes a secret value that should appear masked. | The `SECRET` line should show `***` instead of the actual value. |
| 6 | `./demo.sh hello-remote` | Submits the same hello job to your remote Reactorcide coordinator via `submit --wait`. The coordinator schedules it on a worker. | Job ID printed, status polls from `submitted` to `completed`. |
| 7 | `./demo.sh check-image` | Attempts to pull the demo site image from the registry. Should fail because it hasn't been built yet. | "Image does not exist yet" in green. |
| 8 | `./demo.sh build-local` | Builds a Pysocha static site inside a container using buildkit, packages it with Caddy, and pushes the image to your registry. | buildkitd starts, build stages run, "Image built and pushed!". |
| 9 | `./demo.sh check-image` | Pulls the demo image again. Should succeed now. | "Image already exists" in yellow. |
| 10 | `sudo nerdctl pull <image> && sudo nerdctl run -p 8080:80 <image>` | Pull and run the built image. Visit `http://localhost:8080` to see the demo site. | A dark-themed page titled "Hello from Reactorcide". |
| 11 | `./demo.sh build-remote` | *(optional)* Same build, but dispatched through the coordinator. Proves the full remote pipeline. | Job submitted, polls to `completed`. |
| 12 | `./demo.sh reset` | Deletes the demo image from the registry, removes test and registry secrets. Keeps the API token. | "Image deleted", secrets removed, "API token preserved". |

## What Each Step Demonstrates

- **validate** — Reactorcide auto-detects your container runtime (docker vs containerd) and whether sudo is needed. Shows the full resolved config hierarchy (config file defaults, env var overrides).
- **setup / setup-registry** — Reactorcide's local encrypted secrets storage. Secrets are encrypted at rest and resolved at job execution time. The `${secret:path:key}` syntax in job YAML is how jobs reference secrets without embedding plaintext.
- **setup-remote** — The coordinator API has its own secrets storage. Local secrets are resolved by the CLI for `run-local`, but remote jobs need secrets stored on the server. This step syncs them via the REST API.
- **hello-local** — The `run-local` command: run any job definition on your laptop in a container. No server needed. Secret values are automatically masked in job output.
- **hello-remote** — The `submit` command: dispatch jobs to a Reactorcide coordinator API. The coordinator queues the job and a worker picks it up. `--wait` polls until completion.
- **check-image** — A before/after proof that the build step actually pushed a pullable image.
- **build-local / build-remote** — A real CI/CD job: checks out source, runs a multi-stage Docker build (pysocha static site generator + Caddy web server), authenticates to a registry, and pushes the image. The `capabilities: [docker]` flag in the job YAML grants the container privileged access for buildkit.
- **reset** — Cleans up everything the demo created: registry image, local secrets. Idempotent and safe to run multiple times.

## File Structure

```
demo/
  demo.sh                    Main script
  demo-config.example.yaml   Config template (copy to demo-config.yaml)
  demo.md                    This file
  jobs/
    hello-demo.yaml           Simple hello-world job with secret masking
    build-demo-site.yaml      Container build job (buildkit + registry push)
  site/
    config.yaml               Pysocha site config
    Dockerfile                Multi-stage build (python:slim -> caddy:alpine)
    Caddyfile                 Web server config (serves on :80)
    content/pages/index.md    Demo site content
    content/extra_files/      Static assets (CSS)
    templates/                Jinja2 templates
```

## Troubleshooting

- **"container socket requires elevated privileges"** — The script auto-detects this and uses `sudo -E`. You'll be prompted for your password on local steps.
- **"CNI plugin bridge not found"** — Install CNI plugins: `sudo pacman -S cni-plugins` (Arch) or download from [containernetworking/plugins](https://github.com/containernetworking/plugins/releases).
- **"operation not supported" creating bridge** — Run `sudo modprobe bridge br_netfilter`. If modules aren't found, reboot into your latest kernel.
- **API returns 400 on submit** — Job YAML needs a `source:` block with `type: git` and a `url:` for the coordinator API to accept it.
- **Secret not found on remote** — Local secrets (`${secret:...}`) are resolved locally for `run-local`. For `submit`, the coordinator must have the secret stored via its API.
