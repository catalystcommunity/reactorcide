---
Title: "Hello from Reactorcide"
---

## Hello, World!

This site was built and deployed entirely by **Reactorcide** — a minimalist CI/CD system
for serious engineering teams.

### What just happened?

1. A **Reactorcide job** checked out this demo site's source code
2. [Pysocha](https://github.com/catalystcommunity/pysocha) compiled the Markdown into static HTML
3. [Caddy](https://caddyserver.com/) serves it from a lightweight container
4. The container image was pushed to a registry you can pull from

### Features demonstrated

- **Local execution** with `run-local` — run jobs on your laptop
- **Remote submission** with `submit` — dispatch jobs to a Reactorcide coordinator
- **Encrypted secrets** — registry credentials never leave your machine in plaintext
- **Container builds** — build and push OCI images from inside jobs
- **Docker capability** — jobs that need Docker get it, securely

All from a single `demo.sh` script.
