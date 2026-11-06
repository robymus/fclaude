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
// 1024-65535 is allowed (dev servers on the host). Existing fclaude rules are
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
	return b.String(), nil
}

// FirewallRemoveScript removes every fclaude rule (uninstall). Run elevated.
func FirewallRemoveScript() string {
	return fmt.Sprintf("Get-NetFirewallRule -Group '%s' -ErrorAction SilentlyContinue | Remove-NetFirewallRule\n", FirewallGroup)
}

// FirewallCheckScript prints the number of fclaude rules (no admin needed).
func FirewallCheckScript() string {
	return fmt.Sprintf("@(Get-NetFirewallRule -Group '%s' -ErrorAction SilentlyContinue).Count\n", FirewallGroup)
}
