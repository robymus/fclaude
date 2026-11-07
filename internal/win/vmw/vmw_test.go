package vmw

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite golden files")

type call struct{ args []string }

type fakeRunner struct {
	calls []call
	out   map[string]string // first non-flag arg → output
	err   map[string]error
}

func (f *fakeRunner) Run(_ context.Context, args ...string) (string, error) {
	f.calls = append(f.calls, call{args})
	verb := args[2] // after "-T ws"
	return f.out[verb], f.err[verb]
}

// vmrunErr mimics a failed vmrun: "Error: <msg>" on stdout, exit 255.
func vmrunErr(msg string) error {
	return &CommandError{Tool: "vmrun", Output: "Error: " + msg + "\n", Err: errors.New("exit status 255")}
}

const vmxPath = `C:\Users\bro\.fclaude\vm\fclaude.vmx`

func TestClientCommandLines(t *testing.T) {
	f := &fakeRunner{}
	c := Client{R: f}
	ctx := context.Background()
	_ = c.Start(ctx, vmxPath)
	_ = c.Stop(ctx, vmxPath, false)
	_ = c.Stop(ctx, vmxPath, true)
	_ = c.EnableSharedFolders(ctx, vmxPath)
	_ = c.AddShare(ctx, vmxPath, "fc-1", `C:\dev\app`)
	_ = c.RemoveShare(ctx, vmxPath, "fc-1")
	want := [][]string{
		{"-T", "ws", "start", vmxPath, "nogui"},
		{"-T", "ws", "stop", vmxPath, "soft"},
		{"-T", "ws", "stop", vmxPath, "hard"},
		{"-T", "ws", "enableSharedFolders", vmxPath},
		{"-T", "ws", "addSharedFolder", vmxPath, "fc-1", `C:\dev\app`},
		{"-T", "ws", "removeSharedFolder", vmxPath, "fc-1"},
	}
	for i, w := range want {
		if !reflect.DeepEqual(f.calls[i].args, w) {
			t.Errorf("call %d = %q, want %q", i, f.calls[i].args, w)
		}
	}
}

func TestRunning(t *testing.T) {
	ctx := context.Background()
	// Real `vmrun list` output (Workstation 26.0.1), CRLF already normalized by ExecRunner.
	f := &fakeRunner{out: map[string]string{"list": "Total running VMs: 1\n" + `c:\users\BRO\.fclaude\vm\fclaude.vmx` + "\n"}}
	if ok, err := (Client{R: f}).Running(ctx, vmxPath); err != nil || !ok {
		t.Errorf("running VM not found (case-insensitive path): %v %v", ok, err)
	}
	f.out["list"] = "Total running VMs: 0\n"
	if ok, _ := (Client{R: f}).Running(ctx, vmxPath); ok {
		t.Error("idle host reported running")
	}
	f.err = map[string]error{"list": vmrunErr("boom")}
	if _, err := (Client{R: f}).Running(ctx, vmxPath); err == nil {
		t.Error("list error swallowed")
	}
}

func TestShareIdempotence(t *testing.T) {
	ctx := context.Background()
	f := &fakeRunner{err: map[string]error{
		"addSharedFolder":    vmrunErr("Already exists"),
		"removeSharedFolder": vmrunErr("A file was not found"),
	}}
	c := Client{R: f}
	if err := c.AddShare(ctx, vmxPath, "fc-1", `C:\dev\app`); err != nil {
		t.Errorf("existing share: %v", err)
	}
	if err := c.RemoveShare(ctx, vmxPath, "fc-1"); err != nil {
		t.Errorf("missing share: %v", err)
	}
	f.err["addSharedFolder"] = vmrunErr("A file was not found") // missing host dir: a real failure
	if err := c.AddShare(ctx, vmxPath, "fc-1", `C:\nope`); err == nil {
		t.Error("add with missing host dir should fail")
	}
	f.err["removeSharedFolder"] = vmrunErr("The virtual machine is not powered on")
	if err := c.RemoveShare(ctx, vmxPath, "fc-1"); err == nil || !strings.Contains(err.Error(), "not powered on") {
		t.Errorf("remove error: %v", err)
	}
}

func TestCommandError(t *testing.T) {
	e := &CommandError{Tool: "vmrun", Args: []string{"start", "x.vmx"},
		Output: "\nError: Cannot open VM: x.vmx, The virtual machine cannot be found\n", Err: errors.New("exit status 255")}
	if e.Message() != "Cannot open VM: x.vmx, The virtual machine cannot be found" {
		t.Errorf("Message = %q", e.Message())
	}
	if !strings.HasPrefix(e.Error(), "vmrun start x.vmx: Cannot open VM") || !errors.Is(e, e.Err) {
		t.Errorf("Error = %q", e.Error())
	}
	if e := (&CommandError{Tool: "t", Err: errors.New("exit 1")}); !strings.HasSuffix(e.Error(), "exit 1") {
		t.Errorf("empty output: %q", e.Error())
	}
	if toolName(`C:\Program Files\VMware\VMware Workstation\vmrun.exe`) != "vmrun" {
		t.Error("toolName")
	}
}

func TestExecRunner(t *testing.T) {
	// A missing binary is a CommandError carrying the exec error.
	_, err := ExecRunner{Path: filepath.Join(t.TempDir(), "nope.exe")}.Run(context.Background(), "list")
	var ce *CommandError
	if !errors.As(err, &ce) || ce.Tool != "nope" {
		t.Errorf("err = %v", err)
	}
}

func TestLocate(t *testing.T) {
	has := func(paths ...string) func(string) bool {
		return func(p string) bool {
			for _, x := range paths {
				if p == x {
					return true
				}
			}
			return false
		}
	}
	regDir := filepath.Join("X", "VMware Workstation")
	in, err := locate(func() (string, string) { return regDir + string(filepath.Separator), "26.0.1.25688693" }, has(filepath.Join(regDir, "vmrun.exe")))
	if err != nil || in.Dir != regDir || in.Version != "26.0.1.25688693" || in.Vdiskmanager() != filepath.Join(regDir, "vmware-vdiskmanager.exe") {
		t.Errorf("registry: %+v %v", in, err)
	}
	def := Install{Dir: filepath.Clean(defaultDir)}
	if in, err := locate(func() (string, string) { return "", "" }, has(def.Vmrun())); err != nil || in.Dir != def.Dir {
		t.Errorf("default dir: %+v %v", in, err)
	}
	if _, err := locate(func() (string, string) { return regDir, "16.2.5" }, has(filepath.Join(regDir, "vmrun.exe"))); err == nil || !strings.Contains(err.Error(), "too old") {
		t.Errorf("old version: %v", err)
	}
	if _, err := locate(func() (string, string) { return "", "" }, has()); !errors.Is(err, ErrNotInstalled) {
		t.Errorf("missing: %v", err)
	}
}

func TestVersion(t *testing.T) {
	for v, ok := range map[string]bool{"26.0.1.25688693": true, "17.5.0": true, "17.6.4": true, "17.0.2": false, "16.2.5": false, "x": false, "17": false, "17.x": false} {
		if err := CheckVersion(v); (err == nil) != ok {
			t.Errorf("CheckVersion(%q) = %v", v, err)
		}
	}
}

func TestNetworks(t *testing.T) {
	reg := map[string][2]string{"vmnet1": {"192.168.201.0", ""}, "vmnet8": {"192.168.146.0", ""}}
	n, err := readNetworks(func(v string) (string, string, bool) { r, ok := reg[v]; return r[0], r[1], ok })
	if err != nil {
		t.Fatal(err)
	}
	if n.HostOnly.String() != "192.168.201.0/24" || n.NAT.String() != "192.168.146.0/24" {
		t.Errorf("subnets %v %v", n.HostOnly, n.NAT)
	}
	if HostIP(n.HostOnly).String() != "192.168.201.1" || VMIP(n.HostOnly).String() != "192.168.201.10" || Prefix(n.HostOnly) != 24 {
		t.Errorf("addresses %v %v %d", HostIP(n.HostOnly), VMIP(n.HostOnly), Prefix(n.HostOnly))
	}
	delete(reg, "vmnet8")
	if _, err := readNetworks(func(v string) (string, string, bool) { r, ok := reg[v]; return r[0], r[1], ok }); !errors.Is(err, ErrNoNetworks) {
		t.Errorf("missing vmnet8: %v", err)
	}
	reg["vmnet8"] = [2]string{"bogus", ""}
	if _, err := readNetworks(func(v string) (string, string, bool) { r, ok := reg[v]; return r[0], r[1], ok }); err == nil {
		t.Error("bad address accepted")
	}
	if s, err := ParseSubnet("10.7.3.77", "255.255.0.0"); err != nil || s.String() != "10.7.0.0/16" {
		t.Errorf("mask: %v %v", s, err)
	}
	for _, m := range []string{"255.255.255.252", "nope", "255.0.255.0"} {
		if _, err := ParseSubnet("10.0.0.0", m); err == nil {
			t.Errorf("mask %q accepted", m)
		}
	}
}

func readFixture(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/workstation-26.vmx")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestVMXRoundTripReal(t *testing.T) {
	b := readFixture(t)
	v, err := ParseVMX(b)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(v.Bytes(), b) {
		t.Fatal("unmodified .vmx does not round-trip byte-for-byte")
	}
	if u, ok := v.Get("UUID.BIOS"); !ok || !strings.HasPrefix(u, "56 4d") {
		t.Errorf("uuid.bios = %q %v", u, ok)
	}
	if p, _ := v.Get("sharedFolder0.hostPath"); p != `C:\dev\r4-extra` { // backslashes are literal in .vmx
		t.Errorf("hostPath = %q", p)
	}
}

func TestClearSharesAndDetachSeed(t *testing.T) {
	v, err := ParseVMX(readFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	before := len(v.lines)
	v.ClearShares()
	v.DetachSeed()
	out := string(v.Bytes())
	if strings.Contains(strings.ToLower(out), "sharedfolder0") || strings.Contains(strings.ToLower(out), "sharedfolder1") {
		t.Error("shares left after ClearShares")
	}
	if n, _ := v.Get("sharedFolder.maxNum"); n != "0" {
		t.Errorf("maxNum = %q", n)
	}
	if p, _ := v.Get("sata0:1.present"); p != "FALSE" {
		t.Errorf("seed present = %q", p)
	}
	for _, keep := range []string{`uuid.bios = "56 4d`, `ethernet0.pciSlotNumber = "`, `guestInfo.detailed.data = "architecture='X86'`} {
		if !strings.Contains(out, keep) {
			t.Errorf("lost VMware-written key %q", keep)
		}
	}
	if got := len(v.lines); got != before-14 { // 14 share keys and maxNum removed; maxNum re-added
		t.Errorf("lines %d → %d", before, got)
	}
}

func TestVMXEscapes(t *testing.T) {
	v, err := ParseVMX([]byte("a = \"say |22hi|22 |7C x\"\n# comment\nbroken line\nk = unquoted\n"))
	if err != nil {
		t.Fatal(err)
	}
	if a, _ := v.Get("a"); a != `say "hi" | x` {
		t.Errorf("decode: %q", a)
	}
	v.Set("b", "C:\\x \"q\" |p")
	v.Set("a", "new")
	want := "a = \"new\"\r\n# comment\r\nbroken line\r\nk = unquoted\r\nb = \"C:\\x |22q|22 |7Cp\"\r\n"
	if got := string(v.Bytes()); got != want {
		t.Errorf("render:\n%q\nwant\n%q", got, want)
	}
	if _, ok := v.Get("k"); ok {
		t.Error("unquoted value parsed as a key")
	}
	if decodeValue("|zz|4") != "|zz|4" {
		t.Error("invalid escapes must stay literal")
	}
}

func spec() Spec {
	return Spec{Name: "fclaude", MemMiB: 8192, CPUs: 6, Disk: "fclaude-disk.vmdk", Seed: "seed.iso",
		ConsoleLog: "console.log", MACNAT: "00:50:56:3F:FC:01", MACHost: "00:50:56:3f:fc:02"}
}

func TestNewVMXGolden(t *testing.T) {
	v, err := NewVMX(spec())
	if err != nil {
		t.Fatal(err)
	}
	got := v.Bytes()
	const golden = "testdata/new.vmx.golden"
	if *update {
		if err := os.WriteFile(golden, got, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("NewVMX differs from %s (run with -update after checking):\n%s", golden, got)
	}
	// It must also re-parse to the same thing.
	if v2, err := ParseVMX(got); err != nil || !bytes.Equal(v2.Bytes(), got) {
		t.Errorf("re-parse: %v", err)
	}
}

func TestNewVMXValidates(t *testing.T) {
	for name, mod := range map[string]func(*Spec){
		"no disk":   func(s *Spec) { s.Disk = "" },
		"tiny mem":  func(s *Spec) { s.MemMiB = 512 },
		"bad mac":   func(s *Spec) { s.MACNAT = "52:54:00:fc:1a:01" },
		"high mac":  func(s *Spec) { s.MACHost = "00:50:56:40:00:01" },
		"same macs": func(s *Spec) { s.MACHost = s.MACNAT },
	} {
		s := spec()
		mod(&s)
		if _, err := NewVMX(s); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	s := spec()
	s.ConsoleLog = ""
	v, err := NewVMX(s)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := v.Get("serial0.present"); ok {
		t.Error("serial port without a console log")
	}
}

func TestWriteReadVMX(t *testing.T) {
	v, err := NewVMX(spec())
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "fclaude.vmx")
	if err := v.WriteFile(p); err != nil {
		t.Fatal(err)
	}
	r, err := ReadVMX(p)
	if err != nil || !bytes.Equal(r.Bytes(), v.Bytes()) {
		t.Errorf("read back: %v", err)
	}
	if _, err := ReadVMX(filepath.Join(t.TempDir(), "missing.vmx")); err == nil {
		t.Error("missing file")
	}
	if err := v.WriteFile(filepath.Join(t.TempDir(), "no", "such", "dir", "x.vmx")); err == nil {
		t.Error("write into missing dir")
	}
	entries, _ := os.ReadDir(filepath.Dir(p))
	if len(entries) != 1 {
		t.Errorf("temp file left behind: %v", entries)
	}
}

func TestDisk(t *testing.T) {
	got := ImportDiskArgs(`a\cloud.vmdk`, `b\fclaude-disk.vmdk`, 32)
	want := [][]string{{"-r", `a\cloud.vmdk`, "-t", "0", `b\fclaude-disk.vmdk`}, {"-x", "32GB", `b\fclaude-disk.vmdk`}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%q", got)
	}
	mac, err := RandomMAC(bytes.NewReader([]byte{0xff, 0xab, 0x01}))
	if err != nil || mac != "00:50:56:3f:ab:01" || !ValidStaticMAC(mac) {
		t.Errorf("RandomMAC = %q %v", mac, err)
	}
	if _, err := RandomMAC(bytes.NewReader(nil)); err == nil {
		t.Error("short entropy accepted")
	}
}
