# Reactorcide Agent Guide

Reactorcide is a minimalist CI/CD system focused on isolated container job execution, local-first operation, and safe handling of untrusted outside contributions. This file is intentionally concise; use the design docs as the source of truth for architecture.

## Read First

- `DESIGN.md` - system architecture, security model, deployment shapes, workflow model.
- `runnerlib/DESIGN.md` - runnerlib responsibilities and execution details.
- `README.md` - quick start, common local development commands, and documentation map.

## Non-Negotiable Rules

- Do not commit to this repository.
- Do not push from this repository.
- Do not expose secrets to the conversation, command output, logs, patches, or files.
- Do not ask the user to paste secrets. Use secret references, password files, or command substitution so secret values never enter assistant-visible context.
- Do not run commands that print secret values, such as `reactorcide secrets get ...` by itself, `env` in a secret-bearing shell, or debug output that dumps environment/config.
- Do not replace `${secret:path:key}` references with plaintext values in job YAML, overlays, examples, tests, docs, or generated output.
- Keep changes scoped. Do not rewrite unrelated architecture, formatting, generated files, or user changes unless explicitly requested.

## Secret Handling

Prefer inline environment assignment with command substitution for commands that need secrets, so the value is passed directly to the process and not printed:

```bash
REACTORCIDE_SECRETS_PASSWORD="$(cat ~/.reactorcide-pass)" \
  ./coordinator_api/reactorcide run-local ./jobs/build-all.yaml
```

When a command needs an API token or another stored secret, pass it through the consuming command rather than storing or displaying it:

```bash
REACTORCIDE_SECRETS_PASSWORD="$(cat ~/.reactorcide-pass)" \
  ./coordinator_api/reactorcide submit \
    --api-url "http://example.internal:6080" \
    --token "$(
      REACTORCIDE_SECRETS_PASSWORD="$(cat ~/.reactorcide-pass)" \
        ./coordinator_api/reactorcide secrets get reactorcide/api api_key
    )" \
    ./jobs/my-job.yaml
```

If a secret-bearing command fails, summarize the failure without including secret values. If inspection would require revealing a secret, stop and ask for a safer path.

## Repo Orientation

- `coordinator_api/` - Go CLI, REST API, workers, job specs, secrets, VCS integration, and Corndogs integration.
- `webapp/` - the management UI. A thin bridge (Go) plus a SolidJS SPA in
  `webapp/ui/`, compiled into the binary. It holds NO coordinator credential;
  see the rule below.
- `runnerlib/` - Python job execution library and runner utilities.
- `jobs/` - Reactorcide jobs used to build, test, deploy, and dogfood the system.
- `examples/` - example job definitions and API usage.
- `helm_chart/`, `deployment/`, `docker-compose.yml`, `skaffold.yaml` - local and production deployment assets.

## Architecture Notes Agents Must Preserve

- The webapp holds no coordinator API token. Every browser read and write is
  authenticated by the browser's own session, forwarded through the CSIL-RPC
  bridge (`webapp/internal/handlers/rpc_bridge.go`), which discards any
  credential the client supplied. If the UI needs data it cannot reach, add a
  CSIL operation that authorizes the caller — never a REST call from the webapp,
  and never a service token.
- The SPA is compiled into the webapp binary. Run `./tools build-ui` before
  `go build` in `webapp/`, or the binary serves a placeholder page and a 503.
- Metric series carry no `scope` label. A series with no `component` label is
  the job roll-up; one with a `component` label belongs to that component.
- `runnerbase` carries webi (https://webinstall.dev). A job needing a toolchain
  the image does not have installs it at a PINNED version rather than adding it
  to the image: `webi node@22.23.2`. webi is per-user (`$HOME/.local`), so it is
  installed as the runner user and both `~/.local/bin` and
  `~/.local/opt/<name>/bin` are on the image PATH.

- Trusted CI definitions are separate from untrusted source code. For fork PRs, `SourceURL` can point at the fork, but `CISourceURL` must remain trusted upstream CI content.
- `run-local` is the canonical local execution path. It mounts the current repository as source and trusted CI by default. Use `--source-dir` and `--ci-dir` for separate local trees, or clone requested code with `--code-url` and `--code-ref`.
- Worker jobs run as the image runner uid `1001` by default, or as `run_as.user` when set. Capabilities such as `docker` and `builder` do not implicitly switch jobs to root.
- `run-local` defaults to the host uid for direct bind mounts. Use `--as-runner`, `--user`, `run_as`, or job `run_local` settings when a job needs runner/root/explicit uid behavior.
- `run_local` is local-only configuration. The worker ignores it; `run_as` is portable job identity configuration.
- `HOME` should remain writable as `/home/runner` in both local and worker-oriented execution modes.
- Runnerlib jobs can prepare both origin and upstream remotes from `REACTORCIDE_HEAD_*` and `REACTORCIDE_BASE_*` environment variables. Raw-command jobs that need both remotes must do that setup explicitly.

## Development Workflow

- Check `git status --short` before edits and preserve user changes.
- Use `rg` / `rg --files` for repo search.
- Prefer existing project commands and patterns over introducing new tooling.
- Run targeted tests or validation for the files changed. If a full test would require secrets, privileged containers, network, or a long-running deployment, explain what was not run.
- For Go code, inspect nearby package tests and run the narrowest relevant `go test` command.
- For Python runnerlib code, inspect the local test layout and run the narrowest relevant Python/pytest command available in the repo.
- Do not run deployment, image push, remote submit, or production-affecting jobs unless the user explicitly requests it and the command can be run without exposing secrets.

## Common Commands

Build the CLI:

```bash
cd coordinator_api && go build -o reactorcide .
```

Run the local dev stack:

```bash
docker compose up -d
```

Run the project test helper:

```bash
./tools test
```

Build the web UI into the webapp binary, and test it:

```bash
./tools build-ui
./tools test-ui
```

Run a local job with secrets supplied without disclosure:

```bash
REACTORCIDE_SECRETS_PASSWORD="$(cat ~/.reactorcide-pass)" \
  ./coordinator_api/reactorcide run-local ./jobs/build-all.yaml
```

Test fork PR behavior locally:

```bash
./coordinator_api/reactorcide run-local \
  --source-dir /path/to/tested-worktree \
  --ci-dir ./ \
  --event pull_request_updated \
  .reactorcide/workflows/pr.yaml
```

Use runner uid parity locally:

```bash
./coordinator_api/reactorcide run-local --as-runner ./jobs/my-job.yaml
```
