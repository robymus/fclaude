package install

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/robymus/fclaude/internal/win/config"
	"github.com/robymus/fclaude/internal/win/vmw"
)

type fakeUI struct {
	answers  map[string]string // prompt substring → answer ("y"/"n" for Confirm)
	asked    []string
	log      bytes.Buffer
	steps    []string
	unscript bool // fail on unscripted prompts
}

func (u *fakeUI) Step(_, _ int, title string)   { u.steps = append(u.steps, title) }
func (u *fakeUI) Info(f string, a ...any)       { fmt.Fprintf(&u.log, "info: "+f+"\n", a...) }
func (u *fakeUI) Warn(f string, a ...any)       { fmt.Fprintf(&u.log, "warn: "+f+"\n", a...) }
func (u *fakeUI) Progress(string, int64, int64) {}
func (u *fakeUI) answer(p string) (string, bool) {
	u.asked = append(u.asked, p)
	for k, v := range u.answers {
		if strings.Contains(p, k) {
			return v, true
		}
	}
	return "", false
}

func (u *fakeUI) Ask(p, def string) (string, error) {
	if a, ok := u.answer(p); ok {
		return a, nil
	}
	return def, nil
}

func (u *fakeUI) Confirm(p string, def bool) (bool, error) {
	if a, ok := u.answer(p); ok {
		return a == "y", nil
	}
	if u.unscript {
		return false, fmt.Errorf("unexpected prompt %q", p)
	}
	return def, nil
}

type fakeVM struct {
	calls   []string
	running bool
}

func (v *fakeVM) Start(context.Context, string) error {
	v.calls = append(v.calls, "start")
	return nil
}

func (v *fakeVM) Stop(_ context.Context, _ string, hard bool) error {
	v.calls = append(v.calls, fmt.Sprintf("stop hard=%v", hard))
	v.running = false
	return nil
}

func (v *fakeVM) Running(context.Context, string) (bool, error) { return v.running, nil }

type fakeVdisk struct{ calls [][]string }

func (f *fakeVdisk) Run(_ context.Context, args ...string) (string, error) {
	f.calls = append(f.calls, args)
	if args[0] == "-r" {
		return "", os.WriteFile(args[len(args)-1], []byte("vmdk"), 0o600)
	}
	return "", nil
}

type env struct {
	d        Deps
	ui       *fakeUI
	vm       *fakeVM
	vdisk    *fakeVdisk
	net      *fakeNet
	elevated [][]string
	fw       bool
	root     string
	made     []string // projects folders created
}

func newEnv(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	self := filepath.Join(dir, "fclaude-setup.exe")
	if err := os.WriteFile(self, []byte("binary v1"), 0o600); err != nil {
		t.Fatal(err)
	}
	img := "cloud-image"
	e := &env{ui: &fakeUI{answers: map[string]string{}}, vm: &fakeVM{}, vdisk: &fakeVdisk{},
		net:  &fakeNet{files: map[string]string{"SHA256SUMS": sum(img) + " *" + ImageName + "\n", ImageName: img}},
		root: `C:\dev`}
	e.ui.answers["Projects folder"] = e.root
	settings := filepath.Join(dir, "Code", "settings.json")
	_ = os.MkdirAll(filepath.Dir(settings), 0o700)
	_ = os.WriteFile(settings, []byte("{\n    // mine\n    \"a\": 1,\n}\n"), 0o600)
	_, ho, _ := net.ParseCIDR("192.168.201.0/24")
	_, nat, _ := net.ParseCIDR("192.168.146.0/24")
	e.d = Deps{
		Layout:          Layout{Home: filepath.Join(dir, ".fclaude")},
		UI:              e.ui,
		Locate:          func() (vmw.Install, error) { return vmw.Install{Dir: `C:\VMware`, Version: "26.0.1"}, nil },
		Networks:        func() (vmw.Networks, error) { return vmw.Networks{HostOnly: ho, NAT: nat}, nil },
		NewVM:           func(vmw.Install) VM { return e.vm },
		Vdisk:           func(vmw.Install) vmw.Runner { return e.vdisk },
		Fetch:           e.net.fetch,
		Self:            self,
		Version:         "v0.1.0-test",
		Rand:            rand.Reader,
		HaveSSHClient:   func() bool { return true },
		FirewallPresent: func() (bool, error) { return e.fw, nil },
		Elevate:         func(a []string) error { e.elevated = append(e.elevated, a); e.fw = true; return nil },
		AddPath:         func(string) (bool, error) { return true, nil },
		Provision:       func(context.Context, VMInfo) error { return nil },
		VSCodeSettings:  settings,
		Now:             time.Now,
		DirExists:       func(p string) bool { return len(e.made) > 0 && e.made[0] == p },
		MkdirAll:        func(p string) error { e.made = append(e.made, p); return nil },
	}
	return e
}

func TestSetupFresh(t *testing.T) {
	e := newEnv(t)
	l := e.d.Layout
	if err := Setup(context.Background(), e.d); err != nil {
		t.Fatalf("%v\n%s", err, e.ui.log.String())
	}
	if len(e.ui.steps) != totalSteps {
		t.Errorf("steps %q", e.ui.steps)
	}
	for _, f := range []string{l.Disk(), l.Seed(), l.VMX(), l.Key("fclaude"), l.Key("maint") + ".pub", l.Config(), l.VMInfo()} {
		if !fileExists(f) {
			t.Errorf("missing %s", f)
		}
	}
	if fileExists(l.Disk()+".importing") || fileExists(l.Disk()+unprovisioned) {
		t.Error("setup markers left after a successful install")
	}
	if len(e.made) != 1 || e.made[0] != e.root {
		t.Errorf("projects folder created: %q", e.made)
	}
	info, err := ReadVMInfo(l.VMInfo())
	if err != nil || !info.Provisioned || info.VMIP != "192.168.201.10" || !vmw.ValidStaticMAC(info.MACNAT) || info.MACNAT == info.MACHostOnly || info.SetupVersion != "v0.1.0-test" {
		t.Errorf("vm.json %+v %v", info, err)
	}
	v, _ := vmw.ReadVMX(l.VMX())
	if p, _ := v.Get("sata0:1.present"); p != "FALSE" {
		t.Error("seed not detached after provisioning")
	}
	if strings.Join(e.vm.calls, ",") != "start,stop hard=false,start" {
		t.Errorf("vm calls %v", e.vm.calls)
	}
	if len(e.elevated) != 1 || strings.Join(e.elevated[0], " ") != "install-elevated --subnets 192.168.201.0/24,192.168.146.0/24" {
		t.Errorf("elevation %q", e.elevated)
	}
	for _, b := range Binaries {
		if got, _ := os.ReadFile(filepath.Join(l.Bin(), b)); string(got) != "binary v1" {
			t.Errorf("%s not installed", b)
		}
	}
	cfg, err := config.Load(l.Config())
	if err != nil || cfg.ShareRoot != e.root {
		t.Errorf("config %+v %v", cfg, err)
	}
	s, _ := os.ReadFile(e.d.VSCodeSettings)
	if !strings.Contains(string(s), "// mine") || !strings.Contains(string(s), "fclaude-vscode.exe") {
		t.Errorf("settings.json:\n%s", s)
	}
	if m, _ := filepath.Glob(e.d.VSCodeSettings + ".fclaude-*.bak"); len(m) != 1 {
		t.Error("no settings.json backup")
	}
}

func TestSetupRerunUpgradesOnly(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if err := Setup(ctx, e.d); err != nil {
		t.Fatal(err)
	}
	l := e.d.Layout
	disk, _ := os.ReadFile(l.Disk())
	vmx, _ := os.ReadFile(l.VMX())
	e.vm.calls, e.vdisk.calls, e.net.gets, e.ui.asked = nil, nil, nil, nil
	_ = os.WriteFile(e.d.Self, []byte("binary v2"), 0o600)
	e.d.Version = "v0.2.0"
	if err := Setup(ctx, e.d); err != nil {
		t.Fatal(err)
	}
	if len(e.vm.calls)+len(e.vdisk.calls)+len(e.net.gets) != 0 {
		t.Errorf("re-run touched the VM/disk/network: %v %v %v", e.vm.calls, e.vdisk.calls, e.net.gets)
	}
	for _, p := range e.ui.asked {
		if strings.Contains(p, "Projects folder") {
			t.Error("re-run asked for the projects folder again")
		}
	}
	if d2, _ := os.ReadFile(l.Disk()); !bytes.Equal(d2, disk) {
		t.Error("disk changed")
	}
	if v2, _ := os.ReadFile(l.VMX()); !bytes.Equal(v2, vmx) {
		t.Error("vmx changed")
	}
	if b, _ := os.ReadFile(filepath.Join(l.Bin(), "fclaude.exe")); string(b) != "binary v2" {
		t.Error("binaries not upgraded")
	}
	if fileExists(l.Config() + ".new") {
		t.Error("unchanged config produced a .new")
	}
	if info, _ := ReadVMInfo(l.VMInfo()); info.SetupVersion != "v0.2.0" || !info.Provisioned {
		t.Errorf("vm.json after upgrade %+v", info)
	}
}

func TestSetupExistingDiskGuard(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	l := e.d.Layout
	_ = os.MkdirAll(l.VMDir(), 0o700)
	_ = os.WriteFile(l.Disk(), []byte("precious"), 0o600)
	e.ui.answers["restore"] = "n"
	if err := Setup(ctx, e.d); !errors.Is(err, ErrAborted) {
		t.Fatalf("err = %v", err)
	}
	if b, _ := os.ReadFile(l.Disk()); string(b) != "precious" || len(e.net.gets) != 0 || len(e.vm.calls) != 0 {
		t.Error("guard touched the disk, downloaded or started the VM")
	}
	e.ui.answers["restore"] = "y"
	if err := Setup(ctx, e.d); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(l.Disk()); string(b) != "precious" || len(e.net.gets) != 0 {
		t.Error("restore replaced the disk or downloaded")
	}
}

func TestSetupInterruptedImport(t *testing.T) {
	e := newEnv(t)
	l := e.d.Layout
	_ = os.MkdirAll(l.VMDir(), 0o700)
	_ = os.WriteFile(l.Disk(), []byte("half"), 0o600)
	_ = os.WriteFile(l.Disk()+".importing", nil, 0o600)
	e.ui.unscript = true // must not ask about restoring our own partial disk
	e.ui.answers["VS Code"] = "y"
	e.ui.answers["does not exist"] = "y"
	if err := Setup(context.Background(), e.d); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(l.Disk()); string(b) != "vmdk" {
		t.Error("partial disk not re-imported")
	}
}

func TestSetupStops(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	e.d.Locate = func() (vmw.Install, error) { return vmw.Install{}, vmw.ErrNotInstalled }
	if err := Setup(ctx, e.d); !errors.Is(err, ErrAborted) || !strings.Contains(e.ui.log.String(), "broadcom") {
		t.Errorf("missing VMware: %v\n%s", err, e.ui.log.String())
	}

	e = newEnv(t)
	e.d.Elevate = func([]string) error { return ErrElevationDeclined }
	if err := Setup(ctx, e.d); !errors.Is(err, ErrAborted) || len(e.vm.calls) != 0 {
		t.Errorf("declined UAC: %v, vm %v", err, e.vm.calls)
	}

	e = newEnv(t)
	e.d.Provision = func(context.Context, VMInfo) error { return errors.New("timed out") }
	err := Setup(ctx, e.d)
	if err == nil || !strings.Contains(err.Error(), "console log") {
		t.Errorf("provision failure: %v", err)
	}
	if info, _ := ReadVMInfo(e.d.Layout.VMInfo()); info.Provisioned {
		t.Error("marked provisioned after a failure")
	}
	// The retry picks up where it stopped: no new download or import.
	e.d.Provision = func(context.Context, VMInfo) error { return nil }
	e.net.gets, e.vdisk.calls = nil, nil
	if err := Setup(ctx, e.d); err != nil || len(e.vdisk.calls) != 0 {
		t.Errorf("retry: %v, vdisk %v", err, e.vdisk.calls)
	}
}

func TestSaveConfigWritesNewOnlyWhenDefaultsChange(t *testing.T) {
	e := newEnv(t)
	l := e.d.Layout
	_ = os.MkdirAll(l.Home, 0o700)
	old := `{"share_root": ` + fmt.Sprintf("%q", e.root) + "}\n" // older file: most keys missing
	_ = os.WriteFile(l.Config(), []byte(old), 0o600)
	if err := Setup(context.Background(), e.d); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(l.Config()); string(b) != old {
		t.Error("existing config.json was rewritten")
	}
	if !fileExists(l.Config() + ".new") {
		t.Error("no config.json.new with the new defaults")
	}
}

func TestSetupProjectsFolder(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	e.ui.answers["Projects folder"] = "relative"
	if err := Setup(ctx, e.d); !errors.Is(err, ErrAborted) || strings.Count(e.ui.log.String(), "warn:") != 2 {
		t.Errorf("invalid answers: %v\n%s", err, e.ui.log.String())
	}
	e = newEnv(t)
	e.ui.answers["does not exist"] = "n"
	if err := Setup(ctx, e.d); !errors.Is(err, ErrAborted) || len(e.made) != 0 {
		t.Errorf("declined folder creation: %v %q", err, e.made)
	}
}

func TestEditVSCodeSettingsNew(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.json")
	if err := editVSCodeSettings(p, `C:\x\fclaude-vscode.exe`, time.Now); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if !strings.Contains(string(b), `"claudeCode.claudeProcessWrapper": "C:\\x\\fclaude-vscode.exe"`) {
		t.Errorf("%s", b)
	}
	for _, want := range []string{`"claudeCode.allowDangerouslySkipPermissions": true`, `"claudeCode.initialPermissionMode": "bypassPermissions"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("settings lack %s:\n%s", want, b)
		}
	}
	if m, _ := filepath.Glob(p + ".*.bak"); len(m) != 0 {
		t.Error("backup of a file that did not exist")
	}
	if err := editVSCodeSettings(p, `C:\x\fclaude-vscode.exe`, time.Now); err != nil {
		t.Fatal(err)
	}
	if m, _ := filepath.Glob(p + ".*.bak"); len(m) != 0 {
		t.Error("no-op edit made a backup")
	}
}
