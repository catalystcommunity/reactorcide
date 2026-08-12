# Build a Windows VM Image

Reactorcide can prepare a Windows guest image without a console session. The
builder uses PowerShell, Hyper-V, DISM, BCDBoot, and OpenSSH. These components
are part of Windows 11 Pro with Hyper-V and OpenSSH Client enabled. The builder
does not need Packer, WinRM, RDP, VMConnect, or the Windows ADK.

The build has no guest interaction. You must supply a licensed Windows ISO and
its expected SHA-256 value.

## Build result

The default command creates these files:

```text
C:\ProgramData\Reactorcide\vm-images\win11-base\disk.vhdx
C:\ProgramData\Reactorcide\vm-images\win11-base\ssh_host_ed25519_key.pub
C:\ProgramData\Reactorcide\secrets\guest-ssh-key
C:\ProgramData\Reactorcide\secrets\guest-ssh-key.pub
C:\ProgramData\Reactorcide\config\guest-ssh-host.pub
```

The worker private key stays on the host. The guest image contains only its
matching public key. The builder copies the guest SSH host public key to the
worker config directory.

## Requirements

Use a Windows 11 Pro, Enterprise, or Education host, or use Windows Server. The
host must have these items:

- Hyper-V
- OpenSSH Client
- An elevated PowerShell session
- A Hyper-V switch with DHCP and internet access
- A Windows installation ISO
- Enough disk space for the ISO, build files, and output VHDX

The default switch is `Default Switch`. The guest uses Windows Update to
install OpenSSH Server. If your network blocks Windows capability downloads,
the build stops. Configure a Windows Features on Demand source before you run
the builder in that environment.

The current answer file selects the `en-US` locale. The default image name is
`Windows 11 Pro`. Use `-ImageName` when the ISO uses a different image name.
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

Run this command in an elevated SSH PowerShell session:

```powershell
Set-ExecutionPolicy -Scope Process Bypass -Force
Set-Location C:\Users\micro\reactorcide-windows
.\build-windows-image.ps1 `
  -IsoPath C:\ISO\Windows11.iso `
  -IsoSha256 EXPECTED_64_CHARACTER_SHA256
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
  -OutputDirectory C:\ProgramData\Reactorcide\vm-images\win11-base `
  -SwitchName 'Default Switch' `
  -GuestUser reactorcide `
  -CPUs 4 `
  -MemoryMB 4096 `
  -DiskSizeGB 80 `
  -TimeoutMinutes 120
```

Use separate state, image, and temporary build disks when the host has them:

```powershell
.\build-windows-image.ps1 `
  -IsoPath C:\ISO\Windows11.iso `
  -IsoSha256 EXPECTED_64_CHARACTER_SHA256 `
  -StateDirectory D:\Reactorcide\state `
  -OutputDirectory E:\Reactorcide\images\win11-base `
  -BuildDirectory F:\Reactorcide\image-builds
```

`StateDirectory` receives the worker private key and guest host public key.
`OutputDirectory` receives the reusable bundle. `BuildDirectory` holds the
large temporary VHDX while Windows is applied and prepared.

## Add tools without guest interaction

Use `-ProvisionScript` to run one or more PowerShell scripts in the guest. The
builder copies the scripts to the offline disk. It runs them in the order that
you give them. Each script runs as an administrator before the builder removes
administrator access from the guest job account.

```powershell
.\build-windows-image.ps1 `
  -IsoPath C:\ISO\Windows11.iso `
  -IsoSha256 EXPECTED_64_CHARACTER_SHA256 `
  -ProvisionScript .\provision\install-go.ps1, .\provision\validate-tools.ps1
```

A provisioning script must meet these requirements:

- It must not show a prompt.
- It must return a nonzero exit code when it fails.
- It must verify downloaded-file checksums.
- It must not store credentials in the image.
- It must not restart or stop the guest.

The builder stops and deletes incomplete output when a script fails.

## What the builder does

The builder completes these operations:

1. It verifies the ISO SHA-256.
2. It generates a dedicated Ed25519 worker key when the key does not exist.
3. It creates a dynamic GPT VHDX with EFI, MSR, and Windows partitions.
4. It applies the selected Windows image with DISM.
5. It adds an unattended first-boot file and the worker public key.
6. It boots a temporary Generation 2 VM with Secure Boot.
7. Windows completes OOBE without user input.
8. The guest installs OpenSSH Server and generates stable host keys.
9. The guest enables key authentication and disables SSH password
   authentication.
10. The guest creates the job directory and enables Hyper-V Data Exchange.
11. The guest runs the selected provisioning scripts.
12. The guest changes its local password to an unknown random value.
13. The guest removes the job account from the Administrators group.
14. The guest removes cached answer files and shuts down.
15. The host captures the guest SSH host public key and seals the bundle.

The temporary build password exists only in the temporary offline answer file.
The builder does not print it. The guest changes the password and removes the
cached answer files before image capture. The host deletes temporary build
files after success or failure.

## Validate the image

Run the smoke test after the build:

```powershell
C:\Users\micro\reactorcide-deploy\vmsmoke.exe `
  -bundle C:\ProgramData\Reactorcide\vm-images\win11-base `
  -user reactorcide `
  -key C:\ProgramData\Reactorcide\secrets\guest-ssh-key
```

The result must end with `vmsmoke: OK`. The test must leave no VM in Hyper-V.

The service config example already uses the generated private-key and host-key
paths. Add the real coordinator URL and worker enrollment-token file before
you start the service.

## Publish and share the image through OCI

Publish the complete Windows bundle after validation:

```powershell
& 'C:\Program Files\Reactorcide\reactorcide.exe' vm-image publish `
  E:\Reactorcide\images\win11-base `
  registry.example.com/reactorcide/windows-11:base
```

The command publishes `disk.vhdx` and `ssh_host_ed25519_key.pub` as one
compressed OCI artifact. It prints a digest reference. Use that immutable
digest in jobs and worker prefetch configuration.

Configure each worker with these values:

```text
REACTORCIDE_VM_IMAGE_SOURCE=oci
REACTORCIDE_VM_IMAGE_CACHE_DIR=E:\Reactorcide\oci-cache
REACTORCIDE_VM_REGISTRY_AUTH_FILE=D:\Reactorcide\state\config\oci-auth.json
```

The worker downloads a referenced image once and uses its local cache for later
jobs. It verifies the guest host key from the resolved OCI bundle. Hundreds of
workers can use the same digest, but each worker must have a separate writable
cache. The registry and its blob storage deduplicate the shared artifact.

Use `--vm-image-prefetch` on the worker command to download important images
before the worker registers. Use the digest reference, not a mutable tag.

## Credential roles

The host uses three different identity records:

- The coordinator enrollment token authorizes the worker to join one worker
  pool. Create it in the coordinator worker administration page. Store it in
  `C:\ProgramData\Reactorcide\secrets\enrollment-token`.
- The worker guest key authenticates the host to each guest clone. The image
  builder generates this key pair. It keeps the private key on the host and
  puts only the public key in the image.
- The guest SSH host key authenticates each guest clone to the worker. The
  image builder generates it in the guest and copies only its public key to
  the host.

The worker also creates a stable `worker_key` in its data directory when it
starts for the first time. This value identifies the registered worker. It is
not an enrollment token and it is not an SSH key.

Do not reuse a personal SSH key as the worker guest key. Do not copy the guest
private host key out of the image.

## Image identity limits

The builder does not run Sysprep after it configures SSH. A job clone must
start SSH immediately and must not enter OOBE. As a result, clones have the
same local machine identity and SSH host key.

Do not join these ephemeral clones to a Windows domain. Do not use this image
for jobs that require a unique persistent machine identity. The worker checks
the captured host key because all clones come from the same trusted base.

Build a separate image and key pair for each independent trust domain. Do not
publish a credential-bearing image for unrelated operators.
