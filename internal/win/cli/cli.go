// Package cli is fclaude.exe: terminal sessions in the VM and the management
// commands (PLAN §3.5, §3.6, §4; port of host/bin/fclaude and lib/common.sh).
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/robymus/fclaude/internal/win/config"
	"github.com/robymus/fclaude/internal/win/install"
	"github.com/robymus/fclaude/internal/win/lease"
	"github.com/robymus/fclaude/internal/win/share"
	"github.com/robymus/fclaude/internal/win/vmssh"
	"github.com/robymus/fclaude/internal/win/vmw"
)

// VM is the hypervisor side: power and shared folders (vmw.Client).
type VM interface {
	install.VM
	share.Host
}

// Guest is a command channel into the VM (vmssh.Client).
type Guest interface {
	Output(ctx context.Context, cmd string) (string, error)
	Run(ctx context.Context, cmd string, stdin io.Reader, stdout, stderr io.Writer) error
	Close() error
}

// App is one fclaude invocation's view of the installation.
type App struct {
	Layout install.Layout
	Cfg    config.Config
	Info   install.VMInfo
	VM     VM
	Vdisk  vmw.Runner
	Store  *lease.Store
	// Dial connects to the VM as user with the pinned host key.
	Dial func(ctx context.Context, user string) (Guest, error)
	// SSH runs the interactive ssh.exe with args (tied to this process's
	// lifetime) and returns its exit code.
	SSH  func(args []string) (int, error)
	Self lease.Holder

	In       io.Reader
	Out, Err io.Writer
	Now      func() time.Time
	// Signals returns a channel of Ctrl+C / console-close events and a stop
	// function. nil: no signal handling (tests).
	Signals func() (<-chan os.Signal, func())
	// BootTimeout bounds the wait for the VM's ssh after a start.
	BootTimeout time.Duration
	// Poll is the wait between ssh attempts while the VM boots.
	Poll time.Duration

	tag string // message prefix; "fclaude" when empty
}

// vmBootLock serializes "is it running? clear shares, sweep, start" across
// concurrent fclaude processes. It is a lease-store lock, not a workspace.
const vmBootLock = "vm-boot"

func (a *App) prefix() string {
	if a.tag == "" {
		return "[fclaude] "
	}
	return "[" + a.tag + "] "
}

func (a *App) info(format string, args ...any) {
	_, _ = fmt.Fprintf(a.Err, a.prefix()+format+"\n", args...)
}

func (a *App) warn(format string, args ...any) {
	_, _ = fmt.Fprintf(a.Err, a.prefix()+"WARN: "+format+"\n", args...)
}

// Open loads the installation under home (config.json, vm.json, VMware).
func Open(home string) (*App, error) {
	l := install.Layout{Home: home}
	cfg, err := config.Load(l.Config())
	if errors.Is(err, os.ErrNotExist) {
		return nil, errors.New("fclaude is not set up on this computer: run fclaude-setup.exe")
	}
	if err != nil {
		return nil, err
	}
	info, err := install.ReadVMInfo(l.VMInfo())
	if errors.Is(err, os.ErrNotExist) {
		return nil, errors.New("the fclaude VM is not set up: run fclaude-setup.exe")
	}
	if err != nil {
		return nil, err
	}
	if !info.Provisioned {
		return nil, errors.New("the fclaude VM setup did not finish: run fclaude-setup.exe again")
	}
	in, err := vmw.Locate()
	if err != nil {
		return nil, err
	}
	self, err := lease.Self()
	if err != nil {
		return nil, err
	}
	a := &App{
		Layout: l, Cfg: cfg, Info: info,
		VM:    vmw.Client{R: vmw.ExecRunner{Path: in.Vmrun()}},
		Vdisk: vmw.ExecRunner{Path: in.Vdiskmanager()},
		Store: lease.NewStore(l.Leases()),
		Self:  self,
		In:    os.Stdin, Out: os.Stdout, Err: os.Stderr,
		Now:         time.Now,
		Signals:     notifySignals,
		BootTimeout: 120 * time.Second,
		Poll:        2 * time.Second,
	}
	a.Dial = func(ctx context.Context, user string) (Guest, error) {
		return vmssh.Dial(ctx, vmssh.Config{Addr: info.SSHAddr(), User: user, KeyFile: l.Key(user),
			KnownHosts: l.KnownHosts(), Timeout: 5 * time.Second})
	}
	a.SSH = runSSHExe
	return a, nil
}

// running reports the VM's power state.
func (a *App) running(ctx context.Context) (bool, error) {
	return a.VM.Running(ctx, a.Info.VMX)
}

// EnsureRunning starts the VM if it is off and returns a guest connection
// (fclaude user) once ssh answers. A cold start first forgets every lease and
// shared folder left by earlier sessions: the mounts they describe lived in
// the VM and are gone (Linux reset_refcounts_cold_boot).
func (a *App) EnsureRunning(ctx context.Context) (Guest, error) {
	// Fast path: the VM answers ssh, so it runs (vmrun list costs ~1 s; the
	// VS Code wrapper pays this on every spawn).
	g, err := a.Dial(ctx, a.Cfg.VMUser)
	if err == nil {
		return g, nil
	}
	if errors.Is(err, vmssh.ErrHostKeyMismatch) {
		return nil, hostKeyError(err, a.Info.VMIP)
	}
	err = a.Store.WithLock(vmBootLock, func() error {
		up, err := a.running(ctx)
		if err != nil || up {
			return err
		}
		if v, err := vmw.ReadVMX(a.Info.VMX); err == nil {
			v.ClearShares()
			if err := v.WriteFile(a.Info.VMX); err != nil {
				return err
			}
		}
		if n, err := a.Store.Sweep(); err != nil {
			a.warn("clearing old workspace leases: %v", err)
		} else if n > 0 {
			a.info("VM started from cold - cleared %d workspace lease(s) left by earlier sessions", n)
		}
		a.info("starting the VM...")
		return a.VM.Start(ctx, a.Info.VMX)
	})
	if err != nil {
		return nil, err
	}
	return a.waitGuest(ctx)
}

func (a *App) waitGuest(ctx context.Context) (Guest, error) {
	deadline := a.Now().Add(a.BootTimeout)
	said := false
	for {
		g, err := a.Dial(ctx, a.Cfg.VMUser)
		if err == nil {
			return g, nil
		}
		if errors.Is(err, vmssh.ErrHostKeyMismatch) {
			return nil, hostKeyError(err, a.Info.VMIP)
		}
		if !a.Now().Before(deadline) {
			return nil, fmt.Errorf("the VM did not answer ssh at %s within %v: %w", a.Info.VMIP, a.BootTimeout, err)
		}
		if !said {
			a.info("waiting for the VM...")
			said = true
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(a.Poll):
		}
	}
}

func hostKeyError(err error, ip string) error {
	return fmt.Errorf("%w: the VM at %s is not the one setup pinned (re-run fclaude-setup.exe if you restored or replaced it)", err, ip)
}

func (a *App) manager(g Guest) share.Manager {
	return share.Manager{VMX: a.Info.VMX, Host: a.VM, Guest: g, LocalNodeModules: a.Cfg.LocalNodeModules}
}

// reap releases workspaces whose sessions are all gone (a closed terminal or
// editor ran no cleanup). Best effort.
func (a *App) reap(ctx context.Context, g Guest) {
	released, err := a.Store.Reap(func(ws lease.Workspace) error { return a.manager(g).Teardown(ctx, ws) })
	for _, ws := range released {
		a.info("released %s (its sessions have ended)", ws.WinPath)
	}
	if err != nil {
		a.warn("releasing ended sessions: %v", err)
	}
}

// workspace validates dir (relative to the current directory) as a workspace.
func (a *App) workspace(dir string) (lease.Workspace, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return lease.Workspace{}, err
	}
	if st, err := os.Stat(abs); err != nil || !st.IsDir() {
		return lease.Workspace{}, fmt.Errorf("directory does not exist: %s", dir)
	}
	ws, err := share.Workspace(a.Cfg.ShareRoot, a.Cfg.VMMountBase, abs)
	if err != nil {
		return lease.Workspace{}, fmt.Errorf("%w (projects must live under %s, see %s)", err, a.Cfg.ShareRoot, a.Layout.Config())
	}
	return ws, nil
}

// hold acquires ws for this process; the returned release is idempotent and
// safe to call from a signal handler goroutine.
func (a *App) hold(ctx context.Context, g Guest, ws lease.Workspace) (func(), error) {
	if err := a.Store.Acquire(ws, a.Self, func(others int) error {
		return a.manager(g).Setup(ctx, ws, others)
	}); err != nil {
		return nil, err
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			// A fresh connection: the session's may have died with a VM reboot.
			rg, err := a.Dial(ctx, a.Cfg.VMUser)
			if err != nil {
				rg = deadGuest{err}
			}
			defer func() { _ = rg.Close() }()
			if err := a.Store.Release(ws.Key, a.Self, func(w lease.Workspace) error {
				return a.manager(rg).Teardown(ctx, w)
			}); err != nil {
				a.warn("%v", err)
			}
		})
	}, nil
}

// deadGuest stands in when the VM cannot be reached at release time: the
// in-VM unmount fails, the host-side share is still removed.
type deadGuest struct{ err error }

func (d deadGuest) Output(context.Context, string) (string, error) { return "", d.err }
func (d deadGuest) Run(context.Context, string, io.Reader, io.Writer, io.Writer) error {
	return d.err
}
func (d deadGuest) Close() error { return nil }

// stalenessWarn is the session-start reminder that updates have stalled.
// Never fails the session.
func (a *App) stalenessWarn(ctx context.Context, g Guest) {
	if a.Cfg.UpdateWarnDays <= 0 {
		return
	}
	out, err := g.Output(ctx, stalenessProbe)
	if err != nil {
		return
	}
	for _, m := range stalenessMessages(a.Now().Unix(), a.Cfg.UpdateWarnDays, a.Cfg.UpdateApt, parseProbe(out)) {
		a.warn("%s", m)
	}
}

// loginEnv makes a remote command see the VM account's login PATH (where the
// native claude installer puts ~/.local/bin); see CLAUDE_LOGIN_ENV in lib/common.sh.
const loginEnv = `[ -f "$HOME/.profile" ] && . "$HOME/.profile";`

// q single-quotes s for the VM shell.
func q(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// claudeCommand is the remote command of a session in vmDir.
func claudeCommand(vmDir, hint string, args []string) string {
	var b strings.Builder
	b.WriteString(loginEnv)
	fmt.Fprintf(&b, " cd %s 2>/dev/null || { echo %s >&2; exit 1; }; exec claude --dangerously-skip-permissions",
		q(vmDir), q("fclaude: workspace not mounted in the VM at "+vmDir+" - run: fclaude --cleanup "+hint))
	for _, arg := range args {
		b.WriteString(" " + q(arg))
	}
	return b.String()
}

// sshArgs are ssh.exe's arguments for user, with the pinned host key.
// Paths use forward slashes (OpenSSH for Windows accepts them, and its option
// parser treats backslashes as escapes) and are quoted (user names with spaces).
func (a *App) sshArgs(user string, tty bool, remote string) []string {
	slash := func(p string) string { return filepath.ToSlash(p) }
	args := []string{
		"-i", slash(a.Layout.Key(user)),
		"-o", "IdentitiesOnly=yes",
		"-o", `UserKnownHostsFile="` + slash(a.Layout.KnownHosts()) + `"`,
		"-o", "StrictHostKeyChecking=yes",
		"-o", "LogLevel=ERROR",
		"-o", "ConnectTimeout=10",
		"-o", "ServerAliveInterval=15",
		"-o", "ServerAliveCountMax=4",
	}
	if tty {
		args = append(args, "-t")
	}
	args = append(args, user+"@"+a.Info.VMIP)
	if remote != "" {
		args = append(args, remote)
	}
	return args
}

// interactive runs ssh.exe while this process ignores Ctrl+C (the remote side
// gets it as a keystroke). A console close runs onClose first (Windows gives
// a closing console's processes a few seconds), then exits.
func (a *App) interactive(args []string, onClose func()) (int, error) {
	if a.Signals != nil {
		sig, stop := a.Signals()
		defer stop()
		done := make(chan struct{})
		defer close(done)
		go func() {
			for {
				select {
				case <-done:
					return
				case s := <-sig:
					if isClose(s) {
						onClose()
						os.Exit(1)
					}
				}
			}
		}()
	}
	return a.SSH(args)
}
