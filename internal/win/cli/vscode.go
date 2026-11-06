package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/robymus/fclaude/internal/win/lease"
	"github.com/robymus/fclaude/internal/win/mcphelper"
	"github.com/robymus/fclaude/internal/win/pathmap"
	"github.com/robymus/fclaude/internal/win/vmssh"
)

// VSCodeMain is fclaude-vscode.exe, the Claude extension's
// claudeCode.claudeProcessWrapper (PLAN §3.4, Option A). The extension runs
//
//	fclaude-vscode.exe <its own claude.exe> <claude flags...>
//
// with the workspace as the working directory. The bundled claude.exe is a
// Windows binary the VM cannot run, so it is dropped and the VM's claude runs
// with the same flags, over in-process ssh, with paths rewritten both ways.
func VSCodeMain(home string, argv []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(argv) < 2 {
		_, _ = fmt.Fprintln(stderr, "fclaude-vscode: usage: fclaude-vscode <claude-binary> [claude args...] "+
			"(set it as claudeCode.claudeProcessWrapper in VS Code)")
		return 2
	}
	a, err := Open(home)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "fclaude-vscode: %v\n", err)
		return 1
	}
	a.In, a.Out, a.Err = stdin, stdout, stderr
	a.tag = "fclaude-vscode"
	cwd, err := os.Getwd()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "fclaude-vscode: %v\n", err)
		return 1
	}
	code, err := a.VSCode(context.Background(), cwd, argv[2:], os.Environ())
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "fclaude-vscode: %v\n", err)
		if code == 0 {
			code = 1
		}
	}
	return code
}

// VSCode runs one extension-spawned claude in the VM.
func (a *App) VSCode(ctx context.Context, cwd string, args, environ []string) (int, error) {
	helper := mcphelper.Is(args)
	var wss []lease.Workspace
	var rw *pathmap.Rewriter
	if !helper {
		ws, err := a.workspace(cwd)
		if err != nil {
			return 1, fmt.Errorf("this VS Code folder cannot run in the fclaude VM: %w", err)
		}
		wss = append(wss, ws)
		m, err := pathmap.New(pathmap.RootSpelling(a.Cfg.ShareRoot, cwd), a.Cfg.VMMountBase)
		if err != nil {
			return 1, err
		}
		rw = pathmap.NewRewriter(m, pathmap.DefaultPolicy())
		// Extra workspace folders (multi-root) come as --add-dir: share them
		// too, or drop the ones the VM cannot see.
		var extra []string
		args, extra = splitAddDirs(args)
		for _, d := range extra {
			ws, err := a.workspace(d)
			if err != nil {
				a.warn("not sharing the extra folder %s with the VM: %v", d, err)
				continue
			}
			if !containsKey(wss, ws.Key) {
				wss = append(wss, ws)
			}
			args = append(args, "--add-dir="+d)
		}
		args = rw.Args(args)
	}

	g, err := a.EnsureRunning(ctx)
	if err != nil {
		return 1, err
	}
	defer func() { _ = g.Close() }()
	a.reap(ctx, g)

	var held []func()
	defer func() {
		for i := len(held) - 1; i >= 0; i-- {
			held[i]()
		}
	}()
	for _, ws := range wss {
		release, err := a.hold(ctx, g, ws)
		if err != nil {
			return 1, err
		}
		held = append(held, release)
	}

	envs := forwardEnv(environ)
	var remote string
	if helper {
		remote = loginEnv + " cd " + q(a.Cfg.VMMountBase) + " && exec" + envPrefix(envs) + " claude" + quoteAll(args)
	} else {
		remote = vscodeCommand(wss[0].VMPath, wss[0].WinPath, envs, args)
	}

	if helper {
		return exitCode(g.Run(ctx, remote, a.In, a.Out, a.Err))
	}
	return exitCode(runRewritten(ctx, g, remote, rw, a.In, a.Out, a.Err))
}

// runRewritten runs remote with stdin mapped Windows→VM and stdout VM→Windows.
func runRewritten(ctx context.Context, g Guest, remote string, rw *pathmap.Rewriter, in io.Reader, out, errw io.Writer) error {
	inR, inW := io.Pipe()
	go func() { _ = inW.CloseWithError(rw.Pipe(inW, in, pathmap.ToVM)) }()
	outR, outW := io.Pipe()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		// Drain even after a write error, so the session never blocks on us.
		if err := rw.Pipe(out, outR, pathmap.ToWin); err != nil {
			_, _ = io.Copy(io.Discard, outR)
		}
	}()
	err := g.Run(ctx, remote, inR, outW, errw)
	_ = outW.Close()
	wg.Wait()
	_ = inR.Close()
	return err
}

func exitCode(err error) (int, error) {
	var ee *vmssh.ExitError
	if errors.As(err, &ee) {
		return ee.Status, nil
	}
	if err != nil {
		return 1, err
	}
	return 0, nil
}

func vscodeCommand(vmDir, hint string, envs, args []string) string {
	var b strings.Builder
	b.WriteString(loginEnv)
	fmt.Fprintf(&b, " cd %s 2>/dev/null || { echo %s >&2; exit 1; }; exec%s claude%s",
		q(vmDir), q("fclaude-vscode: workspace not mounted in the VM at "+vmDir+" - run: fclaude --cleanup "+hint),
		envPrefix(envs), quoteAll(args))
	return b.String()
}

func envPrefix(envs []string) string {
	if len(envs) == 0 {
		return ""
	}
	return " env" + quoteAll(envs)
}

// splitAddDirs removes --add-dir arguments (both --add-dir=<d> and
// --add-dir <d>) and returns the remaining arguments and the directories.
func splitAddDirs(args []string) (rest, dirs []string) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case strings.HasPrefix(a, "--add-dir="):
			dirs = append(dirs, strings.TrimPrefix(a, "--add-dir="))
		case a == "--add-dir" && i+1 < len(args):
			i++
			dirs = append(dirs, args[i])
		default:
			rest = append(rest, a)
		}
	}
	return rest, dirs
}

func containsKey(wss []lease.Workspace, key string) bool {
	for _, w := range wss {
		if w.Key == key {
			return true
		}
	}
	return false
}

// forwardEnv picks the extension's CLAUDE_* settings to pass to the VM's
// claude (sshd accepts no environment, so they go on the command line).
// Values holding Windows paths, and settings that only mean something on
// this machine, stay behind.
func forwardEnv(environ []string) []string {
	skip := map[string]bool{
		"CLAUDE_CODE_SSE_PORT":      true, // the extension's localhost IDE server: unreachable from the VM
		"CLAUDE_CODE_GIT_BASH_PATH": true,
	}
	var out []string
	for _, kv := range environ {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || skip[strings.ToUpper(k)] {
			continue
		}
		if !strings.HasPrefix(k, "CLAUDE_CODE_") && !strings.HasPrefix(k, "CLAUDE_AGENT_SDK_") {
			continue
		}
		if looksLikeWindowsPath(v) || !isEnvName(k) {
			continue
		}
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out
}

func looksLikeWindowsPath(v string) bool {
	return strings.Contains(v, `:\`) || strings.Contains(v, `\\`) ||
		len(v) >= 3 && v[1] == ':' && v[2] == '/'
}

func isEnvName(k string) bool {
	for i, c := range k {
		ok := c == '_' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || i > 0 && c >= '0' && c <= '9'
		if !ok {
			return false
		}
	}
	return k != ""
}
