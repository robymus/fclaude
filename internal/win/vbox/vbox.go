// Package vbox drives VirtualBox through VBoxManage.
//
// Everything goes through the Runner interface so the command lines and the
// output parsing are unit-tested without VirtualBox. Output formats were
// written against VirtualBox 7.1 documentation; the fixtures under testdata/
// are to be re-captured from a real install (plan, Windows phase) and any
// divergence fixed here.
package vbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"
)

// Runner executes VBoxManage with args and returns stdout. A non-zero exit
// must be returned as a *CommandError carrying stderr.
type Runner interface {
	Run(ctx context.Context, args ...string) (string, error)
}

// CommandError is a failed VBoxManage invocation.
type CommandError struct {
	Args   []string
	Stderr string
	Err    error
}

func (e *CommandError) Error() string {
	msg := strings.TrimSpace(e.Stderr)
	if msg == "" {
		msg = e.Err.Error()
	}
	return fmt.Sprintf("VBoxManage %s: %s", strings.Join(e.Args, " "), msg)
}

func (e *CommandError) Unwrap() error { return e.Err }

// ExecRunner runs the real VBoxManage binary.
type ExecRunner struct{ Path string }

// Run implements Runner.
func (r ExecRunner) Run(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, r.Path, args...) //nolint:gosec // fixed binary, args built by this package
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	hideWindow(cmd)
	if err := cmd.Run(); err != nil {
		return stdout.String(), &CommandError{Args: args, Stderr: stderr.String(), Err: err}
	}
	return stdout.String(), nil
}

// State is a normalized VM state.
type State string

// VM states as reported by VMState in --machinereadable output.
const (
	StateRunning    State = "running"
	StatePoweredOff State = "poweroff"
	StatePaused     State = "paused"
	StateSaved      State = "saved"
	StateAborted    State = "aborted"
	StateStarting   State = "starting"
	StateStopping   State = "stopping"
	// StateNotFound means no VM with that name is registered.
	StateNotFound State = "notfound"
)

// IsOff reports whether the VM is not running in any form, i.e. a start is a
// cold boot (nothing inside the VM survived).
func (s State) IsOff() bool {
	return s == StatePoweredOff || s == StateAborted || s == StateNotFound
}

// ErrNotFound is returned when the named VM is not registered.
var ErrNotFound = errors.New("vm not registered")

// Client is a typed VBoxManage front end.
type Client struct{ R Runner }

// Info returns `showvminfo --machinereadable` as a key/value map.
func (c Client) Info(ctx context.Context, vm string) (map[string]string, error) {
	out, err := c.R.Run(ctx, "showvminfo", vm, "--machinereadable")
	if err != nil {
		var ce *CommandError
		if errors.As(err, &ce) && (strings.Contains(ce.Stderr, "Could not find a registered machine") ||
			strings.Contains(ce.Stderr, "VBOX_E_OBJECT_NOT_FOUND")) {
			return nil, fmt.Errorf("%s: %w", vm, ErrNotFound)
		}
		return nil, err
	}
	return ParseMachineReadable(out), nil
}

// State returns the VM's state (StateNotFound if it is not registered).
func (c Client) State(ctx context.Context, vm string) (State, error) {
	info, err := c.Info(ctx, vm)
	if errors.Is(err, ErrNotFound) {
		return StateNotFound, nil
	}
	if err != nil {
		return "", err
	}
	st, ok := info["VMState"]
	if !ok {
		return "", fmt.Errorf("showvminfo %s: no VMState in output", vm)
	}
	return State(st), nil
}

// StartHeadless boots the VM without a GUI window.
func (c Client) StartHeadless(ctx context.Context, vm string) error {
	_, err := c.R.Run(ctx, "startvm", vm, "--type", "headless")
	return err
}

// Resume continues a paused VM.
func (c Client) Resume(ctx context.Context, vm string) error {
	_, err := c.R.Run(ctx, "controlvm", vm, "resume")
	return err
}

// ACPIShutdown asks the guest to shut down cleanly.
func (c Client) ACPIShutdown(ctx context.Context, vm string) error {
	_, err := c.R.Run(ctx, "controlvm", vm, "acpipowerbutton")
	return err
}

// PowerOff pulls the virtual plug.
func (c Client) PowerOff(ctx context.Context, vm string) error {
	_, err := c.R.Run(ctx, "controlvm", vm, "poweroff")
	return err
}

// AddTransientShare shares hostPath with the running VM under name until the
// share is removed or the VM powers off. No admin rights are needed.
func (c Client) AddTransientShare(ctx context.Context, vm, name, hostPath string) error {
	_, err := c.R.Run(ctx, "sharedfolder", "add", vm, "--name", name, "--hostpath", hostPath, "--transient")
	return err
}

// RemoveTransientShare withdraws a transient share.
func (c Client) RemoveTransientShare(ctx context.Context, vm, name string) error {
	_, err := c.R.Run(ctx, "sharedfolder", "remove", vm, "--name", name, "--transient")
	return err
}

// Share is one shared folder.
type Share struct{ Name, HostPath string }

// TransientShares lists the transient shares in a showvminfo map.
func TransientShares(info map[string]string) []Share {
	var out []Share
	for k, name := range info {
		idx, ok := strings.CutPrefix(k, "SharedFolderNameTransientMapping")
		if !ok {
			continue
		}
		out = append(out, Share{Name: name, HostPath: info["SharedFolderPathTransientMapping"+idx]})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Version returns the VBoxManage version, e.g. "7.1.4r165100" → (7, 1).
func (c Client) Version(ctx context.Context) (major, minor int, raw string, err error) {
	out, err := c.R.Run(ctx, "--version")
	if err != nil {
		return 0, 0, "", err
	}
	raw = strings.TrimSpace(out)
	major, minor, err = ParseVersion(raw)
	return major, minor, raw, err
}

// ParseVersion extracts major.minor from a VBoxManage --version string.
// VBoxManage may print warnings before the version, so the last line is used.
func ParseVersion(raw string) (major, minor int, err error) {
	lines := strings.Split(strings.TrimSpace(raw), "\n")
	v := strings.TrimSpace(lines[len(lines)-1])
	parts := strings.SplitN(v, ".", 3)
	if len(parts) < 2 {
		return 0, 0, fmt.Errorf("unrecognized VirtualBox version %q", raw)
	}
	major, err1 := strconv.Atoi(parts[0])
	minor, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return 0, 0, fmt.Errorf("unrecognized VirtualBox version %q", raw)
	}
	return major, minor, nil
}

// ParseMachineReadable parses `--machinereadable` output: one key=value per
// line, where keys and values may be double-quoted and quoted values escape
// '"' and '\' with a backslash.
func ParseMachineReadable(out string) map[string]string {
	m := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		key, rest, ok := cutField(line)
		if !ok || !strings.HasPrefix(rest, "=") {
			continue
		}
		val, _, ok := cutField(rest[1:])
		if !ok {
			continue
		}
		m[key] = val
	}
	return m
}

// cutField reads one possibly-quoted field from the start of s and returns
// it with the remainder.
func cutField(s string) (field, rest string, ok bool) {
	if !strings.HasPrefix(s, `"`) {
		i := strings.IndexByte(s, '=')
		if i < 0 {
			return s, "", true
		}
		return s[:i], s[i:], true
	}
	var b strings.Builder
	for i := 1; i < len(s); i++ {
		switch c := s[i]; c {
		case '\\':
			if i+1 < len(s) && (s[i+1] == '"' || s[i+1] == '\\') {
				i++
				b.WriteByte(s[i])
				continue
			}
			b.WriteByte(c)
		case '"':
			return b.String(), s[i+1:], true
		default:
			b.WriteByte(c)
		}
	}
	return "", "", false // unterminated quote
}
