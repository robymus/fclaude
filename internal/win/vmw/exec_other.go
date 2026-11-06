//go:build !windows

package vmw

import "os/exec"

func hideWindow(*exec.Cmd) {}
