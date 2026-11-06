package vbox

import (
	"errors"
	"os"
	"path/filepath"
)

// ErrNotInstalled means no VBoxManage executable could be found.
var ErrNotInstalled = errors.New("VirtualBox (VBoxManage.exe) not found")

// Locate finds VBoxManage: $VBOX_MSI_INSTALL_PATH (set by the VirtualBox
// installer), then the registry InstallDir, then the default location.
func Locate() (string, error) {
	var candidates []string
	if d := os.Getenv("VBOX_MSI_INSTALL_PATH"); d != "" {
		candidates = append(candidates, filepath.Join(d, exeName))
	}
	if d := registryInstallDir(); d != "" {
		candidates = append(candidates, filepath.Join(d, exeName))
	}
	candidates = append(candidates, defaultPaths...)
	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c, nil
		}
	}
	return "", ErrNotInstalled
}
