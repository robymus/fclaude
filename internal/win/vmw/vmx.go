package vmw

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// VMX is an editable .vmx file. Parsing preserves every line it does not
// change — VMware writes its own keys back (uuid.bios, pciSlotNumber, ...) and
// those must survive our edits, or the VM's identity changes.
type VMX struct{ lines []line }

type line struct {
	key, val string // key == "" for comments, blank and unparsable lines
	raw      string
}

// ParseVMX parses .vmx content (LF or CRLF).
func ParseVMX(b []byte) (*VMX, error) {
	v := &VMX{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 64*1024), 16<<20)
	for sc.Scan() {
		raw := strings.TrimRight(sc.Text(), "\r")
		k, val, ok := parseLine(raw)
		if !ok {
			v.lines = append(v.lines, line{raw: raw})
			continue
		}
		v.lines = append(v.lines, line{key: k, val: val, raw: raw})
	}
	return v, sc.Err()
}

func parseLine(raw string) (key, val string, ok bool) {
	t := strings.TrimSpace(raw)
	if t == "" || strings.HasPrefix(t, "#") {
		return "", "", false
	}
	k, rest, found := strings.Cut(t, "=")
	if !found {
		return "", "", false
	}
	k, rest = strings.TrimSpace(k), strings.TrimSpace(rest)
	if k == "" || len(rest) < 2 || rest[0] != '"' || rest[len(rest)-1] != '"' {
		return "", "", false
	}
	return k, decodeValue(rest[1 : len(rest)-1]), true
}

// VMware escapes special characters in values as '|' + two hex digits.
func decodeValue(s string) string {
	if !strings.Contains(s, "|") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '|' && i+2 < len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+3], 16, 8); err == nil {
				b.WriteByte(byte(n))
				i += 2
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func encodeValue(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '"' || c == '|' || c < 0x20 {
			fmt.Fprintf(&b, "|%02X", c)
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}

// Get returns the value of key (case-insensitive, as VMware treats keys).
func (v *VMX) Get(key string) (string, bool) {
	for _, l := range v.lines {
		if l.key != "" && strings.EqualFold(l.key, key) {
			return l.val, true
		}
	}
	return "", false
}

// Set replaces key's value in place, or appends key.
func (v *VMX) Set(key, val string) {
	for i, l := range v.lines {
		if l.key != "" && strings.EqualFold(l.key, key) {
			v.lines[i] = line{key: l.key, val: val}
			return
		}
	}
	v.lines = append(v.lines, line{key: key, val: val})
}

// DeletePrefix removes every key starting with prefix (case-insensitive).
func (v *VMX) DeletePrefix(prefix string) {
	out := v.lines[:0]
	for _, l := range v.lines {
		if l.key != "" && len(l.key) >= len(prefix) && strings.EqualFold(l.key[:len(prefix)], prefix) {
			continue
		}
		out = append(out, l)
	}
	v.lines = out
}

// Bytes renders the file with CRLF line endings, as Workstation writes it.
// Unchanged lines are emitted byte-for-byte.
func (v *VMX) Bytes() []byte {
	var b bytes.Buffer
	for _, l := range v.lines {
		if l.raw != "" || l.key == "" {
			b.WriteString(l.raw)
		} else {
			fmt.Fprintf(&b, "%s = \"%s\"", l.key, encodeValue(l.val))
		}
		b.WriteString("\r\n")
	}
	return b.Bytes()
}

// ClearShares removes every shared folder. The cold-boot sweep calls this
// before starting a powered-off VM: shares persist in the .vmx
// (expiration "never"), and a crash must not carry one into the next boot.
func (v *VMX) ClearShares() {
	v.DeletePrefix("sharedFolder")
	v.Set("sharedFolder.maxNum", "0")
}

// DetachSeed disconnects the cloud-init seed ISO after provisioning, so a
// later boot can never be re-seeded.
func (v *VMX) DetachSeed() { v.Set(seedDev+".present", "FALSE") }

// WriteFile writes the .vmx atomically (temp file + rename) so a crash never
// leaves Workstation a half-written VM definition.
func (v *VMX) WriteFile(path string) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".vmx-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(v.Bytes()); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// ReadVMX parses the .vmx at path.
func ReadVMX(path string) (*VMX, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParseVMX(b)
}

// Spec describes the fclaude VM for NewVMX. Paths are relative to the .vmx
// directory or absolute.
type Spec struct {
	Name       string
	MemMiB     int
	CPUs       int
	Disk       string // .vmdk
	Seed       string // cloud-init NoCloud ISO
	ConsoleLog string // serial console capture (boot diagnostics)
	MACNAT     string // 00:50:56:xx:xx:xx, static range
	MACHost    string
}

const seedDev = "sata0:1"

// NewVMX builds the definition of a new VM.
//
// Hardening: no USB, sound or floppy; clipboard and drag-and-drop disabled.
// HGFS stays enabled — it carries the workspace shares. Two vmxnet3 NICs (NAT
// for internet, host-only for ssh) need the PCIe root ports declared.
// uuid.action=keep and msg.autoAnswer stop a headless start from blocking on
// the "moved or copied?" question if the directory is ever moved or restored.
func NewVMX(s Spec) (*VMX, error) {
	var errs []error
	if s.Name == "" || s.Disk == "" || s.Seed == "" {
		errs = append(errs, fmt.Errorf("vmw: incomplete VM spec %+v", s))
	}
	if s.MemMiB < 1024 || s.CPUs < 1 {
		errs = append(errs, fmt.Errorf("vmw: implausible resources mem=%dMiB cpus=%d", s.MemMiB, s.CPUs))
	}
	for _, m := range []string{s.MACNAT, s.MACHost} {
		if !ValidStaticMAC(m) {
			errs = append(errs, fmt.Errorf("vmw: %q is not a VMware static MAC (00:50:56:00:00:00-00:50:56:3F:FF:FF)", m))
		}
	}
	if s.MACNAT == s.MACHost {
		errs = append(errs, errors.New("vmw: the two NICs need different MACs"))
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	v := &VMX{}
	kv := func(k, val string) { v.lines = append(v.lines, line{key: k, val: val}) }
	kv(".encoding", "UTF-8")
	kv("config.version", "8")
	kv("virtualHW.version", "21")
	kv("displayName", s.Name)
	kv("guestOS", "ubuntu-64")
	kv("firmware", "bios")
	kv("memsize", strconv.Itoa(s.MemMiB))
	kv("numvcpus", strconv.Itoa(s.CPUs))
	kv("cpuid.coresPerSocket", strconv.Itoa(s.CPUs))
	kv("uuid.action", "keep")
	kv("msg.autoAnswer", "TRUE")
	kv("pciBridge0.present", "TRUE")
	for _, n := range []string{"4", "5"} {
		kv("pciBridge"+n+".present", "TRUE")
		kv("pciBridge"+n+".virtualDev", "pcieRootPort")
		kv("pciBridge"+n+".functions", "8")
	}
	kv("sata0.present", "TRUE")
	kv("sata0:0.present", "TRUE")
	kv("sata0:0.fileName", s.Disk)
	kv(seedDev+".present", "TRUE")
	kv(seedDev+".deviceType", "cdrom-image")
	kv(seedDev+".fileName", s.Seed)
	for i, n := range []struct{ conn, mac string }{{"nat", s.MACNAT}, {"hostonly", s.MACHost}} {
		p := "ethernet" + strconv.Itoa(i)
		kv(p+".present", "TRUE")
		kv(p+".connectionType", n.conn)
		kv(p+".virtualDev", "vmxnet3")
		kv(p+".addressType", "static")
		kv(p+".address", strings.ToLower(n.mac))
	}
	if s.ConsoleLog != "" {
		kv("serial0.present", "TRUE")
		kv("serial0.fileType", "file")
		kv("serial0.fileName", s.ConsoleLog)
	}
	kv("usb.present", "FALSE")
	kv("sound.present", "FALSE")
	kv("floppy0.present", "FALSE")
	kv("isolation.tools.copy.disable", "TRUE")
	kv("isolation.tools.paste.disable", "TRUE")
	kv("isolation.tools.dnd.disable", "TRUE")
	kv("isolation.tools.hgfs.disable", "FALSE")
	kv("tools.syncTime", "TRUE")
	kv("sharedFolder.maxNum", "0")
	return v, nil
}

// ValidStaticMAC reports whether mac is in VMware's static range
// 00:50:56:00:00:00–00:50:56:3F:FF:FF (Workstation rejects others).
func ValidStaticMAC(mac string) bool {
	hw, err := net.ParseMAC(mac)
	return err == nil && len(hw) == 6 && hw[0] == 0x00 && hw[1] == 0x50 && hw[2] == 0x56 && hw[3] <= 0x3f
}
