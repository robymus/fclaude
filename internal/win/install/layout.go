// Package install implements fclaude-setup: the interactive installer, the
// elevated firewall step, uninstall and grow-disk (PLAN §4).
//
// Every step is idempotent and disk-guarded, mirroring install.sh: an existing
// disk is a restore (never overwritten), an existing config.json is never
// rewritten (a .new is written beside it), and nothing that is already in
// place is redone.
package install

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Layout is every path under the fclaude home (%USERPROFILE%\.fclaude).
type Layout struct{ Home string }

// Config is config.json (user settings).
func (l Layout) Config() string { return filepath.Join(l.Home, "config.json") }

// VMInfo is vm.json (facts setup discovered or generated).
func (l Layout) VMInfo() string { return filepath.Join(l.Home, "vm.json") }

// SSHDir holds the keypairs and the host-key pin.
func (l Layout) SSHDir() string { return filepath.Join(l.Home, "ssh") }

// Key is the private key of a VM account (fclaude or maint).
func (l Layout) Key(user string) string { return filepath.Join(l.SSHDir(), user) }

// KnownHosts is the pinned VM host key.
func (l Layout) KnownHosts() string { return filepath.Join(l.SSHDir(), "known_hosts") }

// Leases is the lease store directory.
func (l Layout) Leases() string { return filepath.Join(l.Home, "leases") }

// Bin holds the installed executables (on the user PATH).
func (l Layout) Bin() string { return filepath.Join(l.Home, "bin") }

// Downloads caches the cloud image between attempts.
func (l Layout) Downloads() string { return filepath.Join(l.Home, "downloads") }

// VMDir holds the VM: .vmx, disk, seed, console log.
func (l Layout) VMDir() string { return filepath.Join(l.Home, "vm") }

// VMX is the VM definition.
func (l Layout) VMX() string { return filepath.Join(l.VMDir(), "fclaude.vmx") }

// Disk is the VM disk — the one file never deleted without a typed confirmation.
func (l Layout) Disk() string { return filepath.Join(l.VMDir(), DiskName) }

// Seed is the cloud-init NoCloud ISO.
func (l Layout) Seed() string { return filepath.Join(l.VMDir(), SeedName) }

// File names inside VMDir, as the .vmx references them (relative).
const (
	DiskName    = "fclaude-disk.vmdk"
	SeedName    = "seed.iso"
	ConsoleName = "console.log"
)

// Dirs are created by the dirs step.
func (l Layout) Dirs() []string {
	return []string{l.Home, l.SSHDir(), l.Leases(), l.Bin(), l.Downloads(), l.VMDir()}
}

// VMInfo is vm.json: what setup discovered (VMware networks) or generated
// (MACs) for this VM. Unlike config.json it is rewritten freely — the user
// never edits it.
type VMInfo struct {
	VMX            string `json:"vmx"`
	VMIP           string `json:"vm_ip"`
	HostIP         string `json:"host_ip"`
	HostOnlyPrefix int    `json:"hostonly_prefix"`
	HostOnlyNet    string `json:"hostonly_subnet"` // CIDR, for the firewall rules
	NATNet         string `json:"nat_subnet"`      // CIDR, for the firewall rules
	MACNAT         string `json:"mac_nat"`
	MACHostOnly    string `json:"mac_hostonly"`
	Provisioned    bool   `json:"provisioned"`
	SetupVersion   string `json:"setup_version"`
}

// SSHAddr is the VM's ssh endpoint.
func (v VMInfo) SSHAddr() string { return v.VMIP + ":22" }

// ReadVMInfo loads vm.json; os.ErrNotExist when setup has not created it yet.
func ReadVMInfo(path string) (VMInfo, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return VMInfo{}, err
	}
	var v VMInfo
	if err := json.Unmarshal(b, &v); err != nil {
		return VMInfo{}, fmt.Errorf("%s: %w", path, err)
	}
	if v.VMX == "" || v.VMIP == "" {
		return VMInfo{}, fmt.Errorf("%s: incomplete (re-run fclaude-setup)", path)
	}
	return v, nil
}

// WriteVMInfo writes vm.json atomically.
func WriteVMInfo(path string, v VMInfo) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(path, append(b, '\n'), 0o600)
}

func writeAtomic(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil && !errors.Is(err, errors.ErrUnsupported) {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
