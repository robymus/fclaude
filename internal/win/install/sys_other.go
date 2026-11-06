//go:build !windows

package install

import "errors"

var errWindowsOnly = errors.New("only supported on Windows")

// ErrElevationDeclined means the user answered No to the UAC prompt.
var ErrElevationDeclined = errors.New("administrator permission was declined")

// RunElevated is Windows-only.
func RunElevated(string, []string) error { return errWindowsOnly }

// IsElevated is Windows-only.
func IsElevated() bool { return false }

// PowerShell is Windows-only.
func PowerShell(string) (string, error) { return "", errWindowsOnly }

// AddToUserPath is Windows-only.
func AddToUserPath(string) (bool, error) { return false, errWindowsOnly }

// RemoveFromUserPath is Windows-only.
func RemoveFromUserPath(string) (bool, error) { return false, errWindowsOnly }
