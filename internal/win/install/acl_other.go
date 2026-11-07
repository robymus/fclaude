//go:build !windows

package install

import "os"

func restrictToOwner(path string) error { return os.Chmod(path, 0o400) }
