# Build a Windows VM Image

Reactorcide can prepare a Windows guest image without a console session. The
builder uses PowerShell, Hyper-V, DISM, and BCDBoot. These components are part
of Windows 11 Pro with Hyper-V enabled. The builder does not need Packer,
WinRM, RDP, VMConnect, or the Windows ADK. OpenSSH is only for the remote
administration of the host (copy files, run the builder from another
computer).

The build has no guest interaction. Microsoft provides a public Windows 11
Enterprise Evaluation ISO. The repository has a script that downloads the
current supported example and verifies the Microsoft SHA-256 value.

## Build result

The default command creates this file:

```text
C:\ProgramData\Reactorcide\vm-images\win11-base\disk.vhdx
```

The image contains the Reactorcide guest daemon service (`vmguest.exe`,
service `reactorcide-vmguest`). It contains no SSH server, no SSH keys, and no
worker credential. Job VMs from this image have no network adapter. The worker
reaches the guest daemon through Hyper-V sockets. See
[Windows VM Runners](./vm-runners-windows.md).

The builder does not generalize the image (no Sysprep). It shuts the guest
down cleanly after provisioning, and every job VM boots the same Windows
installation. See [Image identity](#image-identity). This process does not
need a console session.

## Requirements

Use a Windows 11 Pro, Enterprise, or Education host, or use Windows Server. The
host must have these items:

- Hyper-V
- OpenSSH Client and OpenSSH Server, for remote administration (optional)
- An elevated PowerShell session
- A Hyper-V switch with DHCP and internet access
- A Windows installation ISO
- A windows/amd64 build of the guest daemon:

  ```sh
  cd coordinator_api
  GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -o vmguest.exe ./cmd/vmguest
  ```

- Enough disk space for the ISO, build files, and output VHDX

Run the host preparation script on a new Windows host:

```powershell
Set-ExecutionPolicy -Scope Process Bypass -Force
.\prepare-windows-host.ps1
```

Restart Windows if the script tells you to restart it. The script enables
Hyper-V, OpenSSH Client, and OpenSSH Server. It does not install a third-party
service.

The default switch is `Default Switch`. Only the temporary build VM uses the
switch. Provisioning scripts use it to download tools. The job VMs have no
network adapter.

The current answer file selects the `en-US` locale. The default image name is
`Windows 11 Pro`. The public evaluation ISO uses `Windows 11 Enterprise
Evaluation`. Use `-ImageName` when the ISO uses a different image name.
This command lists the image names:

```powershell
$iso = Mount-DiskImage -ImagePath C:\ISO\Windows11.iso -PassThru
$root = (($iso | Get-Volume).DriveLetter + ':\')
Get-WindowsImage -ImagePath (Join-Path $root 'sources\install.wim') |
  Select-Object ImageIndex, ImageName
Dismount-DiskImage -ImagePath C:\ISO\Windows11.iso
```

Some ISOs use `install.esd`. Use that file in the list command when
`install.wim` is not present.

## Copy the builder to the host

Copy the complete `deployment/windows` directory to the host. The image
builder uses files in its `image` subdirectory.

```sh
scp -r deployment/windows micro@windows-host:reactorcide-windows
```

## Build the base image

Download the public evaluation ISO on the Windows host:

```powershell
.\download-windows-evaluation.ps1
```

The script downloads Windows 11 Enterprise Evaluation 25H2 from an official
Microsoft redirect. It checks the ISO against the SHA-256 value in the
Microsoft hash document. The evaluation is valid for 90 days. It is suitable
for an example and for VM runner tests. Use media with the correct license for
long-lived production workers.

See the [Microsoft Evaluation Center](https://www.microsoft.com/en-us/evalcenter/evaluate-windows-11-enterprise)
for the release terms and the current download information.

The script refuses to replace a file that has a different hash. You can put
the ISO on another disk:

```powershell
.\download-windows-evaluation.ps1 `
  -Destination E:\Reactorcide\downloads\windows-11-enterprise-evaluation.iso
```

Run this command in an elevated SSH PowerShell session:

```powershell
Set-ExecutionPolicy -Scope Process Bypass -Force
Set-Location C:\Users\micro\reactorcide-windows
.\build-windows-image.ps1 `
  -IsoPath C:\ProgramData\Reactorcide\downloads\windows-11-enterprise-evaluation-25h2-en-us.iso `
  -IsoSha256 A61ADEAB895EF5A4DB436E0A7011C92A2FF17BB0357F58B13BBC4062E535E7B9 `
  -ImageName 'Windows 11 Enterprise Evaluation' `
  -GuestDaemon .\vmguest.exe
```

Do not use a SHA-256 value that you calculated from an untrusted download as
the only source of trust. Get the expected value from the image publisher or
from your controlled artifact store.

The command refuses to replace an existing output directory. Remove or move an
old bundle before you build a replacement.

Use these options when necessary:

```powershell
.\build-windows-image.ps1 `
  -IsoPath C:\ISO\Windows11.iso `
  -IsoSha256 EXPECTED_64_CHARACTER_SHA256 `
  -ImageName 'Windows 11 Pro' `
  -GuestDaemon .\vmguest.exe `
  -OutputDirectory C:\ProgramData\Reactorcide\vm-images\win11-base `
  -SwitchName 'Default Switch' `
  -GuestUser reactorcide `
  -StandardGuestUser `
  -CPUs 4 `
  -MemoryMB 4096 `
  -DiskSizeGB 80 `
  -TimeoutMinutes 120
```

Use separate image and temporary build disks when the host has them:

```powershell
.\build-windows-image.ps1 `
  -IsoPath C:\ISO\Windows11.iso `
  -IsoSha256 EXPECTED_64_CHARACTER_SHA256 `
  -GuestDaemon .\vmguest.exe `
  -OutputDirectory E:\Reactorcide\images\win11-base `
  -BuildDirectory F:\Reactorcide\image-builds
```

`OutputDirectory` receives the reusable bundle. `BuildDirectory` holds the
large temporary VHDX while Windows is applied and prepared.

By default the guest job account is a member of Administrators, as on a
hosted Windows CI runner. Use `-StandardGuestUser` to remove it from
Administrators. The guest daemon does not change the groups of an account that
the image has.

## Add tools without guest interaction

Use `-ProvisionScript` to run one or more PowerShell scripts in the guest. The
builder copies the scripts to the offline disk. It runs them in the order that
you give them. Each script runs as an administrator, before the builder removes
administrator access from the guest job account when you use
`-StandardGuestUser`.

```powershell
.\build-windows-image.ps1 `
  -IsoPath C:\ISO\Windows11.iso `
  -IsoSha256 EXPECTED_64_CHARACTER_SHA256 `
  -GuestDaemon .\vmguest.exe `
  -ProvisionScript .\provision\install-go.ps1, .\provision\validate-tools.ps1
```

A provisioning script must meet these requirements:

- It must not show a prompt.
- It must return a nonzero exit code when it fails.
- It must verify downloaded-file checksums.
- It must not store credentials in the image.
- It must not restart or stop the guest.

The builder stops and deletes incomplete output when a script fails.

## Build a derived Windows image

Use `reactorcide vm-image build windows` to add tools to an existing base
image. This command does not need the ISO. It is the Windows equivalent of
`vm-image build macos`. Use `-ProvisionScript` of `build-windows-image.ps1`
only for a new base image.

Run the command in an elevated PowerShell session on the Hyper-V host:

```powershell
$env:REACTORCIDE_VM_SCRATCH_DIR = 'C:\ProgramData\Reactorcide\vm-scratch'
& 'C:\Program Files\Reactorcide\reactorcide.exe' vm-image build windows `
  --from C:\ProgramData\Reactorcide\vm-images\win11-base `
  --output C:\ProgramData\Reactorcide\vm-images\win11-git `
  --provision .\provision\install-mingit.ps1 `
  --provision .\provision\validate-tools.ps1 `
  --guest-daemon .\vmguest.exe `
  --cpus 4 `
  --memory 8Gi `
  --publish registry.example.com/reactorcide/windows-11-git:1
```

The options are:

| Option | Default | Description |
| --- | --- | --- |
| `--from` | (required) | Base bundle directory, OCI reference, or `s3://` reference. A directory that exists is a local bundle. A reference is pulled into `--cache-dir` first. |
| `--output` | (required) | New bundle directory. It must not exist. |
| `--provision` | none | PowerShell script file. Use the option again for more scripts. The scripts run in the order that you give them. |
| `--guest-user` | `reactorcide` | Guest account that runs the scripts. |
| `--guest-daemon` | none | A windows/amd64 `vmguest.exe`. The build copies it into the image before the first boot. |
| `--cpus`, `--memory` | `4`, `8Gi` | Size of the build VM. |
| `--shutdown-timeout` | `30m` | Time limit for the BitLocker decryption and the guest shutdown. |
| `--publish` | none | Publish the new bundle to this OCI or `s3://` reference. The command prints the pinned reference. |

The command does these steps:

1. It makes a differencing disk of the base in a locked scratch directory
   (`REACTORCIDE_VM_SCRATCH_DIR`, or the system temporary directory). The
   base VHDX does not change.
2. It sets the BitLocker `PreventDeviceEncryption` policy in the offline
   registry of the differencing disk. If you use `--guest-daemon`, it also
   copies the binary into the differencing disk. Windows 11 turns on BitLocker
   device encryption at the first boot of a VM that has a virtual TPM. The
   key protector is the virtual TPM of that VM, and a job VM has a different
   virtual TPM. The policy stays in the new image, so job VMs from a derived
   image do not encrypt their disk.
3. It boots a Generation 2 VM with no network adapter.
4. It runs each script with Windows PowerShell 5.1 (`-File`) as the guest
   account, through the guest daemon. When the account is an administrator,
   the script gets the elevated token. Egress goes through the Hyper-V socket
   tunnel and the host egress forwarder, like a job. Set
   `REACTORCIDE_VM_EGRESS_PROXY` to send it through an egress proxy.
5. As LocalSystem, it turns off BitLocker if it is on and waits until the
   OS volume is fully decrypted. It removes the build files from the job
   directory. Then it shuts the guest down cleanly.
6. It removes the VM and keeps the differencing disk. `Convert-VHD` then
   writes a new standalone dynamic VHDX in the output bundle. `Convert-VHD`
   reads the disk through its parent, so the new disk has all the data and no
   parent. The command does not use `Merge-VHD`, because `Merge-VHD` writes
   into the parent (the base image).
7. It mounts the new disk read-only and checks that it has the guest daemon
   and that BitLocker is off on its OS volume.
8. It makes the bundle read-only and moves it to `--output`.

The build does not run Sysprep. A job VM from a base image of
`build-windows-image.ps1` does not run Windows setup again: it keeps the
computer name of the base, and the job starts in approximately one minute. A
generalized derived image runs specialization, a restart, and OOBE at each
job boot. The guest daemon answers before that restart, and the job fails.
Thus a derived image has the same Windows identity as its base. Do not join
these VMs to a domain.

If a step fails, or if you push Ctrl+C, the command removes the build VM, the
differencing disk, and the incomplete output. If the process stops before it
can do this, the scratch directory and the incomplete output directory
(`.reactorcide-image-build-*` next to `--output`) have no lock. The next worker
start, job boot, or image build removes the VM and the scratch directory. The
next image build to the same parent directory removes the incomplete output
directory.

Windows PowerShell can write its progress records to standard error as
`#< CLIXML` text. To prevent this, set `$ProgressPreference =
'SilentlyContinue'` at the start of a script.

The scripts must obey the rules in
[Add tools without guest interaction](#add-tools-without-guest-interaction).
A script that changes the machine `PATH` or other machine settings keeps the
change in the image. Do not put credentials in the image.

You can use a derived image as the base of another build.

## What the builder does

The builder completes these operations:

1. It verifies the ISO SHA-256.
2. It creates a dynamic GPT VHDX with EFI, MSR, and Windows partitions.
3. It applies the selected Windows image with DISM.
4. It copies the guest daemon to `C:\Program Files\Reactorcide\vmguest.exe`
   and adds an unattended first-boot file. It sets the BitLocker
   `PreventDeviceEncryption` policy in the offline system hive.
5. It boots a temporary Generation 2 VM with Secure Boot.
6. It enables a virtual TPM and disables automatic checkpoints for the
   temporary VM.
7. Windows completes OOBE without user input.
8. The guest creates the job directory.
9. The guest runs the selected provisioning scripts.
10. The guest changes its local password to an unknown random value.
11. With `-StandardGuestUser`, the guest removes the job account from the
    Administrators group.
12. The guest installs the guest daemon service (`vmguest.exe install
    -no-start`). The service does not start in the build VM, because it sets
    the system proxy to its own tunnel. It starts at the first boot of each job
    VM.
13. The guest removes cached answer files. It turns off BitLocker if it is
    on, and waits until the OS volume is fully decrypted. Then it shuts
    down.
14. The host checks the guest daemon and seals the bundle.

The temporary build password exists only in the temporary offline answer file.
The builder does not print it. The guest changes the password and removes the
cached answer files before image capture. The host deletes temporary build
files after success or failure.

## Validate the image

Run the smoke test after the build:

```powershell
C:\Users\micro\reactorcide-deploy\vmsmoke.exe `
  -bundle C:\ProgramData\Reactorcide\vm-images\win11-base `
  -user reactorcide
```

The result must end with `vmsmoke: OK`. The test must leave no VM in Hyper-V.

Add the real coordinator URL and worker enrollment-token file to the service
config before you start the service.

## Publish and share the image through OCI or S3

Publish the complete Windows bundle after validation:

```powershell
& 'C:\Program Files\Reactorcide\reactorcide.exe' vm-image publish `
  E:\Reactorcide\images\win11-base `
  registry.example.com/reactorcide/windows-11:base
```

The command publishes `disk.vhdx` as one compressed OCI artifact. It prints a
pinned digest reference. Set an image alias to that reference (`reactorcide
images alias set`), and use the alias name in jobs. See
[Image Aliases and Worker Image Sets](./image-aliases.md).

To use S3, give an `s3://bucket/prefix/name:tag` reference. The command
writes an OCI image layout under the prefix and prints
`s3://bucket/prefix/name:tag@sha256:DIGEST`. The digest is the same as in an
OCI registry. Reactorcide gets the S3 credentials from the standard AWS
chain. See [VM Images Quick Start](./vm-images-quickstart.md#s3).

The key-free Windows bundle uses the version 2 Windows layer media type. A
bundle without the guest daemon does not work with the current worker: the
lifecycle refuses it before boot. Build and publish a new image. Do not reuse
a bundle that contains SSH keys.

Configure each worker with these values:

```text
REACTORCIDE_VM_IMAGE_SOURCE=oci
REACTORCIDE_VM_IMAGE_CACHE_DIR=E:\Reactorcide\oci-cache
REACTORCIDE_VM_REGISTRY_AUTH_FILE=D:\Reactorcide\state\config\oci-auth.json
```

The `oci` source also accepts `s3://` references. The worker needs the pinned
form (`...@sha256:DIGEST`). For an S3-compatible service, also set
`REACTORCIDE_VM_S3_ENDPOINT`.

The worker downloads a referenced image once and uses its local cache for later
jobs. Hundreds of workers can use the same digest, but each worker must have a
separate writable cache. The registry and its blob storage deduplicate the
shared artifact.

The worker pulls the images of its image set (the aliases that it can
serve) before it claims a job. Use `--vm-image-prefetch` on the worker
command only for an image that is not in the image set. The worker pulls it
before it registers.
Use the digest reference, not a mutable tag.

Registries use HTTPS by default. For an isolated development registry that
does not use TLS, add `--vm-image-registry-plain-http HOST` to the worker
command. The option has no environment variable equivalent.

## Credential roles

The host uses these identity records:

- The coordinator enrollment token authorizes the worker to join one worker
  pool. Create it in the coordinator worker administration page. Store it in
  `C:\ProgramData\Reactorcide\secrets\enrollment-token`.
- The guest needs no credential. Only the host can open a Hyper-V socket to
  a VM, and only for a registered service ID.

The worker also creates a stable `worker_key` in its data directory when it
starts for the first time. This value identifies the registered worker. It is
not an enrollment token.

## Image identity

All job VMs from one image have the same Windows identity: the computer name
`RCIDE-BASE`, the same machine SID, and the image state
`IMAGE_STATE_COMPLETE`. This is intentional:

- A generalized image runs specialization, a restart, and OOBE at each job
  boot. On hardware this made jobs slow, and the guest daemon lost its
  connection at the restart or could not load the user profile.
- A job VM has no network adapter and is deleted after the job. No other
  computer sees its identity.

The builder also prevents BitLocker device encryption. It sets the
`PreventDeviceEncryption` policy in the offline system hive before the first
boot, and the guest turns off BitLocker before capture if it is on. Each job
VM has a new virtual TPM, which cannot unlock a disk that another TPM
encrypted.

Older images (for example `win11-vsock-test5`) were built with an interrupted
Sysprep. Their clones also had the computer name `RCIDE-BASE`, but they
started BitLocker encryption in each job VM. Build those images again.

Do not join these short-lived clones to a Windows domain. Use a separate image
and lifecycle for domain-managed machines.
