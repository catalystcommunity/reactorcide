# VM Image Operations

Reactorcide can build, publish, pull, cache, and remove VM images through the
`reactorcide vm-image` command. macOS and Windows workers use the same pull and
cache code when they start a job.

Windows bundles contain only `disk.vhdx`. The Hyper-V worker injects new SSH
client and host keys into each writable clone before boot. See
[Windows VM Image Build](./windows-vm-image-build.md) for unattended creation,
publishing, and fleet cache configuration.

## Image creation workflow

Use these image levels:

1. Create one bootstrap image for a macOS release.
2. Create a toolchain image from the bootstrap image.
3. Create more specific images from the toolchain image.
4. Publish each reusable image to an OCI registry.
5. Use an immutable digest in a worker or job configuration.

For example:

```text
macos-26-bootstrap
  -> macos-26-xcode
    -> macos-26-xcode-ios
      -> macos-26-xcode-ios-company
```

The bootstrap image contains macOS and the access configuration that the
image builder needs. A derived image can contain Xcode, simulator runtimes,
other toolchains, certificates, system configuration, or project tools.

Do not put private signing keys, registry tokens, or other long-lived secrets
in an image. Supply job secrets through the normal Reactorcide secret path.

## Before you start

Install the Reactorcide CLI on an Apple silicon Mac that can use
Virtualization.framework. Prepare these items:

- A local bootstrap bundle or an OCI reference to one.
- The private SSH key or password for the bootstrap guest account.
- One or more provisioning scripts for a derived image.
- An OCI repository with artifact support.
- Registry credentials, if the repository is not public.

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
- Remote Login on port 22.
- The provisioning SSH public key in the account.
- A writable home directory.
- Permission to run `sudo /sbin/shutdown -h now` without a password.

Keep the SSH private key on the image-builder host. Do not put the private key
in the VM image.

On the SDK used for the current prototype, the first bootstrap still needs
Setup Assistant. Complete Setup Assistant, configure the account and Remote
Login, and shut down the VM. This is a one-time task for the image publisher.
It is not part of a derived image build or a job run.

Publish the stopped local bootstrap bundle:

```sh
reactorcide vm-image publish \
  /opt/reactorcide/images/macos-26-bootstrap \
  registry.example.com/reactorcide/macos-26-bootstrap:26
```

The command prints a digest reference. Record that reference. Use it as the
base of derived image builds.

### Bootstrap credential state

The image builder reads the SSH private key from the host. It does not copy
that private key into the image. The OCI artifact does not contain the
registry credential file.

The current image builder does not remove the guest's `authorized_keys` file
or change its password. These items are part of the cloned disk:

- The public key that permits image-builder and worker access.
- The guest account password hash, if the account has a password.
- Any other account, key, token, history, or credential that was in the base
  image or that a provisioning script added.

A public key is not a secret, but an `authorized_keys` entry is an access
control rule. A person who has the matching private key can log in to a
running clone when the network permits that connection.

Before an image publisher gives an image to another operator, the publisher
must prepare it for that recipient:

1. Use a dedicated image-build key. Do not use a personal SSH key.
2. Replace the build key in `authorized_keys` with the recipient's public key
   in the final provisioning step.
3. Change or disable the bootstrap password and disable SSH password login.
4. Remove shell history, download credentials, package-manager credentials,
   temporary files, private signing keys, and other build secrets.
5. Run a final validation script before publication.

The recipient supplies only a public key. The recipient must not give the
publisher its private key. After key replacement, later image builds must use
the recipient's matching private key.

The current shared-key design is not suitable for one public image that many
independent operators can run safely. That use case needs a boot-time channel
that injects a unique worker or job key. Until Reactorcide has that channel,
publish a credential-bearing runtime image only to its intended trust domain.

Apple announced `VZMacGuestProvisioningOptions` and
`VZMacOSVirtualMachineStartOptions` in 2026. These APIs create the first user
and can enable automatic login and Remote Login on the first boot. The macOS
26.5 and Xcode 26.6 SDK on the prototype host does not include these APIs.
When the supported host SDK includes them, the Reactorcide bootstrap command
should use them and remove the Setup Assistant task.

## Step 3: Write provisioning scripts

Each `--provision` argument names a local text file. Reactorcide reads the
file and runs its contents in the guest with `/bin/zsh -c`. It runs scripts in
the order given on the command line. It runs them as the selected SSH account
and sets `HOME` to that account's home directory.

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

The command creates an APFS clone, boots it, runs each provisioning script in
order, shuts it down, and seals the new bundle. This phase does not need GUI
interaction:

```sh
reactorcide vm-image build macos \
  --from registry.example.com/reactorcide/macos-26-bootstrap@sha256:DIGEST \
  --output /opt/reactorcide/images/macos-26-xcode \
  --ssh-user reactorcide \
  --ssh-private-key-file /path/to/bootstrap-key \
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
  --ssh-user reactorcide \
  --ssh-private-key-file /path/to/bootstrap-key \
  --provision ./images/install-tools.sh
```

The bootstrap account should be able to run this exact command without a
password:

```sh
sudo /sbin/shutdown -h now
```

Limit its sudo policy to that command if provisioning does not need other root
operations. If clean shutdown is not available, `--allow-unclean-stop` runs
`sync` and force-stops the VM. Use that flag only to recover an old bootstrap
image. Do not use it for the normal image pipeline.

Use `--ssh-password-file` only when an old bootstrap needs a password for SSH
or shutdown. The command reads the password from the file and does not print
it. Prefer key authentication and the limited password-free shutdown policy.

## Step 5: Create a more specific image

Use any published derived image as the base for another build. For example,
add project tools and company configuration to the Xcode image:

```sh
reactorcide vm-image build macos \
  --from registry.example.com/reactorcide/macos-26-xcode@sha256:DIGEST \
  --output /opt/reactorcide/images/macos-26-xcode-company \
  --ssh-user reactorcide \
  --ssh-private-key-file /path/to/bootstrap-key \
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

Use a digest reference in production job files. A digest makes the job image
immutable. Use a tag to publish a version or to select a base during image
management.

## Worker prefetch and retention

Set the worker image source to OCI:

```sh
export REACTORCIDE_VM_IMAGE_SOURCE=oci
export REACTORCIDE_VM_IMAGE_CACHE_DIR=/opt/reactorcide/cache/vm-images
export REACTORCIDE_VM_REGISTRY_AUTH_FILE=/opt/reactorcide/worker/oci-auth.json
```

Add one flag for each image that the worker must pull before it registers:

```sh
reactorcide worker \
  --vm-image-prefetch registry.example.com/reactorcide/macos-26-swift@sha256:DIGEST \
  --vm-image-prefetch registry.example.com/reactorcide/macos-26-xcode@sha256:DIGEST \
  --vm-image-max-unused 720h \
  --vm-image-prune-interval 24h \
  OTHER_WORKER_FLAGS
```

The default maximum unused time is 30 days. The worker runs retention once at
startup and once every 24 hours. Each successful job resolve or prefetch
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
Set `REACTORCIDE_VM_METRICS_INTERVAL` to change the five-second interval for
the optional local JSON Lines debug output.
Each sample contains:

- UTC timestamp
- Job ID
- Total guest CPU percentage
- One-minute guest load
- Used and total guest memory bytes
- Used and total guest root-storage bytes

The sampler uses a separate SSH session. It does not add output to the job
log, and it does not read the job environment. The job log continues to use
the existing stdout and stderr path to the coordinator.

The metrics files stay local after VM cleanup. A later coordinator protocol
can upload these files as a separate metrics artifact.
