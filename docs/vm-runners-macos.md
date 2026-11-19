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
4. attaches the cloned disk read-write and a virtio socket (vsock) device. It
   does not attach a network device,
5. starts the VM and waits for the `Running` state,
6. starts the host egress listener on vsock port 7101 for this VM,
7. returns once the guest is booted. The worker then sends a hello to the
   guest daemon on vsock port 7100 until the daemon answers. Then it runs the
   job command.

`Destroy` closes the egress listener, requests a stop, waits briefly for it to
settle, and removes the per-job scratch directory. The base bundle is never
modified.

Job disk clones are ephemeral. Only base images stay on the host. The worker
holds an exclusive `flock` on `.owner.lock` in each scratch directory while the
job runs. macOS releases the lock when the worker process stops, and the VM
stops with that process. When the worker starts, and before each boot, it
removes each `reactorcide-vm-*` directory in the scratch root that it can
lock. Thus a worker crash or a failed delete does not leave disk clones on
the host. A directory without a lock file is removed after 10 minutes.

## Network and egress (no network device)

The guest has no network device (decision D29). A job cannot open a network
connection that goes around the host. All traffic between the worker and the
guest uses the vsock device:

| vsock port | Direction | Use |
| --- | --- | --- |
| 7100 | worker to guest | Job channel: files, trees, command, output, exit status |
| 7101 | guest to worker | Egress tunnel: one HTTP `CONNECT host:port` for each guest TCP flow |

The guest daemon `vmguest` (`coordinator_api/cmd/vmguest`) runs as a root
launchd daemon. At start, it sets a transparent route:

1. It adds the routes `0.0.0.0/1` and `128.0.0.0/1` to `lo0`.
2. It adds a pf rule in the anchor `com.apple/250.reactorcide`. The rule sends
   each TCP connection that does not go to `127.0.0.0/8` to
   `127.0.0.1:15001`.
3. It starts a fake DNS server on `127.0.0.1:53`. It sets this server as the
   resolver for all domains (`scutil`, service `reactorcide`). The fake DNS
   gives each name an address in `198.18.0.0/15` and remembers the name. No
   DNS query leaves the guest.
4. For each redirected connection, it finds the original destination in the
   pf state. It changes a fake address back to its name. It opens
   `CONNECT name:port` on vsock port 7101.

The daemon also has a local HTTP proxy on `127.0.0.1:3128`. If the
transparent route fails, the daemon gives each command `HTTP_PROXY`,
`HTTPS_PROXY`, and `ALL_PROXY` for that proxy, and writes a warning to its
log.

On the host, the egress forwarder reads each `CONNECT` request and opens the
destination:

- If `REACTORCIDE_VM_EGRESS_PROXY` is not set, the forwarder connects to the
  destination directly. This keeps the open egress of the old NAT network.
  The forwarder refuses loopback, unspecified, link-local, and multicast
  addresses. This prevents access to services on the host loopback and to
  cloud metadata addresses.
- If `REACTORCIDE_VM_EGRESS_PROXY` is set to `host:port`, the forwarder sends
  each flow to that HTTP CONNECT proxy. The proxy refusal status goes back to
  the guest. The forwarder never sends a `Proxy-Authorization` header from
  the guest. The egress proxy of the secrets work (WP18) connects here through
  the `EgressDialer` interface in
  `coordinator_api/internal/worker/vmrunner/egress_forwarder.go`.

The policy point is the host. A job that gets root in the guest can change the
guest routes and pf rules. It still has no network device, so its traffic can
only go to the forwarder.

### Where to place the bundle

Point the worker at the directory that holds your bundle(s) and reference a
bundle by name:

```sh
export REACTORCIDE_VM_IMAGE_DIR=/opt/reactorcide/vm-images
export REACTORCIDE_VM_SCRATCH_DIR=/opt/reactorcide/vm-jobs
# a job's image "macos-14-base" then resolves to
#   /opt/reactorcide/vm-images/macos-14-base/  (the bundle dir)
```

With the `local` image source, an image reference must be a relative path
inside `REACTORCIDE_VM_IMAGE_DIR` (default: the current directory). Absolute
paths and paths that escape the image directory are rejected. `LocalImageSource` accepts a bundle directory or a
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

   Reactorcide does not ship a separate macOS installer. Reuse that example
   to create the bundle. Save the serialized hardware model and machine
   identifier bytes to `hardwaremodel.bin` and `machineidentifier.bin`
   respectively (both types expose `DataRepresentation()`), and place the
   resulting `disk.img` and `aux.img` alongside them so the four files match
   the bundle layout above.
3. **Boot the installed guest interactively once** to complete Setup Assistant
   (create the account that runs the jobs), then prepare it as below.

### Prototype image build path

The prototype does not use Packer or Tart. It uses the MIT-licensed
[Code-Hex/vz macOS example](https://github.com/Code-Hex/vz/tree/main/example/macOS)
to install an Apple IPSW. After Setup Assistant creates an account, the
publisher installs the guest daemon. Derived images then install tools through
the guest daemon.

The first prototype used a person to complete Setup Assistant for one
bootstrap image. Derived images now use `reactorcide vm-image build macos` and
do not use Setup Assistant.

Apple announced a Virtualization.framework guest-provisioning API in 2026. It
can create the first user without Setup Assistant. The
SDK on the prototype host does not contain this API. Reactorcide will use it
for bootstrap image creation when it is present in the supported host SDK.
Until then, maintainers create one bootstrap image for each supported macOS
release and publish it. Users build their images from that bootstrap through
the Reactorcide CLI.

### Prepare the guest

Do these steps before you capture the guest as the golden bundle:

1. **Create the `reactorcide` account** if Setup Assistant did not create
   it. Make sure that it can run the job toolchains. The guest daemon runs
   each job command as this account. Set `REACTORCIDE_VM_GUEST_USER` on the
   worker if you use a different name.

2. **The job directory** needs no manual step. The macOS system volume is
   read-only, so `/job` is a synthetic link (`/etc/synthetic.conf`) to
   `/private/var/reactorcide/job` (mode 1777) on the Data volume. macOS makes
   the link at each boot. `install-vmguest.sh`, `vmguest install`, and each
   `reactorcide vm-image build macos` add it. A plain `sudo mkdir /job` fails
   with "Read-only file system".

3. **Bake the toolchain** that the jobs need (Xcode or the command line
   tools, language runtimes, `runnerlib` prerequisites). Everything that a job
   uses must be in the guest. There is no nested container in the guest.

4. **Shut down the guest.**

5. **Install the guest daemon** from the host. Build `vmguest` for the guest
   and run the install script as root while the VM is stopped:

   ```sh
   cd coordinator_api
   GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build -o vmguest ./cmd/vmguest
   sudo ../deployment/macos/install-vmguest.sh \
     /opt/reactorcide/vm-images/macos-26-bootstrap ./vmguest
   ```

   The script attaches `disk.img`, mounts the Data volume of that image, and
   adds `/usr/local/libexec/reactorcide-vmguest` and the launchd file
   `/Library/LaunchDaemons/community.catalyst.reactorcide.vmguest.plist`.
   launchd starts the daemon at boot, with no login. The daemon writes its
   log to `/var/log/reactorcide-vmguest.log` in the guest.

   As an alternative, copy `vmguest` into a running guest that still has a
   network device and run `sudo ./vmguest install`. Then remove the network
   device from that VM configuration.

6. Keep the bundle directory read-only in production and let each job clone
   it.

Remote Login (sshd) and SSH keys are not necessary. Do not put SSH keys,
passwords, or tokens in the image for the worker.

## Guest daemon and guest account

The worker does not authenticate to the guest with a key. Only the host can
open the vsock device of a VM. The guest daemon accepts job channel
connections from the host CID (2) only, when the guest kernel reports the
peer address.

The worker sends the guest account name (`REACTORCIDE_VM_GUEST_USER`, default
`reactorcide`) in each job channel hello. The daemon runs as root. It gives
the uploaded files and trees to that account and runs the command as that
account, with `HOME`, `USER`, and `LOGNAME` of the account.

The worker and the daemon must use the same protocol version. A worker
refuses a daemon with a different version and tells you to update the guest
daemon in the image. Use `reactorcide vm-image build macos --guest-daemon
PATH` to put a new daemon in a derived image (see
[VM Image Operations](./vm-images.md)).

Worker settings:

| Env var                              | Meaning                                        | Default |
| ------------------------------------ | ---------------------------------------------- | ------- |
| `REACTORCIDE_VM_IMAGE_DIR`           | directory relative image refs resolve under    | `.`     |
| `REACTORCIDE_VM_IMAGE_SOURCE`        | `local`, `oci`, or `s3` image source (`oci` and `s3` are the same source; both accept OCI and `s3://` references) | `local` |
| `REACTORCIDE_VM_S3_ENDPOINT`         | S3-compatible endpoint URL for `s3://` images   | `REACTORCIDE_S3_ENDPOINT`, then AWS S3 |
| `REACTORCIDE_VM_IMAGE_CACHE_DIR`     | OCI content and materialized bundle cache       | `~/.cache/reactorcide/vm-images` |
| `REACTORCIDE_VM_REGISTRY_AUTH_FILE`  | Docker-compatible multi-registry credential file | `~/.config/reactorcide/oci-auth.json` |
| `REACTORCIDE_VM_SCRATCH_DIR`         | directory for ephemeral per-job VM clones      | OS temporary directory |
| `REACTORCIDE_VM_GUEST_USER`          | guest account that runs the job                 | `reactorcide` |
| `REACTORCIDE_VM_EGRESS_PROXY`        | HTTP CONNECT proxy (`host:port`) for guest egress | not set: direct connection |
| `REACTORCIDE_VM_EGRESS_PROXY_KEY_FILE` | job key file of the egress proxy (see [Egress Proxy](./egress-proxy.md)) | `REACTORCIDE_EGRESS_JOB_KEY_FILE` |
| `REACTORCIDE_VM_METRICS_DIR`         | optional local JSON Lines debug output          | disabled |
| `REACTORCIDE_VM_METRICS_INTERVAL`    | optional JSON Lines debug sample interval       | `5s`    |

`worker.LoadVMConfig` and the macOS lifecycle read these settings. They apply
to the coordinator-mediated worker and to `reactorcide run-local --backend
vm`. The SSH settings (`REACTORCIDE_VM_SSH_USER`,
`REACTORCIDE_VM_SSH_PRIVATE_KEY_FILE`, `REACTORCIDE_VM_SSH_HOST_KEY_FILE`,
`REACTORCIDE_VM_SSH_PASSWORD`) are removed. The worker ignores them.

### Job input transfer

The worker sends the command, environment, workspace, source tree, `/job`
input mounts, and short-lived VCS credential files through the job channel.
Secret values go only inside protocol frames. The worker and the daemon never
write them to a log. After the command stops, the daemon sends the workflow
output and trigger control files back, and the worker writes them to the host
workspace.

A `TERM` request from the worker signals the process group of the command.
If the worker closes the job channel before the command stops, the daemon
stops the process group.

## Smoke test (validate vz + vsock + guest daemon)

Before you connect the full worker, validate the stack (boot, guest daemon,
egress tunnel, teardown) with the standalone `vmsmoke` command:

```sh
cd coordinator_api
CGO_ENABLED=1 go build -tags vz -o vmsmoke ./cmd/vmsmoke
codesign --force --entitlements ../deployment/macos/vz.entitlements -s - ./vmsmoke

./vmsmoke \
  -bundle /opt/reactorcide/vm-images/macos-14-base \
  -user reactorcide
```

It boots a guest from the bundle with no network device. It runs `echo hello`
and `curl https://example.com/` in the guest through the guest daemon, prints
the output, then destroys the guest. The `curl` step checks the transparent
route and the egress tunnel. Use `-egress-url ''` to skip it. `vmsmoke: OK`
means that vz, the vsock device, the guest daemon, and the egress tunnel
work.

`-extra 'COMMANDS'` runs more shell commands in the guest after the checks,
for example `-extra 'id; ifconfig -l; netstat -rn -f inet'`.

## Running jobs

Once the bundle and the environment settings are in place:

```sh
# Local:
reactorcide run-local --backend vm ./jobs/my-macos-job.yaml

# Worker (coordinator-mediated) selects the same backend via --container-runtime vm.
```

With `run-local` and the `local` image source, the `image` of the job is a
bundle name relative to `REACTORCIDE_VM_IMAGE_DIR` (for example
`image: macos-26-xcode`). An absolute path is refused.

A job that goes through the coordinator must name an image alias, or a
pinned reference that an alias pins. The coordinator refuses other macOS VM
jobs. Use `REACTORCIDE_VM_IMAGE_SOURCE=oci` on the worker, so that it can pull
the images of its image set. See
[Image Aliases and Worker Image Sets](./image-aliases.md).

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

## Hardware validation status

Verified on 2026-11-17 on an Apple Silicon (M5) host with macOS 27.0.1, with
a macOS 26.6 guest (Code-Hex/vz v3.7.1, Go 1.26.4):

- `CGO_ENABLED=1 go build -tags vz` of `reactorcide` and `vmsmoke`, and the
  entitlement signature. `go test -tags vz` of `internal/worker/vmrunner`,
  `internal/vmguest`, `internal/worker`, `internal/coordinatorworker`, and
  `cmd` passes on the host.
- `vmsmoke`: the guest boots with no network device, the guest daemon starts
  from launchd with no login, the job channel works, and egress through the
  transparent route and the tunnel works (`curl https://example.com/` gives
  200, a direct IP address also works, DNS gives a `198.18.x` address). A run
  takes about 26 seconds, boot included, and removes its clone.
- The command runs as the guest account (uid, gid, and groups), with `HOME`,
  `USER`, and `LOGNAME` of the account.
- `reactorcide vm-image build macos --from-local --guest-daemon
  --provision`: the build boots the guest, updates the daemon, makes `/job`,
  runs the script through the tunnel, and stops the guest cleanly (about 14
  seconds). The derived image keeps the result.
- `reactorcide run-local --backend vm`: the workspace, the source tree in
  `/job/src`, and the CI tree in `/job/ci` reach the guest, and the job
  completes.
- Orphan sweep: while job A runs, job B starts and finishes, and A's scratch
  directory stays. After `kill -9` of the worker of job A, its VM process
  stops and its scratch directory stays. The next worker start removes that
  directory before it boots, and no scratch directory is left after the job.

Found and fixed during this run:

- A macOS guest needs a graphics device. With none, it stops before it
  mounts the Data volume, and the daemon never starts (the worker sees
  "connection reset by peer" from vsock). The VM now has a headless display,
  a USB keyboard and pointer, and an entropy device. It still has no network
  device.
- `/job` could not be made on the read-only system volume (see "Prepare the
  guest").
- The daemon now extracts into the directory that a linked destination
  points to.

The guest shows an `en1` interface ("Ethernet Adapter", backed by
`anpi0`). It has no medium and no address (`status: inactive`). It is not a
network path.

### Image aliases and image sets (2026-11-18)

On 2026-11-18 a worker on the M5 host ran the image alias and image set
tests (see [Image Aliases and Worker Image Sets](./image-aliases.md)). The
coordinator, PostgreSQL, Corndogs, and a `registry:2` OCI registry ran on a
Linux development host on the LAN. The worker connected to the coordinator
and to the registry through SSH reverse tunnels on loopback (see the Local
Network note below). The worker used `REACTORCIDE_VM_IMAGE_SOURCE=oci`, an
empty cache, `--concurrency 2`, and `REACTORCIDE_VM_GUEST_USER=runner`.

1. Passed. Startup gate. The worker registered, reported `updating` for set
   version 1, pulled the image (about 5.5 minutes through the tunnel), and
   reported `updated`. A job that was submitted before the worker started
   stayed `submitted` until then.
2. Passed. A job with `image: rcval-mac` and `os: macos` went to the worker
   and completed as `runner`. The worker log, the coordinator log, and the
   job record (`image_display`) show `rcval-mac (REF@sha256:...)`. The
   first test image had an old guest daemon (`lchown /job: read-only file
   system`). The image was rebuilt with `vm-image build macos
   --guest-daemon` and published, and that image was the active pin for
   tests 2 to 6.
3. Passed. Live update. A 15-minute job ran on pin D1. The pin changed to
   D2 (outcome `pending`). The worker reported `updating` and then `updated`
   with both digests. D2 was a new manifest for the same layer, so the pull
   took about 30 seconds. The promotion came about 10 seconds after the
   report. A second rollout (D2 to D3) ran while the long job still ran: a
   job submitted before the promotion got D2 and completed, and a job after
   the promotion got D3. The long job completed on D1.
4. Passed. A pending pin with a digest that is not in the registry: the
   worker stayed ready, reported `failed` (permanent failure, `not found`),
   and ran a new job on the active pin. The alias did not promote. Setting
   the active reference again cancelled the pending pin.
5. Passed (behavior after the review fixes). A new worker and a second
   alias that pins a digest that is not in the registry (permanent
   failure): the gate opened, a job for the good alias ran, and the job for
   the bad alias stayed `submitted`. With the bad alias on a closed port
   (transient failure): the gate stayed closed, the worker sent no
   `RequestJob` for 2 minutes, and a job for the good alias stayed
   `submitted`. After the bad alias was deleted, the job ran.
6. Passed. Each old digest left the set when no job used it: D2 at once
   after the D3 promotion, D1 about 80 seconds after the long job ended.
   The worker logged `removed VM image that left the image set` for each.
   The cache then had only D3. No VM process and no scratch directory
   stayed.

Found during this run:

- Local Network privacy blocks a new ad-hoc signed binary that starts from
  an SSH session. `vm-image publish` to a LAN registry failed with
  `connect: no route to host`, but `curl` and `nc` to the same address
  worked. Loopback is not subject to this control, so this run used SSH
  reverse tunnels. Each rebuild can have a new code identity (see "Local
  Network access").
- Two jobs failed with `VZErrorDomain Code=6` ("The number of virtual
  machines exceeds the limit") when a job started a few seconds after
  another job's VM stopped, while the long job's VM ran. macOS permits two active
  VMs. The slot of a stopped VM is not free at once. The worker did not
  try again, and the job failed with `infra.platform`. This is not an image
  set problem. Fixed after this run: when the start fails with this error,
  the macOS lifecycle waits and tries the start again (2 s, 4 s, 8 s, then
  10 s each time, for a maximum of 60 s in total). The worker log shows
  each new attempt. A job cancel stops the wait. A later run found the
  real cause (see below).

Later run on 2026-11-18 (the same rig, a new worker with `--concurrency 2`,
alias `rcv18-mac`):

1. Failed, then fixed. With only the retry, a job that started right after
   another VM stopped got `VZErrorDomain Code=6` for all of the 54 seconds
   of waits, and the lease failed with `infra.platform`. In one run, 4 of
   10 short jobs had a failed lease, and 2 of them failed again after the
   automatic retry. VMs started only at intervals of 2 minutes. The
   `com.apple.Virtualization.VirtualMachine` XPC process of a stopped VM
   stayed. The cause: Code-Hex/vz releases the `VZVirtualMachine` object
   only in a Go finalizer, and macOS keeps the VM slot until that release.
   With an idle heap, the Go garbage collector runs only every 2 minutes.
   Fix: `Destroy` removes the last reference to the VM and runs a garbage
   collection, and each retry wait does the same. After the fix, 10 of 10
   short jobs started right after the previous VM stopped while the long
   job ran. They gave no limit warning and no failure. A job starts about
   25 seconds after the previous one.
2. Passed. The retry path. A third VM (`vmsmoke`) filled the second slot
   while the long job ran. A new job logged `the Virtualization.framework
   VM limit is full; trying the start again` (attempts 1 to 5). It booted
   5 seconds after the `vmsmoke` VM stopped, and it completed (two runs).
   When the slot stays full for more than 60 seconds, the lease fails with
   `infra.platform` and the job goes back to the queue once.
3. Passed. The first line of the job log is `[reactorcide] image:
   rcv18-mac (127.0.0.1:25000/rcv18/macos@sha256:fcd78be4...)`.
4. Passed. The rebuilt test image (see below) has the current guest
   daemon. Jobs get `/job -> private/var/reactorcide/job` and no `lchown`
   error.

The test bundle `~/rc-vmsmoke/bundle` on this host is a macOS 26.6 guest
VM disk set (the four bundle files) with the command line tools, the
`runner` account, and the guest daemon. `vmsmoke`, `run-local`, and these
tests boot a copy of it. It was rebuilt on 2026-11-18 with `reactorcide
vm-image build macos --from-local --from <old bundle> --guest-daemon
<new vmguest>`. The build took about 1 minute, and the publish to the
registry through the tunnel took 11 minutes more.

In this run, a single-line `command` with no `$NAME` reference did not go
through `REACTORCIDE_JOB_SHELLCMD`. The worker split it into words and ran
it directly, so `;`, `|`, and `$(...)` were not shell syntax. A job with
`echo a; sleep 720` printed `a; sleep 720` and stopped at once. Fixed after
this run: each command goes through the shell prefix (default `sh -c` for
macOS). See [Command Execution](./job-definitions.md#command-execution).
The second session below shows the fix on this host.

Second session (2026-11-18, the same rig):

1. Passed. The default shell prefix for a single-line command. The job had
   no `REACTORCIDE_JOB_SHELLCMD`, so the worker used `sh -c`. The command
   `echo a; echo b | tr b c; echo $(uname -s)` printed `a`, `c`, and
   `Darwin`. Thus `;`, `|`, and `$(...)` are shell syntax.
2. Passed. The command `exit 3` failed the job with exit code 3.
3. Passed. A retry after an alias promotion, for a job that named the
   alias. The retry ran on the new pin. `reactorcide jobs get` showed
   `image_display` (the new image) and `image_retry_of` (the old image).
   The job log had the lines `[reactorcide] image: ...` and
   `[reactorcide] retry of: ...`.
4. Timings. From `images alias set` (the new pin) to the promotion: 84
   seconds. From the new pin to the removal of the old image: 106 seconds.
   The pull of a new pin that has the same layer blob: about 41 seconds
   (through the SSH tunnel).
The check of a `workers:read` token in this session is in
[Windows VM Runner](./vm-runners-windows.md#image-aliases-and-image-sets-2026-11-18).

## Hardware checks that remain

Checks 2, 3, 5, 6, 7 (from the guest side), and 9 of the earlier list passed
on 2026-11-17 (see above). These remain:

1. The `vz` build: `CGO_ENABLED=1 go build -tags vz ./...` and the
   entitlement signature. The Linux build does not compile these files.
   Compile them on a Mac after each change.
2. `VirtioSocketDevice.Listen(7101)` receives guest connections. After
   `Close`, one goroutine for each VM stays blocked in the vz `Accept` (a
   limit of Code-Hex/vz v3.7.1). Monitor the goroutine count of a
   long-running worker.
3. Apple services in the guest open connections at boot. They go through the
   forwarder. With an egress proxy, expect refusals for these destinations.
