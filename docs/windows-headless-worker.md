# Headless Windows Worker

This procedure installs a Reactorcide worker on a Windows host. It uses
OpenSSH, Hyper-V, and the Windows Service Control Manager. It does not install a
service wrapper.

## Requirements

The host must have these items:

- Windows 11 Pro, Enterprise, or Education, or Windows Server
- Hyper-V
- OpenSSH Server
- An administrator account that can use SSH public-key authentication
- A Reactorcide worker enrollment token
- A prepared Hyper-V guest image for VM jobs

Use [Build a Windows VM Image](./windows-vm-image-build.md) to create the image
and guest SSH credentials without RDP or VMConnect.

Use an elevated SSH session. This command must return `True`:

```powershell
([Security.Principal.WindowsPrincipal] [Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole(
  [Security.Principal.WindowsBuiltInRole]::Administrator
)
```

## Build and copy the files

Build the Windows files from the repository root on a Go build host:

```sh
cd coordinator_api
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o reactorcide.exe .
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o vmsmoke.exe ./cmd/vmsmoke
```

Copy the files to the Windows account. This example uses the OpenSSH home
directory:

```sh
scp reactorcide.exe vmsmoke.exe \
  ../deployment/windows/install-reactorcide.ps1 \
  ../deployment/windows/worker-service.example.json \
  micro@windows-host:
```

## Prepare the service configuration

Rename `worker-service.example.json` to `worker-service.json`. Set the real
coordinator URL. Set the guest user and the guest file paths for your image.

Do not put a token, a private key, or a password in the JSON file. Put each
secret in a file. The example config refers to these files:

```text
C:\ProgramData\Reactorcide\secrets\enrollment-token
C:\ProgramData\Reactorcide\secrets\guest-ssh-key
```

The service runs as `LocalSystem`. The install script gives access to
`LocalSystem` and the local Administrators group. It removes inherited access
from `C:\ProgramData\Reactorcide`.

Create the enrollment-token file without terminal output. One safe method is
to copy an existing protected file with `scp`. Do not use `Write-Output`,
`Get-Content`, or a command-line argument for the token.

The guest host-key file is not secret. It lets the worker verify the guest SSH
server. See [Windows VM Runners](./vm-runners-windows.md#guest-credentials-prototype)
for the guest key procedure.

## Install and start the service

Run these commands in the elevated SSH PowerShell session:

```powershell
Set-ExecutionPolicy -Scope Process Bypass -Force
.\install-reactorcide.ps1 `
  -BinaryPath .\reactorcide.exe `
  -ConfigPath .\worker-service.json `
  -EnrollmentTokenPath .\enrollment-token `
  -Start
```

The installer copies the token to the protected ProgramData secrets
directory. Remove the staging token file after the install succeeds.

### Put VM data on other disks

The binary, durable state, image cache, and running VM data use separate
roots. Set them during installation:

```powershell
.\install-reactorcide.ps1 `
  -BinaryPath .\reactorcide.exe `
  -ConfigPath .\worker-service.json `
  -EnrollmentTokenPath .\enrollment-token `
  -InstallDirectory 'C:\Program Files\Reactorcide' `
  -StateDirectory 'D:\Reactorcide\state' `
  -ImageSource oci `
  -ImageDirectory 'E:\Reactorcide\images' `
  -ImageCacheDirectory 'E:\Reactorcide\oci-cache' `
  -VMScratchDirectory 'F:\Reactorcide\vm-runs' `
  -Start
```

`StateDirectory` contains config, secrets, logs, and the stable worker key.
`ImageDirectory` contains local image bundles. `ImageCacheDirectory` contains
downloaded OCI content and materialized read-only bundles.
`VMScratchDirectory` contains per-job differencing disks and workspaces.

The image and scratch roots can use different local disks. Do not use one
writable OCI cache directory from multiple worker processes. Each worker must
have its own cache. The registry shares the immutable image content.

The service has this name:

```text
reactorcide-worker
```

It starts automatically after a delayed start. The Service Control Manager
restarts it after an unexpected exit.

Use these commands to operate it:

```powershell
& 'C:\Program Files\Reactorcide\reactorcide.exe' windows-service status
& 'C:\Program Files\Reactorcide\reactorcide.exe' windows-service stop
& 'C:\Program Files\Reactorcide\reactorcide.exe' windows-service start
& 'C:\Program Files\Reactorcide\reactorcide.exe' windows-service restart
Get-Content 'C:\ProgramData\Reactorcide\logs\worker.log' -Tail 100
```

## Update the worker

Build and copy a new `reactorcide.exe`. Then run these commands:

```powershell
& 'C:\Program Files\Reactorcide\reactorcide.exe' windows-service stop
Copy-Item .\reactorcide.exe 'C:\Program Files\Reactorcide\reactorcide.exe' -Force
& 'C:\Program Files\Reactorcide\reactorcide.exe' windows-service start
```

Stop the service before you replace the executable. Windows locks a running
executable.

## Test Hyper-V before you accept jobs

The VM smoke test needs a prepared bundle. The bundle must contain
`disk.vhdx`. The guest must have OpenSSH Server, the guest public key, and the
Hyper-V Data Exchange service.

Run this command in the elevated SSH session:

```powershell
.\vmsmoke.exe `
  -bundle 'C:\ProgramData\Reactorcide\vm-images\win11-base' `
  -user reactorcide `
  -key 'C:\ProgramData\Reactorcide\secrets\guest-ssh-key'
```

The test creates an ephemeral differencing disk and VM. It waits for guest SSH,
runs a command, and removes the VM. A successful test ends with
`vmsmoke: OK`.

## Remove the service

Run these commands:

```powershell
& 'C:\Program Files\Reactorcide\reactorcide.exe' windows-service stop
& 'C:\Program Files\Reactorcide\reactorcide.exe' windows-service uninstall
```

The uninstall command removes the service registration. It does not remove the
executable, config, logs, images, or secret files.
