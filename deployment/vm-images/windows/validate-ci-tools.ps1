# Validate the CI toolchains as the job account. A job gets the machine
# environment at logon; this script reads it from the registry, because the
# install script ran in the same build session.
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
$env:Path = [Environment]::GetEnvironmentVariable('Path', 'Machine') + ';' + [Environment]::GetEnvironmentVariable('Path', 'User')
$env:JAVA_HOME = [Environment]::GetEnvironmentVariable('JAVA_HOME', 'Machine')

# Native programs write to stderr (java -version, for example). With 'Stop',
# PowerShell 5.1 makes redirected stderr a terminating error, so Check uses
# 'Continue' and tests the exit code.
function Check([string]$Exe, [string[]]$Arguments) {
  $ErrorActionPreference = 'Continue'
  Write-Output "-- $Exe $($Arguments -join ' ')"
  # Collect all output first: Select-Object -First stops the pipeline and
  # ends the program early (java then exits -1).
  $out = @(& $Exe @Arguments 2>&1)
  if ($LASTEXITCODE -ne 0) { throw "$Exe failed ($LASTEXITCODE): $out" }
  $out | Select-Object -First 2 | ForEach-Object { "$_" }
}

Check git @('--version')
Check gcc @('--version')
Check bash @('-c', 'echo bash-ok')
Check go @('version')
Check rustc @('--version')
Check cargo @('--version')
Check zig @('version')
Check node @('--version')
Check npm.cmd @('--version')
Check java @('-version')
Check gradle.bat @('--version', '--quiet')
Check dotnet @('--version')
Check dart @('--version')
Check php @('--version')
Check uv @('--version')
Check python @('--version')
Check python3 @('--version')
Check runnerlib @('--help')

$w = Join-Path $env:TEMP ('ci-validate-' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $w | Out-Null
Push-Location $w
Set-Content -Encoding ascii main.go "package main`nimport `"fmt`"`nfunc main() { fmt.Println(`"go-ok`") }"
Check go @('run', 'main.go')
# Rust (GNU target) and C with the WinLibs GCC.
Set-Content -Encoding ascii m.rs 'fn main() { println!("rust-ok"); }'
Check rustc @('-o', 'm.exe', 'm.rs')
Check .\m.exe @()
Set-Content -Encoding ascii m.c "#include <stdio.h>`nint main(void) { puts(`"c-ok`"); return 0; }"
Check gcc @('-o', 'c.exe', 'm.c')
Check .\c.exe @()
Set-Content -Encoding ascii m.zig "const std = @import(`"std`");`npub fn main() void { std.debug.print(`"zig-ok\n`", .{}); }"
Check zig @('run', 'm.zig')
Pop-Location
Remove-Item -Recurse -Force $w
Get-Content C:\catalyst-tools\VERSIONS
Write-Output 'validate-ok'
