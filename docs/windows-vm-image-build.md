# Build a Windows VM Image

Reactorcide can prepare a Windows guest image without a console session. The
builder uses PowerShell, Hyper-V, DISM, BCDBoot, and OpenSSH. These components
are part of Windows 11 Pro with Hyper-V and OpenSSH Client enabled. The builder
does not need Packer, WinRM, RDP, VMConnect, or the Windows ADK.

The build has no guest interaction. Microsoft provides a public Windows 11
Enterprise Evaluation ISO. The repository has a script that downloads the
current supported example and verifies the Microsoft SHA-256 value.

## Build result

The default command creates this file:

```text
C:\ProgramData\Reactorcide\vm-images\win11-base\disk.vhdx
```

The image contains no SSH client key, SSH authorization key, or SSH host key.
The worker creates all SSH keys for each VM job.

The builder runs Sysprep with the `generalize` option before it captures the
image. The worker writes a new unattended specialization file into each VM
clone. Each clone gets a unique Windows computer name. This process does not
need a console session.

## Requirements

Use a Windows 11 Pro, Enterprise, or Education host, or use Windows Server. The
host must have these items:

- Hyper-V
- OpenSSH Client
- An elevated PowerShell session
- A Hyper-V switch with DHCP and internet access
- A Windows installation ISO
- Enough disk space for the ISO, build files, and output VHDX

Run the host preparation script on a new Windows host:

```powershell
Set-ExecutionPolicy -Scope Process Bypass -Force
.\prepare-windows-host.ps1
```

Restart Windows if the script tells you to restart it. The script enables
Hyper-V, OpenSSH Client, and OpenSSH Server. It does not install a third-party
service.

The default switch is `Default Switch`. The guest uses Windows Update to
install OpenSSH Server. If your network blocks Windows capability downloads,
the build stops. Configure a Windows Features on Demand source before you run
the builder in that environment.

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
  -ImageName 'Windows 11 Enterprise Evaluation'
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

Use separate image and temporary build disks when the host has them:

```powershell
.\build-windows-image.ps1 `
  -IsoPath C:\ISO\Windows11.iso `
  -IsoSha256 EXPECTED_64_CHARACTER_SHA256 `
  -OutputDirectory E:\Reactorcide\images\win11-base `
  -BuildDirectory F:\Reactorcide\image-builds
```

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
2. It creates a dynamic GPT VHDX with EFI, MSR, and Windows partitions.
3. It applies the selected Windows image with DISM.
4. It adds an unattended first-boot file.
5. It boots a temporary Generation 2 VM with Secure Boot.
6. It enables a virtual TPM and disables automatic checkpoints for the
   temporary VM.
7. Windows completes OOBE without user input.
8. The guest installs OpenSSH Server.
9. The guest enables key authentication, disables SSH password
   authentication, selects the injected Ed25519 host key, and permits SSH on
   all Windows firewall profiles.
10. The guest creates the job directory and enables Hyper-V Data Exchange.
11. The guest runs the selected provisioning scripts.
12. The guest changes its local password to an unknown random value.
13. The guest removes the job account from the Administrators group.
14. The guest removes all SSH authorization and host keys.
15. The guest removes cached answer files and shuts down.
16. The host seals the bundle.

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

## Publish and share the image through OCI

Publish the complete Windows bundle after validation:

```powershell
& 'C:\Program Files\Reactorcide\reactorcide.exe' vm-image publish `
  E:\Reactorcide\images\win11-base `
  registry.example.com/reactorcide/windows-11:base
```

The command publishes `disk.vhdx` as one compressed OCI artifact. It prints a
digest reference. Use that immutable digest in jobs and worker prefetch
configuration.

The key-free Windows bundle uses the version 2 Windows layer media type. Build
and publish a new image. Do not reuse a version 1 bundle that contains SSH
keys.

Configure each worker with these values:

```text
REACTORCIDE_VM_IMAGE_SOURCE=oci
REACTORCIDE_VM_IMAGE_CACHE_DIR=E:\Reactorcide\oci-cache
REACTORCIDE_VM_REGISTRY_AUTH_FILE=D:\Reactorcide\state\config\oci-auth.json
```

The worker downloads a referenced image once and uses its local cache for later
jobs. Hundreds of workers can use the same digest, but each worker must have a
separate writable cache. The registry and its blob storage deduplicate the
shared artifact.

Use `--vm-image-prefetch` on the worker command to download important images
before the worker registers. Use the digest reference, not a mutable tag.

Registries use HTTPS by default. For an isolated development registry that
does not use TLS, add `--vm-image-registry-plain-http HOST` to the worker
command. The option has no environment variable equivalent.

## Credential roles

The host uses these identity records:

- The coordinator enrollment token authorizes the worker to join one worker
  pool. Create it in the coordinator worker administration page. Store it in
  `C:\ProgramData\Reactorcide\secrets\enrollment-token`.
- A new client key authenticates the worker to one guest clone. The worker
  keeps the private key in memory. It injects the public key into the per-job
  differencing disk before boot.
- A new host key authenticates one guest clone to the worker. The worker puts
  the private key only in the per-job differencing disk. It pins the matching
  public key for that SSH connection.

The worker also creates a stable `worker_key` in its data directory when it
starts for the first time. This value identifies the registered worker. It is
not an enrollment token and it is not an SSH key.

Do not copy a per-job private key from the VM scratch directory.

## Image identity

The builder runs Sysprep before it captures the base image. The worker writes
an unattended file into each clone before boot. Windows then gives each clone
a unique computer name and machine identity. Each clone also has different SSH
keys.

Do not join these short-lived clones to a Windows domain. Use a separate image
and lifecycle for domain-managed machines.
