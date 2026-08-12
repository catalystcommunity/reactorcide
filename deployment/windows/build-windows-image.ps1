[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [string]$IsoPath,

    [Parameter(Mandatory = $true)]
    [ValidatePattern('^[A-Fa-f0-9]{64}$')]
    [string]$IsoSha256,

    [string]$ImageName = 'Windows 11 Pro',

    [string]$OutputDirectory = 'C:\ProgramData\Reactorcide\vm-images\win11-base',

    [string]$StateDirectory = 'C:\ProgramData\Reactorcide',

    [string]$WorkerKeyPath,

    [string]$GuestHostKeyPath,

    [string]$BuildDirectory,

    [ValidatePattern('^[A-Za-z][A-Za-z0-9_-]{0,19}$')]
    [string]$GuestUser = 'reactorcide',

    [string]$SwitchName = 'Default Switch',

    [ValidateRange(2, 64)]
    [int]$CPUs = 4,

    [ValidateRange(2048, 131072)]
    [int]$MemoryMB = 4096,

    [ValidateRange(40, 2048)]
    [int]$DiskSizeGB = 80,

    [string[]]$ProvisionScript = @(),

    [ValidateRange(15, 360)]
    [int]$TimeoutMinutes = 120
)

$ErrorActionPreference = 'Stop'
$identity = [Security.Principal.WindowsIdentity]::GetCurrent()
$principal = [Security.Principal.WindowsPrincipal]::new($identity)
if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    throw 'Run this script in an elevated PowerShell session.'
}

foreach ($command in @('New-VHD', 'New-VM', 'Get-WindowsImage', 'ssh-keygen.exe')) {
    if (-not (Get-Command $command -ErrorAction SilentlyContinue)) {
        throw "The required command is not available: $command"
    }
}

$stateRoot = [IO.Path]::GetFullPath($StateDirectory)
$outputRoot = [IO.Path]::GetFullPath($OutputDirectory)
if (-not $WorkerKeyPath) {
    $WorkerKeyPath = Join-Path $stateRoot 'secrets\guest-ssh-key'
}
if (-not $GuestHostKeyPath) {
    $GuestHostKeyPath = Join-Path $stateRoot 'config\guest-ssh-host.pub'
}
if (-not $BuildDirectory) {
    $BuildDirectory = Join-Path (Split-Path -Parent $outputRoot) '.image-builds'
}
$buildDirectoryRoot = [IO.Path]::GetFullPath($BuildDirectory)
$resolvedIsoPath = (Resolve-Path -LiteralPath $IsoPath).Path
$actualIsoSha256 = (Get-FileHash -LiteralPath $resolvedIsoPath -Algorithm SHA256).Hash
if (-not $actualIsoSha256.Equals($IsoSha256, [StringComparison]::OrdinalIgnoreCase)) {
    throw 'The Windows ISO SHA-256 does not match the supplied value.'
}

if (Test-Path -LiteralPath $outputRoot) {
    throw "The output directory already exists: $outputRoot"
}

$scriptDirectory = Split-Path -Parent $MyInvocation.MyCommand.Path
$templateDirectory = Join-Path $scriptDirectory 'image'
$buildRoot = Join-Path $buildDirectoryRoot ([guid]::NewGuid().ToString('N'))
$buildVHDX = Join-Path $buildRoot 'disk.vhdx'
$vmName = 'reactorcide-image-' + [guid]::NewGuid().ToString('N').Substring(0, 12)
$iso = $null
$vhdMounted = $false
$vmCreated = $false
$buildComplete = $false

try {
    New-Item -ItemType Directory -Path $buildRoot -Force | Out-Null
    New-Item -ItemType Directory -Path (Split-Path -Parent $WorkerKeyPath) -Force | Out-Null

    if (-not (Test-Path -LiteralPath $WorkerKeyPath)) {
        $startInfo = [Diagnostics.ProcessStartInfo]::new()
        $startInfo.FileName = (Get-Command ssh-keygen.exe).Source
        $startInfo.Arguments = '-q -t ed25519 -N "" -C reactorcide-vm-worker -f "' + $WorkerKeyPath + '"'
        $startInfo.UseShellExecute = $false
        $keygen = [Diagnostics.Process]::Start($startInfo)
        $keygen.WaitForExit()
        if ($keygen.ExitCode -ne 0) {
            throw 'ssh-keygen could not create the worker guest key.'
        }
    }
    if (-not (Test-Path -LiteralPath ($WorkerKeyPath + '.pub'))) {
        throw 'The worker guest public key is missing.'
    }

    & icacls.exe $WorkerKeyPath /inheritance:r /grant:r '*S-1-5-18:F' '*S-1-5-32-544:F' | Out-Null
    if ($LASTEXITCODE -ne 0) {
        throw 'The worker guest private-key access rules could not be set.'
    }

    $iso = Mount-DiskImage -ImagePath $resolvedIsoPath -PassThru
    $isoVolume = $iso | Get-Volume
    $isoRoot = $isoVolume.DriveLetter + ':\'
    $installImagePath = Join-Path $isoRoot 'sources\install.wim'
    if (-not (Test-Path -LiteralPath $installImagePath)) {
        $installImagePath = Join-Path $isoRoot 'sources\install.esd'
    }
    if (-not (Test-Path -LiteralPath $installImagePath)) {
        throw 'The ISO does not contain sources\install.wim or sources\install.esd.'
    }

    $selectedImages = @(Get-WindowsImage -ImagePath $installImagePath | Where-Object { $_.ImageName -eq $ImageName })
    if ($selectedImages.Count -ne 1) {
        throw "The ISO does not contain the requested image: $ImageName"
    }
    $selectedImage = $selectedImages[0]

    New-VHD -Path $buildVHDX -Dynamic -SizeBytes ($DiskSizeGB * 1GB) | Out-Null
    $mountedVHD = Mount-VHD -Path $buildVHDX -PassThru
    $vhdMounted = $true
    $disk = $mountedVHD | Get-Disk
    Initialize-Disk -Number $disk.Number -PartitionStyle GPT

    $efiPartition = New-Partition -DiskNumber $disk.Number -Size 260MB -AssignDriveLetter -GptType '{C12A7328-F81F-11D2-BA4B-00A0C93EC93B}'
    Format-Volume -Partition $efiPartition -FileSystem FAT32 -NewFileSystemLabel 'System' -Confirm:$false | Out-Null
    New-Partition -DiskNumber $disk.Number -Size 16MB -GptType '{E3C9E316-0B5C-4DB8-817D-F92DF00215AE}' | Out-Null
    $windowsPartition = New-Partition -DiskNumber $disk.Number -UseMaximumSize -AssignDriveLetter
    Format-Volume -Partition $windowsPartition -FileSystem NTFS -NewFileSystemLabel 'Windows' -Confirm:$false | Out-Null

    $windowsRoot = $windowsPartition.DriveLetter + ':\'
    & dism.exe /Apply-Image "/ImageFile:$installImagePath" "/Index:$($selectedImage.ImageIndex)" "/ApplyDir:$windowsRoot"
    if ($LASTEXITCODE -ne 0) {
        throw 'DISM could not apply the selected Windows image.'
    }

    & bcdboot.exe (Join-Path $windowsRoot 'Windows') /s ($efiPartition.DriveLetter + ':') /f UEFI
    if ($LASTEXITCODE -ne 0) {
        throw 'BCDBoot could not prepare the guest boot files.'
    }

    $guestSetupDirectory = Join-Path $windowsRoot 'ReactorcideSetup'
    New-Item -ItemType Directory -Path $guestSetupDirectory -Force | Out-Null
    Copy-Item -LiteralPath (Join-Path $templateDirectory 'provision-guest.ps1') -Destination $guestSetupDirectory
    Copy-Item -LiteralPath ($WorkerKeyPath + '.pub') -Destination (Join-Path $guestSetupDirectory 'worker-key.pub')
    if ($ProvisionScript.Count -gt 0) {
        $guestProvisionDirectory = Join-Path $guestSetupDirectory 'provision'
        New-Item -ItemType Directory -Path $guestProvisionDirectory -Force | Out-Null
        for ($index = 0; $index -lt $ProvisionScript.Count; $index++) {
            $sourceScript = (Resolve-Path -LiteralPath $ProvisionScript[$index]).Path
            $destinationName = '{0:D3}.ps1' -f $index
            Copy-Item -LiteralPath $sourceScript -Destination (Join-Path $guestProvisionDirectory $destinationName)
        }
    }

    $password = 'R!c9-' + [guid]::NewGuid().ToString('N')
    $unattend = Get-Content -LiteralPath (Join-Path $templateDirectory 'Autounattend.xml.template') -Raw
    $unattend = $unattend.Replace('__GUEST_USER__', $GuestUser)
    $unattend = $unattend.Replace('__BUILD_PASSWORD__', [System.Security.SecurityElement]::Escape($password))
    $unattendDirectory = Join-Path $windowsRoot 'Windows\Panther\Unattend'
    New-Item -ItemType Directory -Path $unattendDirectory -Force | Out-Null
    Set-Content -LiteralPath (Join-Path $unattendDirectory 'Unattend.xml') -Value $unattend -Encoding utf8
    $password = $null
    $unattend = $null

    Dismount-VHD -Path $buildVHDX
    $vhdMounted = $false
    Dismount-DiskImage -ImagePath $resolvedIsoPath
    $iso = $null

    New-VM -Name $vmName -Generation 2 -MemoryStartupBytes ($MemoryMB * 1MB) -VHDPath $buildVHDX -SwitchName $SwitchName | Out-Null
    $vmCreated = $true
    Set-VMProcessor -VMName $vmName -Count $CPUs
    Set-VMFirmware -VMName $vmName -EnableSecureBoot On -SecureBootTemplate 'MicrosoftWindows'
    Set-VM -VMName $vmName -AutomaticStopAction TurnOff
    Start-VM -Name $vmName

    $deadline = (Get-Date).AddMinutes($TimeoutMinutes)
    do {
        Start-Sleep -Seconds 5
        $state = (Get-VM -Name $vmName).State
    } while ($state -ne 'Off' -and (Get-Date) -lt $deadline)

    if ($state -ne 'Off') {
        throw "The image build did not finish within $TimeoutMinutes minutes."
    }

    Remove-VM -Name $vmName -Force
    $vmCreated = $false

    $mountedVHD = Mount-VHD -Path $buildVHDX -PassThru
    $vhdMounted = $true
    $windowsPartition = $mountedVHD | Get-Disk | Get-Partition | Where-Object { $_.Type -eq 'Basic' } | Sort-Object Size -Descending | Select-Object -First 1
    if (-not $windowsPartition.DriveLetter) {
        $windowsPartition | Add-PartitionAccessPath -AssignDriveLetter
        $windowsPartition = $mountedVHD | Get-Disk | Get-Partition | Where-Object { $_.Type -eq 'Basic' } | Sort-Object Size -Descending | Select-Object -First 1
    }
    $windowsRoot = $windowsPartition.DriveLetter + ':\'
    $failurePath = Join-Path $windowsRoot 'ReactorcideSetup\failed.txt'
    if (Test-Path -LiteralPath $failurePath) {
        throw 'Guest provisioning failed before image capture.'
    }
    if (-not (Test-Path -LiteralPath (Join-Path $windowsRoot 'ReactorcideSetup\complete.txt'))) {
        throw 'The guest shut down before provisioning completed.'
    }
    $cachedAnswerFiles = @(Get-ChildItem -LiteralPath (Join-Path $windowsRoot 'Windows\Panther') -Filter '*unattend*.xml' -File -Recurse -ErrorAction SilentlyContinue)
    if ($cachedAnswerFiles.Count -gt 0 -or (Test-Path -LiteralPath (Join-Path $windowsRoot 'Windows\System32\Sysprep\Unattend.xml'))) {
        throw 'The guest did not remove all cached answer files.'
    }
    $guestHostKeyPath = Join-Path $windowsRoot 'ProgramData\ssh\ssh_host_ed25519_key.pub'
    if (-not (Test-Path -LiteralPath $guestHostKeyPath)) {
        throw 'The guest SSH host public key is missing.'
    }

    New-Item -ItemType Directory -Path $outputRoot | Out-Null
    Copy-Item -LiteralPath $guestHostKeyPath -Destination (Join-Path $outputRoot 'ssh_host_ed25519_key.pub')
    New-Item -ItemType Directory -Path (Split-Path -Parent $GuestHostKeyPath) -Force | Out-Null
    Copy-Item -LiteralPath $guestHostKeyPath -Destination $GuestHostKeyPath -Force
    Dismount-VHD -Path $buildVHDX
    $vhdMounted = $false
    Move-Item -LiteralPath $buildVHDX -Destination (Join-Path $outputRoot 'disk.vhdx')
    $buildComplete = $true

    Write-Output "Created image bundle: $outputRoot"
    Write-Output "Worker private key: $WorkerKeyPath"
    Write-Output "Guest host public key: $GuestHostKeyPath"
} finally {
    if ($vmCreated -and (Get-VM -Name $vmName -ErrorAction SilentlyContinue)) {
        Stop-VM -Name $vmName -TurnOff -Force -ErrorAction SilentlyContinue
        Remove-VM -Name $vmName -Force -ErrorAction SilentlyContinue
    }
    if ($vhdMounted) {
        Dismount-VHD -Path $buildVHDX -ErrorAction SilentlyContinue
    }
    if ($null -ne $iso) {
        Dismount-DiskImage -ImagePath $resolvedIsoPath -ErrorAction SilentlyContinue
    }
    if (-not $buildComplete -and (Test-Path -LiteralPath $outputRoot)) {
        Remove-Item -LiteralPath $outputRoot -Recurse -Force -ErrorAction SilentlyContinue
    }
    if (Test-Path -LiteralPath $buildRoot) {
        Remove-Item -LiteralPath $buildRoot -Recurse -Force -ErrorAction SilentlyContinue
    }
}
