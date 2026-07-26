# Windows VM Runners (Hyper-V)

The `vm` JobRunner backend runs native **Windows** jobs inside ephemeral,
per-job guest VMs on a Windows host, using **Hyper-V driven through PowerShell**
(`New-VHD` / `New-VM` / `Start-VM` / `Get-VMNetworkAdapter` / `Remove-VM`). This
is phase VM-4 of [`VM_RUNNERS_PLAN.md`](../VM_RUNNERS_PLAN.md). It is the
isolation boundary for Windows build/test jobs that cannot run in a Linux
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
- The guest image must run an **OpenSSH server** with the worker's SSH **public
  key** baked in, and the **Hyper-V integration services / Data Exchange** so the
  host can discover the guest's IP (see Guest IP discovery).

## Configuration (host env vars)

The image directory and SSH credentials are shared with the macOS backend and
read by `worker.LoadVMConfig`. Two Windows-specific host knobs are read directly
by the Hyper-V lifecycle at construction (they need no per-job plumbing):

| Env var                              | Meaning                                                        | Default          |
| ------------------------------------ | ------------------------------------------------------------- | ---------------- |
| `REACTORCIDE_VM_IMAGE_DIR`           | directory relative image refs resolve under                   | `.`              |
| `REACTORCIDE_VM_SSH_USER`            | guest account the worker logs in as                           | `runner`         |
| `REACTORCIDE_VM_SSH_PRIVATE_KEY_FILE`| path to the worker's SSH private key (PEM)                     | —                |
| `REACTORCIDE_VM_SSH_PASSWORD`        | password auth (discouraged; key preferred)                    | —                |
| `REACTORCIDE_VM_HYPERV_SWITCH`       | Hyper-V virtual switch new guests attach to                   | `Default Switch` |
| `REACTORCIDE_VM_HYPERV_SECURE_BOOT`  | `off`/`false`/`0`/`no` disables Gen-2 Secure Boot; else on    | on               |

> **Never** log or print the private key or any guest credential. The worker
> reads the key from a *file* precisely so its contents stay out of the
> environment and out of logs.

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
3. creates a Generation-2 VM from it
   (`New-VM -Generation 2 -MemoryStartupBytes <spec> -VHDPath <scratch>\job.vhdx -SwitchName <switch>`),
   sizes the CPUs (`Set-VMProcessor -Count`), optionally disables Secure Boot,
4. starts the VM (`Start-VM`),
5. discovers the guest's IP by polling
   `Get-VMNetworkAdapter -VMName <name> | Select-Object -ExpandProperty IPAddresses`
   until the first non-APIPA IPv4 appears (see below),
6. returns once the guest reports an IP; the worker then waits for guest SSH
   (`:22`) to accept connections before running the job command.

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
  single most likely first-hardware snag** (flagged in `VM_RUNNERS_PLAN.md`).
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

## Producing the base VHDX (golden image)

You install Windows into a Gen-2 Hyper-V VM once, prepare it, then reuse its
VHDX as the read-only base for every job.

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
   guest — there is no nested container inside the guest (see the "Guest
   execution note" in `VM_RUNNERS_PLAN.md`).

5. **Install the worker's SSH public key** (see Guest credentials).

6. Ensure **Hyper-V integration services / Data Exchange** are enabled (they are
   by default on modern Windows guests) so the host can discover the IP.

7. **(Optional) sysprep / generalize** the image
   (`C:\Windows\System32\Sysprep\sysprep.exe /generalize /oobe /shutdown`) if you
   want a clean, re-identity-able base. Otherwise just shut the guest down
   cleanly.

8. **Export / keep the base VHDX.** The prepared `disk.vhdx` is now your golden
   base; keep the bundle directory read-only and let each job clone it via a
   differencing disk. (You can delete the golden VM definition with
   `Remove-VM golden` — keep the VHDX file.)

### Recommended tooling: Packer + the Hyper-V builder (instead of hand-rolling)

Reactorcide ships **no** base-image build tooling — the `reactorcide` binary
only *boots* an existing bundle (and `cmd/vmsmoke` is just a smoke test). The
reproducible path is **[Packer](https://developer.hashicorp.com/packer)** with
its **`hyperv-iso` builder**: it installs Windows from an ISO unattended
(`Autounattend.xml`), provisions the guest (enable OpenSSH Server, bake the
toolchain + the worker's `authorized_keys`), optionally syspreps, and produces
the VHDX. This parallels the macOS backend's Packer + Tart note. Packaging that
VHDX as an OCI artifact for the OCI `ImageSource` (VM-2) is **VM-5 (image build +
packaging)**.

## Guest credentials (prototype)

For the prototype, the guest authenticates the worker with a **baked-in SSH
key pair** (same model as the macOS backend):

- Generate a dedicated key pair for the worker (keep the private key on the host
  only):

  ```sh
  ssh-keygen -t ed25519 -f ~/.ssh/reactorcide_vm -N '' -C 'reactorcide-vm-worker'
  ```

- **Bake the public key into the golden image.** For the guest account, add the
  public key to `C:\Users\<user>\.ssh\authorized_keys`. Note the Windows OpenSSH
  quirk: keys for **administrators** are read from
  `C:\ProgramData\ssh\administrators_authorized_keys` instead, so use a
  non-admin worker account or place the key there accordingly, and check the
  file ACLs (`sshd` refuses over-permissive `authorized_keys`).

- **Point the worker at the matching private key** via
  `REACTORCIDE_VM_SSH_PRIVATE_KEY_FILE` (read from a file so the key material
  never passes through an env var value or gets printed).

```powershell
$env:REACTORCIDE_VM_IMAGE_DIR = 'C:\reactorcide\vm-images'
$env:REACTORCIDE_VM_SSH_USER = 'runner'
$env:REACTORCIDE_VM_SSH_PRIVATE_KEY_FILE = 'C:\Users\me\.ssh\reactorcide_vm'
```

### Security notes / future hardening

- The SSH transport does **not** verify the guest host key: guests are cloned
  fresh per job and have no stable identity to pin, and the host↔guest link runs
  over the switch's private network the lifecycle controls.
- Baking a **shared** worker key into the golden image is a prototype
  simplification: every job clone trusts the same key. **Per-job injected keys**
  (a unique pair minted per boot, delivered over a first-boot channel so no
  long-lived secret lives in the image) are the intended hardening and are out of
  scope for VM-4 — they need a first-boot delivery mechanism (an unattend/answer
  file, a mounted seed volume, or a vsock/KVP channel) that does not yet exist
  here.

## Smoke test (validate Hyper-V + networking + SSH)

Before wiring the full worker, validate the whole stack — boot, IP discovery,
SSH, teardown — with the standalone `vmsmoke` command. No Cgo or signing needed:

```powershell
cd coordinator_api
go build -o vmsmoke.exe .\cmd\vmsmoke

# Run elevated (or as a Hyper-V Administrators member):
.\vmsmoke.exe `
  -bundle C:\reactorcide\vm-images\win11-base `
  -user runner `
  -key C:\Users\me\.ssh\reactorcide_vm
```

It boots a guest from the bundle, logs the guest IP (as `guest_ip`) once DHCP
assigns it, runs `cmd /c echo hello` in the guest over the SSH transport, prints
the output, then destroys the guest. `vmsmoke: OK` means Hyper-V, the virtual
switch, and guest SSH all work.

## Running jobs

Once the bundle, key, and env are in place:

```powershell
# Local:
reactorcide run-local --container-runtime vm .\jobs\my-windows-job.yaml

# Worker (coordinator-mediated) selects the same backend via --container-runtime vm.
```

A `{os: windows}` job should only be scheduled onto a Windows worker (see
`VM_RUNNERS_PLAN.md`).

## What is not yet verified

The Windows lifecycle **compiles** cleanly (`CGO_ENABLED=0 GOOS=windows
GOARCH=amd64 go build ./...`) and its pure parsing helpers are unit-tested, but
the actual `New-VM` / `Start-VM` / IP-discovery / `Remove-VM` flow has **not
been run on hardware yet** — that requires a Windows 11 Pro box with Hyper-V.
Guest IP discovery (Data Exchange + DHCP) is the expected first snag. Iterate on
what the hardware run reports.
