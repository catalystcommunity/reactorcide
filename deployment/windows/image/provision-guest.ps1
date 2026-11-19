[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [ValidatePattern('^[A-Za-z][A-Za-z0-9_-]{0,19}$')]
    [string]$GuestUser,

    # Remove the guest account from Administrators. By default it stays an
    # administrator.
    [switch]$StandardUser
)

$ErrorActionPreference = 'Stop'
$setupDirectory = 'C:\ReactorcideSetup'
$failurePath = Join-Path $setupDirectory 'failed.txt'
$completePath = Join-Path $setupDirectory 'complete.txt'

try {
    $guestDaemon = 'C:\Program Files\Reactorcide\vmguest.exe'
    if (-not (Test-Path -LiteralPath $guestDaemon)) {
        throw 'The Reactorcide guest daemon is not in the image.'
    }

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

    if ($StandardUser) {
        $administrators = Get-LocalGroup -SID 'S-1-5-32-544'
        Remove-LocalGroupMember -Group $administrators.Name -Member $GuestUser -ErrorAction SilentlyContinue
    }

    # Install the guest daemon service last and do not start it: it sets the
    # system proxy to its own tunnel, and this build VM still uses its own
    # network adapter. The service starts at the first boot of each job VM,
    # which has no network adapter.
    & $guestDaemon install -no-start
    if ($LASTEXITCODE -ne 0) {
        throw 'The Reactorcide guest daemon service could not be installed.'
    }
    Set-Content -LiteralPath (Join-Path $setupDirectory 'vmguest-installed.txt') -Value 'ready' -Encoding ascii

    $winlogonPath = 'HKLM:\SOFTWARE\Microsoft\Windows NT\CurrentVersion\Winlogon'
    Remove-ItemProperty -Path $winlogonPath -Name AutoAdminLogon, DefaultUserName, DefaultPassword, AutoLogonCount -ErrorAction SilentlyContinue

    Get-ChildItem -LiteralPath 'C:\Windows\Panther' -Filter '*unattend*.xml' -File -Recurse -ErrorAction SilentlyContinue |
        Remove-Item -Force -ErrorAction SilentlyContinue
    Remove-Item -LiteralPath 'C:\Windows\System32\Sysprep\Unattend.xml' -Force -ErrorAction SilentlyContinue

    # Do not generalize the image (no Sysprep). A generalized image runs
    # specialize, a restart, and OOBE at each job boot. Job VMs have no
    # network adapter and are deleted after the job, so they can share one
    # Windows identity. The image must not be encrypted: each job VM has a
    # new virtual TPM.
    $osVolume = Get-BitLockerVolume -MountPoint $env:SystemDrive -ErrorAction SilentlyContinue
    if ($osVolume -and $osVolume.VolumeStatus -ne 'FullyDecrypted') {
        Disable-BitLocker -MountPoint $env:SystemDrive | Out-Null
        $deadline = (Get-Date).AddMinutes(30)
        while ((Get-BitLockerVolume -MountPoint $env:SystemDrive).VolumeStatus -ne 'FullyDecrypted') {
            if ((Get-Date) -gt $deadline) {
                throw 'The system volume did not decrypt in 30 minutes.'
            }
            Start-Sleep -Seconds 5
        }
    }

    Set-Content -LiteralPath $completePath -Value 'ready' -Encoding ascii
    Set-Content -LiteralPath (Join-Path $setupDirectory 'capture-requested.txt') -Value 'ready' -Encoding ascii
} catch {
    $_.Exception.Message | Set-Content -LiteralPath $failurePath -Encoding utf8
} finally {
    Stop-Computer -Force
}
