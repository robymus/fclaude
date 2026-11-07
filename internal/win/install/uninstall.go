package install

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The typed confirmations (mirroring uninstall.sh): one to uninstall at all,
// a separate one to destroy the VM disk.
const (
	UninstallPhrase  = "remove fclaude"
	DeleteDiskPhrase = "DELETE DISK"
)

// Uninstall removes fclaude from this computer (PLAN §4). The VM disk, its SSH
// keys and the settings survive unless the user types DeleteDiskPhrase, so
// running setup again brings the same VM back (setup's restore question).
func Uninstall(ctx context.Context, d Deps) error {
	ui, l := d.UI, d.Layout
	ui.Info("This removes fclaude from this computer:")
	ui.Info("  - the VM definition in %s (the VM is shut down first)", l.VMDir())
	ui.Info("  - the fclaude firewall rules (Windows asks for administrator rights once)")
	ui.Info("  - the programs in %s and their PATH entry", l.Bin())
	ui.Info("  - the VS Code setting that runs Claude in the VM, if it is set")
	ui.Info("KEPT: the VM disk %s (everything inside the VM),", l.Disk())
	ui.Info("its SSH keys and your settings: running setup again brings this VM back.")
	ui.Info("Deleting the disk too is a separate question at the end. Close VS Code first.")
	ans, err := ui.Ask(fmt.Sprintf("Type '%s' to proceed", UninstallPhrase), "")
	if err != nil {
		return err
	}
	if ans != UninstallPhrase {
		return fmt.Errorf("%w: confirmation phrase did not match; nothing was changed", ErrAborted)
	}

	if err := stopForUninstall(ctx, d); err != nil {
		return err
	}

	// VM files except the disk (and the disk's own markers and lock directory).
	entries, err := os.ReadDir(l.VMDir())
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, e := range entries {
		if keepWithDisk(e.Name()) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(l.VMDir(), e.Name())); err != nil {
			return err
		}
	}
	for _, p := range []string{l.Leases(), l.Downloads()} {
		if err := os.RemoveAll(p); err != nil {
			ui.Warn("Could not remove %s: %v", p, err)
		}
	}
	ui.Info("VM definition removed.")

	if ok, err := d.FirewallPresent(); err != nil || ok {
		ui.Info("Removing the firewall rules (Windows asks for administrator rights)...")
		if err := d.Elevate([]string{"uninstall-elevated"}); err != nil {
			ui.Warn("Firewall rules not removed (%v). To remove them by hand, in an administrator PowerShell run:", err)
			ui.Warn("  %s", strings.TrimSpace(FirewallRemoveScript()))
		} else {
			ui.Info("Firewall rules removed.")
		}
	}

	// After the elevated step, which appends to it.
	if err := os.Remove(filepath.Join(l.Home, "elevated.log")); err != nil && !errors.Is(err, os.ErrNotExist) {
		ui.Warn("Could not remove %s: %v", filepath.Join(l.Home, "elevated.log"), err)
	}

	if changed, err := d.RemovePath(l.Bin()); err != nil {
		ui.Warn("Could not remove %s from your PATH: %v", l.Bin(), err)
	} else if changed {
		ui.Info("Removed %s from your PATH.", l.Bin())
	}

	unsetVSCode(d)

	if fileExists(l.Disk()) {
		ans, err := ui.Ask(fmt.Sprintf("To ALSO delete the VM disk and everything inside the VM, type '%s' (Enter keeps it)", DeleteDiskPhrase), "")
		if err == nil && ans == DeleteDiskPhrase {
			for _, p := range []string{l.VMDir(), l.SSHDir(), l.VMInfo(), l.Config(), l.Config() + ".new"} {
				if err := os.RemoveAll(p); err != nil {
					return fmt.Errorf("deleting %s: %w", p, err)
				}
			}
			ui.Info("VM disk, keys and settings deleted.")
		} else {
			ui.Info("Kept the VM disk %s.", l.Disk())
			ui.Info("To use it again, run fclaude-setup and answer yes to the restore question.")
		}
	}

	// Last: the programs (this one is running from there, so the removal
	// happens just after it exits).
	if err := d.ScheduleRemoval(l.Bin(), l.Home); err != nil {
		ui.Warn("Could not remove %s: %v (delete it by hand)", l.Bin(), err)
	}
	ui.Info("fclaude is uninstalled.")
	return nil
}

// keepWithDisk: the disk file and anything named after it (setup's
// .unprovisioned/.importing markers, VMware's .lck directory).
func keepWithDisk(name string) bool {
	return strings.HasPrefix(strings.ToLower(name), strings.ToLower(DiskName))
}

func stopForUninstall(ctx context.Context, d Deps) error {
	ui, vmx := d.UI, d.Layout.VMX()
	if !fileExists(vmx) {
		return nil
	}
	in, err := d.Locate()
	if err != nil {
		ui.Warn("VMware Workstation not found (%v); assuming the VM is off.", err)
		return nil
	}
	vm := d.NewVM(in)
	if running, err := vm.Running(ctx, vmx); err != nil || !running {
		return nil
	}
	ui.Info("Shutting down the VM...")
	if err := vm.Stop(ctx, vmx, false); err == nil && waitStopped(ctx, vm, vmx, d.Now) == nil {
		return nil
	}
	ui.Warn("The VM did not shut down cleanly; powering it off.")
	if err := vm.Stop(ctx, vmx, true); err != nil {
		return fmt.Errorf("could not stop the VM: %w", err)
	}
	return waitStopped(ctx, vm, vmx, d.Now)
}

// unsetVSCode removes our claudeProcessWrapper from VS Code's settings (with
// consent and a backup); a wrapper pointing elsewhere is left alone.
func unsetVSCode(d Deps) {
	ui, path := d.UI, d.VSCodeSettings
	if path == "" {
		return
	}
	src, err := os.ReadFile(path)
	if err != nil || !bytes.Contains(src, []byte(wrapperSetting)) || !bytes.Contains(bytes.ToLower(src), []byte("fclaude-vscode")) {
		return
	}
	ok, err := ui.Confirm("Remove the fclaude settings from VS Code (Claude then runs on Windows again, with permission prompts)?", true)
	if err != nil || !ok {
		ui.Warn("Left VS Code's settings as they are: remove these from %s by hand", path)
		ui.Warn("(skipping permissions without the VM is unsafe):")
		for _, s := range vscodeSettings("") {
			ui.Warn("  %q", s.key)
		}
		return
	}
	out := src
	for _, s := range vscodeSettings("") {
		if out, err = DeleteJSONCMember(out, s.key); err != nil {
			break
		}
	}
	if err == nil {
		err = writeWithBackup(path, src, out, d.Now)
	}
	if err != nil {
		ui.Warn("Could not edit %s: %v", path, err)
		return
	}
	ui.Info("VS Code settings removed (restart VS Code).")
}
