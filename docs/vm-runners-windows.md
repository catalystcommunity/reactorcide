# Windows VM Runners (Hyper-V)

The `vm` JobRunner backend runs native **Windows** jobs inside ephemeral,
per-job guest VMs on a Windows host, using **Hyper-V driven through PowerShell**
(`New-VHD` / `New-VM` / `Start-VM` / `Get-VMNetworkAdapter` / `Remove-VM`). It is
the isolation boundary for Windows build/test jobs that cannot run in a Linux
container.

Terminology: the process that boots and drives guests is the **worker**.

> Why not `hcsshim`? `github.com/microsoft/hcsshim` targets Windows *containers*
> and utility VMs, not full guest VMs with their own OS and OpenSSH server.
> PowerShell's Hyper-V module is the pragmatic path to a booted guest that the
> shared SSH `GuestTransport` reaches exactly like the macOS backend.

## Pure Go — no Cgo, no special build tag

Unlike the macOS backend (Cgo + the `vz` build tag + code-signing), the Windows
lifecycle is **pure Go**: it shells out to `powershell.exe`. That means:

- It **cross-compiles and compile-verifies from any OS**:

  ```sh
  CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build ./...
  ```

- The **released windows worker includes the `vm` backend by default** — there
  is no `-tags` gymnastics. The lifecycle is guarded only by `//go:build
  windows` (`coordinator_api/internal/worker/vmrunner/lifecycle_windows.go`).

It can only be **run** on a real Hyper-V host (see Requirements).

## Requirements

- **Windows 11 Pro** (or Enterprise/Education; Server works too) with the
  **Hyper-V feature enabled**:

  ```powershell
  # Elevated PowerShell, then reboot:
  Enable-WindowsOptionalFeature -Online -FeatureName Microsoft-Hyper-V -All
  ```

- Run the worker **elevated**, or as a user in the **Hyper-V Administrators**
  local group (the Hyper-V cmdlets require it).
- A working **virtual switch** that gives guests an IP via DHCP. Windows 10/11's
  built-in **"Default Switch"** provides NAT + DHCP and is the default (see the
  switch note below).
- The guest image must contain an **OpenSSH server** and the **Hyper-V
  integration services / Data Exchange**. The base image must not contain SSH
  authorization keys or host keys. The worker adds new keys to each clone.

## Configuration (host env vars)

The image directory configuration is shared with the macOS backend and read by
`worker.LoadVMConfig`. The Hyper-V lifecycle reads the Windows-specific host
settings when it starts:

| Env var                              | Meaning                                                        | Default          |
| ------------------------------------ | ------------------------------------------------------------- | ---------------- |
| `REACTORCIDE_VM_IMAGE_DIR`           | directory relative image refs resolve under                   | `.`              |
| `REACTORCIDE_VM_IMAGE_SOURCE`        | `local` or `oci` image source                                 | `local`          |
| `REACTORCIDE_VM_IMAGE_CACHE_DIR`     | OCI image cache directory                                     | user cache directory |
| `REACTORCIDE_VM_REGISTRY_AUTH_FILE`  | Docker-compatible registry credential file                    | user config directory |
| `REACTORCIDE_VM_SSH_USER`            | guest account the worker logs in as                           | `reactorcide`    |
| `REACTORCIDE_VM_METRICS_DIR`         | optional local JSON Lines debug output                         | disabled         |
| `REACTORCIDE_VM_METRICS_INTERVAL`    | optional JSON Lines debug sample interval                      | `5s`             |
| `REACTORCIDE_VM_SCRATCH_DIR`         | parent directory for per-job differencing disks                | system temporary directory |
| `REACTORCIDE_VM_HYPERV_SWITCH`       | Hyper-V virtual switch new guests attach to                   | `Default Switch` |
| `REACTORCIDE_VM_HYPERV_SECURE_BOOT`  | `off`/`false`/`0`/`no` disables Gen-2 Secure Boot; else on    | on               |

The Windows lifecycle does not use the shared SSH password or key settings.
It creates new keys for each VM job.

### The Hyper-V switch note

`Default Switch` is the zero-config choice: it is a built-in NAT switch with an
integrated DHCP server, so `Get-VMNetworkAdapter` can report the guest IP the
lifecycle needs. If you use a custom **External** or **Internal** switch, make
sure something on it hands out DHCP leases, and set
`REACTORCIDE_VM_HYPERV_SWITCH` to its name:

```powershell
$env:REACTORCIDE_VM_HYPERV_SWITCH = 'reactorcide-nat'
```

### The Secure Boot note

New VMs are **Generation 2** (UEFI). Gen-2 Secure Boot defaults to the
`MicrosoftWindows` template, which is correct for **Windows guests** — leave
`REACTORCIDE_VM_HYPERV_SECURE_BOOT` unset. A **Linux guest** whose bootloader is
not signed by the Microsoft UEFI CA will fail to boot with Secure Boot on; set
`REACTORCIDE_VM_HYPERV_SECURE_BOOT=off` for those images (the lifecycle then
runs `Set-VMFirmware -EnableSecureBoot Off`).

The worker also gives each VM a local key protector and a virtual TPM. Windows
11 needs the virtual TPM. The key protector belongs to the temporary VM. It is
not part of the shared base image.

## The base image: a "bundle" directory (VHDX)

Mirroring the macOS bundle convention, the base image is a **bundle directory**
containing a single base disk:

| File         | What it is                                    | Per-job handling                          |
| ------------ | --------------------------------------------- | ----------------------------------------- |
| `disk.vhdx`  | the guest's base disk (a prepared Windows install) | never written; each job gets a **differencing** child |

This filename is fixed (see the `bundleVHDX` constant in
`coordinator_api/internal/worker/vmrunner/lifecycle_windows.go`). A bare
`*.vhdx` file path is also accepted for convenience, but the bundle directory is
the recommended layout so it resolves identically to the macOS bundle through
`LocalImageSource` and the OCI `ImageSource`.

On each `Boot`, the lifecycle:

1. creates a per-job scratch directory,
2. creates a **differencing VHDX** off the base for copy-on-write
   (`New-VHD -ParentPath <base> -Path <scratch>\job.vhdx -Differencing`) — no
   filesystem-clonefile concerns; Hyper-V's differencing disk *is* the CoW,
3. creates new SSH client and host keys for the job,
4. mounts the differencing disk and puts the public client key and the new host
   key pair in the clone,
5. creates a Generation-2 VM from it
   (`New-VM -Generation 2 -MemoryStartupBytes <spec> -VHDPath <scratch>\job.vhdx -SwitchName <switch>`),
   sizes the CPUs (`Set-VMProcessor -Count`), enables a virtual TPM, disables
   automatic checkpoints, and optionally disables Secure Boot,
6. starts the VM (`Start-VM`),
7. discovers the guest's IP by polling
   `Get-VMNetworkAdapter -VMName <name> | Select-Object -ExpandProperty IPAddresses`
   until a non-APIPA IPv4 accepts an SSH connection (see below),
8. returns once the guest reports a reachable IP. The worker pins the new host public
   key and uses the in-memory client private key for SSH.

`Destroy` force-stops the VM (`Stop-VM -Force -TurnOff`, tolerating an
already-stopped guest), removes it (`Remove-VM -Force`), and deletes the scratch
directory (including the differencing VHDX). An unknown/already-destroyed handle
is a safe no-op. The base VHDX is never modified.

### Guest IP discovery (known fragility)

`Get-VMNetworkAdapter`'s `IPAddresses` are **guest-reported** — they only appear
once the guest's **Hyper-V integration services / Data Exchange (KVP)** service
is running *and* DHCP has assigned a lease. So:

- The lifecycle **polls** with a timeout (it does not expect an IP immediately).
- If the guest image lacks integration services, or the switch has no DHCP,
  discovery times out with a message pointing at Data Exchange. **This is the
  single most likely first-hardware snag.**
- Windows self-assigned **APIPA** addresses (`169.254.0.0/16`) and IPv6 entries
  are skipped so a half-booted guest never resolves to a dead address.

The parsing of that output is isolated in a pure, unit-tested helper
(`parseVMIPv4` in `hyperv_parse_windows.go`) so the fiddly part is testable
without a live Hyper-V host.

### Where to place the bundle

```powershell
$env:REACTORCIDE_VM_IMAGE_DIR = 'C:\reactorcide\vm-images'
# a job's image "win11-base" then resolves to
#   C:\reactorcide\vm-images\win11-base\   (the bundle dir holding disk.vhdx)
```

An absolute image reference is used as-is (`REACTORCIDE_VM_IMAGE_DIR` is ignored
for that job).

For OCI images, use a local cache on each worker. Do not share one writable
cache directory between workers. Publish one immutable bundle to the registry,
use its digest in jobs, and let each worker cache it on its image disk. See
[Build a Windows VM Image](./windows-vm-image-build.md#publish-and-share-the-image-through-oci).

Keep `REACTORCIDE_VM_SCRATCH_DIR` on a disk that has enough IOPS and capacity
for all concurrent differencing VHDX files. This directory is independent of
the local image directory and OCI cache directory.

## Producing the base VHDX (golden image)

Use [Build a Windows VM Image](./windows-vm-image-build.md) for the supported
unattended procedure. It uses the Windows components that are already on a
Hyper-V host. It does not need Packer, RDP, VMConnect, WinRM, or the Windows
ADK.

The following procedure is a manual fallback. It is not necessary for the
supported build path.

1. **Create a Gen-2 VM and install Windows** from an ISO:

   ```powershell
   New-VM -Name win11-golden -Generation 2 -MemoryStartupBytes 4GB `
     -NewVHDPath C:\reactorcide\vm-images\win11-base\disk.vhdx -NewVHDSizeBytes 80GB `
     -SwitchName 'Default Switch'
   Set-VMDvdDrive -VMName win11-golden -Path C:\iso\Win11.iso
   Start-VM win11-golden   # then connect with vmconnect and install Windows
   ```

2. **Enable the OpenSSH Server** in the guest and start it:

   ```powershell
   Add-WindowsCapability -Online -Name OpenSSH.Server~~~~0.0.1.0
   Set-Service sshd -StartupType Automatic
   Start-Service sshd
   ```

3. **Create the worker's account** (e.g. `runner`) and make sure it can run the
   jobs' toolchains.

4. **Bake the toolchain** the jobs need (language runtimes, build tools,
   `runnerlib` prerequisites, etc.). Everything a job uses must be present in the
   guest — there is no nested container inside the guest.

5. Stop `sshd`. Remove all `authorized_keys` files and all
   `C:\ProgramData\ssh\ssh_host_*` files before you seal the image. The worker
   adds replacement keys to each clone.

6. Ensure **Hyper-V integration services / Data Exchange** are enabled (they are
   by default on modern Windows guests) so the host can discover the IP.

7. **Run Sysprep to generalize the image**
   (`C:\Windows\System32\Sysprep\sysprep.exe /generalize /oobe /shutdown`) if you
   use a different image builder. The Reactorcide builder does this step
   automatically. The worker writes the unattended specialization data into
   each clone before boot.

8. **Export / keep the base VHDX.** The prepared `disk.vhdx` is now your golden
   base; keep the bundle directory read-only and let each job clone it via a
   differencing disk. (You can delete the golden VM definition with
   `Remove-VM golden` — keep the VHDX file.)

### Other image builders

You can use another image builder when it produces a Generation 2 VHDX that
meets the bundle and guest requirements. Packer is one option. It is not a
Reactorcide runtime dependency.

## Guest credentials

Do not configure a shared SSH key for Windows guests. The worker completes
these operations for each job:

1. It creates a new Ed25519 client key pair in memory.
2. It creates a new Ed25519 host key pair in the job scratch directory.
3. It mounts the writable differencing VHDX.
4. It puts the client public key in the guest account's `authorized_keys`
   file.
5. It puts the host key pair in `C:\ProgramData\ssh`.
6. It starts the VM and pins the new host public key for the SSH connection.
7. It deletes the VM and the scratch key files during cleanup.

The base image and OCI artifact contain no SSH keys. Different hosts can use
the same image digest. They do not share client keys or host keys.

Use a non-administrator guest account. Windows OpenSSH uses
`C:\ProgramData\ssh\administrators_authorized_keys` for administrator
accounts. The current injector writes the key to the selected user's profile.

### Job input transfer

The worker sends the command, environment, workspace, source tree, `/job`
input mounts, and short-lived VCS credential files through SSH. It converts
the standard `/job` paths to `C:\reactorcide\job` paths. It does not put secret
values on the SSH command line. After the command stops, the worker copies
workflow output and trigger control files back to the host workspace.

## VM resource telemetry

The worker samples each running Windows guest through its existing SSH
connection. It sends these series through normal job telemetry:

- CPU use and configured CPU capacity
- Physical memory use and limit
- Committed memory and swap use
- Used and total capacity for the guest `C:` volume

The worker uses `--metrics-interval` for CPU and memory. It uses
`--storage-metrics-interval` for storage. The defaults are 2 seconds and 10
seconds. Windows reports total processor percentage as a normalized value, so
the worker multiplies it by the guest CPU count before it converts the result
to millicores.

The storage series describes logical use inside the guest. It does not describe
the physical size of the differencing VHDX on the host. Monitor the host volume
that contains `REACTORCIDE_VM_SCRATCH_DIR` to detect physical capacity and IOPS
pressure.

`REACTORCIDE_VM_METRICS_DIR` is optional debug output. Normal coordinator
telemetry does not require it.

## Smoke test (validate Hyper-V + networking + SSH)

Before wiring the full worker, validate the whole stack — boot, IP discovery,
SSH, teardown — with the standalone `vmsmoke` command. No Cgo or signing needed:

```powershell
cd coordinator_api
go build -o vmsmoke.exe .\cmd\vmsmoke

# Run elevated (or as a Hyper-V Administrators member):
.\vmsmoke.exe `
  -bundle C:\reactorcide\vm-images\win11-base `
  -user reactorcide
```

It boots a guest from the bundle and logs the reachable guest IP as `guest_ip`.
It runs a command through SSH and checks CPU, memory, and storage metrics. It
then destroys the guest. `vmsmoke: OK` means Hyper-V, the virtual switch, guest
SSH, and VM resource collection all work.

## Running jobs

Once the bundle and environment settings are in place:

```powershell
# Local:
reactorcide run-local --backend vm .\jobs\my-windows-job.yaml

# Worker (coordinator-mediated) selects the same backend via --container-runtime vm.
```

A `{os: windows}` job should only be scheduled onto a Windows worker.

## Hardware validation status

The Windows lifecycle compiles with `CGO_ENABLED=0 GOOS=windows GOARCH=amd64
go build ./...`. Unit tests cover its parsing and generated PowerShell logic.
The service install and update flow has run on a Windows 11 Pro Hyper-V host.
The unattended builder created a Windows 11 Enterprise Evaluation 25H2 image
from the verified Microsoft ISO on that host.

The hardware smoke test created a differencing VHDX and a Generation 2 VM with
a virtual TPM. It selected a reachable DHCP address, verified the injected SSH
host key, ran a command through SSH, and removed the VM. Run the smoke test on
each new host before you let the worker accept jobs.
