package vmrunner

import (
	"strings"
	"testing"
)

func TestPsQuote(t *testing.T) {
	for in, want := range map[string]string{
		"":                    "''",
		"Default Switch":      "'Default Switch'",
		"it's":                "'it''s'",
		"$(Remove-Item C:\\)": "'$(Remove-Item C:\\)'",
	} {
		if got := psQuote(in); got != want {
			t.Errorf("psQuote(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHypervCreateScriptHasNoNetworkAndQuotesInputs(t *testing.T) {
	script := hypervCreateScript(hypervCreateParams{
		BaseVHDX: `C:\images\it's\disk.vhdx`, DiffPath: `D:\scratch\job.vhdx`, VMName: "reactorcide-vm-test",
		JobID: "job-1234", MemoryBytes: 4 << 30, CPUs: 2, SecureBoot: false,
	})
	for _, required := range []string{
		`New-VHD -ParentPath 'C:\images\it''s\disk.vhdx' -Path 'D:\scratch\job.vhdx' -Differencing`,
		"Mount-VHD", "Program Files\\Reactorcide\\vmguest.exe",
		"New-VM -Name 'reactorcide-vm-test' -Generation 2 -MemoryStartupBytes 4294967296 -VHDPath 'D:\\scratch\\job.vhdx' | Out-Null",
		"Get-VMNetworkAdapter -VMName 'reactorcide-vm-test' | Remove-VMNetworkAdapter",
		"Set-VMProcessor -VMName 'reactorcide-vm-test' -Count 2",
		"Set-VMKeyProtector", "Enable-VMTPM", "AutomaticCheckpointsEnabled $false",
		"Set-VMFirmware -VMName 'reactorcide-vm-test' -EnableSecureBoot Off",
		"Write-Output (Get-VM -Name 'reactorcide-vm-test').Id.ToString()",
	} {
		if !strings.Contains(script, required) {
			t.Fatalf("generated script does not contain %q:\n%s", required, script)
		}
	}
	for _, forbidden := range []string{"-SwitchName", "Start-VM", "authorized_keys", "ssh_host", "Unattend.xml"} {
		if strings.Contains(script, forbidden) {
			t.Fatalf("generated script contains %q", forbidden)
		}
	}
	if secure := hypervCreateScript(hypervCreateParams{VMName: "x", SecureBoot: true}); strings.Contains(secure, "EnableSecureBoot Off") {
		t.Fatal("secure boot script disables secure boot")
	}
}

func TestHypervStartScript(t *testing.T) {
	if got := hypervStartScript("vm'1"); !strings.Contains(got, "Start-VM -Name 'vm''1'") {
		t.Fatalf("start script = %q", got)
	}
}

func TestHvsockServicesUseVsockTemplate(t *testing.T) {
	services := hvsockServices()
	if len(services) != 2 {
		t.Fatalf("services = %v", services)
	}
	// Port 7100 = 0x1bbc, port 7101 = 0x1bbd.
	if got := services[0].ID.String(); got != "00001bbc-facb-11e6-bd58-64006a7986d3" {
		t.Fatalf("job channel service ID = %s", got)
	}
	if got := services[1].ID.String(); got != "00001bbd-facb-11e6-bd58-64006a7986d3" {
		t.Fatalf("egress service ID = %s", got)
	}
	script := hvsockRegistrationScript(services)
	for _, required := range []string{
		`Virtualization\GuestCommunicationServices`,
		"Join-Path $base '00001bbc-facb-11e6-bd58-64006a7986d3'",
		"Join-Path $base '00001bbd-facb-11e6-bd58-64006a7986d3'",
		"-Name ElementName -PropertyType String -Value 'Reactorcide VM job channel'",
		"if (-not (Test-Path -LiteralPath $key))",
	} {
		if !strings.Contains(script, required) {
			t.Fatalf("registration script does not contain %q:\n%s", required, script)
		}
	}
}

func TestParseVMID(t *testing.T) {
	id, err := parseVMID("WARNING: something\r\n3f2504e0-4f89-11d3-9a0c-0305e82c3301\r\n")
	if err != nil {
		t.Fatal(err)
	}
	if id.String() != "3f2504e0-4f89-11d3-9a0c-0305e82c3301" {
		t.Fatalf("id = %s", id)
	}
	if _, err := parseVMID("{3F2504E0-4F89-11D3-9A0C-0305E82C3301}"); err != nil {
		t.Fatalf("braced ID: %v", err)
	}
	for _, bad := range []string{"", "not-a-guid"} {
		if _, err := parseVMID(bad); err == nil {
			t.Fatalf("parseVMID(%q) did not fail", bad)
		}
	}
}
