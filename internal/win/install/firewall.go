package install

import (
	"fmt"
	"net/netip"
	"strings"
)

// FirewallGroup tags every rule setup creates, so uninstall is one
// Remove-NetFirewallRule -Group.
const FirewallGroup = "fclaude"

// FirewallScript returns the PowerShell that (re)creates the rules keeping the
// VM away from privileged host ports (PLAN §3.7): inbound TCP/UDP 1-1023 from
// the VMware subnets is blocked (block beats allow in Windows Firewall), TCP
// 1024-65535 is allowed (dev servers on the host), and VMware's NAT service
// may not reach local networks (see below). Existing fclaude rules are
// removed first, so re-running is idempotent. Run elevated.
func FirewallScript(subnets ...string) (string, error) {
	if len(subnets) == 0 {
		return "", fmt.Errorf("firewall: no subnets")
	}
	for _, s := range subnets {
		p, err := netip.ParsePrefix(s)
		if err != nil || !p.Addr().Is4() || p.Masked() != p {
			return "", fmt.Errorf("firewall: %q is not an IPv4 subnet", s)
		}
	}
	remote := "'" + strings.Join(subnets, "','") + "'"
	var b strings.Builder
	b.WriteString("$ErrorActionPreference = 'Stop'\n")
	fmt.Fprintf(&b, "Get-NetFirewallRule -Group '%s' -ErrorAction SilentlyContinue | Remove-NetFirewallRule\n", FirewallGroup)
	for _, r := range []struct{ name, action, proto, ports string }{
		{"block VM -> host privileged TCP", "Block", "TCP", "1-1023"},
		{"block VM -> host privileged UDP", "Block", "UDP", "1-1023"},
		{"allow VM -> host unprivileged TCP", "Allow", "TCP", "1024-65535"},
	} {
		fmt.Fprintf(&b, "New-NetFirewallRule -Group '%s' -DisplayName 'fclaude: %s' -Direction Inbound -Action %s -Protocol %s -LocalPort %s -RemoteAddress %s | Out-Null\n",
			FirewallGroup, r.name, r.action, r.proto, r.ports, remote)
	}
	// VMware's NAT service proxies the VM's connections from this computer
	// itself, so traffic to the host's other addresses (LAN IP, other
	// adapters) and to the LAN arrives from a local address and the inbound
	// rules above never see the VM. Block that service from private
	// addresses altogether, except DNS (it forwards the VM's queries to the
	// host's resolver, often the LAN router). This applies to every VMware
	// NAT VM on the machine (PLAN §3.7).
	for _, proto := range []string{"TCP", "UDP"} {
		fmt.Fprintf(&b, "New-NetFirewallRule -Group '%s' -DisplayName 'fclaude: block VMware NAT -> local networks %s' -Direction Outbound -Action Block -Program '%s' -Protocol %s -RemotePort %s -RemoteAddress %s | Out-Null\n",
			FirewallGroup, proto, vmnatProgram, proto, "'1-52','54-65535'", privateRanges)
	}
	return b.String(), nil
}

// FirewallRuleCount is how many rules FirewallScript creates.
const FirewallRuleCount = 5

// vmnatProgram is the VMware NAT service binary (Workstation installs it
// into System32).
const vmnatProgram = `%SystemRoot%\System32\vmnat.exe`

// privateRanges: RFC 1918, link-local, CGNAT (Tailscale and the like) and
// IPv6 unique-local / link-local.
const privateRanges = "'10.0.0.0/8','172.16.0.0/12','192.168.0.0/16','169.254.0.0/16','100.64.0.0/10','fc00::/7','fe80::/10'"

// FirewallRemoveScript removes every fclaude rule (uninstall). Run elevated.
func FirewallRemoveScript() string {
	return fmt.Sprintf("Get-NetFirewallRule -Group '%s' -ErrorAction SilentlyContinue | Remove-NetFirewallRule\n", FirewallGroup)
}

// FirewallCheckScript prints the number of fclaude rules (no admin needed).
func FirewallCheckScript() string {
	return fmt.Sprintf("@(Get-NetFirewallRule -Group '%s' -ErrorAction SilentlyContinue).Count\n", FirewallGroup)
}
