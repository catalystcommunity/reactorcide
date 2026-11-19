# Windows VM Runners (Hyper-V)

The `vm` JobRunner backend runs native **Windows** jobs inside ephemeral,
per-job guest VMs on a Windows host, using **Hyper-V driven through PowerShell**
(`New-VHD` / `New-VM` / `Start-VM` / `Remove-VM`). It is the isolation boundary
for Windows build/test jobs that cannot run in a Linux container.

The guest has **no network adapter** (decision D28). The worker reaches the
guest daemon `vmguest` (`coordinator_api/cmd/vmguest`) through Hyper-V sockets
(`AF_HYPERV`, through `github.com/Microsoft/go-winio`). Guest egress uses a
second Hyper-V socket service to the host egress forwarder.

Terminology: the process that boots and drives guests is the **worker**.

> Why not `hcsshim`? `github.com/microsoft/hcsshim` targets Windows *containers*
> and utility VMs, not full guest VMs with their own OS. PowerShell's Hyper-V
> module is the pragmatic path to a booted guest that the shared guest daemon
> transport reaches like the macOS backend.

## Pure Go — no Cgo, no special build tag

Unlike the macOS backend (Cgo + the `vz` build tag + code-signing), the Windows
lifecycle is **pure Go**: it shells out to `powershell.exe` and uses go-winio
for Hyper-V sockets. That means:

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
  local group (the Hyper-V cmdlets require it). The first VM boot writes the
  Hyper-V socket service registration under `HKLM`, which needs administrator
  rights (see below).
- The guest image must contain the guest daemon service
  (`C:\Program Files\Reactorcide\vmguest.exe`, service
  `reactorcide-vmguest`). The builder in
  [Build a Windows VM Image](./windows-vm-image-build.md) installs it. The
  base image needs no SSH server and no keys.
- No virtual switch is necessary for jobs or for `vm-image build windows`.
  Only the ISO builder `build-windows-image.ps1` uses a switch for its
  temporary build VM.

## Configuration (host env vars)

The image directory configuration is shared with the macOS backend and read by
`worker.LoadVMConfig`. The Hyper-V lifecycle reads the Windows-specific host
settings when it starts:

| Env var                              | Meaning                                                        | Default          |
| ------------------------------------ | ------------------------------------------------------------- | ---------------- |
| `REACTORCIDE_VM_IMAGE_DIR`           | directory relative image refs resolve under                   | `.`              |
| `REACTORCIDE_VM_IMAGE_SOURCE`        | `local`, `oci`, or `s3` image source (`oci` and `s3` are the same source). A coordinator VM job needs `oci` or `s3`; `local` is for `run-local` and local tests | `local` (the Windows installer and `worker-service.example.json` set `oci`) |
| `REACTORCIDE_VM_S3_ENDPOINT`         | S3-compatible endpoint URL for `s3://` images                 | `REACTORCIDE_S3_ENDPOINT`, then AWS S3 |
| `REACTORCIDE_VM_IMAGE_CACHE_DIR`     | OCI image cache directory                                     | user cache directory |
| `REACTORCIDE_VM_REGISTRY_AUTH_FILE`  | Docker-compatible registry credential file                    | user config directory |
| `REACTORCIDE_VM_GUEST_USER`          | guest account that runs the job (home `C:/Users/NAME`)        | `reactorcide`    |
| `REACTORCIDE_VM_EGRESS_PROXY`        | HTTP CONNECT proxy (`host:port`) for guest egress             | not set: direct connection |
| `REACTORCIDE_VM_EGRESS_PROXY_KEY_FILE` | job key file of the egress proxy (see [Egress Proxy](./egress-proxy.md)) | `REACTORCIDE_EGRESS_JOB_KEY_FILE` |
| `REACTORCIDE_VM_METRICS_DIR`         | optional local JSON Lines debug output                         | disabled         |
| `REACTORCIDE_VM_METRICS_INTERVAL`    | optional JSON Lines debug sample interval                      | `5s`             |
| `REACTORCIDE_VM_SCRATCH_DIR`         | parent directory for per-job differencing disks                | system temporary directory |
| `REACTORCIDE_VM_HYPERV_SECURE_BOOT`  | `off`/`false`/`0`/`no` disables Gen-2 Secure Boot; else on    | on               |

The worker ignores the removed settings `REACTORCIDE_VM_SSH_USER`,
`REACTORCIDE_VM_SSH_PASSWORD`, `REACTORCIDE_VM_SSH_PRIVATE_KEY_FILE`,
`REACTORCIDE_VM_SSH_HOST_KEY_FILE`, and `REACTORCIDE_VM_HYPERV_SWITCH`. The
installer script moves an old `REACTORCIDE_VM_SSH_USER` value to
`REACTORCIDE_VM_GUEST_USER`.

### Hyper-V socket services

Hyper-V refuses a Hyper-V socket with a service ID that is not in the host
registry. The lifecycle adds these keys on the first boot. The operation is
idempotent:

```text
HKLM:\SOFTWARE\Microsoft\Windows NT\CurrentVersion\Virtualization\GuestCommunicationServices\
  00001bbc-facb-11e6-bd58-64006a7986d3   ElementName = Reactorcide VM job channel   (port 7100)
  00001bbd-facb-11e6-bd58-64006a7986d3   ElementName = Reactorcide VM egress tunnel (port 7101)
```

Each service ID is the vsock template GUID for its port (go-winio
`VsockServiceID`). The macOS and Windows guest daemons use the same port
numbers. If the worker has no administrator rights, add these keys once as an
administrator.

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

1. registers the Hyper-V socket services, once for each worker process,
2. creates a per-job scratch directory,
3. creates a **differencing VHDX** off the base for copy-on-write
   (`New-VHD -ParentPath <base> -Path <scratch>\job.vhdx -Differencing`) — no
   filesystem-clonefile concerns; Hyper-V's differencing disk *is* the CoW,
4. mounts the differencing disk and checks that the guest daemon is in the
   image (the image is not generalized: all job VMs have the same Windows
   identity; see [Windows VM Image Build](./windows-vm-image-build.md#image-identity)),
5. creates a Generation-2 VM from it with no switch
   (`New-VM -Generation 2 -MemoryStartupBytes <spec> -VHDPath <scratch>\job.vhdx`),
   removes every network adapter (`Remove-VMNetworkAdapter`) and checks that
   none is left, sizes the CPUs (`Set-VMProcessor -Count`), enables a virtual
   TPM, disables automatic checkpoints, and optionally disables Secure Boot,
6. reads the VM ID and starts the VM (`Start-VM`),
7. starts the host egress listener for that VM ID on the egress service.
   Hyper-V refuses a bind to a VM ID before the VM runs. The guest dials
   egress only when a job opens a connection, so no connection is lost,
8. returns a dialer for the job channel service of that VM ID. The worker
   then sends a hello to the guest daemon until it answers. Then it runs the
   job command.

`Destroy` closes the egress listener, force-stops the VM
(`Stop-VM -Force -TurnOff`, tolerating an already-stopped guest), removes it
(`Remove-VM -Force`), and deletes the scratch directory (including the
differencing VHDX). An unknown/already-destroyed handle is a safe no-op. The
base VHDX is never modified.

Job VMs and their disks are ephemeral. Only base images stay on the host.
The worker holds an exclusive lock on `.owner.lock` in each scratch directory
while the job runs, and the scratch directory records the VM name in
`vm-name`. Windows releases the lock when the worker process stops. When the
worker starts, and before each boot, it sweeps the scratch root: for each
`reactorcide-vm-*` directory that it can lock, it removes the VM and then the
directory. Thus a worker crash or a failed delete does not leave VMs or disks
on the host. When `Remove-VM` fails, `Destroy` keeps the directory (it holds
the VM name) for the next sweep. A directory without a lock file is removed
after 10 minutes. Workers that share a scratch root do not remove the VMs of
each other.

The pure PowerShell builders are in
`coordinator_api/internal/worker/vmrunner/hyperv_scripts.go`. Unit tests on
every OS check them. A Windows test also parses them with the PowerShell
parser.

## Network and egress (proxy-aware only)

The guest has no network adapter. All traffic between the worker and the
guest uses Hyper-V sockets:

| Port (service ID) | Direction | Use |
| --- | --- | --- |
| 7100 | worker to guest | Job channel: files, trees, command, output, exit status |
| 7101 | guest to worker | Egress tunnel: one HTTP `CONNECT host:port` for each flow |

The guest daemon runs a local HTTP proxy on `127.0.0.1:3128`. When the
service starts, it sets these proxy settings to that proxy:

- WinHTTP: `netsh winhttp set proxy proxy-server=127.0.0.1:3128 bypass-list=<local>`.
- WinINET, for all users: the `ProxySettingsPerUser=0` policy, the machine
  `ProxyEnable`, `ProxyServer`, and `ProxyOverride` values, and the binary
  `Connections\DefaultConnectionSettings` and `SavedLegacySettings` values.
  In per-machine mode WinINET (PowerShell `Invoke-WebRequest`, .NET
  `WebClient`) reads only the binary values.
- Each job command gets `HTTP_PROXY`, `HTTPS_PROXY`, `ALL_PROXY`, and
  `NO_PROXY` (upper and lower case). A job environment value has priority.

The proxy changes each `CONNECT` request (and each plain HTTP request in
absolute form) into a tunnel through the host. A program that uses proxy
settings works. A program that ignores proxy settings fails closed, because
the guest has no network adapter. The guest has no fake DNS in this version:
a program that resolves a name before it uses the proxy fails.

Certificate revocation checks (CRL and OCSP fetches by CryptoAPI) need a
network that Windows thinks is connected. Without a network adapter, the
CryptoAPI URL fetch stops with `ERROR_NOT_CONNECTED` before it uses the
proxy, and schannel clients (Git for Windows with `http.sslBackend=schannel`,
`curl.exe`) fail with `CRYPT_E_REVOCATION_OFFLINE`. Thus, before it sets the
proxy, the daemon adds a Microsoft loopback adapter (hardware ID `*MSLOOP`,
the inbox `netloop.inf` driver) with the address `10.255.255.1/30`, no
gateway, and no DNS server. Windows then reports a connected network, and the
revocation fetches go through the proxy. The adapter connects to nothing:
traffic that does not use the proxy still fails. The daemon adds the adapter
only when it is not there, and it logs a warning when it cannot add it.

**TODO (D28): transparent redirect.** Windows has no nftables or pf. A
transparent TCP redirect needs a Windows Filtering Platform callout driver (a
signed kernel driver). Build it only when real jobs need it. The fake DNS
from the macOS daemon can then be used for Windows too.

On the host, the egress forwarder is the same as on macOS: it connects
directly when `REACTORCIDE_VM_EGRESS_PROXY` is not set, and refuses loopback,
unspecified, link-local, and multicast addresses. When the variable is set, it
sends each flow to that HTTP CONNECT proxy. See
[macOS VM Runners](./vm-runners-macos.md#network-and-egress-no-network-device).

### Where to place the bundle

```powershell
$env:REACTORCIDE_VM_IMAGE_DIR = 'C:\reactorcide\vm-images'
# a job's image "win11-base" then resolves to
#   C:\reactorcide\vm-images\win11-base\   (the bundle dir holding disk.vhdx)
```

With the `local` image source, an image reference must be a relative path
inside `REACTORCIDE_VM_IMAGE_DIR` (default: the current directory). An
absolute path, or a path that escapes the image directory, is refused.

For OCI images, use a local cache on each worker. Do not share one writable
cache directory between workers. Publish one immutable bundle to the registry,
set an image alias to its pinned reference, and let each worker cache it on
its image disk. See
[Build a Windows VM Image](./windows-vm-image-build.md#publish-and-share-the-image-through-oci-or-s3).

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

2. **Install the guest daemon service** in the guest. Copy a windows/amd64
   build of `vmguest.exe` into the guest, then run it elevated:

   ```powershell
   .\vmguest.exe install -no-start
   ```

   The command copies the program to
   `C:\Program Files\Reactorcide\vmguest.exe` and registers the automatic
   service `reactorcide-vmguest`, which runs as LocalSystem. Do not start it in
   the golden VM: it sets the system proxy to its own tunnel.

3. **Optional: create the guest account** (default `reactorcide`). If the
   account does not exist, the guest daemon creates it at the first job, as a
   member of Administrators (or as a standard user when the job sets
   `run_as.administrator: false`). Create it yourself to set its groups for
   all jobs, for example to run jobs as a standard user. Install toolchains
   for all users (machine `PATH`), because the account profile is made at the
   first job.

4. **Bake the toolchain** the jobs need (language runtimes, build tools,
   `runnerlib` prerequisites, etc.). Everything a job uses must be present in the
   guest — there is no nested container inside the guest.

5. **Do not generalize the image.** Do not run Sysprep. Turn off BitLocker
   on the OS volume, set the `PreventDeviceEncryption` policy
   (`HKLM\SYSTEM\CurrentControlSet\Control\BitLocker`, DWORD 1), and shut
   the guest down cleanly. The Reactorcide builder does this step
   automatically.

6. **Remove the network adapter** of the golden VM, or keep it only for the
   build. The job VMs never get one.

7. **Export / keep the base VHDX.** The prepared `disk.vhdx` is now your golden
   base; keep the bundle directory read-only and let each job clone it via a
   differencing disk. (You can delete the golden VM definition with
   `Remove-VM golden` — keep the VHDX file.)

### Other image builders

You can use another image builder when it produces a Generation 2 VHDX that
meets the bundle and guest requirements. Packer is one option. It is not a
Reactorcide runtime dependency.

## Guest daemon and guest account

The worker does not authenticate to the guest with a key. Only the host can
open a Hyper-V socket to a VM, and only for a registered service ID.

The guest daemon runs as LocalSystem. It runs each job command as the guest
account (`REACTORCIDE_VM_GUEST_USER`, default `reactorcide`), not as
LocalSystem, so a job has a normal user profile, `HKCU`, `%USERPROFILE%`,
`%APPDATA%`, and `%TEMP%`.

At the first job connection the daemon:

1. Creates the account if it does not exist. The new account is a member of
   Administrators, unless the job sets `run_as.administrator: false`. The
   daemon does not change the groups of an account that the image has: it
   uses the account as the image makes it.
2. Sets a new random password on the account. Only the daemon knows it.
3. Logs the account on (interactive logon). For an administrator under UAC
   it uses the elevated token, as on a hosted Windows CI runner.
4. Loads the user profile. The first job makes `C:\Users\NAME`.
5. Gives the account access to the daemon's window station and desktop.
   Without this access, programs that load `user32.dll` stop with
   `0xC0000142`.

The first line of the job log tells which account runs the command, for
example `reactorcide: running as reactorcide which is an administrator`. The
worker log has the same line with the job ID. A guest daemon of an older build
does not report the account, and the line is not shown.

```yaml
# Only for a Windows VM job, and only when the guest daemon creates the
# account. The default is true.
run_as:
  administrator: false
```

The command gets the user environment of the account, then the proxy
variables, then the job environment. If the profile has no `HOME`, the daemon
sets `HOME` to `%USERPROFILE%`, as Unix guests do. Git for Windows, OpenSSH,
and many cross-platform tools use `HOME`. The daemon gives the account full
control of each uploaded file and each directory that it makes.

The guest is a one-job VM with no network adapter, so an administrator job
can change only its own disposable guest. It can reach the network only
through the host forwarder.

A `TERM` or `KILL` request stops the process tree of the command
(`taskkill /T /F`). Windows has no POSIX signals. If the worker closes the job
channel before the command stops, the daemon stops the process tree.

The worker and the daemon must use the same protocol version. A worker
refuses a daemon with a different version. Build a new image with the new
`vmguest.exe` when the protocol version changes.

### Job input transfer

The worker sends the command, environment, workspace, source tree, `/job`
input mounts, and short-lived VCS credential files through the job channel.
It converts the standard `/job` paths to `C:\reactorcide\job` paths. Secret
values go only inside protocol frames. The worker and the daemon never write
them to a log. After the command stops, the daemon sends the workflow output
and trigger control files back, and the worker writes them to the host
workspace.

## VM resource telemetry

The worker samples each running Windows guest through a separate guest
daemon connection. It sends these series through normal job telemetry:

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

## Smoke test (validate Hyper-V + Hyper-V sockets + guest daemon)

Before you connect the full worker, validate the stack (boot, guest daemon,
egress tunnel, teardown) with the standalone `vmsmoke` command. No Cgo or
signing needed:

```powershell
cd coordinator_api
go build -o vmsmoke.exe .\cmd\vmsmoke

# Run elevated (the first run registers the Hyper-V socket services):
.\vmsmoke.exe `
  -bundle C:\reactorcide\vm-images\win11-base `
  -user reactorcide
```

It boots a guest from the bundle with no network adapter. It runs a command
through the guest daemon, runs `curl.exe https://example.com/` through the
proxy tunnel, and checks CPU, memory, and storage metrics. It then destroys
the guest. Use `-egress-url=` to skip the egress check. `vmsmoke: OK` means
that Hyper-V, the Hyper-V sockets, the guest daemon, the egress tunnel, and
VM resource collection work.

Use `-term-check` to check only that `TERM` stops the process tree of a job.
Use `-create-admin=false` to make a standard user when the daemon creates the
account. In PowerShell 5.1, write an empty value as `-egress-url=`, because
PowerShell drops an empty argument to a native program.

## Running jobs

Once the bundle and environment settings are in place:

```powershell
# Local:
reactorcide run-local --backend vm .\jobs\my-windows-job.yaml

# Worker (coordinator-mediated) selects the same backend via --container-runtime vm.
```

A `{os: windows}` job should only be scheduled onto a Windows worker.

A job that goes through the coordinator must name an image alias, or a
pinned reference that an alias pins. The coordinator refuses other Windows
VM jobs. Use `REACTORCIDE_VM_IMAGE_SOURCE=oci` on the worker, so that it can
pull the images of its image set. See
[Image Aliases and Worker Image Sets](./image-aliases.md).

### Windows job commands

With `-Command`, the PowerShell exit code shows only the result of the last
statement, so an error in an earlier statement does not make the job fail.
For this reason, start the PowerShell text of each Windows job with
`$ErrorActionPreference = 'Stop'`, and examine `$LASTEXITCODE` after each
external program.

```yaml
job:
  image: windows-vs2022
  command: |
    $ErrorActionPreference = 'Stop'
    git --version
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
    go test ./...
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
  characteristics:
    os: windows
```

Each command goes through a shell prefix, also a single-line command. A
job with the characteristic `os: windows` gets the default prefix
`powershell.exe -NoProfile -NonInteractive -Command` when it does not set
`REACTORCIDE_JOB_SHELLCMD`. The default does not add
`$ErrorActionPreference`; the command must set it. To use another shell,
set `REACTORCIDE_JOB_SHELLCMD`, for example `cmd.exe /c` or
`"C:/Program Files/PowerShell/7/pwsh.exe" -NoProfile -Command`. See
[Command Execution](./job-definitions.md#command-execution).

- `$ErrorActionPreference = 'Stop'` stops the script at the first PowerShell
  error (a cmdlet error or a `throw`). The process exit code is then 1.
- `$ErrorActionPreference` does not apply to an external program (for
  example `git.exe` or `go.exe`). Examine `$LASTEXITCODE` after each one.
- PowerShell 7.3 and later can also stop on an external program error with
  `$PSNativeCommandUseErrorActionPreference = $true`. The images use
  Windows PowerShell 5.1, which does not have this setting. Use the
  explicit `$LASTEXITCODE` check.
- The guest daemon sends the exit code of the process without a change.

## Hardware validation status

The Windows lifecycle compiles with `CGO_ENABLED=0 GOOS=windows GOARCH=amd64
go build ./...`. Unit tests cover its generated PowerShell logic and the job
channel protocol. The service install and update flow has run on a Windows 11
Pro Hyper-V host. The unattended builder created a Windows 11 Enterprise
Evaluation 25H2 image from the verified Microsoft ISO on that host. An earlier
SSH version of the smoke test passed on that host.

On 2026-11-17 the unattended builder made an image with the Hyper-V socket
guest daemon on a Windows 11 Pro host (`lab-wincide`) in about 8 minutes.
Smart App Control blocks unsigned programs, so it is off on that host. Windows
hosts with Smart App Control or another application control policy need
signed Reactorcide binaries (Authenticode), or a policy that allows them.

`vmsmoke` passed on that host in about 60 seconds for each run, with no VM left
after the run. Results of the checks:

1. Passed. The worker registers the two service IDs (it runs elevated).
2. Passed. The VM boots with no network adapter, and the guest sees no
   adapter that is up.
3. Passed after a fix. `winio.ListenHvsock` on the VM ID before `Start-VM`
   fails with "The requested address is not valid in its context". The
   lifecycle now listens after `Start-VM`.
4. Passed. The host connects to the guest daemon job channel.
5. Passed. The service starts at boot with no login. (Repeated on
   `win11-vsock-test6`, which is not generalized: the image state is
   `IMAGE_STATE_COMPLETE`, BitLocker is `FullyDecrypted`, and the job command
   starts about 32 seconds after the VM starts.)
6. Passed after two fixes. WinHTTP (`netsh winhttp show
   proxy`) and the proxy variables point to the guest proxy. `curl.exe`,
   PowerShell `Invoke-WebRequest` (HTTP and HTTPS), and .NET `WebClient`
   get HTTP 200, for an administrator and for a standard user. Before the
   fix, `Invoke-WebRequest` and `WebClient` connected directly: in
   per-machine mode WinINET reads only the binary
   `Connections\DefaultConnectionSettings` value, which the daemon now
   writes. Before the second fix, Git for Windows (MinGit 2.56.1, schannel)
   and `curl.exe` failed on `github.com` with `CRYPT_E_REVOCATION_OFFLINE`:
   the CryptoAPI URL fetch stopped with `ERROR_NOT_CONNECTED` because the
   guest had no network adapter. With the loopback adapter, `curl.exe` gets
   HTTP 200 from `github.com`, `certutil -urlfetch -verify` verifies the
   chain with OCSP and CRL through the proxy, and MinGit `ls-remote` and
   `clone` of GitHub repositories pass. The base image has no `winget`, so
   `winget` is not checked.
7. Passed, also with the loopback adapter. A program that ignores the proxy
   fails closed: `curl.exe --noproxy '*'` cannot resolve the name, a DNS
   query times out, and direct TCP and UDP connections fail ("unreachable
   network").
8. Passed. `TERM` stops the job and its `ping.exe` child in about 0.3
   seconds, and no child is left (`vmsmoke -term-check`).
9. Passed. For an account that does not exist, the first job creates it in
   Administrators (`High Mandatory Level`), or as a standard user
   (`Medium Mandatory Level`) with `run_as.administrator: false`. The job log
   starts with `reactorcide: running as NAME which is (not) an
   administrator`. `%USERPROFILE%`, `%APPDATA%`, `%TEMP%`, and `HOME` are
   below `C:\Users\NAME`.
10. Passed. The job starts `powershell.exe` and `cmd.exe` child processes,
    and their exit codes come back.
11. Passed. The daemon does not change an account that the image has. With
    `-StandardGuestUser` the image account is a standard user
    (`Medium Mandatory Level`). By default it is an administrator
    (`High Mandatory Level`).
12. Passed. The job writes and deletes a file below `C:\reactorcide\job`.
13. Passed. A worker process that stops during a job (`Stop-Process -Force`
    on `vmsmoke`) leaves one running VM and one scratch directory. The next
    worker start removes both before it boots its own job, and no VM or
    scratch directory is left after that job.

### Image aliases and image sets (2026-11-18)

On 2026-11-18 a worker on `lab-wincide` ran the image alias and image set
tests (see [Image Aliases and Worker Image Sets](./image-aliases.md)). The
coordinator, PostgreSQL, Corndogs, and a `registry:2` OCI registry ran on a
Linux development host on the same LAN. The worker connected to the
coordinator through an SSH reverse tunnel, and it pulled from the registry
over the LAN (`--vm-image-registry-plain-http`). The worker used
`REACTORCIDE_VM_IMAGE_SOURCE=oci` and an empty cache. The base image was
`win11-vsock-test6`, published with `reactorcide vm-image publish` (about
7.4 GB compressed).

1. Passed. Startup gate. The worker registered, reported `updating` for set
   version 1, pulled the image in about 2 minutes, and reported `updated`.
   A job that was submitted before the worker started stayed `submitted`
   until then. The first lease came 1 second after `image set updated`.
2. Passed. A job with `image: rcval-win` and `os: windows` went to the
   worker and completed. The worker log (`spawning leased job`), the
   coordinator log (`Job leased to worker`), and the job record
   (`image_display`) show `rcval-win (REF@sha256:...)`. The job log stream
   does not have this line (see below).
3. Passed. Live update. A 15-minute job ran on the old pin. `images alias
   set` gave the new pin (outcome `pending`). The worker reported
   `updating` for version 2, pulled the new image in about 2 minutes, and
   reported `updated` with both digests. A job submitted before the
   promotion got the old pin and ran while the long job ran. The promotion
   came 28 seconds after the report. A job after the promotion got the new
   pin (the provisioning marker was present). The long job completed on
   the old pin.
4. Passed. A pending pin with a digest that is not in the registry: the
   worker stayed ready, reported `failed` (`1 with a permanent failure`,
   `not found`), and ran a new job on the active pin. The alias did not
   promote. `images rollout status` shows the problem. Setting the active
   reference again cancelled the pending pin.
5. Passed (behavior after the review fixes). A new worker and a second
   alias that pins a digest that is not in the registry: the pull is a
   permanent failure, so the gate opened. The worker ran jobs for the good
   alias, and the job for the bad alias stayed `submitted`. With the bad
   alias on a store that cannot be reached (a closed port), the pull is a
   transient failure: the gate stayed closed, the worker sent no
   `RequestJob` for 2.5 minutes, and a job for the good alias stayed
   `submitted`. The retries came after 30 seconds and then 60 seconds.
   When the bad alias was deleted, the newer set opened the gate and the
   job ran.
6. Passed. When the long job ended, the old digest left the set (set
   version 4, about 80 seconds after the job end). The worker logged
   `removed VM image that left the image set`, and the cache removed the
   bundle and its layer blob (43.8 GB to 21.7 GB). No VM and no scratch
   directory stayed.
7. Passed. `reactorcide vm-image build windows --from <published base
   ref> --provision <script> --publish <ref>` pulled the base from the
   registry, ran the script as `reactorcide` (an administrator), shut the
   guest down, made a standalone VHDX, and published it. The total time
   was 5 minutes. No VM, scratch directory, or build directory stayed. The
   result was the new pin of test 3.

Notes from this run:

- The job log stream had no `alias (ref)` line. Owner decision I2 names the
  job log header. Fixed after this run: the first line of the job log of a
  VM job, or of a job that names an alias, is
  `[reactorcide] image: <alias> (<ref>)` (or `[reactorcide] image: <ref>`
  for a VM job without an alias).
- `REACTORCIDE_JOB_SHELLCMD` used only the first word and one more
  argument. `powershell.exe -NoProfile -NonInteractive -Command` gave
  PowerShell the text `-NoProfile -NonInteractive -Command` as one
  argument. Fixed after this run: the worker splits the value at each
  space, so each flag is one argument. Use quotes for a path that has a
  space. A backslash is not an escape character, thus a Windows path
  stays correct.
- With `-Command`, the PowerShell exit code shows only the result of the
  last statement. An error in an earlier statement does not change the
  exit code to non-zero.
  The guest daemon sends the exit code of the process without a change.
  To stop a job at the first error, start the script with
  `$ErrorActionPreference = 'Stop'`, and examine `$LASTEXITCODE` after
  each external program (see [Windows job commands](#windows-job-commands)).
- `reactorcide workers list` needed a browser session at the time of this
  run. Fixed after this run: an API token with `workers:read` or
  `workers:manage` for the pool organization can use it (see
  [CLI reference](./cli-reference.md)).

Check of the two fixes (2026-11-18, later run). A new worker with an empty
cache pulled the alias `rcv18-win` (the published `win11-vsock-test6`
image) and ran a job with
`REACTORCIDE_JOB_SHELLCMD: powershell.exe -NoProfile -NonInteractive
-Command`:

1. Passed. The job completed with exit code 0 and no argument error. In the
   guest, `[Environment]::CommandLine` was `powershell.exe -NoProfile
   -NonInteractive -Command "<script>"`. Thus each flag was one argument.
2. Passed. The first line of the job log was `[reactorcide] image:
   rcv18-win (10.19.81.15:25000/rcv18/windows@sha256:1fe7d00b...)`. The
   second line was `reactorcide: running as reactorcide which is an
   administrator`.

The pull took about 90 seconds over the LAN. The job took about 45 seconds
from the lease to the end, VM boot included. No VM and no scratch directory
stayed.

Second session (2026-11-18, the same rig):

1. Passed. The default shell prefix for a single-line command. The job had
   `os: windows` and no `REACTORCIDE_JOB_SHELLCMD`, so the worker used
   `powershell.exe -NoProfile -NonInteractive -Command`. The command
   `$x = 2; Write-Output "sum=$($x+3)"; Get-Location` printed `sum=5` and
   the location. Thus `;`, `$(...)`, and the variable are PowerShell syntax.
2. Passed. The command `exit 3` failed the job with exit code 3.
3. Passed. A retry after an alias promotion, for a job that named the
   alias and for a job that named a pinned reference directly. Each retry
   ran on the new pin. `reactorcide jobs get` showed `image_display` (the
   new image) and `image_retry_of` (the old image). The job log had the
   lines `[reactorcide] image: ...` and `[reactorcide] retry of: ...`.
4. Timings. From `images alias set` (the new pin) to the promotion: 50 to
   70 seconds. From the new pin to the removal of the old image: 75 to 90
   seconds. The pull of a new pin that has the same layer blob: about 20
   seconds.
5. Passed (a coordinator check, not a host check). A token with only
   `workers:read` ran `reactorcide workers list`
   (with the `IMAGES` column) and `reactorcide workers pools list`. The
   coordinator refused the same token for `reactorcide workers drain` and
   for `reactorcide images alias list`.
