package install

import (
	"strings"
	"testing"
)

func TestFirewallScript(t *testing.T) {
	s, err := FirewallScript("192.168.201.0/24", "192.168.146.0/24")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Index(s, "Remove-NetFirewallRule") > strings.Index(s, "New-NetFirewallRule") {
		t.Error("old rules must be removed before new ones are added")
	}
	for _, want := range []string{
		"-Action Block -Protocol TCP -LocalPort 1-1023 -RemoteAddress '192.168.201.0/24','192.168.146.0/24'",
		"-Action Block -Protocol UDP -LocalPort 1-1023",
		"-Action Allow -Protocol TCP -LocalPort 1024-65535",
		"-Group 'fclaude'", "$ErrorActionPreference = 'Stop'",
		`-Direction Outbound -Action Block -Program '%SystemRoot%\System32\vmnat.exe' -Protocol TCP -RemotePort '1-52','54-65535' -RemoteAddress '10.0.0.0/8','172.16.0.0/12','192.168.0.0/16'`,
		"-Program '%SystemRoot%\\System32\\vmnat.exe' -Protocol UDP -RemotePort '1-52','54-65535'",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in\n%s", want, s)
		}
	}
	for _, bad := range [][]string{nil, {"192.168.201.5/24"}, {"fe80::/64"}, {"nope"}, {"10.0.0.0/8';Remove-Item C:\\"}} {
		if _, err := FirewallScript(bad...); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if n := strings.Count(s, "New-NetFirewallRule"); n != FirewallRuleCount {
		t.Errorf("%d rules, FirewallRuleCount says %d", n, FirewallRuleCount)
	}
	if !strings.Contains(FirewallRemoveScript(), "-Group 'fclaude'") || !strings.Contains(FirewallCheckScript(), ".Count") {
		t.Error("remove/check scripts")
	}
}
