# Install the Reactorcide CI toolchains in a Windows guest image.
#
# Run with `reactorcide vm-image build windows --guest-user reactorcide`
# (an administrator; the guest daemon uses its elevated token).
# The set is the Linux runnerbase image (git, Go, Rust, Python + uv,
# runnerlib, a C toolchain) plus the catalyst-tools bundle (Zig, JDK 17 + Gradle, .NET 8,
# Dart, Node, PHP). OCaml and webi are not installed on Windows.
#
# Everything is machine-wide: tool trees in C:\catalyst-tools, the machine
# PATH, and machine JAVA_HOME. A job account gets them at logon.
#
# Every download is pinned by version and SHA-256 (SHA-512 for .NET). The
# VC++ runtime bootstrapper has no published checksum: it must have a valid
# Microsoft Authenticode signature.
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'

$T = 'C:\catalyst-tools'
$Bin = Join-Path $T 'bin'
$DL = Join-Path $env:TEMP ('ci-tools-' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Force -Path $T, $Bin, $DL | Out-Null

function Say($m) { Write-Output "== $m" }

function Fetch([string[]]$Urls, [string]$Algo, [string]$Sum, [string]$Out) {
  foreach ($u in $Urls) {
    & curl.exe -fsSL --retry 5 --retry-delay 3 -o $Out $u
    if ($LASTEXITCODE -eq 0) { break }
  }
  if ($LASTEXITCODE -ne 0) { throw "download failed: $($Urls -join ', ')" }
  $h = (Get-FileHash -Algorithm $Algo -LiteralPath $Out).Hash.ToLowerInvariant()
  if ($h -ne $Sum) { throw "checksum mismatch: $($Urls[0])" }
}

function FetchSigned([string]$Url, [string]$Out) {
  & curl.exe -fsSL --retry 5 --retry-delay 3 -o $Out $Url
  if ($LASTEXITCODE -ne 0) { throw "download failed: $Url" }
  $sig = Get-AuthenticodeSignature -LiteralPath $Out
  if ($sig.Status -ne 'Valid' -or $sig.SignerCertificate.Subject -notmatch 'O=Microsoft Corporation') {
    throw "bad signature on $Url ($($sig.Status))"
  }
}

function Unzip([string]$Zip, [string]$Dest) {
  New-Item -ItemType Directory -Force -Path $Dest | Out-Null
  & tar.exe -xf $Zip -C $Dest
  if ($LASTEXITCODE -ne 0) { throw "extract failed: $Zip" }
}

# Native program with stderr output: see validate-ci-tools.ps1.
function Native([string]$Exe, [string[]]$Arguments) {
  $ErrorActionPreference = 'Continue'
  & $Exe @Arguments 2>&1 | ForEach-Object { "$_" }
  if ($LASTEXITCODE -ne 0) { throw "$Exe $($Arguments -join ' ') exited $LASTEXITCODE" }
}

function Run([string]$Exe, [string[]]$Arguments, [int[]]$Ok = @(0)) {
  $p = Start-Process -FilePath $Exe -ArgumentList $Arguments -Wait -PassThru -NoNewWindow
  if ($Ok -notcontains $p.ExitCode) { throw "$Exe exited $($p.ExitCode)" }
}

$paths = New-Object System.Collections.Generic.List[string]

Say 'Git for Windows 2.56.1'
Fetch @('https://github.com/git-for-windows/git/releases/download/v2.56.1.windows.1/Git-2.56.1-64-bit.exe') SHA256 'c330c72e10b408cc39994bfefdb63542b0d93d21699325c3e0bd874af8a5a559' "$DL\git.exe"
Run "$DL\git.exe" @('/VERYSILENT', '/NORESTART', '/NOCANCEL', '/SP-', '/SUPPRESSMSGBOXES', '/o:PathOption=Cmd')
$paths.Add('C:\Program Files\Git\cmd'); $paths.Add('C:\Program Files\Git\bin')

Say 'VC++ runtime (for PHP)'
FetchSigned 'https://aka.ms/vs/17/release/vc_redist.x64.exe' "$DL\vc_redist.exe"
Run "$DL\vc_redist.exe" @('/install', '/quiet', '/norestart') @(0, 1638, 3010)

Say 'WinLibs GCC 16.2.0 (MinGW-w64, msvcrt)'
# The C toolchain for the Rust GNU target and for jobs (gcc, g++, mingw32-make).
Fetch @('https://github.com/brechtsanders/winlibs_mingw/releases/download/16.2.0posix-23.1.3-14.0.0-msvcrt-r3/winlibs-x86_64-posix-seh-gcc-16.2.0-mingw-w64msvcrt-14.0.0-r3.zip') SHA256 '62af32ea58acdea1318ea5dd8eb7017eaee694dd40b269d52a59c769dcfefb82' "$DL\winlibs.zip"
if (Test-Path "$T\mingw64") { Remove-Item -Recurse -Force "$T\mingw64" }
Unzip "$DL\winlibs.zip" $T
$paths.Add("$T\mingw64\bin")

Say 'Go 1.26.4'
Fetch @('https://go.dev/dl/go1.26.4.windows-amd64.zip') SHA256 '3ca8fb4630b07c419cbdd51f754e31363cfcfb83b3a5354d9e895c90be2cc345' "$DL\go.zip"
if (Test-Path "$T\go") { Remove-Item -Recurse -Force "$T\go" }
Unzip "$DL\go.zip" $T
$paths.Add("$T\go\bin")

Say 'Rust 1.100.0 (GNU)'
# The GNU target links with its own bundled linker; no Visual Studio needed.
# The Visual Studio Build Tools bootstrapper cannot download behind the
# guest egress proxy (BITS and WinINET go direct: error 12007).
Fetch @('https://static.rust-lang.org/dist/rust-1.100.0-x86_64-pc-windows-gnu.msi') SHA256 'd78056d2b6e82418583d4849e689c780eb393fc73cf09e98eaf205e2cd778b17' "$DL\rust.msi"
Run 'msiexec.exe' @('/i', "`"$DL\rust.msi`"", '/qn', '/norestart', 'ALLUSERS=1')

Say 'Zig 0.14.1'
Fetch @('https://ziglang.org/download/0.14.1/zig-x86_64-windows-0.14.1.zip') SHA256 '554f5378228923ffd558eac35e21af020c73789d87afeabf4bfd16f2e6feed2c' "$DL\zig.zip"
Unzip "$DL\zig.zip" $DL
if (Test-Path "$T\zig-0.14.1") { Remove-Item -Recurse -Force "$T\zig-0.14.1" }
Move-Item "$DL\zig-x86_64-windows-0.14.1" "$T\zig-0.14.1"
$paths.Add("$T\zig-0.14.1")

Say 'Node 26.13.0'
Fetch @('https://nodejs.org/dist/v26.13.0/node-v26.13.0-win-x64.zip') SHA256 'dcc60d59a0746999f99064a01c020445d54d84228c691cdbf25b49ac3b59150a' "$DL\node.zip"
Unzip "$DL\node.zip" $DL
if (Test-Path "$T\node-v26.13.0") { Remove-Item -Recurse -Force "$T\node-v26.13.0" }
Move-Item "$DL\node-v26.13.0-win-x64" "$T\node-v26.13.0"
$paths.Add("$T\node-v26.13.0")

Say 'Temurin JDK 17.0.21.1+2'
Fetch @('https://github.com/adoptium/temurin17-binaries/releases/download/jdk-17.0.21.1%2B2/OpenJDK17U-jdk_x64_windows_hotspot_17.0.21.1_2.zip') SHA256 'eafba0944f777abdc74d5aad16049c9b5a1275b6ce99e2939ba9f7f3e6f08c87' "$DL\jdk.zip"
Unzip "$DL\jdk.zip" "$DL\jdk"
if (Test-Path "$T\jdk-17") { Remove-Item -Recurse -Force "$T\jdk-17" }
Move-Item (Get-ChildItem "$DL\jdk" -Directory | Select-Object -First 1).FullName "$T\jdk-17"
[Environment]::SetEnvironmentVariable('JAVA_HOME', "$T\jdk-17", 'Machine')
$paths.Add("$T\jdk-17\bin")

Say 'Gradle 8.10.2'
Fetch @('https://services.gradle.org/distributions/gradle-8.10.2-bin.zip') SHA256 '31c55713e40233a8303827ceb42ca48a47267a0ad4bab9177123121e71524c26' "$DL\gradle.zip"
if (Test-Path "$T\gradle-8.10.2") { Remove-Item -Recurse -Force "$T\gradle-8.10.2" }
Unzip "$DL\gradle.zip" $T
$paths.Add("$T\gradle-8.10.2\bin")

Say '.NET SDK 8.0.428'
Fetch @('https://builds.dotnet.microsoft.com/dotnet/Sdk/8.0.428/dotnet-sdk-8.0.428-win-x64.zip') SHA512 'cce1dc5cfb2c5b73ddd9d3658f6009deb85be3a58dbec44a6dfe3910bb32b044ad2fc6bb28c77aded70c73270a204d207fe790dcb82ffca6c7b42c1335b51c21' "$DL\dotnet.zip"
Unzip "$DL\dotnet.zip" 'C:\Program Files\dotnet'
[Environment]::SetEnvironmentVariable('DOTNET_ROOT', 'C:\Program Files\dotnet', 'Machine')
[Environment]::SetEnvironmentVariable('DOTNET_CLI_TELEMETRY_OPTOUT', '1', 'Machine')
$paths.Add('C:\Program Files\dotnet')

Say 'Dart 3.14.1'
Fetch @('https://storage.googleapis.com/dart-archive/channels/stable/release/3.14.1/sdk/dartsdk-windows-x64-release.zip') SHA256 'fb2550f71e3408f2a69ff6ee29573f341991a927f8bc9ad0601dd3c9b19945c8' "$DL\dart.zip"
if (Test-Path "$T\dart-sdk") { Remove-Item -Recurse -Force "$T\dart-sdk" }
Unzip "$DL\dart.zip" $T
$paths.Add("$T\dart-sdk\bin")

Say 'PHP 8.4.28 (NTS)'
# windows.php.net moves a release to archives/ when a newer one ships.
$phpZip = Split-Path -Leaf 'https://windows.php.net/downloads/releases/php-8.4.28-nts-Win32-vs17-x64.zip'
Fetch @('https://windows.php.net/downloads/releases/php-8.4.28-nts-Win32-vs17-x64.zip', "https://windows.php.net/downloads/releases/archives/$phpZip") SHA256 'c1f9280588b7b6ad7b518969022f0b0175f106fab9d0639ad0e4ce7cd47ac1ed' "$DL\php.zip"
if (Test-Path "$T\php-8.4") { Remove-Item -Recurse -Force "$T\php-8.4" }
Unzip "$DL\php.zip" "$T\php-8.4"
Copy-Item "$T\php-8.4\php.ini-production" "$T\php-8.4\php.ini"
$paths.Add("$T\php-8.4")

Say 'uv 0.13.8, Python 3.14, runnerlib v0.13.0'
Fetch @('https://github.com/astral-sh/uv/releases/download/0.13.8/uv-x86_64-pc-windows-msvc.zip') SHA256 '529e78a9cec6cd7e3e014f7ecd8bda0c113969d4ed0f2efeedb85c30943325b8' "$DL\uv.zip"
Unzip "$DL\uv.zip" $Bin
$env:UV_PYTHON_INSTALL_DIR = "$T\python"
$env:UV_TOOL_DIR = "$T\uv-tools"
$env:UV_TOOL_BIN_DIR = $Bin
$env:UV_CACHE_DIR = "$DL\uv-cache"
Native "$Bin\uv.exe" @('python', 'install', '3.14')
$py = (& "$Bin\uv.exe" python find 3.14).Trim()
$pyDir = Split-Path -Parent $py
Copy-Item $py (Join-Path $pyDir 'python3.exe')
$paths.Add($pyDir); $paths.Add("$pyDir\Scripts")
$env:Path = "C:\Program Files\Git\cmd;$env:Path"
Native "$Bin\uv.exe" @('tool', 'install', '--python', $py, 'git+https://github.com/catalystcommunity/reactorcide@v0.13.0#subdirectory=runnerlib')
$paths.Add($Bin)

Say 'Machine PATH'
$machine = [Environment]::GetEnvironmentVariable('Path', 'Machine').Split(';') | Where-Object { $_ }
$new = @($paths) + @($machine | Where-Object { $paths -notcontains $_ })
[Environment]::SetEnvironmentVariable('Path', ($new -join ';'), 'Machine')

@'
git 2.56.1
go 1.26.4
rust 1.100.0 (x86_64-pc-windows-gnu)
gcc 16.2.0 (winlibs msvcrt)
zig 0.14.1
node 26.13.0
jdk temurin-17.0.21.1+2
gradle 8.10.2
dotnet-sdk 8.0.428
dart 3.14.1
php 8.4.28
uv 0.13.8
python 3.14
runnerlib v0.13.0
'@ | Set-Content -Encoding ascii "$T\VERSIONS"

Remove-Item -Recurse -Force $DL -ErrorAction SilentlyContinue
Say 'done'
