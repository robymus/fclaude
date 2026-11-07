package vmw

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Minimum supported Workstation version: 17.5 is the first with the free
// personal licence and a mature Windows Hypervisor Platform (ULM) mode.
const (
	MinMajor = 17
	MinMinor = 5
)

// ErrNotInstalled means VMware Workstation could not be found.
var ErrNotInstalled = errors.New("VMware Workstation Pro not found")

// Install is a located VMware Workstation installation.
type Install struct {
	Dir     string // e.g. C:\Program Files\VMware\VMware Workstation
	Version string // ProductVersion from the registry, e.g. 26.0.1.25688693; "" if unknown
}

// Vmrun is the path of vmrun.exe.
func (i Install) Vmrun() string { return filepath.Join(i.Dir, "vmrun.exe") }

// Vdiskmanager is the path of vmware-vdiskmanager.exe.
func (i Install) Vdiskmanager() string { return filepath.Join(i.Dir, "vmware-vdiskmanager.exe") }

const defaultDir = `C:\Program Files\VMware\VMware Workstation`

// Locate finds Workstation: the registry InstallPath (readable without
// admin), then the default directory. It requires vmrun.exe to exist and, when
// the registry reports a version, that it is at least MinMajor.MinMinor.
func Locate() (Install, error) {
	return locate(registryInstall, fileExists)
}

func locate(reg func() (dir, version string), exists func(string) bool) (Install, error) {
	dir, version := reg()
	for _, d := range []string{dir, defaultDir} {
		if d == "" {
			continue
		}
		in := Install{Dir: filepath.Clean(d), Version: version}
		if !exists(in.Vmrun()) {
			continue
		}
		if version != "" {
			if err := CheckVersion(version); err != nil {
				return Install{}, err
			}
		}
		return in, nil
	}
	return Install{}, ErrNotInstalled
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// CheckVersion rejects a Workstation older than MinMajor.MinMinor.
func CheckVersion(v string) error {
	major, minor, err := ParseVersion(v)
	if err != nil {
		return err
	}
	if major < MinMajor || (major == MinMajor && minor < MinMinor) {
		return fmt.Errorf("VMware Workstation %s is too old: %d.%d or newer is required", v, MinMajor, MinMinor)
	}
	return nil
}

// ParseVersion reads major.minor from a version like 26.0.1.25688693.
func ParseVersion(v string) (major, minor int, err error) {
	parts := strings.Split(strings.TrimSpace(v), ".")
	if len(parts) < 2 {
		return 0, 0, fmt.Errorf("vmw: unrecognized version %q", v)
	}
	if major, err = strconv.Atoi(parts[0]); err != nil {
		return 0, 0, fmt.Errorf("vmw: unrecognized version %q", v)
	}
	if minor, err = strconv.Atoi(parts[1]); err != nil {
		return 0, 0, fmt.Errorf("vmw: unrecognized version %q", v)
	}
	return major, minor, nil
}
