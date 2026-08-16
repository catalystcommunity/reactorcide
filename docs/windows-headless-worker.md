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
without RDP or VMConnect.

The repository also has `prepare-windows-host.ps1`. Use it on a new host to
enable Hyper-V and OpenSSH. Restart the host if the script tells you to do so.

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
coordinator URL and guest user.

The coordinator URL must use HTTPS. Reactorcide sends the enrollment token,
session credentials, job secrets, and logs on this connection. For an isolated
development network, you can add `--allow-insecure-transport` to the service
arguments. This exception has no environment variable equivalent.

Do not put the enrollment token in the JSON file. The example config refers to
this file:

```text
C:\ProgramData\Reactorcide\secrets\enrollment-token
```

The service runs as `LocalSystem`. The install script gives access to
`LocalSystem` and the local Administrators group. It removes inherited access
from `C:\ProgramData\Reactorcide`.

Create the enrollment-token file without terminal output. One safe method is
to copy an existing protected file with `scp`. Do not use `Write-Output`,
`Get-Content`, or a command-line argument for the token.

The worker creates and injects new guest SSH keys for each VM job. The service
does not need a guest key file.

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
`disk.vhdx`. The guest must have OpenSSH Server and the Hyper-V Data Exchange
service. The base image must not contain SSH keys.

Run this command in the elevated SSH session:

```powershell
.\vmsmoke.exe `
  -bundle 'C:\ProgramData\Reactorcide\vm-images\win11-base' `
  -user reactorcide
```

The test creates an ephemeral differencing disk and VM. It waits for guest SSH,
runs a command, checks CPU, memory, and storage metrics, and removes the VM. A
successful test ends with `vmsmoke: OK`.

## Remove the service

Run these commands:

```powershell
& 'C:\Program Files\Reactorcide\reactorcide.exe' windows-service stop
& 'C:\Program Files\Reactorcide\reactorcide.exe' windows-service uninstall
```

The uninstall command removes the service registration. It does not remove the
executable, config, logs, images, or secret files.
