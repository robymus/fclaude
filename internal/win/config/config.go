// Package config loads, validates and writes the Windows host configuration
// (%USERPROFILE%\.fclaude\config.json).
//
// The Windows port keeps the Linux design rule that the install-time config is
// never overwritten: a newer installer writes config.json.new beside an
// existing file and leaves merging to the user.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/robymus/fclaude/internal/win/winpath"
)

// FileName is the config file name inside the fclaude home directory.
const FileName = "config.json"

// Config is the Windows host configuration. JSON field names are the stable
// on-disk contract; keep them when renaming Go fields.
type Config struct {
	// ShareRoot is the Windows directory workspaces must live under, e.g. C:\dev.
	// A workspace C:\dev\a\b is mounted in the VM at VMMountBase/a/b.
	ShareRoot string `json:"share_root"`
	// VMMountBase is the VM directory ShareRoot maps to.
	VMMountBase string `json:"vm_mount_base"`

	VMName    string `json:"vm_name"`
	VMUser    string `json:"vm_user"`
	MaintUser string `json:"maint_user"`
	// Network facts (VM/host addresses, subnets, MTU) are not configuration:
	// VMware assigns the subnets, so setup discovers them and records them in
	// vm.json (package install).

	MemGiB  int `json:"mem_gib"`
	VCPU    int `json:"vcpu"`
	DiskGiB int `json:"disk_gib"`

	// UpdateApt enables the weekly in-VM apt upgrade timer.
	UpdateApt bool `json:"update_apt"`
	// UpdateWarnDays: warn at session start if the last successful update is older.
	UpdateWarnDays int `json:"update_warn_days"`

	// LocalNodeModules gives a workspace with a package.json a VM-local
	// node_modules (bind-mounted over the shared one): npm needs symlinks,
	// which shared folders lack, and native modules are Linux builds anyway.
	// The Windows side then sees an empty node_modules.
	LocalNodeModules bool `json:"local_node_modules"`
}

// Defaults returns the default configuration. ShareRoot defaults to C:\dev;
// the installer asks for it.
func Defaults() Config {
	return Config{
		ShareRoot:        `C:\dev`,
		VMMountBase:      "/home/fclaude",
		VMName:           "fclaude",
		VMUser:           "fclaude",
		MaintUser:        "maint",
		MemGiB:           8,
		VCPU:             6,
		DiskGiB:          32,
		UpdateApt:        true,
		UpdateWarnDays:   15,
		LocalNodeModules: true,
	}
}

// Validate reports every problem with c at once, so a hand-edited file can be
// fixed in one pass.
func (c Config) Validate() error {
	var errs []error
	add := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }

	if !winpath.IsAbs(c.ShareRoot) {
		add("share_root %q must be an absolute Windows path like C:\\dev", c.ShareRoot)
	} else if winpath.IsDriveRoot(c.ShareRoot) {
		add("share_root %q must not be a drive root", c.ShareRoot)
	}
	if !strings.HasPrefix(c.VMMountBase, "/") || c.VMMountBase == "/" || strings.HasSuffix(c.VMMountBase, "/") {
		add("vm_mount_base %q must be an absolute VM path without a trailing slash", c.VMMountBase)
	}
	for name, v := range map[string]string{"vm_name": c.VMName, "vm_user": c.VMUser, "maint_user": c.MaintUser} {
		if !isSimpleName(v) {
			add("%s %q must be non-empty and contain only [a-z0-9_-]", name, v)
		}
	}
	if c.MemGiB < 2 {
		add("mem_gib %d must be at least 2", c.MemGiB)
	}
	if c.VCPU < 1 {
		add("vcpu %d must be at least 1", c.VCPU)
	}
	if c.DiskGiB < 16 {
		add("disk_gib %d must be at least 16", c.DiskGiB)
	}
	if c.UpdateWarnDays < 1 {
		add("update_warn_days %d must be at least 1", c.UpdateWarnDays)
	}
	return errors.Join(errs...)
}

func isSimpleName(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' && r != '-' {
			return false
		}
	}
	return true
}

// Parse decodes a config document over the defaults (so keys missing from an
// older file get today's defaults), rejects unknown keys (typos), and validates.
func Parse(data []byte) (Config, error) {
	c := Defaults()
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return Config{}, fmt.Errorf("parse config: %w", err)
	}
	if dec.More() {
		return Config{}, errors.New("parse config: trailing data after the JSON object")
	}
	c.ShareRoot = winpath.Clean(c.ShareRoot)
	if err := c.Validate(); err != nil {
		return Config{}, fmt.Errorf("invalid config: %w", err)
	}
	return c, nil
}

// Load reads and parses the config file at path.
func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Config{}, fmt.Errorf("config not found at %s — run fclaude-setup first", path)
		}
		return Config{}, err
	}
	return Parse(data)
}

// Marshal renders c as indented JSON with a trailing newline.
func (c Config) Marshal() ([]byte, error) {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// WriteNoClobber writes c to path, or to path+".new" if path already exists.
// It returns the path actually written. The existing file is never modified.
func WriteNoClobber(path string, c Config) (string, error) {
	if err := c.Validate(); err != nil {
		return "", err
	}
	data, err := c.Marshal()
	if err != nil {
		return "", err
	}
	target := path
	if _, err := os.Stat(path); err == nil {
		target = path + ".new"
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return "", err
	}
	return target, writeFileAtomic(target, data, 0o600)
}

// writeFileAtomic writes via a temp file + rename so a crash never leaves a
// truncated config behind.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }() // no-op after a successful rename
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, perm); err != nil {
		return err
	}
	return os.Rename(name, path)
}
