package vbox

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

// VMSpec describes the fclaude VM for DefineCommands.
type VMSpec struct {
	Name       string
	UUID       string
	BaseFolder string // where VirtualBox keeps the .vbox file
	MemMiB     int
	CPUs       int
	DiskPath   string // VDI
	SeedPath   string // cloud-init NoCloud ISO
	ConsoleLog string // serial console capture (boot diagnostics)
	MACNAT     string // aa:bb:cc:dd:ee:ff
	MACHost    string
	HostOnlyIf string // e.g. "VirtualBox Host-Only Ethernet Adapter"
}

// vboxMAC converts aa:bb:cc:dd:ee:ff to the 12-hex-digit form VBoxManage wants.
func vboxMAC(mac string) (string, error) {
	hw, err := net.ParseMAC(mac)
	if err != nil || len(hw) != 6 {
		return "", fmt.Errorf("invalid MAC %q", mac)
	}
	return strings.ToUpper(strings.ReplaceAll(hw.String(), ":", "")), nil
}

// DefineCommands returns the VBoxManage invocations that register and fully
// configure the VM. They are idempotent only as a whole on a VM that does not
// exist yet; the installer checks State() == StateNotFound first.
//
// Hardening: no audio, USB, clipboard or drag-and-drop between host and VM —
// the VM is reached only over ssh and shared folders. NAT keeps
// localhost-reachable off so the guest cannot reach host loopback services.
// Flag spellings follow VirtualBox 7.1 and must be confirmed on a real install.
func DefineCommands(s VMSpec) ([][]string, error) {
	if s.Name == "" || s.UUID == "" || s.DiskPath == "" || s.SeedPath == "" || s.HostOnlyIf == "" {
		return nil, fmt.Errorf("vbox: incomplete VM spec %+v", s)
	}
	if s.MemMiB < 1024 || s.CPUs < 1 {
		return nil, fmt.Errorf("vbox: implausible resources mem=%dMiB cpus=%d", s.MemMiB, s.CPUs)
	}
	nat, err := vboxMAC(s.MACNAT)
	if err != nil {
		return nil, err
	}
	host, err := vboxMAC(s.MACHost)
	if err != nil {
		return nil, err
	}
	create := []string{"createvm", "--name", s.Name, "--uuid", s.UUID, "--ostype", "Ubuntu_64", "--register"}
	if s.BaseFolder != "" {
		create = append(create, "--basefolder", s.BaseFolder)
	}
	modify := []string{
		"modifyvm", s.Name,
		"--memory", strconv.Itoa(s.MemMiB),
		"--cpus", strconv.Itoa(s.CPUs),
		"--firmware", "bios",
		"--ioapic", "on",
		"--rtc-use-utc", "on",
		"--graphicscontroller", "vmsvga", "--vram", "16",
		"--audio-enabled", "off",
		"--usb-ohci", "off", "--usb-ehci", "off", "--usb-xhci", "off",
		"--clipboard-mode", "disabled",
		"--drag-and-drop", "disabled",
		"--boot1", "disk", "--boot2", "dvd", "--boot3", "none", "--boot4", "none",
		"--nic1", "nat", "--nictype1", "virtio", "--macaddress1", nat,
		"--nat-localhostreachable1", "off",
		"--nic2", "hostonly", "--nictype2", "virtio", "--macaddress2", host,
		"--hostonlyadapter2", s.HostOnlyIf,
	}
	if s.ConsoleLog != "" {
		// Ubuntu cloud images log to ttyS0; capture it for boot diagnostics.
		modify = append(modify, "--uart1", "0x3F8", "4", "--uart-mode1", "file", s.ConsoleLog)
	}
	return [][]string{
		create,
		modify,
		{"storagectl", s.Name, "--name", "SATA", "--add", "sata", "--controller", "IntelAhci",
			"--portcount", "2", "--hostiocache", "off"},
		{"storageattach", s.Name, "--storagectl", "SATA", "--port", "0", "--device", "0",
			"--type", "hdd", "--medium", s.DiskPath, "--nonrotational", "on", "--discard", "on"},
		{"storageattach", s.Name, "--storagectl", "SATA", "--port", "1", "--device", "0",
			"--type", "dvddrive", "--medium", s.SeedPath},
	}, nil
}

// ImportDiskCommands converts the downloaded cloud-image VMDK into the VM's
// VDI and grows it to sizeMiB (the guest grows its partition on first boot).
//
// clonemedium registers the source in the global media registry, and the
// Ubuntu VMDK carries an all-zero UUID, so a second import from the same file
// fails ("UUID ... does not match the value stored in the media registry").
// The source is therefore closed before (a stale entry from an interrupted
// run) and after the clone; closemedium on an unregistered file succeeds.
func ImportDiskCommands(vmdk, vdi string, sizeMiB int) [][]string {
	return [][]string{
		{"closemedium", "disk", vmdk},
		{"clonemedium", "disk", vmdk, vdi, "--format", "VDI"},
		{"closemedium", "disk", vmdk},
		{"modifymedium", "disk", vdi, "--resize", strconv.Itoa(sizeMiB)},
	}
}
