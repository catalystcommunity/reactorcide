# Reactorcide Documentation

Use this page to select the correct guide.

## Start Here

- [Installation and Deployment](./getting-started.md): Select local, VM, or
  Kubernetes installation.
- [Connect a VCS Repository](./vcs-setup.md): Connect GitHub. Use the
  extension checklist to complete GitLab or add a new provider.
- [Job Definition Reference](./job-definitions.md): Define repository jobs and
  workflows.
- [Security Model](./security-model.md): Understand trust boundaries before
  you give secrets or runtime capabilities to a job.
- [Organizations and Trusted CI Policy](./organizations-and-ci-policy.md):
  Configure tenants, token scope, worker classes, execution profiles, CI
  admission, approvals, reports, and audit export.

## Operator Guides

- [CLI Reference](./cli-reference.md): Find the command for each coordinator
  operation.
- [Runtime Behavior](./runtime-behavior.md): Compare local, worker, and
  Kubernetes paths and users.
- [Worker Operation](./workers.md): Enroll workers and configure queues,
  characteristics, and resources.
- [Secrets](./secrets.md): Configure local and server secret storage.
- [VCS Credentials and Secret Grants](./vcs-credentials-and-secret-grants.md):
  Configure checkout credentials and job secret authorization.
- [UI Authentication and Authorization](./ui-auth.md): Configure login, roles,
  and visibility.
- [Kubernetes Deployment](../helm_chart/DEPLOYMENT.md): Install and operate the
  Helm chart.
- [macOS VM Runner](./vm-runners-macos.md): Configure the macOS VM backend.
- [VM Image Operations](./vm-images.md): Build, publish, pull, prefetch, and
  remove VM images.
- [Windows VM Runner](./vm-runners-windows.md): Configure the Windows VM
  backend.

## Job Author Guides

- [Writing Pipelines](./writing-pipelines.md): Create follow-up jobs.
- [Workflow Design](./workflow-design.md): Understand coordinator workflow
  state and dependency rules.
- [Runnerlib Usage](../runnerlib/docs/usage.md): Use the job-side library.
- [Plugin Development](../runnerlib/docs/plugin_development.md): Add runnerlib
  lifecycle hooks.

## Contributor Guides

- [System Design](../DESIGN.md): Read the implemented architecture and trust
  boundaries.
- [Release Process](./release-process.md): Create release tags, build
  artifacts, and recover a failed release.
- [Runnerlib Design](../runnerlib/DESIGN.md): Read runnerlib responsibilities.
- [VCS Provider Integration](../coordinator_api/docs/vcs_integration.md): Add a
  VCS adapter.
- [Coordinator Tests](../coordinator_api/test/README.md): Run coordinator
  integration tests.
