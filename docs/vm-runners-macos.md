# macOS VM Runners (Apple Silicon)

The `vm` JobRunner backend runs native **macOS** jobs inside ephemeral, per-job
guest VMs on an Apple Silicon host, using Apple's Virtualization.framework
through [`github.com/Code-Hex/vz/v3`](https://github.com/Code-Hex/vz). It is the
isolation boundary for macOS build/test/notarize jobs that cannot run in a Linux
container.

Terminology: the process that boots and drives guests is the **worker**.

## Requirements

- **Apple Silicon Mac** (arm64). macOS guests are only virtualizable on Apple
  Silicon.
- macOS 13+ on the host is recommended (the vz APIs used require macOS 12+).
- The worker binary must be **built with Cgo and the `vz` build tag** and
  **code-signed with the `com.apple.security.virtualization` entitlement**.

The default release build cross-compiles darwin with `CGO_ENABLED=0` and so
selects a stub backend that returns a clear "rebuild with `-tags vz`" error.
You must build the worker locally on the Mac to get the real backend.

## Build + sign the worker

```sh
scripts/build-macos-worker.sh
# -> coordinator_api/reactorcide, signed with deployment/macos/vz.entitlements
```

That script runs, in `coordinator_api/`:

```sh
CGO_ENABLED=1 go build -tags vz -o reactorcide ./
codesign --force --entitlements ../deployment/macos/vz.entitlements -s - ./reactorcide
```

Ad-hoc signing (`-s -`) is sufficient for local/dev use on the same machine.
Without the entitlement, `VZVirtualMachine` creation fails at runtime.

## The base image: a "bundle" directory

Unlike the Linux container runners (which take an image reference) and the
Windows lifecycle (a single VHDX file), the macOS lifecycle expects the base
image to be a **bundle directory** containing four artifacts produced when
macOS is installed into a VM:

| File                     | What it is                                          | Per-job handling            |
| ------------------------ | --------------------------------------------------- | --------------------------- |
| `disk.img`               | the guest's main disk image                         | cloned copy-on-write, R/W   |
| `aux.img`                | auxiliary storage (NVRAM/EFI-equivalent state)      | cloned copy-on-write, R/W   |
| `hardwaremodel.bin`      | serialized `VZMacHardwareModel`                     | read-only, shared from base |
| `machineidentifier.bin`  | serialized `VZMacMachineIdentifier`                 | read-only, shared from base |

These filenames are fixed (see the `bundle*` constants in
`coordinator_api/internal/worker/vmrunner/lifecycle_darwin_vz.go`).

On each `Boot`, the lifecycle:

1. creates a per-job scratch directory,
2. `clonefile(2)`-clones `disk.img` and `aux.img` into it (APFS copy-on-write;
   falls back to a full copy, logged, if the clone fails — e.g. a non-APFS
   volume or a cross-filesystem scratch dir),
3. builds the VM from the cloned aux + the base hardware model / machine
   identifier, sized from the job's CPU/memory,
4. attaches the cloned disk read-write and a NAT network device with a fresh
   random locally-administered MAC,
5. starts the VM, waits for the `Running` state, then discovers the guest's IP
   by matching that MAC in `/var/db/dhcpd_leases` (polling, since DHCP takes a
   few seconds),
6. returns once the guest is booted; the worker then waits for guest SSH
   (`:22`) to accept connections before running the job command.

`Destroy` requests a stop, waits briefly for it to settle, and removes the
per-job scratch directory. The base bundle is never modified.

### Where to place the bundle

Point the worker at the directory that holds your bundle(s) and reference a
bundle by name:

```sh
export REACTORCIDE_VM_IMAGE_DIR=/opt/reactorcide/vm-images
export REACTORCIDE_VM_SCRATCH_DIR=/opt/reactorcide/vm-jobs
# a job's image "macos-14-base" then resolves to
#   /opt/reactorcide/vm-images/macos-14-base/  (the bundle dir)
```

When `REACTORCIDE_VM_IMAGE_DIR` is set, an image reference must be a relative
path inside that directory. Absolute paths and paths that escape the image
directory are rejected. `LocalImageSource` accepts a bundle directory or a
single file. The lifecycle selects the required format.

Set `REACTORCIDE_VM_SCRATCH_DIR` to a directory on the same APFS volume as the
base images. This lets the worker use APFS copy-on-write clones. If the two
directories are on different file systems, the worker copies the full disk
image for each job.

## Producing the base bundle (golden image)

For current image build, publish, pull, cache, and retention commands, see
[`vm-images.md`](vm-images.md).

You install macOS into a VM once, capture the four artifacts, then reuse that
bundle as the read-only base for every job.

1. **Download a restore image (IPSW)** for the macOS version you want to bake.
   Apple's `VZMacOSRestoreImage.latestSupported` / restore-image APIs can fetch
   the latest supported image; you can also download an IPSW manually.
2. **Install macOS into a VM** with `VZMacOSInstaller`. The Code-Hex/vz repo
   ships a complete, working example of this flow — see its
   [`example/macOS`](https://github.com/Code-Hex/vz/tree/main/example/macOS)
   installer (`install.go` / `main.go`). That example:
   - creates a new `disk.img` (e.g. 64 GiB, sparse),
   - derives the hardware model and a fresh machine identifier from the restore
     image's `MostFeaturefulSupportedConfiguration`,
   - creates the auxiliary storage with
     `vz.NewMacAuxiliaryStorage(path, vz.WithCreatingMacAuxiliaryStorage(hwModel))`,
   - runs `vz.NewMacOSInstaller(vm, ipswPath)` and waits for it to finish.

   Reactorcide does **not** ship its own installer for VM-3; reuse that example
   to create the bundle. Save the serialized hardware model and machine
   identifier bytes to `hardwaremodel.bin` and `machineidentifier.bin`
   respectively (both types expose `DataRepresentation()`), and place the
   resulting `disk.img` and `aux.img` alongside them so the four files match
   the bundle layout above.
3. **Boot the installed guest interactively once** to complete Setup Assistant
   (create the account the worker will SSH in as), then prepare it as below.

### Prototype image build path

The prototype does not use Packer or Tart. It uses the MIT-licensed
[Code-Hex/vz macOS example](https://github.com/Code-Hex/vz/tree/main/example/macOS)
to install an Apple IPSW. It then uses SSH to install tools after Setup
Assistant enables an account and Remote Login.

The first prototype used a person to complete Setup Assistant for one
bootstrap image. Derived images now use `reactorcide vm-image build macos` and
do not use Setup Assistant.

Apple announced a Virtualization.framework guest-provisioning API in 2026. It
can create the first user and enable Remote Login without Setup Assistant. The
SDK on the prototype host does not contain this API. Reactorcide will use it
for bootstrap image creation when it is present in the supported host SDK.
Until then, maintainers create one bootstrap image for each supported macOS
release and publish it. Users build their images from that bootstrap through
the Reactorcide CLI.

### Prepare the guest (inside the VM)

Do this in the running guest before capturing it as the golden bundle:

1. **Enable Remote Login (sshd):**

   ```sh
   sudo systemsetup -setremotelogin on
   ```

   (Or System Settings → General → Sharing → Remote Login.)

2. **Create the worker's `reactorcide` login account** if it was not created
   during Setup Assistant. Make sure that it can run the job toolchains.

3. **Bake the toolchain** the jobs need (Xcode / command line tools, language
   runtimes, `runnerlib` prerequisites, etc.). Everything a job uses must be
   present in the guest — there is no nested container inside the guest.

4. **Install the worker's SSH public key** (see the credentials section next).

5. Shut the guest down cleanly. The `disk.img` and `aux.img` you captured now
   contain the prepared state; keep the bundle directory read-only in
   production and let each job clone it.

## Guest credentials (prototype)

For the prototype, the guest authenticates the worker with a public key in
the image and a private key on the worker host:

- Generate a dedicated key pair for the worker (do this on the host, keep the
  private key on the host only):

  ```sh
  ssh-keygen -t ed25519 -f ~/.ssh/reactorcide_vm -N '' -C 'reactorcide-vm-worker'
  ```

- **Bake the public key into the golden image**: append
  `~/.ssh/reactorcide_vm.pub`'s contents to the guest account's
  `~/.ssh/authorized_keys` (mode `600`, `~/.ssh` mode `700`).

- **Point the worker at the matching private key** via environment variables
  (the worker reads the key from a *file* so the key material never passes
  through an env var value or gets printed):

  | Env var                              | Meaning                                        | Default |
  | ------------------------------------ | ---------------------------------------------- | ------- |
  | `REACTORCIDE_VM_IMAGE_DIR`           | directory relative image refs resolve under    | `.`     |
  | `REACTORCIDE_VM_IMAGE_SOURCE`        | `local` or `oci` image source                   | `local` |
  | `REACTORCIDE_VM_IMAGE_CACHE_DIR`     | OCI content and materialized bundle cache       | `~/.cache/reactorcide/vm-images` |
  | `REACTORCIDE_VM_REGISTRY_AUTH_FILE`  | Docker-compatible multi-registry credential file | `~/.config/reactorcide/oci-auth.json` |
  | `REACTORCIDE_VM_SCRATCH_DIR`         | directory for ephemeral per-job VM clones      | OS temporary directory |
  | `REACTORCIDE_VM_SSH_USER`            | guest account the worker logs in as            | `reactorcide`|
  | `REACTORCIDE_VM_SSH_PRIVATE_KEY_FILE`| path to the worker's SSH private key (PEM)      | —       |
  | `REACTORCIDE_VM_SSH_PASSWORD`        | password auth (discouraged; key preferred)     | —       |
  | `REACTORCIDE_VM_METRICS_DIR`         | local JSON Lines metrics directory              | `~/.local/state/reactorcide/vm-metrics` |
  | `REACTORCIDE_VM_METRICS_INTERVAL`    | guest metrics sample interval                   | `5s`    |

  These are read by `worker.LoadVMConfig` and wired into the `GuestCreds` the
  VMRunner passes to the SSH transport. They apply to both the coordinator-
  mediated worker and `reactorcide run-local --backend vm`.

Example:

```sh
export REACTORCIDE_VM_IMAGE_DIR=/opt/reactorcide/vm-images
export REACTORCIDE_VM_SSH_USER=reactorcide
export REACTORCIDE_VM_SSH_PRIVATE_KEY_FILE="$HOME/.ssh/reactorcide_vm"
```

> **Never** log or print the private key or any guest credential. The worker
> reads the key from a file precisely so its contents stay out of the
> environment and out of logs.

### Security notes / future hardening

- The SSH transport does **not** verify the guest host key: guests are cloned
  fresh per job and have no stable identity to pin, and the host↔guest link
  runs over the VM's private NAT network the lifecycle controls end to end.
- The private SSH key stays on the worker host. The image builder does not
  copy it into the image.
- The public key stays in the guest's `authorized_keys` file. The guest
  password state also stays in the cloned disk. The build process does not
  remove or rotate these items automatically.
- A public key is not confidential, but its `authorized_keys` entry permits
  access to a person who has the matching private key.
- Before handoff, replace the build key with the recipient's public key.
  Change or disable the guest password, disable SSH password login, and remove
  all build credentials and private signing material. The recipient must not
  give its private key to the image publisher.
- One image for many independent operators needs a boot-time channel that
  injects a unique key. The current prototype does not have that channel. Do
  not publish a runtime image with a shared access key outside its intended
  trust domain.
- See [VM Image Operations](./vm-images.md#bootstrap-credential-state) for the
  complete image-handoff checklist.

## Smoke test (validate vz + networking + SSH)

Before wiring the full worker, validate the whole stack — boot, IP discovery,
SSH, teardown — with the standalone `vmsmoke` command:

```sh
cd coordinator_api
CGO_ENABLED=1 go build -tags vz -o vmsmoke ./cmd/vmsmoke
codesign --force --entitlements ../deployment/macos/vz.entitlements -s - ./vmsmoke

./vmsmoke \
  -bundle /opt/reactorcide/vm-images/macos-14-base \
  -user reactorcide \
  -key "$HOME/.ssh/reactorcide_vm"
```

It boots a guest from the bundle, logs the guest IP (as `guest_ip`) once DHCP
assigns it, runs `echo hello` in the guest over the SSH transport, prints the
output, then destroys the guest. `vmsmoke: OK` means vz, NAT networking, and
guest SSH all work.

## Running jobs

Once the bundle, key, and env are in place:

```sh
# Local:
reactorcide run-local --backend vm ./jobs/my-macos-job.yaml

# Worker (coordinator-mediated) selects the same backend via --container-runtime vm.
```

A `{os: macos}` job should only be scheduled onto a macOS worker.

## Worker service and privacy approval

The prototype runs the worker as a user LaunchAgent. It does not run as a root
LaunchDaemon. A root LaunchDaemon could not get the macOS host key that
Virtualization.framework needs. The operation failed with
`errSecInteractionNotAllowed`. The LaunchAgent works because it runs in a
logged-in user security session. In this prototype, startup means startup of
that user session, not startup before login.

### Prompts seen during prototype installation

macOS 26 displayed two approval prompts when the LaunchAgent first ran:

- Allow `reactorcide` to access storage.
- Allow `reactorcide` to find services on the local network.

The user had to select **Allow** before the worker could complete its first
job. A supported installer must account for both approvals. POSIX ownership and
file modes do not replace macOS privacy approval.

The prototype binary has an ad-hoc signature. It has no bound `Info.plist` and
no Apple team identifier. Its generated code identifier can change after a
rebuild. This can make macOS treat a new build as a new program.

### Local Network access

The worker connects to a coordinator on the local network. Apple states that a
LaunchAgent is subject to Local Network privacy controls. The first connection
can show a user prompt. A root process and a LaunchDaemon are normally exempt,
but the Virtualization.framework host-key constraint prevents use of that model
for this worker. Apple also states that MDM cannot set Local Network privacy
approval. See [TN3179: Understanding local network privacy](https://developer.apple.com/documentation/technotes/tn3179-understanding-local-network-privacy).

For the supported package:

1. Put the worker in an application bundle with an `Info.plist`.
2. Set `NSLocalNetworkUsageDescription` to explain the coordinator connection.
3. Sign the package and its helper with a stable Apple-issued identity.
4. Keep the worker alive while approval is pending, and show a clear health
   state to the installer.

These changes give the prompt a stable identity and useful text. They do not
remove the required user choice on an unmanaged Mac.

For a dedicated CI Mac, Apple documents a system setting that exempts selected
Ethernet or Wi-Fi CIDR ranges from Local Network privacy checks. This setting
needs root access and a restart, and it applies to all programs on the Mac. An
installer can offer this as an explicit administrator option for the
coordinator subnet. It must not enable a broad range by default.

### Storage access

The prototype stores images and job scratch data on `/opt/cispace`, which is a
separate mounted APFS volume. The exact privacy service for the observed storage
prompt was not recorded. On the next clean installation, record the full prompt
text and the matching entry in **System Settings > Privacy & Security**. It can
be Files & Folders, Full Disk Access, or mounted-volume access. Do not assume
that Full Disk Access is required until this test identifies the service.

Use a stable signed application identity before this test. For managed Macs,
test an MDM Privacy Preferences Policy Control profile for the identified
storage service. Apple supports policy control for several file-access classes,
including network volumes. See [Privacy Preferences Policy Control payload settings](https://support.apple.com/guide/deployment/dep38df53c2a/web).

For unmanaged Macs, the installer must open the correct Privacy & Security pane
and wait for the user to approve access if macOS requires it. The installer
must then run a read, write, clone, and delete test in the image and scratch
directories before it starts the worker.
