package install

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestLayout(t *testing.T) {
	l := Layout{Home: filepath.Join("H", ".fclaude")}
	if l.Disk() != filepath.Join(l.Home, "vm", DiskName) || l.Key("maint") != filepath.Join(l.Home, "ssh", "maint") ||
		l.KnownHosts() != filepath.Join(l.Home, "ssh", "known_hosts") || l.VMX() != filepath.Join(l.Home, "vm", "fclaude.vmx") {
		t.Errorf("unexpected layout %+v", l)
	}
	for _, d := range l.Dirs() {
		if !strings.HasPrefix(d, l.Home) {
			t.Errorf("dir %s outside home", d)
		}
	}
}

func TestVMInfoRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "vm.json")
	if _, err := ReadVMInfo(p); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing: %v", err)
	}
	v := VMInfo{VMX: `C:\x\fclaude.vmx`, VMIP: "192.168.201.10", HostIP: "192.168.201.1", HostOnlyPrefix: 24,
		HostOnlyNet: "192.168.201.0/24", NATNet: "192.168.146.0/24", MACNAT: "00:50:56:01:02:03", MACHostOnly: "00:50:56:01:02:04"}
	if err := WriteVMInfo(p, v); err != nil {
		t.Fatal(err)
	}
	got, err := ReadVMInfo(p)
	if err != nil || got != v || got.SSHAddr() != "192.168.201.10:22" {
		t.Errorf("%+v %v", got, err)
	}
	for _, bad := range []string{"not json", `{"vmx": ""}`} {
		_ = os.WriteFile(p, []byte(bad), 0o600)
		if _, err := ReadVMInfo(p); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	entries, _ := os.ReadDir(filepath.Dir(p))
	if len(entries) != 1 {
		t.Errorf("temp files left: %v", entries)
	}
}

func TestEnsureKey(t *testing.T) {
	p := filepath.Join(t.TempDir(), "ssh", "fclaude")
	line, err := EnsureKey(p, "fclaude@fclaude-win")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(line, "ssh-ed25519 ") || !strings.HasSuffix(line, " fclaude@fclaude-win") {
		t.Errorf("public line %q", line)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	s, err := ssh.ParsePrivateKey(b)
	if err != nil {
		t.Fatal(err)
	}
	pub, _ := os.ReadFile(p + ".pub")
	if strings.TrimSpace(string(pub)) != line || !strings.Contains(line, strings.TrimSpace(string(ssh.MarshalAuthorizedKey(s.PublicKey())))) {
		t.Error(".pub does not match the private key")
	}
	// Existing key: kept (restore case), same public line.
	again, err := EnsureKey(p, "fclaude@fclaude-win")
	if err != nil || again != line {
		t.Errorf("existing key not reused: %q %v", again, err)
	}
	bad := filepath.Join(t.TempDir(), "garbage")
	_ = os.WriteFile(bad, []byte("nope"), 0o600)
	if _, err := EnsureKey(bad, "x"); err == nil {
		t.Error("unreadable existing key accepted")
	}
}

func TestToolError(t *testing.T) {
	e := &toolError{"icacls", " denied \n", errors.New("exit 5")}
	if e.Error() != "icacls: denied" || !errors.Is(e, e.err) {
		t.Error(e.Error())
	}
	if (&toolError{"x", "", errors.New("boom")}).Error() != "x: boom" {
		t.Error("empty output")
	}
}
