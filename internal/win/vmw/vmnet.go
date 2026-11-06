package vmw

import (
	"errors"
	"fmt"
	"net"
)

// Networks are the VMware virtual networks the VM uses. The VMware installer
// picks both subnets; they are read from the registry, never assumed.
type Networks struct {
	HostOnly *net.IPNet // VMnet1: host ↔ VM (ssh)
	NAT      *net.IPNet // VMnet8: internet
}

// ErrNoNetworks means the VMnet configuration could not be read.
var ErrNoNetworks = errors.New("VMware virtual networks VMnet1/VMnet8 not configured")

// ReadNetworks reads VMnet1 and VMnet8 from
// HKLM\SOFTWARE\VMware, Inc.\VMnetLib\VMnetConfig (no admin needed).
func ReadNetworks() (Networks, error) {
	return readNetworks(registryVMnet)
}

func readNetworks(reg func(vmnet string) (addr, mask string, ok bool)) (Networks, error) {
	var n Networks
	for _, x := range []struct {
		name string
		dst  **net.IPNet
	}{{"vmnet1", &n.HostOnly}, {"vmnet8", &n.NAT}} {
		addr, mask, ok := reg(x.name)
		if !ok {
			return Networks{}, fmt.Errorf("%w: %s missing", ErrNoNetworks, x.name)
		}
		sub, err := ParseSubnet(addr, mask)
		if err != nil {
			return Networks{}, fmt.Errorf("%s: %w", x.name, err)
		}
		*x.dst = sub
	}
	return n, nil
}

// ParseSubnet builds an IPv4 subnet from VMware's IPSubnetAddress and
// IPSubnetMask values. Workstation 26 stores only the address; the mask then
// defaults to /24, VMware's only default.
func ParseSubnet(addr, mask string) (*net.IPNet, error) {
	ip := net.ParseIP(addr).To4()
	if ip == nil {
		return nil, fmt.Errorf("vmw: invalid subnet address %q", addr)
	}
	m := net.CIDRMask(24, 32)
	if mask != "" {
		mi := net.ParseIP(mask).To4()
		if mi == nil {
			return nil, fmt.Errorf("vmw: invalid subnet mask %q", mask)
		}
		m = net.IPMask(mi)
		if ones, bits := m.Size(); bits == 0 || ones > 28 {
			return nil, fmt.Errorf("vmw: unusable subnet mask %q", mask)
		}
	}
	return &net.IPNet{IP: ip.Mask(m), Mask: m}, nil
}

// HostIP is the Windows host's address on a VMnet subnet (.1).
func HostIP(n *net.IPNet) net.IP { return nth(n, 1) }

// VMIP is the VM's static host-only address (.10). VMware's DHCP hands out
// .128 and up, so .10 never collides.
func VMIP(n *net.IPNet) net.IP { return nth(n, 10) }

// Prefix is the subnet's prefix length (24 for VMware's default).
func Prefix(n *net.IPNet) int {
	ones, _ := n.Mask.Size()
	return ones
}

func nth(n *net.IPNet, i byte) net.IP {
	ip := make(net.IP, 4)
	copy(ip, n.IP.To4())
	ip[3] += i
	return ip
}
