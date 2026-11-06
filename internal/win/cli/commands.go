package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/robymus/fclaude/internal/win/install"
	"github.com/robymus/fclaude/internal/win/lease"
	"github.com/robymus/fclaude/internal/win/vmw"
	"github.com/robymus/fclaude/internal/win/winpath"
)

// Session runs claude in dir (mounted), or in the VM home with noMount.
func (a *App) Session(ctx context.Context, dir string, noMount bool, claudeArgs []string) (int, error) {
	var ws lease.Workspace
	if !noMount {
		var err error
		if ws, err = a.workspace(dir); err != nil {
			return 1, err
		}
	}
	g, err := a.EnsureRunning(ctx)
	if err != nil {
		return 1, err
	}
	a.reap(ctx, g)
	a.stalenessWarn(ctx, g)
	if noMount {
		_ = g.Close()
		return a.interactive(a.sshArgs(a.Cfg.VMUser, true, loginEnv+" cd "+q(a.Cfg.VMMountBase)+" && exec claude --dangerously-skip-permissions"+quoteAll(claudeArgs)), func() {})
	}
	release, err := a.hold(ctx, g, ws)
	_ = g.Close()
	if err != nil {
		return 1, err
	}
	defer release()
	return a.interactive(a.sshArgs(a.Cfg.VMUser, true, claudeCommand(ws.VMPath, ws.WinPath, claudeArgs)), release)
}

func quoteAll(args []string) string {
	var b strings.Builder
	for _, s := range args {
		b.WriteString(" " + q(s))
	}
	return b.String()
}

// Mount shares dirs and holds them until Ctrl+C or console close.
func (a *App) Mount(ctx context.Context, dirs []string) error {
	if len(dirs) == 0 {
		return errors.New("--mount needs at least one directory")
	}
	var wss []lease.Workspace
	seen := map[string]bool{}
	for _, d := range dirs {
		ws, err := a.workspace(d)
		if err != nil {
			return err
		}
		if !seen[ws.Key] {
			seen[ws.Key] = true
			wss = append(wss, ws)
		}
	}
	g, err := a.EnsureRunning(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = g.Close() }()
	a.reap(ctx, g)

	var held []func()
	releaseAll := func() {
		for i := len(held) - 1; i >= 0; i-- {
			held[i]()
		}
	}
	defer releaseAll()
	for _, ws := range wss {
		release, err := a.hold(ctx, g, ws)
		if err != nil {
			return err
		}
		held = append(held, release)
		a.info("  %s -> %s", ws.WinPath, ws.VMPath)
	}
	a.info("%d workspace(s) mounted. Press Ctrl+C to unmount and exit.", len(held))
	if a.Signals == nil {
		return nil
	}
	sig, stop := a.Signals()
	defer stop()
	<-sig
	a.info("unmounting...")
	return nil
}

// statusScript gathers the guest-side facts in one round trip.
const statusScript = loginEnv + `
free -m | awk '/^Mem:/{print "MEM=" $3 " MiB used of " $2 " MiB"}'
df -h / | awk 'NR==2{print "DISK=" $3 " used of " $2 " (" $5 ")"}'
echo "CLAUDE=$(claude --version 2>/dev/null)"
echo "APT=$(cat /var/lib/fclaude/last-apt-upgrade 2>/dev/null)"
echo "UP=$(uptime -p 2>/dev/null)"
`

// Status prints the VM state, resources and workspaces. It never starts the
// VM; when it runs, workspaces of ended sessions are released first.
func (a *App) Status(ctx context.Context) error {
	up, err := a.running(ctx)
	state := "stopped"
	switch {
	case err != nil:
		state = "unknown (" + err.Error() + ")"
	case up:
		state = "running"
	}
	w := a.Out
	p := func(k, v string) { _, _ = fmt.Fprintf(w, "  %-12s %s\n", k+":", v) }
	_, _ = fmt.Fprintf(w, "fclaude VM %q (%s)\n", a.Cfg.VMName, a.Info.VMX)
	p("State", state)
	p("IP", a.Info.VMIP)
	if v, err := vmw.ReadVMX(a.Info.VMX); err == nil {
		cpus, _ := v.Get("numvcpus")
		mem, _ := v.Get("memsize")
		p("vCPUs", cpus)
		if n, err := strconv.Atoi(mem); err == nil {
			p("Memory", fmt.Sprintf("%d GiB configured", n/1024))
		}
	}
	if st, err := os.Stat(a.Layout.Disk()); err == nil {
		p("Disk file", fmt.Sprintf("%.1f GiB on Windows (%s)", float64(st.Size())/(1<<30), a.Layout.Disk()))
	}
	if up {
		if g, err := a.Dial(ctx, a.Cfg.VMUser); err != nil {
			p("Guest", "not reachable over ssh: "+err.Error())
		} else {
			if out, err := g.Output(ctx, statusScript); err == nil {
				facts := map[string]string{}
				sc := bufio.NewScanner(strings.NewReader(out))
				for sc.Scan() {
					if k, v, ok := strings.Cut(sc.Text(), "="); ok && v != "" {
						facts[k] = v
					}
				}
				for _, f := range []struct{ k, label string }{{"UP", "Uptime"}, {"MEM", "Guest mem"}, {"DISK", "Guest /"}, {"CLAUDE", "Claude"}, {"APT", "Last apt"}} {
					if v := facts[f.k]; v != "" {
						p(f.label, v)
					}
				}
			}
			a.reap(ctx, g)
			_ = g.Close()
		}
	}
	list, err := a.Store.List()
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintln(w, "\nActive workspaces:")
	if len(list) == 0 {
		_, _ = fmt.Fprintln(w, "  (none)")
	}
	for _, s := range list {
		name := s.Workspace.WinPath
		if name == "" {
			name = "(unknown workspace, key " + s.Workspace.Key + ")"
		}
		_, _ = fmt.Fprintf(w, "  %-40s -> %-30s sessions=%d\n", name, s.Workspace.VMPath, len(s.Live))
	}
	return nil
}

// Cleanup force-releases one workspace: unmount in the VM, remove the share,
// forget every lease on it. target is a directory or a path --status printed.
func (a *App) Cleanup(ctx context.Context, target string) error {
	ws, err := a.findWorkspace(target)
	if err != nil {
		return err
	}
	up, err := a.running(ctx)
	if err != nil {
		return err
	}
	if up {
		g, err := a.Dial(ctx, a.Cfg.VMUser)
		if err != nil {
			g = deadGuest{err}
		}
		terr := a.manager(g).Teardown(ctx, ws)
		_ = g.Close()
		if terr != nil {
			return terr
		}
	}
	if err := a.Store.Forget(ws.Key); err != nil {
		return err
	}
	a.info("released %s", ws.WinPath)
	return nil
}

func (a *App) findWorkspace(target string) (lease.Workspace, error) {
	list, err := a.Store.List()
	if err != nil {
		return lease.Workspace{}, err
	}
	abs, _ := filepath.Abs(target)
	for _, s := range list {
		w := s.Workspace
		if w.WinPath != "" && (strings.EqualFold(w.WinPath, winpath.Clean(abs)) || strings.EqualFold(w.WinPath, winpath.Clean(target))) ||
			w.VMPath != "" && w.VMPath == target || w.Key == target {
			return w, nil
		}
	}
	if st, err := os.Stat(abs); err == nil && st.IsDir() {
		return a.workspace(abs) // not recorded: still clean up whatever is mounted
	}
	return lease.Workspace{}, fmt.Errorf("no workspace %q (see fclaude --status)", target)
}

// Update updates Claude Code and the system packages in the VM now.
func (a *App) Update(ctx context.Context) (int, error) {
	g, err := a.EnsureRunning(ctx)
	if err != nil {
		return 1, err
	}
	_ = g.Close()
	a.info("updating Claude Code (native auto-updater, on demand)...")
	if code, err := a.interactive(a.sshArgs(a.Cfg.VMUser, true, loginEnv+" claude update"), func() {}); err != nil || code != 0 {
		a.warn("claude update returned %d %v (already latest is reported as success)", code, errOrEmpty(err))
	}
	a.info("updating system packages (apt; network gate bypassed)...")
	code, err := a.interactive(a.sshArgs(a.Cfg.VMUser, true, "sudo env FCLAUDE_UPDATE_FORCE=1 /usr/local/sbin/fclaude-apt-upgrade.sh"), func() {})
	if err != nil || code != 0 {
		a.warn("apt upgrade returned %d %v (see /var/log/fclaude-apt-upgrade.log in the VM)", code, errOrEmpty(err))
	}
	return code, err
}

func errOrEmpty(err error) any {
	if err == nil {
		return ""
	}
	return err
}

// Maintenance opens a shell in the VM as the maintenance user.
func (a *App) Maintenance(ctx context.Context) (int, error) {
	g, err := a.EnsureRunning(ctx)
	if err != nil {
		return 1, err
	}
	_ = g.Close()
	return a.interactive(a.sshArgs(a.Cfg.MaintUser, true, ""), func() {})
}

// Shutdown powers the VM off after confirmation.
func (a *App) Shutdown(ctx context.Context) error {
	up, err := a.running(ctx)
	if err != nil {
		return err
	}
	if !up {
		a.info("the VM is not running.")
		return nil
	}
	if !a.confirm("Shut down the fclaude VM? Running sessions end. [y/N] ") {
		a.info("cancelled.")
		return nil
	}
	if err := a.VM.Stop(ctx, a.Info.VMX, false); err != nil {
		return err
	}
	if err := install.WaitStopped(ctx, a.VM, a.Info.VMX); err != nil {
		return err
	}
	a.info("the VM is shut down.")
	return nil
}

func (a *App) confirm(prompt string) bool {
	_, _ = fmt.Fprint(a.Err, prompt)
	line, _ := bufio.NewReader(a.In).ReadString('\n')
	s := strings.ToLower(strings.TrimSpace(line))
	return s == "y" || s == "yes"
}

// growScript grows the root partition and filesystem to the disk's new size.
// growpart exits 1 for "nothing to do", which is fine.
const growScript = `set -e
src=$(findmnt -nro SOURCE /)
disk=/dev/$(lsblk -no PKNAME "$src")
part=$(cat /sys/class/block/$(basename "$src")/partition)
sudo growpart "$disk" "$part" || [ $? -eq 1 ]
sudo resize2fs "$src"
df -h /
`

// ParseSizeGB accepts 64, 64G, 64GB (case-insensitive).
func ParseSizeGB(s string) (int, error) {
	t := strings.TrimSuffix(strings.TrimSuffix(strings.ToUpper(strings.TrimSpace(s)), "B"), "G")
	n, err := strconv.Atoi(t)
	if err != nil || n <= 0 || n > 4096 {
		return 0, fmt.Errorf("invalid size %q (use GB, e.g. 64 or 64G)", s)
	}
	return n, nil
}

// GrowDisk grows the VM disk to sizeGB: stop, vmware-vdiskmanager -x, start,
// grow the partition and filesystem in the VM.
func (a *App) GrowDisk(ctx context.Context, size string) error {
	gb, err := ParseSizeGB(size)
	if err != nil {
		return err
	}
	up, err := a.running(ctx)
	if err != nil {
		return err
	}
	if up {
		if !a.confirm(fmt.Sprintf("Growing the disk to %d GB restarts the VM; running sessions end. Continue? [y/N] ", gb)) {
			a.info("cancelled.")
			return nil
		}
		a.info("shutting down the VM...")
		if err := a.VM.Stop(ctx, a.Info.VMX, false); err != nil {
			return err
		}
		if err := install.WaitStopped(ctx, a.VM, a.Info.VMX); err != nil {
			return err
		}
	}
	a.info("growing %s to %d GB...", a.Layout.Disk(), gb)
	if out, err := a.Vdisk.Run(ctx, vmw.GrowDiskArgs(a.Layout.Disk(), gb)...); err != nil {
		return fmt.Errorf("%w\n%s", err, out)
	}
	g, err := a.EnsureRunning(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = g.Close() }()
	a.info("growing the partition and filesystem in the VM...")
	out, err := g.Output(ctx, growScript)
	_, _ = fmt.Fprint(a.Out, out)
	if err != nil {
		return fmt.Errorf("growing the filesystem: %w", err)
	}
	a.info("done.")
	return nil
}
