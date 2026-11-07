package vmw

import "golang.org/x/sys/windows/registry"

func readString(path, name string) (string, bool) {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, path, registry.QUERY_VALUE)
	if err != nil {
		return "", false
	}
	defer func() { _ = k.Close() }()
	v, _, err := k.GetStringValue(name)
	return v, err == nil
}

func registryInstall() (dir, version string) {
	const key = `SOFTWARE\VMware, Inc.\VMware Workstation`
	dir, _ = readString(key, "InstallPath")
	version, _ = readString(key, "ProductVersion")
	return dir, version
}

func registryVMnet(vmnet string) (addr, mask string, ok bool) {
	key := `SOFTWARE\VMware, Inc.\VMnetLib\VMnetConfig\` + vmnet
	addr, ok = readString(key, "IPSubnetAddress")
	mask, _ = readString(key, "IPSubnetMask")
	return addr, mask, ok
}
