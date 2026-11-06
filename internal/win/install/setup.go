package install

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/robymus/fclaude/internal/win/config"
	"github.com/robymus/fclaude/internal/win/seed"
	"github.com/robymus/fclaude/internal/win/vmw"
	"github.com/robymus/fclaude/internal/win/winpath"
)

// UI is the console the wizard talks through.
type UI interface {
	Step(n, total int, title string)
	Info(format string, a ...any)
	Warn(format string, a ...any)
	Ask(prompt, def string) (string, error)
	Confirm(prompt string, def bool) (bool, error)
	Progress(label string, done, total int64)
}

// VM is the slice of vmw.Client setup needs.
type VM interface {
	Start(ctx context.Context, vmx string) error
	Stop(ctx context.Context, vmx string, hard bool) error
	Running(ctx context.Context, vmx string) (bool, error)
}

// Deps are setup's connections to the outside world (replaced by fakes in tests).
type Deps struct {
	Layout   Layout
	UI       UI
	Locate   func() (vmw.Install, error)
	Networks func() (vmw.Networks, error)
	NewVM    func(vmw.Install) VM
	Vdisk    func(vmw.Install) vmw.Runner
	Fetch    Fetcher
	// Self is the running executable, copied into Layout.Bin.
	Self    string
	Version string
	Rand    io.Reader
	// HaveSSHClient reports whether Windows' OpenSSH client (ssh.exe) exists.
	HaveSSHClient func() bool
	// FirewallPresent reports whether the fclaude rules exist (no admin needed).
	FirewallPresent func() (bool, error)
	// Elevate runs Self with args through UAC and waits.
	Elevate func(args []string) error
	AddPath func(dir string) (bool, error)
	// Provision waits until the freshly booted VM has finished provisioning and
	// pins its host key (vmssh.FetchHostKey + WritePin).
	Provision func(ctx context.Context, info VMInfo) error
	// VSCodeSettings is the user settings.json path ("" when VS Code is absent).
	VSCodeSettings string
	Now            func() time.Time
	// DirExists and MkdirAll handle the projects folder (a Windows path;
	// replaced in tests that run on Linux). nil means the os package.
	DirExists func(string) bool
	MkdirAll  func(string) error
}

func (d Deps) dirExists(p string) bool {
	if d.DirExists != nil {
		return d.DirExists(p)
	}
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

func (d Deps) mkdirAll(p string) error {
	if d.MkdirAll != nil {
		return d.MkdirAll(p)
	}
	return os.MkdirAll(p, 0o750)
}

// ErrAborted is a setup the user chose to stop; nothing destructive happened.
var ErrAborted = errors.New("setup aborted")

const totalSteps = 11

// Binaries setup installs into Layout.Bin (one executable, three personalities).
var Binaries = []string{"fclaude.exe", "fclaude-vscode.exe", "fclaude-setup.exe"}

// Setup runs the wizard (PLAN §4). It is safe to re-run at any point: done
// work is detected and skipped, and an already provisioned install is only
// upgraded (binaries, PATH) — never re-provisioned, and its disk never touched.
func Setup(ctx context.Context, d Deps) error {
	ui, l := d.UI, d.Layout
	prev, prevErr := ReadVMInfo(l.VMInfo())
	upgrade := prevErr == nil && prev.Provisioned && fileExists(l.Disk()) && fileExists(l.VMX())

	// 1. Configuration.
	ui.Step(1, totalSteps, "Configuration")
	cfg, cfgExisted, err := setupConfig(d)
	if err != nil {
		return err
	}

	// 2. Preflight.
	ui.Step(2, totalSteps, "Checking this computer")
	in, nets, err := preflight(d, cfg)
	if err != nil {
		return err
	}

	// 3. Directories.
	ui.Step(3, totalSteps, "Folders")
	for _, dir := range l.Dirs() {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}

	info := prev
	if !upgrade {
		// 4. Disk (guarded).
		ui.Step(4, totalSteps, "VM disk")
		if err := setupDisk(ctx, d, in, cfg); err != nil {
			return err
		}
		// 5. Keys, 6. seed and VM definition.
		ui.Step(5, totalSteps, "SSH keys")
		pub, err := EnsureKey(l.Key(cfg.VMUser), cfg.VMUser+"@fclaude-win")
		if err != nil {
			return err
		}
		mpub, err := EnsureKey(l.Key(cfg.MaintUser), cfg.MaintUser+"@fclaude-win")
		if err != nil {
			return err
		}
		ui.Step(6, totalSteps, "VM definition")
		if info, err = setupVM(d, cfg, nets, prev, pub, mpub); err != nil {
			return err
		}
	} else {
		ui.Info("Existing installation found (provisioned VM at %s): upgrading the programs only.", l.VMX())
		info.HostOnlyNet, info.NATNet = nets.HostOnly.String(), nets.NAT.String()
	}

	// 7. Firewall (the one elevated step).
	ui.Step(7, totalSteps, "Firewall")
	if err := setupFirewall(d, info); err != nil {
		return err
	}

	// 8. Programs + PATH.
	ui.Step(8, totalSteps, "Programs")
	if err := installBinaries(d); err != nil {
		return err
	}
	if changed, err := d.AddPath(l.Bin()); err != nil {
		ui.Warn("Could not add %s to your PATH: %v", l.Bin(), err)
	} else if changed {
		ui.Info("Added %s to your PATH (open a new terminal to use `fclaude`).", l.Bin())
	}

	// 9. Config + vm.json.
	ui.Step(9, totalSteps, "Saving settings")
	if err := saveConfig(d, cfg, cfgExisted); err != nil {
		return err
	}
	info.SetupVersion = d.Version
	if err := WriteVMInfo(l.VMInfo(), info); err != nil {
		return err
	}

	// 10. Boot and provision.
	ui.Step(10, totalSteps, "Starting the VM")
	if !info.Provisioned {
		if err := bootAndProvision(ctx, d, in, &info); err != nil {
			return err
		}
	} else {
		ui.Info("The VM is already provisioned.")
	}

	// 11. Finish.
	ui.Step(11, totalSteps, "Finishing")
	return finish(d)
}

func setupConfig(d Deps) (config.Config, bool, error) {
	ui, path := d.UI, d.Layout.Config()
	if fileExists(path) {
		cfg, err := config.Load(path)
		if err != nil {
			return config.Config{}, true, fmt.Errorf("%w (fix it or move it away, then re-run setup)", err)
		}
		ui.Info("Using your existing settings in %s.", path)
		return cfg, true, nil
	}
	cfg := config.Defaults()
	ui.Info("Your projects must live under one folder, which is shared with the VM one project at a time.")
	def := cfg.ShareRoot
	for attempt := 1; ; attempt++ {
		root, err := ui.Ask("Projects folder", def)
		if err != nil {
			return config.Config{}, false, err
		}
		cfg.ShareRoot = winpath.Clean(strings.TrimSpace(root))
		err = cfg.Validate()
		if err == nil {
			return cfg, false, nil
		}
		if attempt == 3 {
			return config.Config{}, false, fmt.Errorf("%w: %w", ErrAborted, err)
		}
		ui.Warn("%v", err)
	}
}

func preflight(d Deps, cfg config.Config) (vmw.Install, vmw.Networks, error) {
	ui := d.UI
	in, err := d.Locate()
	if err != nil {
		ui.Warn("%v", err)
		ui.Info("Install VMware Workstation Pro (free) first:")
		ui.Info("  1. Sign in or register at https://support.broadcom.com")
		ui.Info("  2. My Downloads > Free Software Downloads > VMware Workstation Pro, Windows build")
		ui.Info("  3. Install with the defaults, then run this setup again.")
		return vmw.Install{}, vmw.Networks{}, fmt.Errorf("%w: VMware Workstation Pro is required", ErrAborted)
	}
	ui.Info("VMware Workstation %s found.", in.Version)
	nets, err := d.Networks()
	if err != nil {
		return vmw.Install{}, vmw.Networks{}, fmt.Errorf("%w (repair VMware Workstation's virtual networks: Virtual Network Editor > Restore Defaults)", err)
	}
	if !d.HaveSSHClient() {
		ui.Warn("The Windows OpenSSH client is missing; `fclaude` terminal sessions need it.")
		ui.Info("  Install it: Settings > System > Optional features > OpenSSH Client.")
	}
	if !d.dirExists(cfg.ShareRoot) {
		ok, err := ui.Confirm(fmt.Sprintf("%s does not exist. Create it?", cfg.ShareRoot), true)
		if err != nil {
			return vmw.Install{}, vmw.Networks{}, err
		}
		if !ok {
			return vmw.Install{}, vmw.Networks{}, fmt.Errorf("%w: the projects folder %s does not exist", ErrAborted, cfg.ShareRoot)
		}
		if err := d.mkdirAll(cfg.ShareRoot); err != nil {
			return vmw.Install{}, vmw.Networks{}, err
		}
	}
	return in, nets, nil
}

// setupDisk never overwrites an existing disk: one found without a finished
// installation is either a restore from backup (kept as is) or a reason to stop.
func setupDisk(ctx context.Context, d Deps, in vmw.Install, cfg config.Config) error {
	ui, disk := d.UI, d.Layout.Disk()
	marker := disk + ".importing"
	if fileExists(marker) {
		// A previous run died while converting: that partial disk is ours.
		ui.Info("Removing an incomplete disk from an interrupted setup.")
		if err := os.Remove(disk); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := os.Remove(marker); err != nil {
			return err
		}
	}
	if fileExists(disk) && fileExists(disk+unprovisioned) {
		ui.Info("Resuming: the VM disk from the previous setup run is kept.")
		return nil
	}
	if fileExists(disk) {
		ok, err := ui.Confirm(fmt.Sprintf("A VM disk already exists at %s. Is this a restore from a backup (keep it and use it)?", disk), false)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("%w: refusing to touch the existing disk %s (move it away to start fresh)", ErrAborted, disk)
		}
		ui.Info("Keeping the existing disk.")
		return nil
	}
	ui.Info("Downloading the Ubuntu 24.04 cloud image (about 600 MB, verified against SHA256SUMS)...")
	img, err := FetchImage(ctx, d.Fetch, d.Layout.Downloads(), func(n int64) { ui.Progress("download", n, 0) })
	if err != nil {
		return err
	}
	ui.Info("Converting the image to a %d GB VM disk...", cfg.DiskGiB)
	// Convert straight to the final name (a monolithic sparse VMDK names its own
	// extent inside, so it must not be renamed afterwards). The marker makes an
	// interrupted conversion recognizable as ours on the next run.
	if err := os.WriteFile(marker, nil, 0o600); err != nil {
		return err
	}
	vd := d.Vdisk(in)
	for _, a := range vmw.ImportDiskArgs(img, disk, cfg.DiskGiB) {
		if out, err := vd.Run(ctx, a...); err != nil {
			_ = os.Remove(disk)
			_ = os.Remove(marker)
			return fmt.Errorf("%w\n%s", err, out)
		}
	}
	// Ours until provisioned: a re-run after a later failure resumes with it
	// instead of asking whether it is a restore.
	if err := os.WriteFile(disk+unprovisioned, nil, 0o600); err != nil {
		return err
	}
	return os.Remove(marker)
}

// unprovisioned marks a disk this setup imported and has not provisioned yet.
const unprovisioned = ".unprovisioned"

func setupVM(d Deps, cfg config.Config, nets vmw.Networks, prev VMInfo, pub, mpub string) (VMInfo, error) {
	l := d.Layout
	info := VMInfo{
		VMX:            l.VMX(),
		VMIP:           vmw.VMIP(nets.HostOnly).String(),
		HostIP:         vmw.HostIP(nets.HostOnly).String(),
		HostOnlyPrefix: vmw.Prefix(nets.HostOnly),
		HostOnlyNet:    nets.HostOnly.String(),
		NATNet:         nets.NAT.String(),
		MACNAT:         prev.MACNAT,
		MACHostOnly:    prev.MACHostOnly,
	}
	var err error
	if !vmw.ValidStaticMAC(info.MACNAT) {
		if info.MACNAT, err = vmw.RandomMAC(d.Rand); err != nil {
			return VMInfo{}, err
		}
	}
	for !vmw.ValidStaticMAC(info.MACHostOnly) || info.MACHostOnly == info.MACNAT {
		if info.MACHostOnly, err = vmw.RandomMAC(d.Rand); err != nil {
			return VMInfo{}, err
		}
	}
	files, err := seed.Files(seed.Params{
		VMName: cfg.VMName, VMUser: cfg.VMUser, VMUID: 1000, MaintUser: cfg.MaintUser,
		PubKey: pub, MaintPubKey: mpub, AnsibleDir: "/var/lib/fclaude/ansible",
		UpdateApt: cfg.UpdateApt, MTU: seed.MTU, MACNAT: info.MACNAT, MACHostOnly: info.MACHostOnly,
		VMIP: info.VMIP, HostOnlyPrefix: info.HostOnlyPrefix,
	})
	if err != nil {
		return VMInfo{}, err
	}
	if err := seed.WriteISO(l.Seed(), files); err != nil {
		return VMInfo{}, err
	}
	if fileExists(l.VMX()) {
		d.UI.Info("Keeping the existing VM definition %s.", l.VMX())
		return info, nil
	}
	v, err := vmw.NewVMX(vmw.Spec{Name: cfg.VMName, MemMiB: cfg.MemGiB * 1024, CPUs: cfg.VCPU,
		Disk: DiskName, Seed: SeedName, ConsoleLog: ConsoleName, MACNAT: info.MACNAT, MACHost: info.MACHostOnly})
	if err != nil {
		return VMInfo{}, err
	}
	return info, v.WriteFile(l.VMX())
}

func setupFirewall(d Deps, info VMInfo) error {
	ui := d.UI
	if ok, err := d.FirewallPresent(); err == nil && ok {
		ui.Info("Firewall rules are in place.")
		return nil
	}
	ui.Info("The VM must not reach privileged services on this computer (file sharing, RPC, ...).")
	ui.Info("Adding firewall rules needs administrator rights: Windows will ask once (UAC).")
	if _, err := FirewallScript(info.HostOnlyNet, info.NATNet); err != nil {
		return err
	}
	if err := d.Elevate([]string{"install-elevated", "--subnets", info.HostOnlyNet + "," + info.NATNet}); err != nil {
		return fmt.Errorf("%w: firewall rules not added (%w); run setup again to retry", ErrAborted, err)
	}
	if ok, err := d.FirewallPresent(); err != nil || !ok {
		return fmt.Errorf("firewall rules still missing after the elevated step (%w)", err)
	}
	ui.Info("Firewall rules added.")
	return nil
}

func installBinaries(d Deps) error {
	src, err := os.ReadFile(d.Self)
	if err != nil {
		return err
	}
	for _, name := range Binaries {
		dst := filepath.Join(d.Layout.Bin(), name)
		if cur, err := os.ReadFile(dst); err == nil && bytes.Equal(cur, src) {
			continue
		}
		// A running .exe cannot be overwritten on Windows, but it can be
		// renamed: move the old one aside (deleted on the next run).
		old := dst + ".old"
		_ = os.Remove(old)
		if fileExists(dst) {
			if err := os.Rename(dst, old); err != nil {
				return fmt.Errorf("replace %s: %w (close running fclaude sessions and retry)", dst, err)
			}
		}
		if err := writeAtomic(dst, src, 0o755); err != nil {
			return err
		}
		_ = os.Remove(old)
	}
	d.UI.Info("Programs installed in %s.", d.Layout.Bin())
	return nil
}

// saveConfig never rewrites an existing config.json. If today's defaults would
// add settings, a config.json.new is written beside it for the user to merge.
func saveConfig(d Deps, cfg config.Config, existed bool) error {
	path := d.Layout.Config()
	if !existed {
		_, err := config.WriteNoClobber(path, cfg)
		return err
	}
	cur, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	want, err := cfg.Marshal()
	if err != nil {
		return err
	}
	if bytes.Equal(bytes.TrimSpace(cur), bytes.TrimSpace(want)) {
		return nil
	}
	written, err := config.WriteNoClobber(path, cfg)
	if err == nil {
		d.UI.Info("New settings are available: compare %s with your %s.", written, path)
	}
	return err
}

func bootAndProvision(ctx context.Context, d Deps, in vmw.Install, info *VMInfo) error {
	ui, l := d.UI, d.Layout
	vm := d.NewVM(in)
	if err := vm.Start(ctx, l.VMX()); err != nil {
		return err
	}
	ui.Info("The VM is starting and provisioning itself (Ubuntu updates, Docker, Node, Claude Code).")
	ui.Info("This takes about 5 minutes; progress follows.")
	if err := d.Provision(ctx, *info); err != nil {
		return fmt.Errorf("provisioning: %w (the VM console log is %s)", err, filepath.Join(l.VMDir(), ConsoleName))
	}
	// The seed must never be read again: shut down, detach it, start again.
	ui.Info("Provisioned. Detaching the setup disk...")
	if err := vm.Stop(ctx, l.VMX(), false); err != nil {
		return err
	}
	if err := waitStopped(ctx, vm, l.VMX(), d.Now); err != nil {
		return err
	}
	v, err := vmw.ReadVMX(l.VMX())
	if err != nil {
		return err
	}
	v.DetachSeed()
	v.ClearShares()
	if err := v.WriteFile(l.VMX()); err != nil {
		return err
	}
	info.Provisioned = true
	if err := WriteVMInfo(l.VMInfo(), *info); err != nil {
		return err
	}
	if err := os.Remove(l.Disk() + unprovisioned); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return vm.Start(ctx, l.VMX())
}

func waitStopped(ctx context.Context, vm VM, vmx string, now func() time.Time) error {
	deadline := now().Add(3 * time.Minute)
	for now().Before(deadline) {
		running, err := vm.Running(ctx, vmx)
		if err == nil && !running {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return errors.New("the VM did not shut down within 3 minutes")
}

func finish(d Deps) error {
	ui, l := d.UI, d.Layout
	wrapper := filepath.Join(l.Bin(), "fclaude-vscode.exe")
	if d.VSCodeSettings != "" {
		ok, err := ui.Confirm("Set up the VS Code Claude extension to run inside the VM?", true)
		if err != nil {
			return err
		}
		if ok {
			if err := editVSCodeSettings(d.VSCodeSettings, wrapper, d.Now); err != nil {
				ui.Warn("Could not edit %s: %v", d.VSCodeSettings, err)
			} else {
				ui.Info("VS Code configured (restart VS Code).")
			}
		}
	}
	ui.Info("Done. To use the VM from VS Code, set in settings.json:")
	ui.Info(`  "claudeCode.claudeProcessWrapper": %q`, wrapper)
	ui.Info("Next: log in to Claude inside the VM once — run `fclaude --maintenance`, then `claude`.")
	return nil
}

// editVSCodeSettings sets the wrapper in settings.json, after a timestamped
// backup. Comments and formatting are preserved (SetJSONCString).
func editVSCodeSettings(path, wrapper string, now func() time.Time) error {
	src, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	out, err := SetJSONCString(src, "claudeCode.claudeProcessWrapper", wrapper)
	if err != nil {
		return err
	}
	if bytes.Equal(out, src) {
		return nil
	}
	if len(src) > 0 {
		backup := fmt.Sprintf("%s.fclaude-%s.bak", path, now().Format("20060102-150405"))
		if err := os.WriteFile(backup, src, 0o600); err != nil {
			return err
		}
	}
	return writeAtomic(path, out, 0o600)
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
