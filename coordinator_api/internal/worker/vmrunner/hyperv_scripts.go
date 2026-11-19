package vmrunner

import (
	"fmt"
	"strings"

	"github.com/Microsoft/go-winio/pkg/guid"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/vmguest"
)

// This file holds the pure PowerShell builders of the Hyper-V lifecycle
// (lifecycle_windows.go), so that tests run on every OS.

// psQuote renders s as a PowerShell single-quoted string literal, escaping any
// embedded single quotes by doubling them. Single-quoted PowerShell strings are
// literal (no variable/subexpression interpolation), so this is the safe way to
// embed host-controlled values (VM names, file paths) into a generated script
// without command injection.
func psQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// hvsockService is a Hyper-V socket service that the host registers.
type hvsockService struct {
	ID   guid.GUID
	Name string
}

// hvsockServices are the job channel (host to guest) and the egress tunnel
// (guest to host). Each service ID is the vsock template GUID for its port,
// so the guest daemon uses the same numbers on macOS and Windows.
func hvsockServices() []hvsockService {
	return []hvsockService{
		{ID: winioVsockServiceID(vmguest.JobPort), Name: "Reactorcide VM job channel"},
		{ID: winioVsockServiceID(vmguest.EgressPort), Name: "Reactorcide VM egress tunnel"},
	}
}

// winioVsockServiceID is go-winio VsockServiceID: the template
// 00000000-facb-11e6-bd58-64006a7986d3 with Data1 set to port. It is copied
// here because go-winio builds its socket code only on Windows.
func winioVsockServiceID(port uint32) guid.GUID {
	g, _ := guid.FromString("00000000-facb-11e6-bd58-64006a7986d3")
	g.Data1 = port
	return g
}

// hvsockRegistrationScript registers the Hyper-V socket services on the host.
// Hyper-V refuses a socket with a service ID that is not in the registry
// (GuestCommunicationServices). The script is idempotent and needs
// administrator rights.
func hvsockRegistrationScript(services []hvsockService) string {
	var b strings.Builder
	b.WriteString("$ErrorActionPreference = 'Stop'\n")
	b.WriteString("$base = 'HKLM:\\SOFTWARE\\Microsoft\\Windows NT\\CurrentVersion\\Virtualization\\GuestCommunicationServices'\n")
	for _, s := range services {
		fmt.Fprintf(&b, "$key = Join-Path $base %s\n", psQuote(s.ID.String()))
		b.WriteString("if (-not (Test-Path -LiteralPath $key)) { New-Item -Path $key -Force | Out-Null }\n")
		fmt.Fprintf(&b, "New-ItemProperty -LiteralPath $key -Name ElementName -PropertyType String -Value %s -Force | Out-Null\n", psQuote(s.Name))
	}
	return b.String()
}

// hypervCreateParams are the inputs of hypervCreateScript.
type hypervCreateParams struct {
	BaseVHDX    string
	DiffPath    string
	VMName      string
	JobID       string
	MemoryBytes int64
	CPUs        int
	SecureBoot  bool
	// GuestDaemon is an optional host path of a windows/amd64 vmguest.exe.
	// The script copies it over the guest daemon of the differencing disk
	// before the first boot (vm-image build windows --guest-daemon).
	GuestDaemon string
	// PreventDeviceEncryption sets the BitLocker PreventDeviceEncryption
	// policy in the offline SYSTEM hive of the differencing disk. Windows 11
	// turns on BitLocker device encryption at the first boot of a VM with a
	// vTPM. The key protector is the vTPM of the build VM, so a job VM
	// with a new vTPM cannot unlock the disk. Image builds use it.
	PreventDeviceEncryption bool
}

// hypervCreateScript builds the PowerShell that creates the differencing
// disk, checks that it holds the guest daemon, and creates the
// Generation-2 VM with no network adapter. It prints the VM ID. It does not
// start the VM: the host egress listener must exist first.
// $ErrorActionPreference = 'Stop' makes any cmdlet failure abort the whole
// script with a non-zero exit so runPowerShell surfaces it.
func hypervCreateScript(p hypervCreateParams) string {
	var b strings.Builder
	b.WriteString("$ErrorActionPreference = 'Stop'\n")
	fmt.Fprintf(&b, "New-VHD -ParentPath %s -Path %s -Differencing | Out-Null\n",
		psQuote(p.BaseVHDX), psQuote(p.DiffPath))
	b.WriteString("$mounted = $null\n")
	b.WriteString("try {\n")
	fmt.Fprintf(&b, "  $mounted = Mount-VHD -Path %s -PassThru\n", psQuote(p.DiffPath))
	b.WriteString("  $partition = $mounted | Get-Disk | Get-Partition | Where-Object { $_.Type -eq 'Basic' } | Sort-Object Size -Descending | Select-Object -First 1\n")
	b.WriteString("  if (-not $partition) { throw 'The VM disk has no Windows data partition.' }\n")
	b.WriteString("  if (-not $partition.DriveLetter) { $partition | Add-PartitionAccessPath -AssignDriveLetter; $partition = $mounted | Get-Disk | Get-Partition | Where-Object { $_.Type -eq 'Basic' } | Sort-Object Size -Descending | Select-Object -First 1 }\n")
	b.WriteString("  $windowsRoot = $partition.DriveLetter + ':\\'\n")
	if p.GuestDaemon != "" {
		fmt.Fprintf(&b, "  Copy-Item -LiteralPath %s -Destination (Join-Path $windowsRoot %s) -Force\n",
			psQuote(p.GuestDaemon), psQuote(strings.TrimPrefix(vmguest.WindowsDaemonPath, `C:\`)))
	}
	if p.PreventDeviceEncryption {
		b.WriteString(offlinePreventDeviceEncryptionScript("  "))
	}
	fmt.Fprintf(&b, "  if (-not (Test-Path -LiteralPath (Join-Path $windowsRoot %s))) { throw 'The VM image has no Reactorcide guest daemon.' }\n",
		psQuote(strings.TrimPrefix(vmguest.WindowsDaemonPath, `C:\`)))
	b.WriteString("} finally {\n")
	fmt.Fprintf(&b, "  if ($null -ne $mounted) { Dismount-VHD -Path %s -ErrorAction SilentlyContinue }\n", psQuote(p.DiffPath))
	b.WriteString("}\n")
	// No -SwitchName: the VM gets no network. New-VM still adds an
	// unconnected adapter, so remove every adapter.
	fmt.Fprintf(&b, "New-VM -Name %s -Generation 2 -MemoryStartupBytes %d -VHDPath %s | Out-Null\n",
		psQuote(p.VMName), p.MemoryBytes, psQuote(p.DiffPath))
	fmt.Fprintf(&b, "Get-VMNetworkAdapter -VMName %s | Remove-VMNetworkAdapter\n", psQuote(p.VMName))
	fmt.Fprintf(&b, "if (@(Get-VMNetworkAdapter -VMName %s).Count -ne 0) { throw 'The VM still has a network adapter.' }\n", psQuote(p.VMName))
	fmt.Fprintf(&b, "Set-VMProcessor -VMName %s -Count %d\n", psQuote(p.VMName), p.CPUs)
	fmt.Fprintf(&b, "Set-VMKeyProtector -VMName %s -NewLocalKeyProtector\n", psQuote(p.VMName))
	fmt.Fprintf(&b, "Enable-VMTPM -VMName %s\n", psQuote(p.VMName))
	fmt.Fprintf(&b, "Set-VM -VMName %s -AutomaticCheckpointsEnabled $false\n", psQuote(p.VMName))
	if !p.SecureBoot {
		fmt.Fprintf(&b, "Set-VMFirmware -VMName %s -EnableSecureBoot Off\n", psQuote(p.VMName))
	}
	fmt.Fprintf(&b, "Write-Output (Get-VM -Name %s).Id.ToString()\n", psQuote(p.VMName))
	return b.String()
}

// hypervStartScript starts the VM.
func hypervStartScript(vmName string) string {
	return "$ErrorActionPreference = 'Stop'\nStart-VM -Name " + psQuote(vmName) + "\n"
}

// hypervForceRemoveScript turns off and removes the named VM. It does
// nothing when the VM does not exist. It filters the full Get-VM list:
// "Get-VM -Name X -ErrorAction SilentlyContinue" for a missing VM still
// stops a script that has $ErrorActionPreference = 'Stop' (exit code 1, no
// message, Windows 11 26200), so a sweep could never remove a scratch dir
// whose VM is already gone.
func hypervForceRemoveScript(vmName string) string {
	return "$ErrorActionPreference = 'Stop'\n" +
		"@(Get-VM | Where-Object { $_.Name -eq " + psQuote(vmName) + " }) | ForEach-Object {\n" +
		"  Stop-VM -VM $_ -Force -TurnOff -ErrorAction SilentlyContinue\n" +
		"  Remove-VM -VM $_ -Force\n" +
		"}\n"
}

// parseVMID reads the VM ID that hypervCreateScript prints on its last line.
func parseVMID(out string) (guid.GUID, error) {
	lines := strings.Fields(strings.TrimSpace(out))
	if len(lines) == 0 {
		return guid.GUID{}, fmt.Errorf("vmrunner/windows: the create script printed no VM ID")
	}
	id, err := guid.FromString(strings.Trim(lines[len(lines)-1], "{}"))
	if err != nil {
		return guid.GUID{}, fmt.Errorf("vmrunner/windows: bad VM ID %q: %w", lines[len(lines)-1], err)
	}
	return id, nil
}
