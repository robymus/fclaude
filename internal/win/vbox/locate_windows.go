package vbox

import "golang.org/x/sys/windows/registry"

const exeName = "VBoxManage.exe"

var defaultPaths = []string{`C:\Program Files\Oracle\VirtualBox\VBoxManage.exe`}

func registryInstallDir() string {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Oracle\VirtualBox`, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer func() { _ = k.Close() }()
	dir, _, err := k.GetStringValue("InstallDir")
	if err != nil {
		return ""
	}
	return dir
}
