package procjob

import (
	"os/exec"
	"syscall"
)

// Prepare asks the kernel to SIGKILL the child when this process dies.
func Prepare(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Pdeathsig = syscall.SIGKILL
}

// Attach is a no-op on Linux; Prepare already did the work.
func Attach(*exec.Cmd) error { return nil }
