[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [string]$BinaryPath,

    [Parameter(Mandatory = $true)]
    [string]$ConfigPath,

    [string]$EnrollmentTokenPath,

    [string]$InstallDirectory = 'C:\Program Files\Reactorcide',

    [string]$StateDirectory = 'C:\ProgramData\Reactorcide',

    [ValidateSet('local', 'oci')]
    [string]$ImageSource = 'local',

    [string]$ImageDirectory,

    [string]$ImageCacheDirectory,

    [string]$VMScratchDirectory,

    [switch]$Start
)

$ErrorActionPreference = 'Stop'

$identity = [Security.Principal.WindowsIdentity]::GetCurrent()
$principal = [Security.Principal.WindowsPrincipal]::new($identity)
if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    throw 'Run this script in an elevated PowerShell session.'
}

$sourceBinary = (Resolve-Path -LiteralPath $BinaryPath).Path
$sourceConfig = (Resolve-Path -LiteralPath $ConfigPath).Path
$sourceEnrollmentToken = $null
if ($EnrollmentTokenPath) {
    $sourceEnrollmentToken = (Resolve-Path -LiteralPath $EnrollmentTokenPath).Path
}
$stateRoot = [IO.Path]::GetFullPath($StateDirectory)
if (-not $ImageDirectory) {
    $ImageDirectory = Join-Path $stateRoot 'vm-images'
}
if (-not $ImageCacheDirectory) {
    $ImageCacheDirectory = Join-Path $ImageDirectory 'oci-cache'
}
if (-not $VMScratchDirectory) {
    $VMScratchDirectory = Join-Path $stateRoot 'vm-scratch'
}
$imageRoot = [IO.Path]::GetFullPath($ImageDirectory)
$imageCacheRoot = [IO.Path]::GetFullPath($ImageCacheDirectory)
$vmScratchRoot = [IO.Path]::GetFullPath($VMScratchDirectory)
$installedBinary = Join-Path $InstallDirectory 'reactorcide.exe'
$installedConfig = Join-Path $stateRoot 'config\worker-service.json'

if (Get-Service -Name 'reactorcide-worker' -ErrorAction SilentlyContinue) {
    Stop-Service -Name 'reactorcide-worker' -ErrorAction SilentlyContinue
    & $installedBinary windows-service uninstall
    if ($LASTEXITCODE -ne 0) {
        throw 'The existing Reactorcide service could not be removed.'
    }
}

@(
    $InstallDirectory,
    (Split-Path -Parent $installedConfig),
    (Join-Path $stateRoot 'data'),
    (Join-Path $stateRoot 'logs'),
    (Join-Path $stateRoot 'secrets'),
    $imageRoot,
    $imageCacheRoot,
    $vmScratchRoot,
    (Join-Path $vmScratchRoot 'workspaces')
) | ForEach-Object {
    New-Item -ItemType Directory -Path $_ -Force | Out-Null
}

Copy-Item -LiteralPath $sourceBinary -Destination $installedBinary -Force
$serviceConfig = Get-Content -LiteralPath $sourceConfig -Raw | ConvertFrom-Json
function Set-WorkerArgument {
    param([object]$Config, [string]$Name, [string]$Value)

    $arguments = @($Config.arguments)
    $index = [Array]::IndexOf([string[]]$arguments, $Name)
    if ($index -ge 0) {
        if ($index + 1 -ge $arguments.Count) {
            throw "The service config argument has no value: $Name"
        }
        $arguments[$index + 1] = $Value
    } else {
        $arguments += @($Name, $Value)
    }
    $Config.arguments = $arguments
}

Set-WorkerArgument -Config $serviceConfig -Name '--enrollment-token-file' -Value (Join-Path $stateRoot 'secrets\enrollment-token')
Set-WorkerArgument -Config $serviceConfig -Name '--data-dir' -Value (Join-Path $stateRoot 'data')
Set-WorkerArgument -Config $serviceConfig -Name '--workspace-dir' -Value (Join-Path $vmScratchRoot 'workspaces')
if ($null -eq $serviceConfig.environment) {
    $serviceConfig | Add-Member -NotePropertyName 'environment' -NotePropertyValue ([pscustomobject]@{}) -Force
}
$serviceConfig.environment | Add-Member -NotePropertyName 'REACTORCIDE_VM_IMAGE_SOURCE' -NotePropertyValue $ImageSource -Force
$serviceConfig.environment | Add-Member -NotePropertyName 'REACTORCIDE_VM_IMAGE_DIR' -NotePropertyValue $imageRoot -Force
$serviceConfig.environment | Add-Member -NotePropertyName 'REACTORCIDE_VM_IMAGE_CACHE_DIR' -NotePropertyValue $imageCacheRoot -Force
$serviceConfig.environment | Add-Member -NotePropertyName 'REACTORCIDE_VM_SCRATCH_DIR' -NotePropertyValue $vmScratchRoot -Force
$serviceConfig.environment | Add-Member -NotePropertyName 'REACTORCIDE_VM_REGISTRY_AUTH_FILE' -NotePropertyValue (Join-Path $stateRoot 'config\oci-auth.json') -Force
$serviceConfig.environment.PSObject.Properties.Remove('REACTORCIDE_VM_SSH_PRIVATE_KEY_FILE')
$serviceConfig.environment.PSObject.Properties.Remove('REACTORCIDE_VM_SSH_HOST_KEY_FILE')
$serviceConfig.log_file = Join-Path $stateRoot 'logs\worker.log'
$serviceConfig | ConvertTo-Json -Depth 8 | Set-Content -LiteralPath $installedConfig -Encoding utf8
if ($sourceEnrollmentToken) {
    Copy-Item -LiteralPath $sourceEnrollmentToken -Destination (Join-Path $stateRoot 'secrets\enrollment-token') -Force
}

foreach ($path in @($stateRoot, $imageRoot, $imageCacheRoot, $vmScratchRoot) | Select-Object -Unique) {
    & icacls.exe $path /inheritance:r /grant:r '*S-1-5-18:(OI)(CI)F' '*S-1-5-32-544:(OI)(CI)F' | Out-Null
    if ($LASTEXITCODE -ne 0) {
        throw "The directory access rules could not be set: $path"
    }
}

& $installedBinary windows-service install --config $installedConfig
if ($LASTEXITCODE -ne 0) {
    throw 'The Reactorcide service could not be installed.'
}

if ($Start) {
    if (-not (Test-Path -LiteralPath (Join-Path $stateRoot 'secrets\enrollment-token'))) {
        throw 'The enrollment-token file is required before the service can start.'
    }
    & $installedBinary windows-service start
    if ($LASTEXITCODE -ne 0) {
        throw 'The Reactorcide service could not be started.'
    }
}

Write-Output "Installed $installedBinary"
Write-Output "Installed $installedConfig"
Write-Output 'Service name: reactorcide-worker'
