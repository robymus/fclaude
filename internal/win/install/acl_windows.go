package install

import (
	"os"
	"os/exec"
	"syscall"
)

// restrictToOwner removes inherited ACEs and grants only the current user
// read access (what Windows OpenSSH requires of a private key).
func restrictToOwner(path string) error {
	user := os.Getenv("USERNAME")
	cmd := exec.Command("icacls", path, "/inheritance:r", "/grant:r", user+":R") //nolint:gosec // fixed tool, our own path
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return &toolError{"icacls", string(out), err}
	}
	return nil
}
