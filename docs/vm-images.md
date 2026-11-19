# VM Image Operations

Reactorcide can build, publish, pull, cache, and remove VM images through the
`reactorcide vm-image` command. macOS and Windows workers use the same pull and
cache code when they start a job.

For the full procedure from a new base image to a job alias, see
[VM Images Quick Start](./vm-images-quickstart.md). Reactorcide does not
publish base images. You make them and keep them in an OCI registry or in S3.

Windows bundles contain only `disk.vhdx`. The image contains the guest daemon
service and no keys. See
[Windows VM Image Build](./windows-vm-image-build.md) for unattended creation,
derived images (`vm-image build windows`), publishing, and fleet cache
configuration.

Every VM image must contain the Reactorcide guest daemon `vmguest`
(`coordinator_api/cmd/vmguest`). Job VMs have no network device. The worker
reaches the daemon through a hypervisor socket (vsock on macOS, Hyper-V
sockets on Windows), and guest egress goes through the same socket to the
host egress forwarder. See [macOS VM Runner](./vm-runners-macos.md) and
[Windows VM Runner](./vm-runners-windows.md).

## Image creation workflow

Use these image levels:

1. Create one bootstrap image for a macOS release.
2. Create a toolchain image from the bootstrap image.
3. Create more specific images from the toolchain image.
4. Publish each reusable image to an OCI registry or to S3.
5. Set an image alias to the pinned digest reference. Jobs name the alias.

For example:

```text
macos-26-bootstrap
  -> macos-26-xcode
    -> macos-26-xcode-ios
      -> macos-26-xcode-ios-company
```

The bootstrap image contains macOS, the job account, and the guest daemon. A derived image can contain Xcode, simulator runtimes,
other toolchains, certificates, system configuration, or project tools.

Do not put private signing keys, registry tokens, or other long-lived secrets
in an image. Supply job secrets through the normal Reactorcide secret path.

## Before you start

Install the Reactorcide CLI on an Apple silicon Mac that can use
Virtualization.framework. Prepare these items:

- A local bootstrap bundle or an OCI reference to one. The bootstrap must
  contain the guest daemon.
- One or more provisioning scripts for a derived image.
- Optional: a darwin/arm64 `vmguest` binary, to update the guest daemon in a
  derived image.
- An OCI repository with artifact support, or an S3 bucket.
- Registry or S3 credentials, if the store is not public.

The local bundle must contain these files:

```text
disk.img
aux.img
hardwaremodel.bin
machineidentifier.bin
```

See [macOS VM Runner](./vm-runners-macos.md) for the host and bootstrap VM
setup.

## Step 1: Log in to a registry when necessary

A public registry does not need a login. For a private registry, pass a
password or token through standard input. Do not put it in a command
argument:

```sh
registry-password-command | reactorcide vm-image registry login \
  --username ci-user \
  --password-stdin \
  registry.example.com
```

The default credential file is:

```text
~/.config/reactorcide/oci-auth.json
```

The file uses the Docker credential-file format. It can contain credentials
for several registry hosts. Reactorcide sets its mode to `0600` when it writes
the file.

Use `--auth-file` to select a different credential file. The worker uses
`REACTORCIDE_VM_REGISTRY_AUTH_FILE` for the same purpose. Reactorcide never
logs credential values.

Remove one registry credential with this command:

```sh
reactorcide vm-image registry logout registry.example.com
```

## Step 2: Create and publish a bootstrap image

A bootstrap image is the base for all other macOS images. Create one for each
macOS release or base system that the installation supports. The bootstrap
guest needs:

- A completed macOS installation.
- A `reactorcide` account, or another selected account.
- A writable home directory.
- The guest daemon, installed as a launchd daemon.

On the SDK used for the current prototype, the first bootstrap still needs
Setup Assistant. Complete Setup Assistant, configure the account, and shut
down the VM. Then install the guest daemon from the host while the VM is
stopped:

```sh
cd coordinator_api
GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build -o vmguest ./cmd/vmguest
sudo ../deployment/macos/install-vmguest.sh \
  /opt/reactorcide/images/macos-26-bootstrap ./vmguest
```

This is a one-time task for the image publisher. It is not part of a derived
image build or a job run. Remote Login and SSH keys are not necessary.

Publish the stopped local bootstrap bundle:

```sh
reactorcide vm-image publish \
  /opt/reactorcide/images/macos-26-bootstrap \
  registry.example.com/reactorcide/macos-26-bootstrap:26
```

The command prints a digest reference. Record that reference. Use it as the
base of derived image builds.

### Bootstrap credential state

The image contains no worker credential. Only the host can open the vsock
device of a VM, so the guest daemon needs no key or password. Different
operators can run the same image digest.

The cloned disk still contains these items:

- The guest account password hash, if the account has a password.
- Any other account, key, token, history, or credential that was in the base
  image or that a provisioning script added.

Before an image publisher gives an image to another operator, the publisher
must prepare it for that recipient:

1. Change or disable the bootstrap account password.
2. Remove shell history, download credentials, package-manager credentials,
   temporary files, private signing keys, and other build secrets.
3. Run a final validation script before publication.

Apple announced `VZMacGuestProvisioningOptions` and
`VZMacOSVirtualMachineStartOptions` in 2026. These APIs create the first user
and can enable automatic login and Remote Login on the first boot. The macOS
26.5 and Xcode 26.6 SDK on the prototype host does not include these APIs.
When the supported host SDK includes them, the Reactorcide bootstrap command
should use them and remove the Setup Assistant task. Do not enable Remote
Login with these APIs: the guest daemon replaces it.

## Step 3: Write provisioning scripts

Each `--provision` argument names a local text file. Reactorcide reads the
file and runs its contents in the guest with `/bin/zsh -c`. It runs scripts in
the order given on the command line. The guest daemon runs them as the
account that `--guest-user` names (default `reactorcide`) and sets `HOME` to
that account's home directory.

The build VM has no network device. A script reaches the network through the
transparent route of the guest daemon and the host egress forwarder, like a
job. Set `REACTORCIDE_VM_EGRESS_PROXY` on the build host to send this traffic
through an egress proxy.

The command does not copy the script file or adjacent files into the guest. A
script must download its input, use content that is already in the base image,
or mount content through a method that the script configures.

A provisioning script should:

- Start with `set -eu` or an equivalent error policy.
- Verify checksums for downloaded packages.
- Return a nonzero status when installation or validation fails.
- Avoid interactive commands.
- Avoid persistent credentials and private signing keys.
- Run correctly when the base image has the expected initial state.

For example, a configuration layer can select and initialize an existing
Xcode installation:

```sh
#!/bin/zsh
set -eu

sudo xcode-select --switch /Applications/Xcode.app/Contents/Developer
sudo xcodebuild -license accept
sudo xcodebuild -runFirstLaunch
xcodebuild -version
swift --version
```

Store provisioning scripts in source control with the image definition. Pin
the base image by digest and pin downloaded tools by version and checksum.

## Step 4: Build and publish a derived toolchain image

The command creates an APFS clone, boots it, installs the guest daemon from
`--guest-daemon` if you give it, runs each provisioning script in order, shuts
it down through the guest daemon, and seals the new bundle. This phase does
not need GUI interaction:

```sh
reactorcide vm-image build macos \
  --from registry.example.com/reactorcide/macos-26-bootstrap@sha256:DIGEST \
  --output /opt/reactorcide/images/macos-26-xcode \
  --guest-user reactorcide \
  --provision ./images/install-xcode.sh \
  --provision ./images/install-simulator-runtimes.sh \
  --provision ./images/validate-xcode.sh \
  --publish registry.example.com/reactorcide/macos-26-xcode:26.4
```

The build command prints provisioning output. If `--publish` is set, it prints
the immutable OCI digest after a successful upload.

Use `--from-local` when `--from` is a local bundle path:

```sh
reactorcide vm-image build macos \
  --from-local \
  --from /opt/reactorcide/images/macos-26-bootstrap \
  --output /opt/reactorcide/images/macos-26-swift \
  --guest-daemon ./vmguest \
  --provision ./images/install-tools.sh
```

Before the provision scripts, each build makes `/job` in the guest (a
synthetic link to `/private/var/reactorcide/job`, see
[macOS VM runners](./vm-runners-macos.md#prepare-the-guest)). Images from
before this step get `/job` at their next build.

The guest daemon runs as root, so the shutdown at the end needs no sudo
policy. A script that needs root must use `sudo` with a policy that the image
permits. If the guest does not stop, `--allow-unclean-stop` force-stops the VM
after `sync`. Use that flag only to recover a bad image. Do not use it for the
normal image pipeline.

Use `--guest-daemon PATH` with a darwin/arm64 build of `vmguest` to update the
guest daemon in the new image. The worker refuses a guest daemon with a
different protocol version, so update the daemon when the protocol version
changes.

## Step 5: Create a more specific image

Use any published derived image as the base for another build. For example,
add project tools and company configuration to the Xcode image:

```sh
reactorcide vm-image build macos \
  --from registry.example.com/reactorcide/macos-26-xcode@sha256:DIGEST \
  --output /opt/reactorcide/images/macos-26-xcode-company \
  --guest-user reactorcide \
  --provision ./images/install-project-tools.sh \
  --provision ./images/install-company-ca.sh \
  --provision ./images/configure-build-defaults.sh \
  --publish registry.example.com/company/macos-26-xcode:26.4-1
```

This process can create several image levels. Keep common and stable tools in
an early level. Keep project-specific tools in a later level. This structure
reduces repeated installation work and gives jobs a clear image choice.

## Step 6: Publish or pull an existing bundle

Publish an existing local bundle:

```sh
reactorcide vm-image publish \
  /opt/reactorcide/images/macos-26-swift \
  registry.example.com/reactorcide/macos-26-swift:26.4
```

The command prints the digest reference after a successful upload.

Pull an image into the local content-addressed cache:

```sh
reactorcide vm-image pull \
  --cache-dir /opt/reactorcide/cache/vm-images \
  registry.example.com/reactorcide/macos-26-swift@sha256:DIGEST
```

Add `--output DIR` to copy the materialized bundle to a new directory. The
worker does not need this option. It boots the materialized cache bundle
directly.

All `vm-image` commands that read or write an image also accept an `s3://`
reference. See [S3 image store](#s3-image-store).

Use `--plain-http REGISTRY_HOST` only for a development registry that does not
use TLS. Loopback registries also require this explicit option. The option has
no environment variable equivalent. Do not use plain HTTP for registry
credentials or production images.

## OCI artifact format

VM images are OCI 1.1 artifacts. They use this artifact media type:

- Artifact: `application/vnd.reactorcide.vm-image.v1`

A macOS image uses this layer media type:

- Layer: `application/vnd.reactorcide.vm-image.macos.bundle.v1.tar+zstd`

A Windows image uses this layer media type:

- Layer: `application/vnd.reactorcide.vm-image.windows.bundle.v2.tar+zstd`

Each layer is a tar archive with Zstandard compression. A macOS layer contains
the four macOS bundle files. A Windows layer contains only `disk.vhdx`. The
pull operation verifies the OCI digest before it extracts the layer. It rejects
extra files, links, paths, and missing files. It extracts the disk as a sparse
file and makes the completed bundle read-only.

A VM job names an image alias, or a pinned reference that an alias pins
(see [Image aliases](#image-aliases)). The digest makes the job image
immutable. Use a tag to publish a version or to select a base during image
management.

## S3 image store

An `s3://` reference has this form:

```text
s3://bucket/prefix/name:tag@sha256:DIGEST
```

The first path segment is the bucket. The text after the first `:` is the
tag. A tag can be any text, and a hash is a valid tag. A key prefix and a name
must not contain `:`. The text after the last `@` is the digest when it is a
valid `sha256` digest.

The store is an OCI image layout under `prefix/name/`:

```text
oci-layout
index.json
blobs/sha256/HEX
```

`index.json` records each tag in the `org.opencontainers.image.ref.name`
annotation. The blobs are the same bytes as in an OCI registry, so the
manifest digest is the same in the two stores. The local cache keeps an image
under its digest. An image that you pull from a registry and from S3 uses one
cache entry.

`vm-image publish DIR s3://bucket/prefix/name:tag` uploads the blobs, writes
`index.json`, and prints the pinned reference. It uses multipart upload for
large blobs. It checks the size and digest of each blob during the upload,
and it stops the upload when the check fails. Two publishes to the same
`index.json` at the same time can lose one tag. The blobs stay correct.

`vm-image pull` accepts a tag or a digest. The worker accepts only a pinned
`s3://` reference.

The S3 client uses the standard AWS credential chain (environment variables,
shared files, SSO, or an instance or task role). Reactorcide does not print
credential values. These settings select the service:

| Setting | Use |
| --- | --- |
| `--s3-endpoint` or `REACTORCIDE_VM_S3_ENDPOINT` | S3-compatible endpoint URL. The fallback is `REACTORCIDE_S3_ENDPOINT`. An endpoint uses path-style addresses. |
| `--s3-region` or `AWS_REGION` | Region. The default is `us-east-1`. |

## Image aliases

A VM job names its image by an image alias, or by a pinned reference that
an alias pins. The coordinator refuses any other macOS or Windows VM job
when it creates the job. See
[Job Definition Reference](./job-definitions.md#image-aliases) for the
resolution rules.

An alias pins one reference with a digest (`@sha256:<digest>`). The
reference can be `oci://host/repository:tag@sha256:<digest>`, a plain OCI
reference, or `s3://bucket/prefix/name:tag@sha256:<digest>`. A job that uses
an alias gets the characteristic `image_digest`, so that only a worker that
has the image claims the job.

Each image-aware VM worker gets an image set from the coordinator. The set
has the images of the aliases that the worker can serve. The worker pulls
the set before it claims a job, and it sends the ready digests in
`image_digest` itself. Do not set `REACTORCIDE_WORKER_CUSTOM_IMAGE_DIGEST`.
The worker adds that value to its ready digests, so it claims the jobs of
that digest also when it does not have the image. The `local` image source
cannot resolve the pinned reference of a coordinator VM job, so a worker
with the `local` source cannot run such a job, with or without that value.
A VM worker that gets jobs from a coordinator needs the `oci` (or `s3`)
source.

To manage aliases, give an alias a new pin, and see the image state of the
workers, see [Image Aliases and Worker Image Sets](./image-aliases.md). For
the worker side, see
[Worker Operation: Image sets and pre-seed](./workers.md#image-sets-and-pre-seed).

## Worker prefetch and retention

Set the worker image source to OCI. The `oci` source (or its other name
`s3`) accepts registry references and pinned `s3://` references:

```sh
export REACTORCIDE_VM_IMAGE_SOURCE=oci
export REACTORCIDE_VM_IMAGE_CACHE_DIR=/opt/reactorcide/cache/vm-images
export REACTORCIDE_VM_REGISTRY_AUTH_FILE=/opt/reactorcide/worker/oci-auth.json
```

The worker pulls the images of its image set automatically, before it
claims a job, and keeps them while the set names them (see
[Image Aliases and Worker Image Sets](./image-aliases.md)). Retention never
removes an image of the set or of a running job.

A prefetch flag is optional. Add one flag for each more image that the
worker must pull before it registers, for example an image that no alias
pins yet:

```sh
reactorcide worker \
  --vm-image-prefetch registry.example.com/reactorcide/macos-26-swift@sha256:DIGEST \
  --vm-image-prefetch registry.example.com/reactorcide/macos-26-xcode@sha256:DIGEST \
  --vm-image-max-unused 720h \
  --vm-image-prune-interval 24h \
  OTHER_WORKER_FLAGS
```

The default maximum unused time is 30 days (`--vm-image-max-unused`). The
worker runs retention once at startup and then at each
`--vm-image-prune-interval` (default 24 hours; `0` stops the periodic
check). Retention keeps the images of the current image set, of the
prefetch list, and of running jobs. Each successful job resolve or prefetch
updates the image access time. Removal deletes the materialized bundle,
manifest, and unshared OCI blobs. A running job uses its own APFS clone, so
cache removal does not change that job.

Run retention directly with this command:

```sh
reactorcide vm-image cache prune \
  --cache-dir /opt/reactorcide/cache/vm-images \
  --max-unused 720h
```

## Local VM metrics

The VM runner writes one JSON object per line to:

```text
~/.local/state/reactorcide/vm-metrics/JOB_ID.jsonl
```

Set `REACTORCIDE_VM_METRICS_DIR` to change the directory. Set
`REACTORCIDE_VM_METRICS_INTERVAL` to change the five-second interval for
the optional local JSON Lines debug output.
Each sample contains:

- UTC timestamp
- Job ID
- Total guest CPU percentage
- One-minute guest load
- Used and total guest memory bytes
- Used and total guest root-storage bytes

The sampler uses a separate guest daemon connection. It does not add output
to the job log, and it does not read the job environment. The job log continues to use
the existing stdout and stderr path to the coordinator.

The metrics files stay local after VM cleanup. A later coordinator protocol
can upload these files as a separate metrics artifact.

## Hardware validation

The macOS image build passed on Apple silicon hardware on 2026-11-17:
`install-vmguest.sh`, the launchd start of the guest daemon,
`vm-image build macos` with `--guest-daemon` and `--provision`, and egress
through the tunnel. The Windows derived build (`vm-image build windows`)
passed on 2026-11-18. See
[macOS VM Runners: Hardware validation status](./vm-runners-macos.md#hardware-validation-status)
and
[Windows VM Runners: Hardware validation status](./vm-runners-windows.md#hardware-validation-status).
