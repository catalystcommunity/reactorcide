//go:build windows

package vmrunner

import (
	"context"
	"encoding/xml"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWindowsLifecycleCreateScriptParses(t *testing.T) {
	lifecycle := &windowsVMLifecycle{switchName: defaultHyperVSwitch, secureBoot: true}
	script := lifecycle.createScript(
		`C:\images\base\disk.vhdx`,
		`D:\scratch\job.vhdx`,
		"reactorcide-vm-test",
		`D:\scratch\guest_authorized_key.pub`,
		`D:\scratch\ssh_host_ed25519_key`,
		BootSpec{CPUs: 2, MemoryBytes: 4 << 30, GuestUser: "reactorcide", JobID: "job-1234"},
	)
	for _, required := range []string{"Mount-VHD", "authorized_keys", "ssh_host_ed25519_key", "Unattend.xml", "Set-VMKeyProtector", "Enable-VMTPM", "AutomaticCheckpointsEnabled $false", "Start-VM"} {
		if !strings.Contains(script, required) {
			t.Fatalf("generated script does not contain %q", required)
		}
	}

	path := filepath.Join(t.TempDir(), "create-vm.ps1")
	if err := os.WriteFile(path, []byte(script), 0600); err != nil {
		t.Fatal(err)
	}
	parser := "$tokens = $null; $errors = $null; " +
		"[void][Management.Automation.Language.Parser]::ParseFile(" + psQuote(path) + ", [ref]$tokens, [ref]$errors); " +
		"if ($errors.Count -gt 0) { $errors | ForEach-Object { Write-Error $_.Message }; exit 1 }"
	if _, err := runPowerShell(context.Background(), parser); err != nil {
		t.Fatalf("generated PowerShell did not parse: %v", err)
	}
}

func TestWindowsComputerNameIsUniqueSafeAndBounded(t *testing.T) {
	first := windowsComputerName("01JABC-def-123")
	second := windowsComputerName("01JXYZ-def-456")
	if first == second || len(first) > 15 || len(second) > 15 {
		t.Fatalf("computer names = %q, %q", first, second)
	}
	for _, name := range []string{first, second} {
		for _, r := range name {
			if !(r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-') {
				t.Fatalf("computer name %q contains %q", name, r)
			}
		}
	}
}

func TestWindowsSpecializeUnattendIsValidXML(t *testing.T) {
	var document struct {
		XMLName xml.Name `xml:"unattend"`
	}
	if err := xml.Unmarshal([]byte(windowsSpecializeUnattend("RC-JOB123")), &document); err != nil {
		t.Fatal(err)
	}
}
