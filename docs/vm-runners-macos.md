# macOS VM Runners (Apple Silicon)

The `vm` JobRunner backend runs native **macOS** jobs inside ephemeral, per-job
guest VMs on an Apple Silicon host, using Apple's Virtualization.framework
through [`github.com/Code-Hex/vz/v3`](https://github.com/Code-Hex/vz). This is
phase VM-3 of [`VM_RUNNERS_PLAN.md`](../VM_RUNNERS_PLAN.md). It is the isolation
boundary for macOS build/test/notarize jobs that cannot run in a Linux
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
# a job's image "macos-14-base" then resolves to
#   /opt/reactorcide/vm-images/macos-14-base/  (the bundle dir)
```

An absolute image reference is used as-is (`REACTORCIDE_VM_IMAGE_DIR` is
ignored for that job). `LocalImageSource` (VM-1) accepts either a bundle
directory or a single file; VM-2 will add an OCI-backed source with the same
interface.

## Producing the base bundle (golden image)

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

### Recommended tooling: Packer + Tart (instead of hand-rolling)

Reactorcide ships **no** base-image build tooling — the `reactorcide` binary
only *boots* an existing bundle (and `cmd/vmsmoke` is just a smoke test). Rather
than script `VZMacOSInstaller` yourself, the well-trodden path for reproducible
Apple-Silicon macOS images is **Tart** (Cirrus Labs; built on the same
Virtualization.framework) driven by **Packer** via `packer-plugin-tart`:

- `brew install cirruslabs/cli/tart`, then a Packer template (`tart-cli` builder)
  installs macOS from an IPSW and provisions the guest (enable Remote Login, bake
  toolchain + the worker's SSH public key) — no manual click-through.
- Tart distributes images through **any OCI registry** (push/pull like Docker),
  which lines up with our OCI `ImageSource` (VM-2) and your
  `containers.catalystsquad.com` plan.

Caveat: Tart stores a VM as its **own** package layout (`disk.img` + `config.json`
+ `nvram.bin`), not our four-file bundle (`disk.img`/`aux.img`/`hardwaremodel.bin`/
`machineidentifier.bin`). So adopting Tart means either (a) a small conversion
step producing our bundle, or (b) teaching the runner to read Tart's layout /
consuming Tart's OCI images directly. That build-vs-adopt decision belongs to
**VM-5 (image build + packaging)** — flagged, not decided. Given Tart overlaps
heavily with what VM-3 already does (vz lifecycle + OCI images), VM-5 should
weigh consuming Tart images/format vs. our own bundle before investing in a
custom installer.

### Prepare the guest (inside the VM)

Do this in the running guest before capturing it as the golden bundle:

1. **Enable Remote Login (sshd):**

   ```sh
   sudo systemsetup -setremotelogin on
   ```

   (Or System Settings → General → Sharing → Remote Login.)

2. **Create the worker's login account** (e.g. `runner`) if you didn't during
   Setup Assistant, and make sure it can run the jobs' toolchains.

3. **Bake the toolchain** the jobs need (Xcode / command line tools, language
   runtimes, `runnerlib` prerequisites, etc.). Everything a job uses must be
   present in the guest — there is no nested container inside the guest (see
   the "Guest execution note" in `VM_RUNNERS_PLAN.md`).

4. **Install the worker's SSH public key** (see the credentials section next).

5. Shut the guest down cleanly. The `disk.img` and `aux.img` you captured now
   contain the prepared state; keep the bundle directory read-only in
   production and let each job clone it.

## Guest credentials (prototype)

For the prototype, the guest authenticates the worker with a **baked-in SSH
key pair**:

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
  | `REACTORCIDE_VM_SSH_USER`            | guest account the worker logs in as            | `runner`|
  | `REACTORCIDE_VM_SSH_PRIVATE_KEY_FILE`| path to the worker's SSH private key (PEM)      | —       |
  | `REACTORCIDE_VM_SSH_PASSWORD`        | password auth (discouraged; key preferred)     | —       |

  These are read by `worker.LoadVMConfig` and wired into the `GuestCreds` the
  VMRunner passes to the SSH transport. They apply to both the coordinator-
  mediated worker and `reactorcide run-local --backend vm`.

Example:

```sh
export REACTORCIDE_VM_IMAGE_DIR=/opt/reactorcide/vm-images
export REACTORCIDE_VM_SSH_USER=runner
export REACTORCIDE_VM_SSH_PRIVATE_KEY_FILE="$HOME/.ssh/reactorcide_vm"
```

> **Never** log or print the private key or any guest credential. The worker
> reads the key from a file precisely so its contents stay out of the
> environment and out of logs.

### Security notes / future hardening

- The SSH transport does **not** verify the guest host key: guests are cloned
  fresh per job and have no stable identity to pin, and the host↔guest link
  runs over the VM's private NAT network the lifecycle controls end to end.
- Baking a **shared** worker key into the golden image is a prototype
  simplification: every job clone trusts the same key. **Per-job injected
  keys** (a unique key pair minted per boot, delivered over a first-boot
  channel so no long-lived secret lives in the image) are the intended
  hardening and are out of scope for VM-3 — they need a first-boot delivery
  mechanism (cloud-init-style config, a vsock channel, or a mounted seed
  volume) that does not yet exist here.

## Smoke test (validate vz + networking + SSH)

Before wiring the full worker, validate the whole stack — boot, IP discovery,
SSH, teardown — with the standalone `vmsmoke` command:

```sh
cd coordinator_api
CGO_ENABLED=1 go build -tags vz -o vmsmoke ./cmd/vmsmoke
codesign --force --entitlements ../deployment/macos/vz.entitlements -s - ./vmsmoke

./vmsmoke \
  -bundle /opt/reactorcide/vm-images/macos-14-base \
  -user runner \
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

A `{os: macos}` job should only be scheduled onto a macOS worker (see
`VM_RUNNERS_PLAN.md`).
