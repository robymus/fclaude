package vbox

import (
	"os/exec"
	"syscall"
)

// hideWindow stops each VBoxManage call flashing a console window when the
// caller (e.g. the VS Code wrapper) has no console of its own.
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
}
