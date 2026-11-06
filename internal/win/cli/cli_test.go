package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/robymus/fclaude/internal/win/config"
	"github.com/robymus/fclaude/internal/win/install"
	"github.com/robymus/fclaude/internal/win/lease"
	"github.com/robymus/fclaude/internal/win/vmssh"
	"github.com/robymus/fclaude/internal/win/vmw"
)

type fakeVM struct {
	mu      sync.Mutex
	running bool
	calls   []string
	shares  map[string]string
}

func (v *fakeVM) log(f string, a ...any) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.calls = append(v.calls, fmt.Sprintf(f, a...))
}

func (v *fakeVM) Start(context.Context, string) error { v.log("start"); v.running = true; return nil }
func (v *fakeVM) Stop(_ context.Context, _ string, hard bool) error {
	v.log("stop hard=%v", hard)
	v.running = false
	return nil
}
func (v *fakeVM) Running(context.Context, string) (bool, error)     { return v.running, nil }
func (v *fakeVM) EnableSharedFolders(context.Context, string) error { return nil }
func (v *fakeVM) AddShare(_ context.Context, _, name, dir string) error {
	v.log("add %s", name)
	v.shares[name] = dir
	return nil
}

func (v *fakeVM) RemoveShare(_ context.Context, _, name string) error {
	v.log("remove %s", name)
	delete(v.shares, name)
	return nil
}

// fakeGuest answers by script content.
type fakeGuest struct {
	mu   sync.Mutex
	cmds []string
	// reply returns the output for a command; nil = "" and success.
	reply func(cmd string) (string, error)
}

func (g *fakeGuest) Output(_ context.Context, cmd string) (string, error) {
	g.mu.Lock()
	g.cmds = append(g.cmds, cmd)
	g.mu.Unlock()
	if g.reply != nil {
		return g.reply(cmd)
	}
	return "", nil
}
func (g *fakeGuest) Close() error { return nil }

type testApp struct {
	*App
	vm      *fakeVM
	guest   *fakeGuest
	dials   int
	ssh     [][]string
	stderr  *bytes.Buffer
	stdout  *bytes.Buffer
	vdisk   [][]string
	dialErr error
}

func newTestApp(t *testing.T) *testApp {
	t.Helper()
	home := t.TempDir()
	l := install.Layout{Home: home}
	_ = os.MkdirAll(l.VMDir(), 0o700)
	v, err := vmw.NewVMX(vmw.Spec{Name: "fclaude", MemMiB: 8192, CPUs: 6, Disk: install.DiskName, Seed: install.SeedName,
		ConsoleLog: install.ConsoleName, MACNAT: "00:50:56:00:00:01", MACHost: "00:50:56:00:00:02"})
	if err != nil {
		t.Fatal(err)
	}
	v.Set("sharedFolder0.present", "TRUE")
	v.Set("sharedFolder.maxNum", "1")
	if err := v.WriteFile(l.VMX()); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(l.Disk(), make([]byte, 1<<20), 0o600)
	ta := &testApp{vm: &fakeVM{shares: map[string]string{}}, guest: &fakeGuest{}, stderr: &bytes.Buffer{}, stdout: &bytes.Buffer{}}
	self, err := lease.Self()
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	cfg.ShareRoot = filepath.Join(home, "dev")
	ta.App = &App{
		Layout: l, Cfg: cfg,
		Info:  install.VMInfo{VMX: l.VMX(), VMIP: "192.168.201.10", Provisioned: true},
		VM:    ta.vm,
		Vdisk: runnerFunc(func(args ...string) (string, error) { ta.vdisk = append(ta.vdisk, args); return "", nil }),
		Store: lease.NewStore(l.Leases()),
		Self:  self,
		In:    strings.NewReader(""), Out: ta.stdout, Err: ta.stderr,
		Now:         time.Now,
		BootTimeout: time.Second, Poll: time.Millisecond,
	}
	ta.Dial = func(context.Context, string) (Guest, error) {
		ta.dials++
		if ta.dialErr != nil {
			return nil, ta.dialErr
		}
		return ta.guest, nil
	}
	ta.SSH = func(args []string) (int, error) { ta.ssh = append(ta.ssh, args); return 0, nil }
	return ta
}

type runnerFunc func(args ...string) (string, error)

func (f runnerFunc) Run(_ context.Context, args ...string) (string, error) { return f(args...) }

func TestParse(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want options
		err  string
	}{
		{nil, options{dir: "."}, ""},
		{[]string{"--dir", `C:\dev\x`, "--resume"}, options{dir: `C:\dev\x`, claude: []string{"--resume"}}, ""},
		{[]string{"--no-mount", "--", "-p", "--status"}, options{dir: ".", noMount: true, claude: []string{"-p", "--status"}}, ""},
		{[]string{"--mount", "a", "b", "--no-mount"}, options{dir: ".", mode: "mount", mounts: []string{"a", "b"}, noMount: true}, ""},
		{[]string{"-s"}, options{dir: ".", mode: "status"}, ""},
		{[]string{"--cleanup", "x"}, options{dir: ".", mode: "cleanup", target: "x"}, ""},
		{[]string{"grow-disk", "64G"}, options{dir: ".", mode: "grow-disk", target: "64G"}, ""},
		{[]string{"uninstall"}, options{dir: ".", mode: "uninstall"}, ""},
		{[]string{"--status", "--update"}, options{}, "cannot be combined"},
		{[]string{"--dir"}, options{}, "needs a path"},
		{[]string{"--cleanup"}, options{}, "needs a workspace"},
		{[]string{"grow-disk"}, options{}, "usage"},
	} {
		got, err := parse(tc.args)
		if tc.err != "" {
			if err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Errorf("%v: err %v, want %q", tc.args, err, tc.err)
			}
			continue
		}
		if err != nil || fmt.Sprint(got) != fmt.Sprint(tc.want) {
			t.Errorf("%v: got %+v err=%v, want %+v", tc.args, got, err, tc.want)
		}
	}
}

func TestStaleness(t *testing.T) {
	const day = 86400
	now := int64(100 * day)
	f := parseProbe("APT_OK=" + fmt.Sprint(now-20*day) + "\nATTEMPT=error exit 100\nCLAUDE=-\n")
	if f.AptOK != now-20*day || f.Attempt != "error" || f.Claude != -1 {
		t.Fatalf("parse: %+v", f)
	}
	msgs := stalenessMessages(now, 15, true, f)
	if len(msgs) != 1 || !strings.Contains(msgs[0], "20 days") || !strings.Contains(msgs[0], "FAILED") {
		t.Errorf("msgs %q", msgs)
	}
	if m := stalenessMessages(now, 15, false, f); len(m) != 0 {
		t.Errorf("apt disabled: %q", m)
	}
	m := stalenessMessages(now, 15, true, probeFacts{AptOK: -1, Claude: now - 30*day})
	if len(m) != 2 || !strings.Contains(m[0], "unknown time") || !strings.Contains(m[0], "no successful") || !strings.Contains(m[1], "30 days") {
		t.Errorf("unknown apt + stale claude: %q", m)
	}
	if m := stalenessMessages(now, 15, true, probeFacts{AptOK: now - day, Attempt: "ok", Claude: now}); len(m) != 0 {
		t.Errorf("fresh: %q", m)
	}
	if m := stalenessMessages(now, 0, true, probeFacts{AptOK: -1}); m != nil {
		t.Errorf("disabled: %q", m)
	}
}

func TestClaudeCommandAndSSHArgs(t *testing.T) {
	cmd := claudeCommand("/home/fclaude/it's", `C:\dev\it's`, []string{"--resume", "a b"})
	for _, want := range []string{loginEnv, `cd '/home/fclaude/it'\''s'`, "exec claude --dangerously-skip-permissions '--resume' 'a b'", `fclaude --cleanup C:\dev\it'\''s`} {
		if !strings.Contains(cmd, want) {
			t.Errorf("command %q lacks %q", cmd, want)
		}
	}
	ta := newTestApp(t)
	args := strings.Join(ta.sshArgs("maint", true, ""), " ")
	kh := filepath.ToSlash(ta.Layout.KnownHosts())
	for _, want := range []string{"-i " + filepath.ToSlash(ta.Layout.Key("maint")), `UserKnownHostsFile="` + kh + `"`, "StrictHostKeyChecking=yes", "-t maint@192.168.201.10"} {
		if !strings.Contains(args, want) {
			t.Errorf("ssh args %q lack %q", args, want)
		}
	}
	if strings.HasSuffix(args, " ") {
		t.Error("empty remote command appended")
	}
}

func TestEnsureRunningColdBootClearsSharesAndLeases(t *testing.T) {
	ta := newTestApp(t)
	ws := lease.Workspace{Key: "k1", WinPath: `C:\dev\a`, VMPath: "/home/fclaude/a", Share: "fc-k1"}
	if err := ta.Store.Acquire(ws, lease.Holder{PID: 1, Started: 1}, func(int) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := ta.EnsureRunning(context.Background()); err != nil {
		t.Fatal(err)
	}
	if strings.Join(ta.vm.calls, ",") != "start" {
		t.Errorf("vm calls %v", ta.vm.calls)
	}
	if st, _ := ta.Store.List(); len(st) != 0 {
		t.Errorf("leases survived a cold boot: %+v", st)
	}
	v, _ := vmw.ReadVMX(ta.Info.VMX)
	if _, ok := v.Get("sharedFolder0.present"); ok {
		t.Error("shares survived a cold boot")
	}
	if !strings.Contains(ta.stderr.String(), "cleared 1 workspace lease") {
		t.Errorf("stderr: %s", ta.stderr.String())
	}

	// Warm: nothing is started or swept.
	ta.vm.calls = nil
	_ = ta.Store.Acquire(ws, lease.Holder{PID: 1, Started: 1}, func(int) error { return nil })
	if _, err := ta.EnsureRunning(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(ta.vm.calls) != 0 {
		t.Errorf("warm start acted: %v", ta.vm.calls)
	}
	if st, _ := ta.Store.List(); len(st) != 1 {
		t.Errorf("warm start dropped leases: %+v", st)
	}
}

func TestEnsureRunningTimeoutAndHostKey(t *testing.T) {
	ta := newTestApp(t)
	ta.dialErr = errors.New("connection refused")
	if _, err := ta.EnsureRunning(context.Background()); err == nil || !strings.Contains(err.Error(), "did not answer ssh") {
		t.Fatalf("err %v", err)
	}
	if ta.dials < 2 {
		t.Errorf("dials %d: no retry", ta.dials)
	}
	ta.dials = 0
	ta.dialErr = fmt.Errorf("x: %w", vmssh.ErrHostKeyMismatch)
	if _, err := ta.EnsureRunning(context.Background()); !errors.Is(err, vmssh.ErrHostKeyMismatch) || ta.dials != 1 {
		t.Fatalf("host key mismatch: err %v dials %d (must not retry)", err, ta.dials)
	}
}

func TestStatus(t *testing.T) {
	ta := newTestApp(t)
	if err := ta.Status(context.Background()); err != nil {
		t.Fatal(err)
	}
	out := ta.stdout.String()
	for _, want := range []string{"State:       stopped", "vCPUs:       6", "Memory:      8 GiB", "(none)"} {
		if !strings.Contains(out, want) {
			t.Errorf("status lacks %q:\n%s", want, out)
		}
	}
	if ta.dials != 0 || len(ta.vm.calls) != 0 {
		t.Error("status of a stopped VM must not start or dial it")
	}

	ta = newTestApp(t)
	ta.vm.running = true
	ta.guest.reply = func(string) (string, error) {
		return "MEM=900 MiB used of 7900 MiB\nDISK=5G used of 31G (17%)\nCLAUDE=2.1.318 (Claude Code)\nAPT=ok 2026-11-01\nUP=\n", nil
	}
	dead := lease.Workspace{Key: "k2", WinPath: `C:\dev\gone`, VMPath: "/home/fclaude/gone", Share: "fc-k2"}
	ta.Store.Alive = func(h lease.Holder) bool { return h.PID != 99 }
	_ = ta.Store.Acquire(dead, lease.Holder{PID: 99, Started: 1}, func(int) error { return nil })
	live := lease.Workspace{Key: "k3", WinPath: `C:\dev\live`, VMPath: "/home/fclaude/live", Share: "fc-k3"}
	_ = ta.Store.Acquire(live, lease.Holder{PID: 98, Started: 1}, func(int) error { return nil })
	if err := ta.Status(context.Background()); err != nil {
		t.Fatal(err)
	}
	out = ta.stdout.String()
	for _, want := range []string{"State:       running", "Claude:      2.1.318", "Guest /:     5G used", `C:\dev\live`, "sessions=1"} {
		if !strings.Contains(out, want) {
			t.Errorf("status lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Uptime") || strings.Contains(out, `C:\dev\gone`) {
		t.Errorf("empty fact printed or dead workspace listed:\n%s", out)
	}
	if strings.Join(ta.vm.calls, ",") != "remove fc-k2" {
		t.Errorf("reap: vm calls %v", ta.vm.calls)
	}
}

func TestCleanupByRecordedPath(t *testing.T) {
	ta := newTestApp(t)
	ta.vm.running = true
	ws := lease.Workspace{Key: "k4", WinPath: `C:\dev\stuck`, VMPath: "/home/fclaude/stuck", Share: "fc-k4"}
	_ = ta.Store.Acquire(ws, lease.Holder{PID: 1, Started: 1}, func(int) error { return nil })
	if err := ta.Cleanup(context.Background(), "/home/fclaude/stuck"); err != nil {
		t.Fatal(err)
	}
	if strings.Join(ta.vm.calls, ",") != "remove fc-k4" {
		t.Errorf("vm calls %v", ta.vm.calls)
	}
	if st, _ := ta.Store.List(); len(st) != 0 {
		t.Errorf("lease kept: %+v", st)
	}
	if err := ta.Cleanup(context.Background(), "nothing-like-this"); err == nil {
		t.Error("unknown workspace accepted")
	}
}

func TestShutdownAsks(t *testing.T) {
	ta := newTestApp(t)
	ta.vm.running = true
	ta.In = strings.NewReader("n\n")
	if err := ta.Shutdown(context.Background()); err != nil || ta.vm.running == false {
		t.Fatalf("declined shutdown: err %v running %v", err, ta.vm.running)
	}
	ta.In = strings.NewReader("y\n")
	if err := ta.Shutdown(context.Background()); err != nil || ta.vm.running {
		t.Fatalf("shutdown: err %v running %v", err, ta.vm.running)
	}
}

func TestGrowDisk(t *testing.T) {
	for in, want := range map[string]int{"64": 64, "64G": 64, "64gb": 64, " 100GB ": 100} {
		if n, err := ParseSizeGB(in); err != nil || n != want {
			t.Errorf("ParseSizeGB(%q) = %d %v", in, n, err)
		}
	}
	for _, in := range []string{"", "0", "-3", "1T", "x"} {
		if _, err := ParseSizeGB(in); err == nil {
			t.Errorf("ParseSizeGB(%q) accepted", in)
		}
	}
	ta := newTestApp(t)
	ta.vm.running = true
	ta.In = strings.NewReader("y\n")
	ta.guest.reply = func(string) (string, error) { return "/dev/sda1 63G\n", nil }
	if err := ta.GrowDisk(context.Background(), "64G"); err != nil {
		t.Fatal(err)
	}
	if strings.Join(ta.vm.calls, ",") != "stop hard=false,start" {
		t.Errorf("vm calls %v", ta.vm.calls)
	}
	if len(ta.vdisk) != 1 || strings.Join(ta.vdisk[0], " ") != strings.Join(vmw.GrowDiskArgs(ta.Layout.Disk(), 64), " ") {
		t.Errorf("vdisk %v", ta.vdisk)
	}
	if last := ta.guest.cmds[len(ta.guest.cmds)-1]; last != growScript || !strings.Contains(ta.stdout.String(), "63G") {
		t.Errorf("grow script not run: %q", ta.guest.cmds)
	}
}

// Session tests need real Windows paths (share.Workspace, share.Setup's
// directory check): they run in the Windows test pass.
func windowsOnly(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "windows" {
		t.Skip("needs Windows paths")
	}
}

func TestSessionMountsRunsAndReleases(t *testing.T) {
	windowsOnly(t)
	ta := newTestApp(t)
	ta.vm.running = true
	proj := filepath.Join(ta.Cfg.ShareRoot, "proj")
	_ = os.MkdirAll(proj, 0o700)
	// The fast-path check fails, so Setup adds the share and mounts.
	ta.guest.reply = func(cmd string) (string, error) {
		if strings.Contains(cmd, "vmware-hgfsclient | grep") {
			return "", errors.New("exit 1")
		}
		return "", nil
	}
	var during []lease.Status
	ta.SSH = func(args []string) (int, error) {
		ta.ssh = append(ta.ssh, args)
		during, _ = ta.Store.List()
		return 3, nil
	}
	code, err := ta.Session(context.Background(), proj, false, []string{"--resume"})
	if err != nil || code != 3 {
		t.Fatalf("code %d err %v", code, err)
	}
	if len(during) != 1 || len(during[0].Live) != 1 || during[0].Workspace.VMPath != "/home/fclaude/proj" {
		t.Errorf("lease during the session: %+v", during)
	}
	remote := ta.ssh[0][len(ta.ssh[0])-1]
	if !strings.Contains(remote, "cd '/home/fclaude/proj'") || !strings.HasSuffix(remote, "'--resume'") {
		t.Errorf("remote %q", remote)
	}
	if st, _ := ta.Store.List(); len(st) != 0 {
		t.Errorf("not released: %+v", st)
	}
	calls := strings.Join(ta.vm.calls, ",")
	if !strings.HasPrefix(calls, "add fc-") || !strings.Contains(calls, ",remove fc-") {
		t.Errorf("vm calls %v", ta.vm.calls)
	}
}

func TestSessionOutsideShareRoot(t *testing.T) {
	windowsOnly(t)
	ta := newTestApp(t)
	if _, err := ta.Session(context.Background(), ta.Layout.Home, false, nil); err == nil || !strings.Contains(err.Error(), "projects must live under") {
		t.Fatalf("err %v", err)
	}
	if ta.dials != 0 || len(ta.vm.calls) != 0 {
		t.Error("a refused session touched the VM")
	}
}
