// Package vmw drives VMware Workstation Pro through vmrun and
// vmware-vdiskmanager.
//
// The VM is never registered in the Workstation library: every operation
// addresses it by its .vmx path, and the .vmx is written by this package
// (vmx.go). All tool invocations go through the Runner interface so command
// lines and output parsing are unit-tested without VMware. Output formats
// were captured from Workstation 26.0.1 on Windows 11 (PLAN §0.7).
package vmw

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/robymus/fclaude/internal/win/winpath"
)

// Runner executes one tool with args and returns its combined output. A
// non-zero exit must be returned as a *CommandError.
type Runner interface {
	Run(ctx context.Context, args ...string) (string, error)
}

// CommandError is a failed tool invocation. vmrun reports errors on stdout as
// "Error: <message>" with exit status 255.
type CommandError struct {
	Tool   string
	Args   []string
	Output string
	Err    error
}

// Message is the tool's own error text, without vmrun's "Error: " prefix.
func (e *CommandError) Message() string {
	for _, l := range strings.Split(e.Output, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			return strings.TrimPrefix(l, "Error: ")
		}
	}
	return ""
}

func (e *CommandError) Error() string {
	msg := e.Message()
	if msg == "" {
		msg = e.Err.Error()
	}
	return fmt.Sprintf("%s %s: %s", e.Tool, strings.Join(e.Args, " "), msg)
}

func (e *CommandError) Unwrap() error { return e.Err }

// ExecRunner runs the real executable at Path.
type ExecRunner struct{ Path string }

// Run implements Runner. stdout and stderr are combined: vmrun prints its
// errors on stdout, vdiskmanager on either.
func (r ExecRunner) Run(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, r.Path, args...) //nolint:gosec // fixed binary, args built by this package
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	hideWindow(cmd)
	err := cmd.Run()
	s := strings.ReplaceAll(out.String(), "\r\n", "\n")
	if err != nil {
		return s, &CommandError{Tool: toolName(r.Path), Args: args, Output: s, Err: err}
	}
	return s, nil
}

func toolName(p string) string {
	i := strings.LastIndexAny(p, `\/`)
	return strings.TrimSuffix(p[i+1:], ".exe")
}

// vmrun messages that mean "already in the requested state".
const (
	msgAlreadyExists = "Already exists"
	msgNotFound      = "A file was not found"
)

func hasMessage(err error, msg string) bool {
	ce, ok := err.(*CommandError) //nolint:errorlint // Runner contract: returned directly, never wrapped
	return ok && strings.EqualFold(ce.Message(), msg)
}

// Client is a typed vmrun front end. R runs vmrun.exe.
type Client struct{ R Runner }

func (c Client) vmrun(ctx context.Context, args ...string) (string, error) {
	return c.R.Run(ctx, append([]string{"-T", "ws"}, args...)...)
}

// Start boots the VM without a window. Starting a running VM is a no-op.
func (c Client) Start(ctx context.Context, vmx string) error {
	_, err := c.vmrun(ctx, "start", vmx, "nogui")
	return err
}

// Stop shuts the VM down: soft asks the guest (ACPI), hard powers off.
func (c Client) Stop(ctx context.Context, vmx string, hard bool) error {
	mode := "soft"
	if hard {
		mode = "hard"
	}
	_, err := c.vmrun(ctx, "stop", vmx, mode)
	return err
}

// Running reports whether the VM at vmx is running.
func (c Client) Running(ctx context.Context, vmx string) (bool, error) {
	out, err := c.vmrun(ctx, "list")
	if err != nil {
		return false, err
	}
	want := winpath.Clean(vmx)
	for _, p := range ParseList(out) {
		if strings.EqualFold(winpath.Clean(p), want) {
			return true, nil
		}
	}
	return false, nil
}

// ParseList returns the .vmx paths from `vmrun list` output
// ("Total running VMs: N" followed by one path per line).
func ParseList(out string) []string {
	var vms []string
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "Total running VMs:") {
			continue
		}
		vms = append(vms, l)
	}
	return vms
}

// EnableSharedFolders turns HGFS sharing on for the running VM.
func (c Client) EnableSharedFolders(ctx context.Context, vmx string) error {
	_, err := c.vmrun(ctx, "enableSharedFolders", vmx)
	return err
}

// AddShare shares hostDir with the running VM as name. A share that already
// exists under name counts as success: names are derived from the host path,
// so the same name means the same directory. The caller must check that
// hostDir exists — vmrun reports a missing directory with the same message as
// a missing share.
func (c Client) AddShare(ctx context.Context, vmx, name, hostDir string) error {
	_, err := c.vmrun(ctx, "addSharedFolder", vmx, name, hostDir)
	if hasMessage(err, msgAlreadyExists) {
		return nil
	}
	return err
}

// RemoveShare removes the share name from the running VM. Removing a share
// that does not exist counts as success.
func (c Client) RemoveShare(ctx context.Context, vmx, name string) error {
	_, err := c.vmrun(ctx, "removeSharedFolder", vmx, name)
	if hasMessage(err, msgNotFound) {
		return nil
	}
	return err
}
