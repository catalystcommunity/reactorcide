package vmrunner

import (
	"encoding/base64"
	"fmt"
	"strings"
	"unicode/utf16"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/vmguest"
)

// This file holds the pure PowerShell builders of the Windows derived image
// build (image_build_windows.go), so that tests run on every OS.

const (
	// windowsBuildScriptDir is the guest directory of the provisioning
	// scripts. It is in the job directory, which the guest account can
	// write, and the capture step empties it.
	windowsBuildScriptDir = `C:\reactorcide\job\.image-build`
	windowsGuestJobDir    = `C:\reactorcide\job`
)

// windowsProvisionScriptPath returns the guest path of provisioning script
// index (0-based).
func windowsProvisionScriptPath(index int) string {
	return fmt.Sprintf(`%s\provision-%03d.ps1`, windowsBuildScriptDir, index+1)
}

// windowsProvisionArgs runs one provisioning script file with Windows
// PowerShell. -File gives the exit code of the script.
func windowsProvisionArgs(path string) []string {
	return []string{"powershell.exe", "-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", path}
}

// windowsScriptBytes returns the script as UTF-8 with a byte order mark.
// Windows PowerShell 5.1 reads a file without a BOM as the ANSI code page.
func windowsScriptBytes(script string) []byte {
	const bom = "\xef\xbb\xbf"
	if strings.HasPrefix(script, bom) {
		return []byte(script)
	}
	return []byte(bom + script)
}

// powerShellEncodedArgs runs script with -EncodedCommand (base64 of
// UTF-16LE). No quote in the script can break the command line.
func powerShellEncodedArgs(script string) []string {
	units := utf16.Encode([]rune(script))
	raw := make([]byte, 0, len(units)*2)
	for _, u := range units {
		raw = append(raw, byte(u), byte(u>>8))
	}
	return []string{"powershell.exe", "-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
		"-EncodedCommand", base64.StdEncoding.EncodeToString(raw)}
}

// offlinePreventDeviceEncryptionScript loads the SYSTEM hive of the mounted
// Windows volume ($windowsRoot), sets PreventDeviceEncryption = 1 in every
// control set, and unloads the hive. indent prefixes each line.
func offlinePreventDeviceEncryptionScript(indent string) string {
	lines := []string{
		"$hive = 'HKLM\\ReactorcideImageBuild'",
		"& reg.exe load $hive (Join-Path $windowsRoot 'Windows\\System32\\config\\SYSTEM') | Out-Null",
		"if ($LASTEXITCODE -ne 0) { throw 'The offline SYSTEM hive could not be loaded.' }",
		"try {",
		"  foreach ($set in @('ControlSet001', 'ControlSet002')) {",
		"    if (Test-Path -LiteralPath ('Registry::' + $hive + '\\' + $set)) {",
		"      & reg.exe add ($hive + '\\' + $set + '\\Control\\BitLocker') /v PreventDeviceEncryption /t REG_DWORD /d 1 /f | Out-Null",
		"      if ($LASTEXITCODE -ne 0) { throw 'The BitLocker device encryption policy could not be set.' }",
		"    }",
		"  }",
		"} finally {",
		"  [GC]::Collect()",
		"  & reg.exe unload $hive | Out-Null",
		"}",
	}
	var b strings.Builder
	for _, line := range lines {
		b.WriteString(indent + line + "\n")
	}
	return b.String()
}

// windowsCaptureScript runs in the guest as LocalSystem after the
// provisioning scripts. It prepares the disk for capture and shuts the guest
// down cleanly.
//
// The build does not run Sysprep. A job clone of a base image from
// build-windows-image.ps1 does not run Windows setup again (it keeps the
// base computer name), and a job starts within a minute. A generalized
// derived image runs specialize, a restart, and OOBE at each job boot. The
// guest daemon answers before that restart, so the job fails. A derived image
// is therefore in the same state as a booted job clone of its base.
//
// BitLocker device encryption must be off: the key protector is the vTPM of
// the build VM, and a job VM has a different vTPM.
func windowsCaptureScript() string {
	var b strings.Builder
	b.WriteString("$ErrorActionPreference = 'Stop'\n")
	b.WriteString("$ProgressPreference = 'SilentlyContinue'\n")
	b.WriteString("$volume = Get-BitLockerVolume -MountPoint $env:SystemDrive -ErrorAction SilentlyContinue\n")
	b.WriteString("if ($volume -and $volume.VolumeStatus -ne 'FullyDecrypted') {\n")
	b.WriteString("  Write-Output \"reactorcide: turning off BitLocker on the OS volume (status $($volume.VolumeStatus))\"\n")
	b.WriteString("  Disable-BitLocker -MountPoint $env:SystemDrive | Out-Null\n")
	b.WriteString("  $deadline = (Get-Date).AddMinutes(30)\n")
	b.WriteString("  while ((Get-BitLockerVolume -MountPoint $env:SystemDrive).VolumeStatus -ne 'FullyDecrypted') {\n")
	b.WriteString("    if ((Get-Date) -gt $deadline) { Write-Output 'reactorcide: BitLocker decryption did not complete'; exit 1 }\n")
	b.WriteString("    Start-Sleep -Seconds 5\n")
	b.WriteString("  }\n")
	b.WriteString("}\n")
	fmt.Fprintf(&b, "if (Test-Path -LiteralPath %s) { Get-ChildItem -LiteralPath %s -Force | Remove-Item -Recurse -Force -ErrorAction SilentlyContinue }\n",
		psQuote(windowsGuestJobDir), psQuote(windowsGuestJobDir))
	b.WriteString("Write-Output 'reactorcide: shutting down the guest for capture'\n")
	b.WriteString("Stop-Computer -Force\n")
	return b.String()
}

// hypervVMStateScript prints the state of the VM (for example Running or
// Off).
func hypervVMStateScript(vmName string) string {
	return "$ErrorActionPreference = 'Stop'\nWrite-Output (Get-VM -Name " + psQuote(vmName) + ").State.ToString()\n"
}

// windowsImageConvertScript writes a new standalone dynamic VHDX from the
// differencing disk of the build VM. Convert-VHD reads the virtual disk
// through its parent chain, so the result has the merged content and no
// parent. The base image is not changed (Merge-VHD would write into it).
func windowsImageConvertScript(childVHDX, outputVHDX string) string {
	var b strings.Builder
	b.WriteString("$ErrorActionPreference = 'Stop'\n")
	fmt.Fprintf(&b, "Convert-VHD -Path %s -DestinationPath %s -VHDType Dynamic\n", psQuote(childVHDX), psQuote(outputVHDX))
	fmt.Fprintf(&b, "$vhd = Get-VHD -Path %s\n", psQuote(outputVHDX))
	b.WriteString("if ($vhd.VhdType -ne 'Dynamic' -or $vhd.ParentPath) { throw 'The converted image disk is not a standalone dynamic VHDX.' }\n")
	return b.String()
}

// windowsImageVerifyScript mounts the new image disk read-only and checks
// that it has the guest daemon and that BitLocker is off on its OS volume.
func windowsImageVerifyScript(vhdx string) string {
	var b strings.Builder
	b.WriteString("$ErrorActionPreference = 'Stop'\n")
	b.WriteString("$mounted = $null\n")
	b.WriteString("try {\n")
	fmt.Fprintf(&b, "  $mounted = Mount-VHD -Path %s -ReadOnly -PassThru\n", psQuote(vhdx))
	b.WriteString("  $partition = $mounted | Get-Disk | Get-Partition | Where-Object { $_.Type -eq 'Basic' } | Sort-Object Size -Descending | Select-Object -First 1\n")
	b.WriteString("  if (-not $partition) { throw 'The image disk has no Windows data partition.' }\n")
	b.WriteString("  if (-not $partition.DriveLetter) { $partition | Add-PartitionAccessPath -AssignDriveLetter; $partition = $mounted | Get-Disk | Get-Partition | Where-Object { $_.Type -eq 'Basic' } | Sort-Object Size -Descending | Select-Object -First 1 }\n")
	b.WriteString("  $windowsRoot = $partition.DriveLetter + ':\\'\n")
	fmt.Fprintf(&b, "  if (-not (Test-Path -LiteralPath (Join-Path $windowsRoot %s))) { throw 'The image has no Reactorcide guest daemon.' }\n",
		psQuote(strings.TrimPrefix(vmguest.WindowsDaemonPath, `C:\`)))
	b.WriteString("  $bitlocker = Get-BitLockerVolume -MountPoint ($partition.DriveLetter + ':') -ErrorAction SilentlyContinue\n")
	b.WriteString("  if ($bitlocker -and $bitlocker.VolumeStatus -ne 'FullyDecrypted') { throw \"The image OS volume is not fully decrypted (BitLocker status $($bitlocker.VolumeStatus)).\" }\n")
	b.WriteString("} finally {\n")
	fmt.Fprintf(&b, "  if ($null -ne $mounted) { Dismount-VHD -Path %s -ErrorAction SilentlyContinue }\n", psQuote(vhdx))
	b.WriteString("}\n")
	return b.String()
}
