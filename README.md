# reactorcide

This is the minimalist CI/CD system that aims to be fully fledged for the needs of a serious engineering team. It should work well for open source or business needs, with a particular focus on "outside contributions" have security in place. It evolved from a simple set of bash utilities.

You should be able to run jobs from your laptop just as easily as from the whole system, so if your source control (git, mercurial, whatever) provider is down, fine. It's just building blocks. Glue to make it specific to a provider is also provided where we needed it.

## Documentation

- **[DESIGN.md](./DESIGN.md)** - Complete system architecture, design principles, and deployment models
- **[CLAUDE.md](./CLAUDE.md)** - Implementation guidance for AI assistants and contributors
- **[runnerlib/DESIGN.md](./runnerlib/DESIGN.md)** - Detailed runnerlib architecture and API
- **[deployment-plan.md](./deployment-plan.md)** - Deployment strategy and migration roadmap

## Philosophy

We want to react to source code change events. Ultimately we want to have:
* An isolated run from a known state as much as is feasible
* A configuration for some knobs for the thing running the job (the CI/CD system, or reactorcide in this case)
* A configuration passed to the job itself (the VCS ref, the context, secrets, etc)
* No ties to the VCS host, so if say, this VCS host is down, we can still run reactorcide from a checkout on anything we can pass the job to (the API, and a place to pull it from, maybe a tarball included in the payload, whatever)
* Functions to make some checks easier. Like, has the commit we've checked out already been tagged? With <foo> specifically? Files changed? Those should just be function calls.
* Run the actual job inside a given docker container. We'll have standards of a "this is where the lib and your metadata is mounted, we'll run your entrypoint" style API

We might add some additional things later like a webhook-to-job API and a log capture mechanism or something. Secrets are a whole thing, a job queue is a whole thing. We're doing the basic versions of those to start. We're trying to be minimal so peeps (mostly us) can glue this up however they want.

## Status

Just beginning and playing. Join the [Catalyst Community Discord](https://discord.gg/sfNb9xRjPn) to chat about it.

If you want to try it, we'll have more here when the new not-bash version is ready (for us, because the instructions will also be for us).

