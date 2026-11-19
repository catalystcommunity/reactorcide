package vmrunner

import (
	"encoding/base64"
	"strings"
	"testing"
	"unicode/utf16"
)

func TestHypervCreateScriptCopiesGuestDaemonBeforeCheck(t *testing.T) {
	script := hypervCreateScript(hypervCreateParams{
		BaseVHDX: `C:\base\disk.vhdx`, DiffPath: `C:\scratch\job.vhdx`, VMName: "vm",
		GuestDaemon: `C:\Users\it's\vmguest.exe`,
	})
	copyLine := `Copy-Item -LiteralPath 'C:\Users\it''s\vmguest.exe' -Destination (Join-Path $windowsRoot 'Program Files\Reactorcide\vmguest.exe') -Force`
	copyAt := strings.Index(script, copyLine)
	checkAt := strings.Index(script, "The VM image has no Reactorcide guest daemon.")
	if copyAt < 0 || checkAt < 0 || copyAt > checkAt {
		t.Fatalf("the daemon copy must come before the daemon check:\n%s", script)
	}
	if plain := hypervCreateScript(hypervCreateParams{VMName: "vm"}); strings.Contains(plain, "Copy-Item") {
		t.Fatal("a boot without --guest-daemon copies a guest daemon")
	}
}

func TestWindowsCaptureScript(t *testing.T) {
	script := windowsCaptureScript()
	for _, required := range []string{
		"$ErrorActionPreference = 'Stop'",
		"Disable-BitLocker",
		"FullyDecrypted",
		`'C:\reactorcide\job'`,
		"Stop-Computer -Force",
	} {
		if !strings.Contains(script, required) {
			t.Fatalf("capture script does not contain %q:\n%s", required, script)
		}
	}
	// BitLocker must be off before the shutdown.
	if strings.Index(script, "FullyDecrypted") > strings.Index(script, "Stop-Computer") {
		t.Fatal("the guest shuts down before BitLocker is off")
	}
	// Sysprep makes each job boot run Windows setup and restart.
	for _, forbidden := range []string{"Sysprep", "Restart-Computer"} {
		if strings.Contains(script, forbidden) {
			t.Fatalf("capture script contains %q", forbidden)
		}
	}
}

func TestWindowsImageConvertScript(t *testing.T) {
	script := windowsImageConvertScript(`C:\scratch\reactorcide-vm-1\job.vhdx`, `D:\images\.build-1\disk.vhdx`)
	for _, required := range []string{
		`Convert-VHD -Path 'C:\scratch\reactorcide-vm-1\job.vhdx' -DestinationPath 'D:\images\.build-1\disk.vhdx' -VHDType Dynamic`,
		"$vhd.ParentPath",
	} {
		if !strings.Contains(script, required) {
			t.Fatalf("convert script does not contain %q:\n%s", required, script)
		}
	}
	if strings.Contains(script, "Merge-VHD") {
		t.Fatal("convert script merges into the base image")
	}
}

func TestWindowsImageVerifyScript(t *testing.T) {
	script := windowsImageVerifyScript(`D:\it's\disk.vhdx`)
	for _, required := range []string{
		`Mount-VHD -Path 'D:\it''s\disk.vhdx' -ReadOnly -PassThru`,
		`'Program Files\Reactorcide\vmguest.exe'`,
		"Get-BitLockerVolume -MountPoint ($partition.DriveLetter + ':')",
		`Dismount-VHD -Path 'D:\it''s\disk.vhdx'`,
	} {
		if !strings.Contains(script, required) {
			t.Fatalf("verify script does not contain %q:\n%s", required, script)
		}
	}
	if strings.Index(script, "} finally {") > strings.Index(script, "Dismount-VHD") {
		t.Fatal("dismount is not in the finally block")
	}
}

func TestHypervVMStateScript(t *testing.T) {
	if got := hypervVMStateScript("vm'1"); !strings.Contains(got, "(Get-VM -Name 'vm''1').State.ToString()") {
		t.Fatalf("state script = %q", got)
	}
}

func TestPowerShellEncodedArgs(t *testing.T) {
	script := "Write-Output \"it's\"; exit 3"
	args := powerShellEncodedArgs(script)
	if args[0] != "powershell.exe" || args[len(args)-2] != "-EncodedCommand" {
		t.Fatalf("args = %v", args)
	}
	raw, err := base64.StdEncoding.DecodeString(args[len(args)-1])
	if err != nil || len(raw)%2 != 0 {
		t.Fatalf("encoded command is not base64 UTF-16: %v", err)
	}
	units := make([]uint16, len(raw)/2)
	for i := range units {
		units[i] = uint16(raw[2*i]) | uint16(raw[2*i+1])<<8
	}
	if got := string(utf16.Decode(units)); got != script {
		t.Fatalf("decoded = %q, want %q", got, script)
	}
}

func TestWindowsProvisionScriptHelpers(t *testing.T) {
	if got := windowsProvisionScriptPath(0); got != `C:\reactorcide\job\.image-build\provision-001.ps1` {
		t.Fatalf("path = %q", got)
	}
	args := windowsProvisionArgs(`C:\x.ps1`)
	if strings.Join(args, " ") != `powershell.exe -NoLogo -NoProfile -NonInteractive -ExecutionPolicy Bypass -File C:\x.ps1` {
		t.Fatalf("args = %v", args)
	}
	data := windowsScriptBytes("Write-Output ü")
	if string(data[:3]) != "\xef\xbb\xbf" {
		t.Fatal("script has no UTF-8 byte order mark")
	}
	if again := windowsScriptBytes(string(data)); len(again) != len(data) {
		t.Fatal("a script with a byte order mark gets a second one")
	}
}

func TestHypervCreateScriptPreventsDeviceEncryption(t *testing.T) {
	script := hypervCreateScript(hypervCreateParams{VMName: "vm", PreventDeviceEncryption: true})
	for _, required := range []string{
		"reg.exe load $hive (Join-Path $windowsRoot 'Windows\\System32\\config\\SYSTEM')",
		"/v PreventDeviceEncryption /t REG_DWORD /d 1 /f",
		"reg.exe unload $hive",
	} {
		if !strings.Contains(script, required) {
			t.Fatalf("create script does not contain %q:\n%s", required, script)
		}
	}
	if strings.Index(script, "reg.exe unload") > strings.Index(script, "Dismount-VHD") {
		t.Fatal("the hive is unloaded after the disk is dismounted")
	}
	if plain := hypervCreateScript(hypervCreateParams{VMName: "vm"}); strings.Contains(plain, "PreventDeviceEncryption") {
		t.Fatal("a job boot changes the BitLocker policy")
	}
}

func TestHypervForceRemoveScriptToleratesMissingVM(t *testing.T) {
	script := hypervForceRemoveScript("vm'1")
	if !strings.Contains(script, "@(Get-VM | Where-Object { $_.Name -eq 'vm''1' })") {
		t.Fatalf("remove script = %q", script)
	}
	if strings.Contains(script, "Get-VM -Name") {
		t.Fatal("Get-VM -Name stops the script when the VM is missing")
	}
}
