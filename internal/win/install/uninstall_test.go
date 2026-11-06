package install

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// installed returns an env after a successful fresh setup with the VS Code
// setting written, plus uninstall's own fakes.
func installed(t *testing.T) (*env, *[]string) {
	t.Helper()
	e := newEnv(t)
	if err := Setup(context.Background(), e.d); err != nil {
		t.Fatalf("setup: %v\n%s", err, e.ui.log.String())
	}
	var actions []string
	e.d.RemovePath = func(dir string) (bool, error) { actions = append(actions, "path-"+dir); return true, nil }
	e.d.ScheduleRemoval = func(bin, home string) error { actions = append(actions, "remove-"+bin+"|"+home); return nil }
	e.elevated = nil
	e.vm.calls = nil
	return e, &actions
}

func TestUninstallWrongPhraseChangesNothing(t *testing.T) {
	e, actions := installed(t)
	l := e.d.Layout
	e.ui.answers["Type 'remove fclaude'"] = "remove"
	err := Uninstall(context.Background(), e.d)
	if !errors.Is(err, ErrAborted) {
		t.Fatalf("err = %v, want ErrAborted", err)
	}
	for _, p := range []string{l.VMX(), l.Disk(), l.Seed(), l.Key("fclaude")} {
		if !fileExists(p) {
			t.Errorf("%s removed by an aborted uninstall", p)
		}
	}
	if len(*actions) != 0 || len(e.elevated) != 0 || len(e.vm.calls) != 0 {
		t.Errorf("aborted uninstall acted: %v %v %v", *actions, e.elevated, e.vm.calls)
	}
}

func TestUninstallKeepsDiskThenReinstallRestores(t *testing.T) {
	e, actions := installed(t)
	l := e.d.Layout
	disk, _ := os.ReadFile(l.Disk())
	e.vm.running = true
	e.ui.answers["Type 'remove fclaude'"] = UninstallPhrase
	e.ui.answers["Remove the fclaude setting from VS Code"] = "y"
	e.ui.answers["type 'DELETE DISK'"] = "delete disk" // not the exact phrase: keep
	if err := Uninstall(context.Background(), e.d); err != nil {
		t.Fatalf("%v\n%s", err, e.ui.log.String())
	}
	if strings.Join(e.vm.calls, ",") != "stop hard=false" {
		t.Errorf("vm calls %v", e.vm.calls)
	}
	if b, err := os.ReadFile(l.Disk()); err != nil || string(b) != string(disk) {
		t.Fatalf("disk not kept intact: %q %v", b, err)
	}
	for _, p := range []string{l.VMX(), l.Seed(), l.Leases(), l.Downloads()} {
		if fileExists(p) {
			t.Errorf("%s not removed", p)
		}
	}
	for _, p := range []string{l.Key("fclaude"), l.Key("maint"), l.VMInfo(), l.Config()} {
		if !fileExists(p) {
			t.Errorf("%s must be kept with the disk", p)
		}
	}
	if len(e.elevated) != 1 || e.elevated[0][0] != "uninstall-elevated" {
		t.Errorf("elevated %v", e.elevated)
	}
	want := []string{"path-" + l.Bin(), "remove-" + l.Bin() + "|" + l.Home}
	if strings.Join(*actions, ",") != strings.Join(want, ",") {
		t.Errorf("actions %v, want %v", *actions, want)
	}
	if b, _ := os.ReadFile(e.d.VSCodeSettings); strings.Contains(string(b), wrapperSetting) || !strings.Contains(string(b), "// mine") {
		t.Errorf("VS Code settings after uninstall:\n%s", b)
	}

	// Reinstall onto the kept disk: the restore question, no download, no
	// conversion, same disk; the VM boots and is found provisioned.
	e.vdisk.calls, e.net.gets, e.vm.calls = nil, nil, nil
	e.ui.answers["Is this a restore"] = "y"
	if err := Setup(context.Background(), e.d); err != nil {
		t.Fatalf("reinstall: %v\n%s", err, e.ui.log.String())
	}
	if len(e.vdisk.calls)+len(e.net.gets) != 0 {
		t.Errorf("reinstall touched the disk/network: %v %v", e.vdisk.calls, e.net.gets)
	}
	if b, _ := os.ReadFile(l.Disk()); string(b) != string(disk) {
		t.Error("reinstall changed the disk")
	}
	if strings.Contains(e.ui.log.String(), "the VM only accepts the keys") {
		t.Errorf("kept keys reported missing:\n%s", e.ui.log.String())
	}
	if info, err := ReadVMInfo(l.VMInfo()); err != nil || !info.Provisioned {
		t.Errorf("vm.json after reinstall: %+v %v", info, err)
	}
}

func TestUninstallDeleteDisk(t *testing.T) {
	e, _ := installed(t)
	l := e.d.Layout
	extra := filepath.Join(l.Home, "captures")
	_ = os.MkdirAll(extra, 0o700)
	e.ui.answers["Type 'remove fclaude'"] = UninstallPhrase
	e.ui.answers["type 'DELETE DISK'"] = DeleteDiskPhrase
	if err := Uninstall(context.Background(), e.d); err != nil {
		t.Fatalf("%v\n%s", err, e.ui.log.String())
	}
	for _, p := range []string{l.VMDir(), l.SSHDir(), l.VMInfo(), l.Config()} {
		if fileExists(p) {
			t.Errorf("%s survived DELETE DISK", p)
		}
	}
	if !fileExists(extra) {
		t.Error("uninstall removed a folder it does not own")
	}
}

func TestUninstallFirewallDeclinedContinues(t *testing.T) {
	e, actions := installed(t)
	e.d.Elevate = func([]string) error { return ErrElevationDeclined }
	e.ui.answers["Type 'remove fclaude'"] = UninstallPhrase
	if err := Uninstall(context.Background(), e.d); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(e.ui.log.String(), "Get-NetFirewallRule -Group 'fclaude'") || len(*actions) != 2 {
		t.Errorf("declined UAC: %v\n%s", *actions, e.ui.log.String())
	}
}

func TestKeepWithDisk(t *testing.T) {
	for name, want := range map[string]bool{
		DiskName: true, DiskName + ".unprovisioned": true, "FCLAUDE-DISK.VMDK.lck": true,
		"fclaude.vmx": false, SeedName: false, "vmware.log": false, "fclaude.nvram": false,
	} {
		if keepWithDisk(name) != want {
			t.Errorf("keepWithDisk(%q) != %v", name, want)
		}
	}
}
