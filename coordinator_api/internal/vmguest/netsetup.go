package vmguest

import (
	"bufio"
	"bytes"
	"fmt"
	"net/netip"
	"strings"
)

// macAnchor is the pf anchor of the redirect rule. The default pf.conf loads
// the com.apple/* anchors.
const macAnchor = "com.apple/250.reactorcide"

type commandStep struct {
	stdin string
	name  string
	args  []string
}

// macNetworkSteps are the commands of the macOS transparent route: routes to
// lo0, the pf redirect to port, pf on, and a resolver for all domains.
func macNetworkSteps(port string, dnsServers []string) []commandStep {
	steps := []commandStep{
		{name: "/sbin/route", args: []string{"-n", "add", "-net", "0.0.0.0/1", "-interface", "lo0"}},
		{name: "/sbin/route", args: []string{"-n", "add", "-net", "128.0.0.0/1", "-interface", "lo0"}},
		{stdin: "rdr pass on lo0 inet proto tcp from any to ! 127.0.0.0/8 -> 127.0.0.1 port " + port + "\n",
			name: "/sbin/pfctl", args: []string{"-q", "-a", macAnchor, "-f", "-"}},
		{name: "/sbin/pfctl", args: []string{"-q", "-E"}},
	}
	if len(dnsServers) > 0 {
		steps = append(steps, commandStep{
			stdin: "d.init\nd.add ServerAddresses * " + strings.Join(dnsServers, " ") +
				"\nd.add SupplementalMatchDomains * \"\"\nset State:/Network/Service/reactorcide/DNS\nquit\n",
			name: "/usr/sbin/scutil",
		})
	}
	return steps
}

// parsePfState finds the destination of the connection from peer in the
// state list of pfctl on macOS: "ALL tcp 127.0.0.1:15001 <- DEST <- PEER".
func parsePfState(out []byte, peer netip.AddrPort) (netip.AddrPort, error) {
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) >= 7 && f[1] == "tcp" && f[3] == "<-" && f[5] == "<-" && f[6] == peer.String() {
			return netip.ParseAddrPort(f[4])
		}
	}
	return netip.AddrPort{}, fmt.Errorf("no pf state for %s", peer)
}

// psQuote renders s as a PowerShell single-quoted string literal.
func psQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// windowsProxyScript sets the system proxy settings of a Windows guest to
// the local proxy listener at addr: WinHTTP (netsh) and machine-wide WinINET
// settings. Proxy-aware programs then use the tunnel. Other programs fail
// closed, because the guest has no network card.
//
// TODO: transparent redirect for programs that ignore proxy settings. This
// needs a Windows Filtering Platform callout driver (a signed kernel driver).
// Build it only when real jobs need it (decision D28).
func windowsProxyScript(addr string) string {
	var b strings.Builder
	b.WriteString("$ErrorActionPreference = 'Stop'\n")
	fmt.Fprintf(&b, "& netsh.exe winhttp set proxy %s %s | Out-Null\n", psQuote("proxy-server="+addr), psQuote("bypass-list=<local>"))
	b.WriteString("if ($LASTEXITCODE -ne 0) { throw 'netsh winhttp set proxy failed' }\n")
	b.WriteString("$policy = 'HKLM:\\SOFTWARE\\Policies\\Microsoft\\Windows\\CurrentVersion\\Internet Settings'\n")
	b.WriteString("if (-not (Test-Path -LiteralPath $policy)) { New-Item -Path $policy -Force | Out-Null }\n")
	b.WriteString("New-ItemProperty -LiteralPath $policy -Name ProxySettingsPerUser -PropertyType DWord -Value 0 -Force | Out-Null\n")
	b.WriteString("$machine = 'HKLM:\\SOFTWARE\\Microsoft\\Windows\\CurrentVersion\\Internet Settings'\n")
	b.WriteString("New-ItemProperty -LiteralPath $machine -Name ProxyEnable -PropertyType DWord -Value 1 -Force | Out-Null\n")
	fmt.Fprintf(&b, "New-ItemProperty -LiteralPath $machine -Name ProxyServer -PropertyType String -Value %s -Force | Out-Null\n", psQuote(addr))
	b.WriteString("New-ItemProperty -LiteralPath $machine -Name ProxyOverride -PropertyType String -Value '<local>' -Force | Out-Null\n")
	// In per-machine mode WinINET (PowerShell Invoke-WebRequest, .NET
	// WebClient) reads only the binary connection settings, not ProxyServer.
	// Without them it connects directly and fails to resolve the name.
	b.WriteString("$connections = Join-Path $machine 'Connections'\n")
	b.WriteString("if (-not (Test-Path -LiteralPath $connections)) { New-Item -Path $connections -Force | Out-Null }\n")
	blob := wininetConnectionSettings(addr, "<local>")
	parts := make([]string, len(blob))
	for i, c := range blob {
		parts[i] = fmt.Sprintf("0x%02x", c)
	}
	fmt.Fprintf(&b, "$settings = [byte[]](%s)\n", strings.Join(parts, ","))
	b.WriteString("foreach ($name in 'DefaultConnectionSettings', 'SavedLegacySettings') { New-ItemProperty -LiteralPath $connections -Name $name -PropertyType Binary -Value $settings -Force | Out-Null }\n")
	return b.String()
}

// wininetConnectionSettings encodes the WinINET DefaultConnectionSettings
// value for a fixed proxy: version, change counter, flags (direct and proxy),
// the proxy server, the bypass list, an empty auto-configuration URL, and
// zero padding.
func wininetConnectionSettings(server, bypass string) []byte {
	var out []byte
	u32 := func(v uint32) { out = append(out, byte(v), byte(v>>8), byte(v>>16), byte(v>>24)) }
	str := func(v string) { u32(uint32(len(v))); out = append(out, v...) }
	u32(0x46)
	u32(1)
	u32(0x03) // PROXY_TYPE_DIRECT | PROXY_TYPE_PROXY
	str(server)
	str(bypass)
	str("")
	return append(out, make([]byte, 32)...)
}

// The Windows loopback adapter (see ensureOfflineLoopback).
const (
	loopbackHardwareID = "*MSLOOP"
	loopbackINF        = `INF\netloop.inf`
	loopbackAddress    = "10.255.255.1"
	loopbackPrefix     = 30
)

// loopbackAddressScript waits for the loopback adapter and gives it
// loopbackAddress with no gateway and no DNS servers.
func loopbackAddressScript() string {
	var b strings.Builder
	b.WriteString("$ErrorActionPreference = 'Stop'\n")
	b.WriteString("$adapter = $null\n")
	b.WriteString("for ($i = 0; $i -lt 60 -and -not $adapter; $i++) {\n")
	fmt.Fprintf(&b, "  $adapter = Get-NetAdapter -IncludeHidden -ErrorAction SilentlyContinue | Where-Object ComponentID -eq %s | Select-Object -First 1\n", psQuote(loopbackHardwareID))
	b.WriteString("  if (-not $adapter) { Start-Sleep -Milliseconds 500 }\n")
	b.WriteString("}\n")
	b.WriteString("if (-not $adapter) { throw 'the loopback adapter did not appear' }\n")
	fmt.Fprintf(&b, "if (-not (Get-NetIPAddress -InterfaceIndex $adapter.ifIndex -IPAddress %s -ErrorAction SilentlyContinue)) {\n", psQuote(loopbackAddress))
	b.WriteString("  Set-NetIPInterface -InterfaceIndex $adapter.ifIndex -Dhcp Disabled -ErrorAction SilentlyContinue\n")
	fmt.Fprintf(&b, "  New-NetIPAddress -InterfaceIndex $adapter.ifIndex -IPAddress %s -PrefixLength %d | Out-Null\n", psQuote(loopbackAddress), loopbackPrefix)
	b.WriteString("}\n")
	return b.String()
}
