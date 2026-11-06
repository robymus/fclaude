package vbox

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// fakeRunner records invocations and replays canned responses keyed by the
// first two args.
type fakeRunner struct {
	calls [][]string
	resp  map[string]struct {
		out string
		err error
	}
}

func (f *fakeRunner) on(key, out string, err error) {
	if f.resp == nil {
		f.resp = map[string]struct {
			out string
			err error
		}{}
	}
	f.resp[key] = struct {
		out string
		err error
	}{out, err}
}

func (f *fakeRunner) Run(_ context.Context, args ...string) (string, error) {
	f.calls = append(f.calls, args)
	key := args[0]
	if len(args) > 1 {
		key += " " + args[1]
	}
	r := f.resp[key]
	return r.out, r.err
}

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestParseMachineReadable(t *testing.T) {
	m := ParseMachineReadable(fixture(t, "showvminfo-running.txt"))
	want := map[string]string{
		"VMState":  "running",
		"memory":   "8192",
		"CfgFile":  `C:\Users\bro\VirtualBox VMs\fclaude\fclaude.vbox`,
		"SATA-0-0": `C:\Users\bro\.fclaude\fclaude-disk.vdi`,
	}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("%s = %q, want %q", k, m[k], v)
		}
	}
	if _, ok := m["Encryption:     disabled"]; ok {
		t.Error("non key=value line parsed")
	}
	if m := ParseMachineReadable(`"unterminated=1` + "\n" + `k="v`); len(m) != 0 {
		t.Errorf("malformed lines parsed: %v", m)
	}
	if m := ParseMachineReadable(fixture(t, "showvminfo-off-crlf.txt")); m["VMState"] != "poweroff" {
		t.Errorf("CRLF: %v", m)
	}
}

func TestTransientShares(t *testing.T) {
	got := TransientShares(ParseMachineReadable(fixture(t, "showvminfo-running.txt")))
	want := []Share{{"fc-0a1b2c3d4e5f6071", `C:\dev\app`}, {"fc-quote", `C:\dev\say "hi"`}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v", got)
	}
}

func TestState(t *testing.T) {
	ctx := context.Background()
	f := &fakeRunner{}
	c := Client{R: f}
	f.on("showvminfo fclaude", fixture(t, "showvminfo-running.txt"), nil)
	if st, err := c.State(ctx, "fclaude"); err != nil || st != StateRunning || st.IsOff() {
		t.Fatalf("running: %v %v", st, err)
	}
	f.on("showvminfo fclaude", "", &CommandError{Stderr: "VBoxManage.exe: error: Could not find a registered machine named 'fclaude'", Err: errors.New("exit 1")})
	if st, err := c.State(ctx, "fclaude"); err != nil || st != StateNotFound || !st.IsOff() {
		t.Fatalf("notfound: %v %v", st, err)
	}
	f.on("showvminfo fclaude", "", &CommandError{Stderr: "something else", Err: errors.New("exit 1")})
	if _, err := c.State(ctx, "fclaude"); err == nil || !strings.Contains(err.Error(), "something else") {
		t.Fatalf("other error: %v", err)
	}
	f.on("showvminfo fclaude", `name="fclaude"`, nil)
	if _, err := c.State(ctx, "fclaude"); err == nil {
		t.Fatal("missing VMState not reported")
	}
}

func TestCommands(t *testing.T) {
	ctx := context.Background()
	f := &fakeRunner{}
	c := Client{R: f}
	_ = c.StartHeadless(ctx, "fclaude")
	_ = c.Resume(ctx, "fclaude")
	_ = c.ACPIShutdown(ctx, "fclaude")
	_ = c.PowerOff(ctx, "fclaude")
	_ = c.AddTransientShare(ctx, "fclaude", "fc-1", `C:\dev\my app`)
	_ = c.RemoveTransientShare(ctx, "fclaude", "fc-1")
	want := [][]string{
		{"startvm", "fclaude", "--type", "headless"},
		{"controlvm", "fclaude", "resume"},
		{"controlvm", "fclaude", "acpipowerbutton"},
		{"controlvm", "fclaude", "poweroff"},
		{"sharedfolder", "add", "fclaude", "--name", "fc-1", "--hostpath", `C:\dev\my app`, "--transient"},
		{"sharedfolder", "remove", "fclaude", "--name", "fc-1", "--transient"},
	}
	if !reflect.DeepEqual(f.calls, want) {
		t.Errorf("calls:\n%q\nwant\n%q", f.calls, want)
	}
}

func TestVersion(t *testing.T) {
	f := &fakeRunner{}
	f.on("--version", "WARNING: something\n7.1.4r165100\n", nil)
	maj, minor, raw, err := Client{R: f}.Version(context.Background())
	if err != nil || maj != 7 || minor != 1 || !strings.HasSuffix(raw, "7.1.4r165100") {
		t.Fatalf("%d.%d %q %v", maj, minor, raw, err)
	}
	for _, bad := range []string{"", "seven", "7", "7.x.1"} {
		if _, _, err := ParseVersion(bad); err == nil {
			t.Errorf("ParseVersion(%q) accepted", bad)
		}
	}
}

func TestDefineCommands(t *testing.T) {
	spec := VMSpec{
		Name: "fclaude", UUID: "fc1aede0-0000-4000-8000-525400fc1ade", MemMiB: 8192, CPUs: 6,
		DiskPath: `C:\u\.fclaude\fclaude-disk.vdi`, SeedPath: `C:\u\.fclaude\seed.iso`,
		ConsoleLog: `C:\u\.fclaude\console.log`,
		MACNAT:     "52:54:00:fc:1a:df", MACHost: "52:54:00:fc:1a:e0",
		HostOnlyIf: "VirtualBox Host-Only Ethernet Adapter",
	}
	cmds, err := DefineCommands(spec)
	if err != nil {
		t.Fatal(err)
	}
	if len(cmds) != 5 || cmds[0][0] != "createvm" || cmds[1][0] != "modifyvm" {
		t.Fatalf("unexpected shape: %q", cmds)
	}
	mod := strings.Join(cmds[1], " ")
	for _, want := range []string{"--memory 8192", "--cpus 6", "--macaddress1 525400FC1ADF",
		"--macaddress2 525400FC1AE0", "--nat-localhostreachable1 off", "--clipboard-mode disabled",
		"--drag-and-drop disabled", "--uart-mode1 file " + spec.ConsoleLog} {
		if !strings.Contains(mod, want) {
			t.Errorf("modifyvm missing %q", want)
		}
	}
	if cmds[3][len(cmds[3])-5] != spec.DiskPath || cmds[4][len(cmds[4])-1] != spec.SeedPath {
		t.Errorf("storage attach wrong: %q %q", cmds[3], cmds[4])
	}

	bad := spec
	bad.MACNAT = "nope"
	if _, err := DefineCommands(bad); err == nil {
		t.Error("bad MAC accepted")
	}
	bad = spec
	bad.MemMiB = 10
	if _, err := DefineCommands(bad); err == nil {
		t.Error("tiny memory accepted")
	}
	if _, err := DefineCommands(VMSpec{}); err == nil {
		t.Error("empty spec accepted")
	}
}

func TestImportDiskCommands(t *testing.T) {
	got := ImportDiskCommands("a.vmdk", "b.vdi", 32768)
	want := [][]string{{"clonemedium", "disk", "a.vmdk", "b.vdi", "--format", "VDI"},
		{"modifymedium", "disk", "b.vdi", "--resize", "32768"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%q", got)
	}
}

func TestCommandError(t *testing.T) {
	e := &CommandError{Args: []string{"startvm", "x"}, Stderr: "  boom \n", Err: errors.New("exit 1")}
	if e.Error() != "VBoxManage startvm x: boom" || !errors.Is(e, e.Err) {
		t.Errorf("%q", e.Error())
	}
	if (&CommandError{Args: []string{"x"}, Err: errors.New("exit 2")}).Error() != "VBoxManage x: exit 2" {
		t.Error("fallback message")
	}
}

// TestMain lets the test binary stand in for VBoxManage (portable to Windows CI).
func TestMain(m *testing.M) {
	switch os.Getenv("FAKE_VBOXMANAGE") {
	case "ok":
		_, _ = os.Stdout.WriteString("hi\n")
		os.Exit(0)
	case "fail":
		_, _ = os.Stderr.WriteString("bad\n")
		os.Exit(3)
	}
	os.Exit(m.Run())
}

func TestExecRunner(t *testing.T) {
	r := ExecRunner{Path: testExe(t)}
	t.Setenv("FAKE_VBOXMANAGE", "ok")
	out, err := r.Run(context.Background(), "-test.run=^$")
	if err != nil || out != "hi\n" {
		t.Fatalf("%q %v", out, err)
	}
	t.Setenv("FAKE_VBOXMANAGE", "fail")
	_, err = r.Run(context.Background(), "-test.run=^$")
	var ce *CommandError
	if !errors.As(err, &ce) || !strings.Contains(ce.Stderr, "bad") {
		t.Fatalf("%v", err)
	}
}

func TestLocate(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, exeName)
	if err := os.WriteFile(exe, nil, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VBOX_MSI_INSTALL_PATH", dir)
	if got, err := Locate(); err != nil || got != exe {
		t.Fatalf("Locate = %q, %v", got, err)
	}
}

// testExe is this test binary's absolute path. os.Args[0] can be a bare
// relative name (e.g. when launched through WSL interop), which os/exec
// refuses to run.
func testExe(t *testing.T) string {
	t.Helper()
	p, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return p
}
