// Command fclaude-win is the experimental Windows host tooling for fclaude.
//
// One binary, several personalities, chosen by the executable's base name
// (the installer copies the same file under each name):
//
//	fclaude.exe          CLI (session, --status, --maintenance, ...)
//	fclaude-vscode.exe   claudeCode.claudeProcessWrapper for VS Code
//	fclaude-setup.exe    interactive installer (the release asset)
//	fclaude-capture.exe  research: run the bundled claude natively, log all stdio
//
// See .plans/windows-PLAN.md.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/robymus/fclaude/internal/win/capture"
	"github.com/robymus/fclaude/internal/win/cli"
	"github.com/robymus/fclaude/internal/win/install"
)

// version is set at release build time: -ldflags "-X main.version=<tag>".
var version = "dev"

func main() {
	os.Exit(run(os.Args, os.Stdin, os.Stdout, os.Stderr))
}

// mode derives the personality from argv[0].
func mode(argv0 string) string {
	base := strings.ToLower(filepath.Base(strings.ReplaceAll(argv0, `\`, "/")))
	base = strings.TrimSuffix(base, ".exe")
	switch {
	case base == "fclaude-vscode" || base == "fclaude-capture":
		return base
	// Browsers save a repeated download as "fclaude-setup (1).exe", and
	// people rename it (fclaude-setup-v0.1.0.exe): still the installer.
	case strings.HasPrefix(base, "fclaude-setup"):
		return "fclaude-setup"
	default:
		return "fclaude"
	}
}

func run(argv []string, stdin io.Reader, stdout, stderr io.Writer) int {
	m := mode(argv[0])
	args := argv[1:]

	// The wrapper personalities pass every argument through to claude, so only
	// the CLI and the installer have flags of their own.
	if m != "fclaude-capture" && m != "fclaude-vscode" && len(args) == 1 {
		switch arg := args[len(args)-1]; arg {
		case "--version", "-V":
			_, _ = fmt.Fprintf(stdout, "%s %s (experimental Windows build)\n", m, version)
			return 0
		case "--help", "-h":
			if m == "fclaude" {
				_, _ = io.WriteString(stdout, cli.Usage)
				return 0
			}
			usage(stdout, m)
			return 0
		}
	}

	// The UAC-elevated steps setup and uninstall start (through whichever
	// personality ran them).
	if m == "fclaude" || m == "fclaude-setup" {
		if len(args) > 0 && (args[0] == "install-elevated" || args[0] == "uninstall-elevated") {
			return runElevated(args, stderr)
		}
	}

	switch m {
	case "fclaude-capture":
		return runCapture(argv, stdin, stdout, stderr)
	case "fclaude-setup":
		return runSetup(args, stdin, stdout, stderr)
	case "fclaude-vscode":
		home, err := fclaudeHome()
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "fclaude-vscode: %v\n", err)
			return 1
		}
		return cli.VSCodeMain(home, argv, stdin, stdout, stderr)
	default: // fclaude
		home, err := fclaudeHome()
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "fclaude: %v\n", err)
			return 1
		}
		return cli.Main(home, version, args, stdin, stdout, stderr)
	}
}

func usage(w io.Writer, m string) {
	_, _ = fmt.Fprintf(w, `%s — fclaude for Windows (experimental)

Usage:
  %s --version
  %s --help

Personalities (same binary, chosen by file name):
  fclaude.exe          CLI
  fclaude-vscode.exe   VS Code process wrapper
  fclaude-setup.exe    installer
  fclaude-capture.exe  stdio capture of a native claude (research)
`, m, m, m)
}

// fclaudeHome is %USERPROFILE%\.fclaude (or $FCLAUDE_HOME).
func fclaudeHome() (string, error) {
	if h := os.Getenv("FCLAUDE_HOME"); h != "" {
		return h, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".fclaude"), nil
}

// runCapture: invoked by the extension as `fclaude-capture.exe <claude.exe> <flags...>`.
// Logging failures never break the session: claude still runs, unlogged.
func runCapture(argv []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(argv) < 2 {
		_, _ = fmt.Fprintln(stderr, "fclaude-capture: usage: fclaude-capture <claude-binary> [args...]")
		return 2
	}
	var log io.Writer // nil until a log file opens; io.Discard otherwise
	dir := os.Getenv("FCLAUDE_CAPTURE_DIR")
	if dir == "" {
		if home, err := fclaudeHome(); err == nil {
			dir = filepath.Join(home, "captures")
		}
	}
	if dir != "" {
		name := fmt.Sprintf("%s-%d.jsonl", time.Now().Format("20060102-150405"), os.Getpid())
		if err := os.MkdirAll(dir, 0o700); err == nil {
			if f, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600); err == nil {
				defer func() { _ = f.Close() }()
				log = f
			} else {
				_, _ = fmt.Fprintf(stderr, "fclaude-capture: not logging: %v\n", err)
			}
		}
	}
	if log == nil {
		log = io.Discard
	}
	code, err := capture.Run(capture.Options{
		Command: argv[1], Args: argv[2:], Wrapper: argv,
		Stdin: stdin, Stdout: stdout, Stderr: stderr, Log: log,
	})
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "fclaude-capture: %v\n", err)
		if code == 0 {
			code = 1
		}
	}
	return code
}

// runElevated is install-elevated / uninstall-elevated (run through UAC).
func runElevated(args []string, stderr io.Writer) int {
	if len(args) == 0 {
		return 2
	}
	step, rest := args[0], args[1:]
	home, err := fclaudeHome()
	if err == nil {
		if step == "install-elevated" {
			err = install.ElevatedStep(home, rest)
		} else {
			err = install.ElevatedUninstallStep(home)
		}
	}
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "fclaude: %s: %v\n", step, err)
		return 1
	}
	return 0
}

// runSetup is fclaude-setup: the interactive installer.
func runSetup(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	home, err := fclaudeHome()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "fclaude-setup: %v\n", err)
		return 1
	}
	if len(args) > 0 {
		_, _ = fmt.Fprintf(stderr, "fclaude-setup: unknown arguments %q (run it without arguments)\n", args)
		return 2
	}
	ui := install.NewConsoleUI(stdin, stdout)
	_, _ = fmt.Fprintf(stdout, "fclaude setup %s — Claude Code in an isolated VM (experimental Windows build)\n", version)
	code := 0
	d, err := install.ProductionDeps(home, version, ui)
	if err == nil {
		err = install.Setup(context.Background(), d)
	}
	if err != nil {
		ui.Warn("%v", err)
		code = 1
	} else {
		ui.Info("Setup finished.")
	}
	ui.Pause()
	return code
}
