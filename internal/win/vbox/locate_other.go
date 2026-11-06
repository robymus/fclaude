//go:build !windows

package vbox

// Non-Windows builds exist for CI only; VirtualBox on Linux is not a
// supported fclaude host (Linux uses KVM).
const exeName = "VBoxManage"

var defaultPaths = []string{"/usr/bin/VBoxManage"}

func registryInstallDir() string { return "" }
