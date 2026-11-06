//go:build !windows

package vbox

import "os/exec"

func hideWindow(*exec.Cmd) {}
