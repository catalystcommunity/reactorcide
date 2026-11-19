# Reactorcide Documentation

Use this page to select the correct guide.

## Start Here

- [Installation and Deployment](./getting-started.md): Select local, VM, or
  Kubernetes installation.
- [Connect a VCS Repository](./vcs-setup.md): Connect GitHub. Use the
  extension checklist to complete GitLab or add a new provider.
- [GitHub Webhook Setup](./github-webhook-setup.md): Find the webhook
  endpoint and the events to select.
- [Job Definition Reference](./job-definitions.md): Define repository jobs and
  workflows.
- [Security Model](./security-model.md): Understand trust boundaries before
  you give secrets or runtime capabilities to a job.
- [Organizations and Coordinator CI Policy](./organizations-and-ci-policy.md):
  Configure tenants, token scope, worker classes, execution profiles, CI
  admission, approvals, reports, and audit export.
- [Principals](./principals.md): Understand principals (humans, service
  principals, the system principal), identities, subjects, actors of runs,
  and service principal tokens.
- [Inbound Federation](./federation.md): Let a workload of another OIDC
  issuer act as a service principal with no static token.
- [OIDC Tokens for Jobs](./oidc.md): Give jobs signed OIDC tokens for cloud
  providers and other consumers, and write safe trust conditions.
- [Ref Classes](./ref-classes.md): Understand the labels that the coordinator
  gives to the ref of each event, the rules that add them, and the secret
  grants, policies, and worker classes that read them.
- [Required Set](./required-set.md): Select the workflows that block a merge,
  promote advisory workflows with approval, and quarantine a workflow for a
  limited time.
- [Reporting and Ownership](./reporting-and-ownership.md): Control what
  Reactorcide writes to the VCS, compare with another CI system, import code
  owners, and accept workflow path dependencies.

## Operator Guides

- [CLI Reference](./cli-reference.md): Find the command for each coordinator
  operation.
- [Runtime Behavior](./runtime-behavior.md): Compare local, worker, and
  Kubernetes paths and users.
- [Worker Operation](./workers.md): Enroll workers and configure queues,
  characteristics, and resources.
- [Priority and Lanes](./priority.md): Set the lanes that give each job its
  queue priority, and the priority shifts of groups.
- [Secrets](./secrets.md): Configure local and server secret storage.
- [Egress Proxy](./egress-proxy.md): Keep secrets out of jobs with
  proxy-delivered grants, and send all job egress through the proxy.
- [Builds, Caches, and Job Settings](./builds-and-caches.md): Cache
  namespaces, reserved cache facts, job links, environment defaults, registry
  mirrors, the rootless builder, and the image policy.
- [Artifacts and Test Results](./artifacts-and-tests.md): Publish job
  artifacts, read JUnit test results, find flaky tests, and quarantine a test.
- [Provenance](./provenance.md): The signed SLSA provenance of jobs, its
  verification, and deploy-time admission.
- [Runner Images](./runner-images.md): The job image contract and the pinned
  versions of the runner image.
- [GitHub App](./github-app.md): Register a pool of GitHub Apps for
  installation tokens, check runs, and self-service project registration.
- [VCS Credentials and Secret Grants](./vcs-credentials-and-secret-grants.md):
  Configure checkout credentials and job secret authorization.
- [UI Authentication and Authorization](./ui-auth.md): Configure login, roles,
  and visibility.
- [Kubernetes Deployment](../helm_chart/DEPLOYMENT.md): Install and operate the
  Helm chart.
- [Upgrade to 0.13](./upgrade-0.13.md): Upgrade order, behavior changes at
  the defaults, new optional features, and rollback.
- [macOS VM Runner](./vm-runners-macos.md): Configure the macOS VM backend.
- [VM Images Quick Start](./vm-images-quickstart.md): Create a base image,
  put it in an OCI registry or S3, build derived images, and set an alias.
- [VM Image Operations](./vm-images.md): Build, publish, pull, prefetch, and
  remove VM images.
- [Image Aliases and Worker Image Sets](./image-aliases.md): Manage image
  aliases, roll out a new pin to workers, and read worker image states.
- [Windows VM Runner](./vm-runners-windows.md): Configure the Windows VM
  backend.
- [Headless Windows Worker](./windows-headless-worker.md): Install and update a
  Windows worker through SSH.
- [Windows VM Image Build](./windows-vm-image-build.md): Build a Hyper-V guest
  image without guest interaction.

## Job Author Guides

- [Writing Pipelines](./writing-pipelines.md): Create follow-up jobs.
- [Workflow Design](./workflow-design.md): Understand coordinator workflow
  state and dependency rules.
- [Events, Checks, and Schedules](./events-and-schedules.md): Understand
  events, duplicate handling, `Reactorcide Checks`, conditional jobs, and
  scheduled builds.
- [Outbound Events](./outbound-events.md): Send phase events of runs to a
  signed webhook. Subscriptions, delivery guarantees, and replay.
- [Failure Diagnosis](./diagnosis.md): Let a diagnosis tool (a service
  principal) read failed jobs, post diagnosis entries, and retry with shifts.
- [Observability](./observability.md): Prometheus metrics, rollups, the
  Insights dashboards, pipeline owner alerts, and SLOs with error budgets.
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
