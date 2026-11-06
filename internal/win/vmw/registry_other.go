//go:build !windows

package vmw

func registryInstall() (dir, version string) { return "", "" }

func registryVMnet(string) (addr, mask string, ok bool) { return "", "", false }
