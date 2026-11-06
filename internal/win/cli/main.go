package cli

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/robymus/fclaude/internal/win/install"
)

// Usage is fclaude --help.
const Usage = `Usage: fclaude [options] [-- claude-args...]

Runs Claude Code (--dangerously-skip-permissions) inside the fclaude VM, with
the current folder shared into it. The folder must be under your projects
folder (share_root in %USERPROFILE%\.fclaude\config.json).

Options:
  --dir <path>       Run in <path> instead of the current folder.
  --no-mount         Run claude in the VM home with nothing shared.
  --mount <dir>...   Share the given folders into the VM and hold them until
                     Ctrl+C. Does not start claude.
  -m, --maintenance  Open a shell in the VM as the maintenance user.
  -s, --status       Print VM state, resources and active workspaces
                     (does not start the VM).
  --cleanup <ws>     Force-release one stuck workspace (a folder, or a path as
                     --status prints it). Does not start the VM.
  --update           Update Claude Code + system packages in the VM now.
  --shutdown         Power off the VM (asks first).
  -h, --help         Show this help.
  -V, --version      Show the version.

Commands:
  fclaude grow-disk <size>   Grow the VM disk, e.g. 64G (restarts the VM).
  fclaude uninstall          Remove fclaude (asks; the VM disk is kept unless
                             you type DELETE DISK).

Anything after -- (or any unrecognized argument) is passed to claude.
`

type options struct {
	mode    string // "" (session), mount, maint, status, cleanup, update, shutdown, grow-disk, uninstall
	dir     string
	noMount bool
	mounts  []string
	target  string
	claude  []string
}

func parse(args []string) (options, error) {
	o := options{dir: "."}
	if len(args) > 0 {
		switch args[0] {
		case "uninstall":
			if len(args) != 1 {
				return o, errors.New("usage: fclaude uninstall")
			}
			o.mode = "uninstall"
			return o, nil
		case "grow-disk":
			if len(args) != 2 {
				return o, errors.New("usage: fclaude grow-disk <size>   (e.g. 64G)")
			}
			o.mode, o.target = "grow-disk", args[1]
			return o, nil
		}
	}
	setMode := func(m string) error {
		if o.mode != "" && o.mode != m {
			return fmt.Errorf("--%s cannot be combined with --%s", m, o.mode)
		}
		o.mode = m
		return nil
	}
	for i := 0; i < len(args); i++ {
		var err error
		switch a := args[i]; a {
		case "--dir":
			if i+1 >= len(args) {
				return o, errors.New("--dir needs a path")
			}
			i++
			o.dir = args[i]
		case "--no-mount":
			o.noMount = true
		case "--mount":
			err = setMode("mount")
			for i+1 < len(args) && len(args[i+1]) > 0 && args[i+1][0] != '-' {
				i++
				o.mounts = append(o.mounts, args[i])
			}
		case "-m", "--maintenance":
			err = setMode("maintenance")
		case "-s", "--status":
			err = setMode("status")
		case "--cleanup":
			if i+1 >= len(args) {
				return o, errors.New("--cleanup needs a workspace path")
			}
			err = setMode("cleanup")
			i++
			o.target = args[i]
		case "--update":
			err = setMode("update")
		case "--shutdown":
			err = setMode("shutdown")
		case "--":
			o.claude = append(o.claude, args[i+1:]...)
			i = len(args)
		default:
			o.claude = append(o.claude, a)
		}
		if err != nil {
			return o, err
		}
	}
	return o, nil
}

// Main is fclaude.exe. home is the fclaude directory (%USERPROFILE%\.fclaude).
func Main(home, version string, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	o, err := parse(args)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "fclaude: %v (see fclaude --help)\n", err)
		return 2
	}
	ctx := context.Background()
	if o.mode == "uninstall" {
		ui := install.NewConsoleUI(stdin, stdout)
		d, err := install.ProductionDeps(home, version, ui)
		if err == nil {
			err = install.Uninstall(ctx, d)
		}
		if err != nil {
			ui.Warn("%v", err)
			return 1
		}
		return 0
	}
	a, err := Open(home)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "fclaude: %v\n", err)
		return 1
	}
	a.In, a.Out, a.Err = stdin, stdout, stderr
	code, err := a.run(ctx, o)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "fclaude: %v\n", err)
		if code == 0 {
			code = 1
		}
	}
	return code
}

func (a *App) run(ctx context.Context, o options) (int, error) {
	switch o.mode {
	case "mount":
		return 0, a.Mount(ctx, o.mounts)
	case "maintenance":
		return a.Maintenance(ctx)
	case "status":
		return 0, a.Status(ctx)
	case "cleanup":
		return 0, a.Cleanup(ctx, o.target)
	case "update":
		return a.Update(ctx)
	case "shutdown":
		return 0, a.Shutdown(ctx)
	case "grow-disk":
		return 0, a.GrowDisk(ctx, o.target)
	default:
		return a.Session(ctx, o.dir, o.noMount, o.claude)
	}
}
