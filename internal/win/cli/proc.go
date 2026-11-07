package cli

import (
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"

	"github.com/robymus/fclaude/internal/win/procjob"
)

// notifySignals delivers Ctrl+C (os.Interrupt) and, on Windows, console
// close / logoff / shutdown (Go maps those to SIGTERM and gives the handler
// a few seconds before the process is killed).
func notifySignals() (<-chan os.Signal, func()) {
	c := make(chan os.Signal, 2)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	return c, func() { signal.Stop(c) }
}

func isClose(s os.Signal) bool { return s == syscall.SIGTERM }

// sshExe is Windows' built-in OpenSSH client (an optional feature, on by
// default in Windows 11), else whatever ssh is on PATH.
func sshExe() (string, error) {
	if runtime.GOOS == "windows" {
		p := filepath.Join(os.Getenv("SystemRoot"), "System32", "OpenSSH", "ssh.exe")
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	p, err := exec.LookPath("ssh")
	if err != nil {
		return "", errors.New("the OpenSSH client (ssh.exe) is missing: Settings > System > Optional features > OpenSSH Client")
	}
	return p, nil
}

// runSSHExe runs ssh on this console. The child sits in a kill-on-close job:
// if fclaude dies, ssh dies with it.
func runSSHExe(args []string) (int, error) {
	exe, err := sshExe()
	if err != nil {
		return 1, err
	}
	cmd := exec.Command(exe, args...) //nolint:gosec // G204: ssh.exe with arguments we build
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	procjob.Prepare(cmd)
	if err := cmd.Start(); err != nil {
		return 1, err
	}
	if err := procjob.Attach(cmd); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return 1, err
	}
	err = cmd.Wait()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode(), nil
	}
	if err != nil {
		return 1, err
	}
	return 0, nil
}
