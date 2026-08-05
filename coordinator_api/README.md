# Reactorcide Coordinator and CLI

This Go module builds the `reactorcide` binary. The binary includes the
coordinator, worker, local runner, remote client, token commands, and secret
commands.

## Build

Use the Go version in `go.mod`:

```bash
go build -o reactorcide .
```

## Main Commands

```text
reactorcide serve
reactorcide worker
reactorcide run-local
reactorcide submit
reactorcide logs
reactorcide token
reactorcide secrets
reactorcide secret-grants
reactorcide migrate
```

Run `./reactorcide COMMAND --help` for current arguments.

## Tests

Run a package test:

```bash
go test ./internal/vcs
```

Run all module tests:

```bash
go test ./...
```

Some integration tests need a container runtime. See
[test/README.md](./test/README.md).

## Documentation

- [System Design](../DESIGN.md)
- [Installation and Deployment](../docs/getting-started.md)
- [Worker Operation](../docs/workers.md)
- [VCS Provider Integration](./docs/vcs_integration.md)
