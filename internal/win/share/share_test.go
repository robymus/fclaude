package share

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/robymus/fclaude/internal/win/lease"
)

type fakeHost struct {
	calls  []string
	addErr error
	rmErr  error
}

func (f *fakeHost) EnableSharedFolders(context.Context, string) error {
	f.calls = append(f.calls, "enable")
	return nil
}

func (f *fakeHost) AddShare(_ context.Context, _, name, dir string) error {
	f.calls = append(f.calls, "add "+name+" "+dir)
	return f.addErr
}

func (f *fakeHost) RemoveShare(_ context.Context, _, name string) error {
	f.calls = append(f.calls, "remove "+name)
	return f.rmErr
}

type fakeGuest struct {
	scripts []string
	err     error
	setUp   bool // the fast-path check succeeds
}

func (f *fakeGuest) Output(_ context.Context, cmd string) (string, error) {
	f.scripts = append(f.scripts, cmd)
	if strings.HasPrefix(cmd, "d=") && strings.Contains(cmd, "|| exit 1") { // checkScript
		if f.setUp {
			return "", nil
		}
		return "", errors.New("exit 1")
	}
	return "", f.err
}

func workspace(t *testing.T) lease.Workspace {
	t.Helper()
	dir := t.TempDir()
	return lease.Workspace{Key: "0123456789abcdef", WinPath: dir, VMPath: "/home/fclaude/app", Share: "fc-0123456789abcdef"}
}

func TestWorkspace(t *testing.T) {
	ws, err := Workspace(`C:\dev`, "/home/fclaude", `c:\DEV\work\my app\`)
	if err != nil {
		t.Fatal(err)
	}
	if ws.VMPath != "/home/fclaude/work/my app" || ws.Share != "fc-"+ws.Key || ws.Key != lease.Key(ws.WinPath) {
		t.Errorf("%+v", ws)
	}
	if other, _ := Workspace(`C:\dev`, "/home/fclaude", `C:\dev\Work\My App`); other.Key != ws.Key {
		t.Error("key must be case-insensitive (NTFS)")
	}
	for _, bad := range []string{`C:\dev`, `C:\elsewhere\app`, `C:\dev\.claude`} {
		if _, err := Workspace(`C:\dev`, "/home/fclaude", bad); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
}

func TestSetupSequenceAndRollback(t *testing.T) {
	ctx := context.Background()
	ws := workspace(t)
	h, g := &fakeHost{}, &fakeGuest{}
	m := Manager{VMX: "x.vmx", Host: h, Guest: g}
	if err := m.Setup(ctx, ws, 0); err != nil {
		t.Fatal(err)
	}
	want := []string{"enable", "add " + ws.Share + " " + ws.WinPath}
	if strings.Join(h.calls, "|") != strings.Join(want, "|") || len(g.scripts) != 2 { // check + mount
		t.Errorf("calls %q scripts %d", h.calls, len(g.scripts))
	}

	// Already set up: one guest check, no vmrun at all.
	h, g = &fakeHost{}, &fakeGuest{setUp: true}
	m = Manager{VMX: "x.vmx", Host: h, Guest: g}
	if err := m.Setup(ctx, ws, 1); err != nil || len(h.calls) != 0 || len(g.scripts) != 1 {
		t.Errorf("fast path: %v calls %q scripts %d", err, h.calls, len(g.scripts))
	}

	// Guest mount fails, nobody else holds it: the share is rolled back.
	h, g = &fakeHost{}, &fakeGuest{err: errors.New("vmhgfs-fuse: no such device")}
	m = Manager{VMX: "x.vmx", Host: h, Guest: g}
	if err := m.Setup(ctx, ws, 0); err == nil || !strings.Contains(err.Error(), "no such device") {
		t.Errorf("err = %v", err)
	}
	if h.calls[len(h.calls)-1] != "remove "+ws.Share {
		t.Errorf("no rollback: %q", h.calls)
	}
	// ...but another live holder keeps it.
	h = &fakeHost{}
	m.Host = h
	_ = m.Setup(ctx, ws, 1)
	if strings.HasPrefix(h.calls[len(h.calls)-1], "remove") {
		t.Errorf("share removed under another holder: %q", h.calls)
	}

	// Host failure: no mount attempt (only the failed check), nothing to roll back.
	h, g = &fakeHost{addErr: errors.New("vmrun: not powered on")}, &fakeGuest{}
	m = Manager{VMX: "x.vmx", Host: h, Guest: g}
	if err := m.Setup(ctx, ws, 0); err == nil || len(g.scripts) != 1 {
		t.Errorf("err %v scripts %d", err, len(g.scripts))
	}

	// Missing host directory is caught before vmrun (which would misreport it).
	gone := ws
	gone.WinPath = filepath.Join(ws.WinPath, "missing")
	h = &fakeHost{}
	m = Manager{VMX: "x.vmx", Host: h, Guest: &fakeGuest{}}
	if err := m.Setup(ctx, gone, 0); err == nil || len(h.calls) != 0 {
		t.Errorf("missing dir: %v %q", err, h.calls)
	}
}

func TestTeardown(t *testing.T) {
	ctx := context.Background()
	ws := workspace(t)
	h, g := &fakeHost{}, &fakeGuest{}
	if err := (Manager{VMX: "x.vmx", Host: h, Guest: g}).Teardown(ctx, ws); err != nil || h.calls[0] != "remove "+ws.Share {
		t.Errorf("%v %q", err, h.calls)
	}
	// An unreachable guest still lets the share go, and both errors surface.
	h, g = &fakeHost{rmErr: errors.New("rm failed")}, &fakeGuest{err: errors.New("ssh: connection refused")}
	err := (Manager{VMX: "x.vmx", Host: h, Guest: g}).Teardown(ctx, ws)
	if len(h.calls) != 1 || err == nil || !strings.Contains(err.Error(), "connection refused") || !strings.Contains(err.Error(), "rm failed") {
		t.Errorf("%v %q", err, h.calls)
	}
}

func TestQuote(t *testing.T) {
	if q(`/home/fclaude/it's here`) != `'/home/fclaude/it'\''s here'` {
		t.Error(q(`/home/fclaude/it's here`))
	}
}

// --- The guest scripts, run under bash against a fake mount table ---

const stubs = `
tab="$FAKE_MOUNTS"
case "$(basename "$0")" in
mountpoint) [ "$1" = -q ] && shift; grep -qF " $1" "$tab" ;;
findmnt) # findmnt -no SOURCE --mountpoint P
  awk -v p="$4" '{ s=$1; $1=""; sub(/^ /,""); if ($0==p) print s }' "$tab" ;;
sudo) exec "$@" ;;
vmhgfs-fuse) # like the real one: the source is "vmhgfs-fuse" unless -o fsname= names it
  echo "vmhgfs-fuse $*" >> "$FAKE_LOG"; [ -n "$FAKE_FAIL_MOUNT" ] && exit 1
  src=vmhgfs-fuse; case "$4" in *fsname=*) src="${4##*fsname=}"; src="${src%%,*}" ;; esac
  echo "$src $2" >> "$tab" ;;
mount) [ "$1" = --bind ] && echo "bind:$2 $3" >> "$tab" ;;
umount) [ "$1" = -l ] && shift; echo "umount $1" >> "$FAKE_LOG"; grep -vF " $1" "$tab" > "$tab.n" || true; [ "$(grep -cF " $1" "$tab")" != 0 ] || exit 32; mv "$tab.n" "$tab" ;;
install) shift; while [ "$1" = -o ] || [ "$1" = -g ]; do shift 2; done; mkdir -p "$1" ;;
vmware-hgfsclient) cat "$FAKE_SHARES" ;;
esac
`

type guestSim struct {
	t       *testing.T
	bin     string
	mounts  string
	log     string
	shares  string // what the host offers (vmware-hgfsclient)
	failMnt bool
}

// simHost adds/removes shares in the guest simulator's host share list.
type simHost struct{ g *guestSim }

func (h simHost) EnableSharedFolders(context.Context, string) error { return nil }

func (h simHost) AddShare(_ context.Context, _, name, _ string) error {
	b, _ := os.ReadFile(h.g.shares)
	if !strings.Contains(string(b), name+"\n") {
		b = append(b, name+"\n"...)
	}
	return os.WriteFile(h.g.shares, b, 0o600)
}

func (h simHost) RemoveShare(_ context.Context, _, name string) error {
	b, _ := os.ReadFile(h.g.shares)
	return os.WriteFile(h.g.shares, []byte(strings.ReplaceAll(string(b), name+"\n", "")), 0o600)
}

func newGuestSim(t *testing.T) *guestSim {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("guest scripts are exercised under bash on Linux")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("no bash")
	}
	dir := t.TempDir()
	g := &guestSim{t: t, bin: filepath.Join(dir, "bin"), mounts: filepath.Join(dir, "mounts"), log: filepath.Join(dir, "log"), shares: filepath.Join(dir, "shares")}
	if err := os.MkdirAll(g.bin, 0o755); err != nil {
		t.Fatal(err)
	}
	stub := filepath.Join(g.bin, "stub")
	if err := os.WriteFile(stub, []byte("#!/bin/bash\n"+stubs), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"mountpoint", "findmnt", "sudo", "vmhgfs-fuse", "mount", "umount", "install", "vmware-hgfsclient"} {
		if err := os.Symlink(stub, filepath.Join(g.bin, n)); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{g.mounts, g.log, g.shares} {
		if err := os.WriteFile(f, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return g
}

func (g *guestSim) Output(_ context.Context, script string) (string, error) {
	cmd := exec.Command("bash", "-c", script)
	cmd.Env = append(os.Environ(), "PATH="+g.bin+":"+os.Getenv("PATH"), "FAKE_MOUNTS="+g.mounts, "FAKE_LOG="+g.log, "FAKE_SHARES="+g.shares)
	if g.failMnt {
		cmd.Env = append(cmd.Env, "FAKE_FAIL_MOUNT=1")
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), errors.New(string(out) + err.Error())
	}
	return string(out), nil
}

func (g *guestSim) table() string { b, _ := os.ReadFile(g.mounts); return string(b) }
func (g *guestSim) logs() string  { b, _ := os.ReadFile(g.log); return string(b) }
func (g *guestSim) reset()        { _ = os.WriteFile(g.log, nil, 0o600) }

func TestGuestScripts(t *testing.T) {
	ctx := context.Background()
	g := newGuestSim(t)
	ws := workspace(t)
	ws.VMPath = filepath.Join(t.TempDir(), "home", "fclaude", "my app") // space: quoting matters
	host := simHost{g}
	m := Manager{VMX: "x.vmx", Host: host, Guest: g, LocalNodeModules: true, nmStore: filepath.Join(t.TempDir(), "nm")}
	src := ".host:/" + ws.Share

	// Cold: mounted with the right source.
	if err := m.Setup(ctx, ws, 0); err != nil {
		t.Fatal(err)
	}
	if g.table() != src+" "+ws.VMPath+"\n" {
		t.Fatalf("table after cold setup:\n%s", g.table())
	}
	// Second session: nothing to do.
	g.reset()
	if err := m.Setup(ctx, ws, 1); err != nil || g.logs() != "" {
		t.Errorf("idempotent setup touched mounts: %v\n%s", err, g.logs())
	}
	// Share removed on the host behind a live mount (R3): the mount looks fine
	// but is empty. The check must notice; re-adding heals the mount in place.
	_ = host.RemoveShare(ctx, "", ws.Share)
	g.reset()
	if err := m.Setup(ctx, ws, 1); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(g.shares); string(b) != ws.Share+"\n" || g.logs() != "" {
		t.Errorf("hidden removal not healed by re-add (shares %q, mount log %q)", b, g.logs())
	}
	// A different share mounted at the path (stale): replaced.
	_ = os.WriteFile(g.mounts, []byte(".host:/fc-stale "+ws.VMPath+"\n"), 0o600)
	g.reset()
	if err := m.Setup(ctx, ws, 1); err != nil || g.table() != src+" "+ws.VMPath+"\n" || !strings.Contains(g.logs(), "umount "+ws.VMPath) {
		t.Errorf("stale mount not replaced: %v\ntable:\n%s\nlog:\n%s", err, g.table(), g.logs())
	}

	// node_modules: bound from the VM-local store once a package.json exists.
	if err := os.MkdirAll(ws.VMPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws.VMPath, "package.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	nm := filepath.Join(ws.VMPath, "node_modules")
	if err := m.Setup(ctx, ws, 1); err != nil || !strings.Contains(g.table(), "bind:"+filepath.Join(m.nmStore, ws.Key)+" "+nm) {
		t.Errorf("node_modules not bound: %v\n%s", err, g.table())
	}
	if err := m.Setup(ctx, ws, 1); err != nil || strings.Count(g.table(), "bind:") != 1 {
		t.Errorf("node_modules bound twice:\n%s", g.table())
	}

	// Teardown: node_modules first, then the share.
	g.reset()
	if err := m.Teardown(ctx, ws); err != nil {
		t.Fatal(err)
	}
	if g.table() != "" || g.logs() != "umount "+nm+"\numount "+ws.VMPath+"\n" {
		t.Errorf("teardown:\ntable:\n%s\nlog:\n%s", g.table(), g.logs())
	}
	// Teardown of something not mounted is a no-op, not an error.
	if err := m.Teardown(ctx, ws); err != nil {
		t.Errorf("second teardown: %v", err)
	}

	// A failing guest mount is reported.
	g.failMnt = true
	if err := m.Setup(ctx, ws, 0); err == nil {
		t.Error("failed vmhgfs-fuse not reported")
	}
}
