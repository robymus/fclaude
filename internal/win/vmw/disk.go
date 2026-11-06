package vmw

import (
	"fmt"
	"io"
)

// ImportDiskArgs converts the downloaded cloud-image VMDK (streamOptimized,
// which Workstation will not boot) to a monolithic sparse VMDK and grows it to
// sizeGB. The guest grows its partition on first boot. Each element is one
// vmware-vdiskmanager invocation.
func ImportDiskArgs(src, dst string, sizeGB int) [][]string {
	return [][]string{
		{"-r", src, "-t", "0", dst},
		GrowDiskArgs(dst, sizeGB),
	}
}

// GrowDiskArgs grows disk to sizeGB. Workstation can only do this while the VM
// is powered off; the guest then needs growpart + resize2fs.
func GrowDiskArgs(disk string, sizeGB int) []string {
	return []string{"-x", fmt.Sprintf("%dGB", sizeGB), disk}
}

// RandomMAC returns a random address in VMware's static range
// (00:50:56:00:00:00–00:50:56:3F:FF:FF), reading entropy from r.
func RandomMAC(r io.Reader) (string, error) {
	var b [3]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		return "", err
	}
	return fmt.Sprintf("00:50:56:%02x:%02x:%02x", b[0]&0x3f, b[1], b[2]), nil
}
