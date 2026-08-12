[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [ValidatePattern('^[A-Za-z][A-Za-z0-9_-]{0,19}$')]
    [string]$GuestUser
)

$ErrorActionPreference = 'Stop'
$setupDirectory = 'C:\ReactorcideSetup'
$failurePath = Join-Path $setupDirectory 'failed.txt'
$completePath = Join-Path $setupDirectory 'complete.txt'

try {
    $capability = Get-WindowsCapability -Online -Name 'OpenSSH.Server~~~~0.0.1.0'
    if ($capability.State -ne 'Installed') {
        Add-WindowsCapability -Online -Name 'OpenSSH.Server~~~~0.0.1.0' | Out-Null
    }

    $sshDirectory = Join-Path $env:ProgramData 'ssh'
    Set-Service -Name sshd -StartupType Automatic
    Start-Service -Name sshd
    Stop-Service -Name sshd
    & (Join-Path $env:SystemRoot 'System32\OpenSSH\ssh-keygen.exe') -A
    if ($LASTEXITCODE -ne 0) {
        throw 'OpenSSH could not generate its host keys.'
    }

    $userProfile = Join-Path 'C:\Users' $GuestUser
    $userSSHDirectory = Join-Path $userProfile '.ssh'
    $authorizedKeys = Join-Path $userSSHDirectory 'authorized_keys'
    New-Item -ItemType Directory -Path $userSSHDirectory -Force | Out-Null
    Copy-Item -LiteralPath (Join-Path $setupDirectory 'worker-key.pub') -Destination $authorizedKeys -Force

    & icacls.exe $userSSHDirectory /inheritance:r /grant:r "${GuestUser}:(OI)(CI)F" '*S-1-5-18:(OI)(CI)F' '*S-1-5-32-544:(OI)(CI)F' | Out-Null
    if ($LASTEXITCODE -ne 0) {
        throw 'The guest SSH directory access rules could not be set.'
    }
    & icacls.exe $authorizedKeys /inheritance:r /grant:r "${GuestUser}:F" '*S-1-5-18:F' '*S-1-5-32-544:F' | Out-Null
    if ($LASTEXITCODE -ne 0) {
        throw 'The authorized_keys access rules could not be set.'
    }

    $sshdConfigPath = Join-Path $sshDirectory 'sshd_config'
    if (-not (Test-Path -LiteralPath $sshdConfigPath)) {
        Copy-Item -LiteralPath (Join-Path $env:SystemRoot 'System32\OpenSSH\sshd_config_default') -Destination $sshdConfigPath
    }
    $sshdConfig = Get-Content -LiteralPath $sshdConfigPath -Raw
    function Add-GlobalSSHDDirective {
        param([string]$Config, [string]$Line)

        $match = [regex]::Match($Config, '(?m)^\s*Match\s+')
        if ($match.Success) {
            return $Config.Insert($match.Index, $Line + "`r`n")
        }
        return $Config + "`r`n" + $Line + "`r`n"
    }

    foreach ($option in @{
        PubkeyAuthentication = 'yes'
        PasswordAuthentication = 'no'
        KbdInteractiveAuthentication = 'no'
    }.GetEnumerator()) {
        $pattern = '(?m)^\s*#?\s*' + [regex]::Escape($option.Key) + '\s+.*$'
        $line = $option.Key + ' ' + $option.Value
        if ([regex]::IsMatch($sshdConfig, $pattern)) {
            $sshdConfig = [regex]::Replace($sshdConfig, $pattern, $line)
        } else {
            $sshdConfig = Add-GlobalSSHDDirective -Config $sshdConfig -Line $line
        }
    }
    $sshdConfig = Add-GlobalSSHDDirective -Config $sshdConfig -Line "AllowUsers $GuestUser"
    Set-Content -LiteralPath $sshdConfigPath -Value $sshdConfig -Encoding ascii

    & (Join-Path $env:SystemRoot 'System32\OpenSSH\sshd.exe') -t
    if ($LASTEXITCODE -ne 0) {
        throw 'The generated sshd_config file is not valid.'
    }

    Start-Service -Name sshd
    if (-not (Get-NetFirewallRule -Name 'OpenSSH-Server-In-TCP' -ErrorAction SilentlyContinue)) {
        New-NetFirewallRule -Name 'OpenSSH-Server-In-TCP' -DisplayName 'OpenSSH Server (sshd)' -Enabled True -Direction Inbound -Protocol TCP -Action Allow -LocalPort 22 | Out-Null
    }

    Set-Service -Name vmickvpexchange -StartupType Automatic
    Start-Service -Name vmickvpexchange

    New-Item -ItemType Directory -Path 'C:\reactorcide\job' -Force | Out-Null
    & icacls.exe 'C:\reactorcide' /inheritance:r /grant:r "${GuestUser}:(OI)(CI)F" '*S-1-5-18:(OI)(CI)F' '*S-1-5-32-544:(OI)(CI)F' | Out-Null
    if ($LASTEXITCODE -ne 0) {
        throw 'The guest job directory access rules could not be set.'
    }

    $provisionDirectory = Join-Path $setupDirectory 'provision'
    if (Test-Path -LiteralPath $provisionDirectory) {
        Get-ChildItem -LiteralPath $provisionDirectory -Filter '*.ps1' -File | Sort-Object Name | ForEach-Object {
            & powershell.exe -NoLogo -NoProfile -NonInteractive -ExecutionPolicy Bypass -File $_.FullName
            if ($LASTEXITCODE -ne 0) {
                throw "The provisioning script failed: $($_.Name)"
            }
        }
    }

    $passwordBytes = New-Object byte[] 48
    $random = [Security.Cryptography.RandomNumberGenerator]::Create()
    $random.GetBytes($passwordBytes)
    $random.Dispose()
    $runtimePassword = [Convert]::ToBase64String($passwordBytes) | ConvertTo-SecureString -AsPlainText -Force
    Set-LocalUser -Name $GuestUser -Password $runtimePassword
    [Array]::Clear($passwordBytes, 0, $passwordBytes.Length)

    $administrators = Get-LocalGroup -SID 'S-1-5-32-544'
    Remove-LocalGroupMember -Group $administrators.Name -Member $GuestUser -ErrorAction SilentlyContinue

    $winlogonPath = 'HKLM:\SOFTWARE\Microsoft\Windows NT\CurrentVersion\Winlogon'
    Remove-ItemProperty -Path $winlogonPath -Name AutoAdminLogon, DefaultUserName, DefaultPassword, AutoLogonCount -ErrorAction SilentlyContinue

    Get-ChildItem -LiteralPath 'C:\Windows\Panther' -Filter '*unattend*.xml' -File -Recurse -ErrorAction SilentlyContinue |
        Remove-Item -Force -ErrorAction SilentlyContinue
    Remove-Item -LiteralPath 'C:\Windows\System32\Sysprep\Unattend.xml' -Force -ErrorAction SilentlyContinue

    Set-Content -LiteralPath $completePath -Value 'ready' -Encoding ascii
} catch {
    $_.Exception.Message | Set-Content -LiteralPath $failurePath -Encoding utf8
} finally {
    Stop-Computer -Force
}
