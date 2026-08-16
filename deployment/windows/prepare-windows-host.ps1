[CmdletBinding()]
param(
    [switch]$SkipHyperV,
    [switch]$SkipOpenSSHServer
)

$ErrorActionPreference = 'Stop'
$identity = [Security.Principal.WindowsIdentity]::GetCurrent()
$principal = [Security.Principal.WindowsPrincipal]::new($identity)
if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    throw 'Run this script in an elevated PowerShell session.'
}

$restartRequired = $false
if (-not $SkipHyperV) {
    $result = Enable-WindowsOptionalFeature -Online -FeatureName Microsoft-Hyper-V-All -All -NoRestart
    if ($result.RestartNeeded) {
        $restartRequired = $true
    }
}

foreach ($capability in @('OpenSSH.Client~~~~0.0.1.0')) {
    if ((Get-WindowsCapability -Online -Name $capability).State -ne 'Installed') {
        Add-WindowsCapability -Online -Name $capability | Out-Null
    }
}

if (-not $SkipOpenSSHServer) {
    $serverCapability = 'OpenSSH.Server~~~~0.0.1.0'
    if ((Get-WindowsCapability -Online -Name $serverCapability).State -ne 'Installed') {
        Add-WindowsCapability -Online -Name $serverCapability | Out-Null
    }
    Set-Service -Name sshd -StartupType Automatic
    Start-Service -Name sshd
    if (-not (Get-NetFirewallRule -Name 'OpenSSH-Server-In-TCP' -ErrorAction SilentlyContinue)) {
        New-NetFirewallRule -Name 'OpenSSH-Server-In-TCP' -DisplayName 'OpenSSH Server (sshd)' -Enabled True -Direction Inbound -Protocol TCP -Action Allow -LocalPort 22 | Out-Null
    }
}

if ($restartRequired) {
    Write-Warning 'Restart Windows before you build or run a Hyper-V VM.'
} else {
    Write-Output 'The Windows host has the required features.'
}
