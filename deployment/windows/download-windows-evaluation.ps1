[CmdletBinding()]
param(
    [string]$Destination = 'C:\ProgramData\Reactorcide\downloads\windows-11-enterprise-evaluation-25h2-en-us.iso',

    [string]$DownloadUrl = 'https://aka.ms/Win11E-ISO-25H2-en-us',

    [ValidatePattern('^[A-Fa-f0-9]{64}$')]
    [string]$Sha256 = 'A61ADEAB895EF5A4DB436E0A7011C92A2FF17BB0357F58B13BBC4062E535E7B9'
)

$ErrorActionPreference = 'Stop'
$destinationPath = [IO.Path]::GetFullPath($Destination)
$destinationDirectory = Split-Path -Parent $destinationPath
New-Item -ItemType Directory -Path $destinationDirectory -Force | Out-Null

if (Test-Path -LiteralPath $destinationPath) {
    $existingHash = (Get-FileHash -LiteralPath $destinationPath -Algorithm SHA256).Hash
    if ($existingHash.Equals($Sha256, [StringComparison]::OrdinalIgnoreCase)) {
        Write-Output "The verified Windows ISO is present: $destinationPath"
        exit 0
    }
    throw "The destination exists, but its SHA-256 is not correct: $destinationPath"
}

$partialPath = $destinationPath + '.partial'
if (Test-Path -LiteralPath $partialPath) {
    Remove-Item -LiteralPath $partialPath -Force
}

try {
    $downloaded = $false
    if (Get-Command curl.exe -ErrorAction SilentlyContinue) {
        & curl.exe --fail --location --silent --show-error --output $partialPath $DownloadUrl
        if ($LASTEXITCODE -ne 0) {
            throw 'curl.exe could not download the Windows ISO.'
        }
        $downloaded = $true
    } elseif (Get-Command Start-BitsTransfer -ErrorAction SilentlyContinue) {
        try {
            Start-BitsTransfer -Source $DownloadUrl -Destination $partialPath -DisplayName 'Windows 11 Enterprise Evaluation ISO'
            $downloaded = $true
        } catch {
            Write-Warning 'BITS is not available for this logon. The download will use HTTPS directly.'
            Remove-Item -LiteralPath $partialPath -Force -ErrorAction SilentlyContinue
        }
    }
    if (-not $downloaded) {
        Invoke-WebRequest -Uri $DownloadUrl -OutFile $partialPath -UseBasicParsing
    }

    $actualHash = (Get-FileHash -LiteralPath $partialPath -Algorithm SHA256).Hash
    if (-not $actualHash.Equals($Sha256, [StringComparison]::OrdinalIgnoreCase)) {
        throw 'The downloaded Windows ISO SHA-256 does not match the Microsoft value.'
    }
    Move-Item -LiteralPath $partialPath -Destination $destinationPath
    Write-Output "Downloaded and verified: $destinationPath"
} finally {
    if (Test-Path -LiteralPath $partialPath) {
        Remove-Item -LiteralPath $partialPath -Force
    }
}
