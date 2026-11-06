// Package seed builds the cloud-init NoCloud seed for the VirtualBox VM.
//
// It renders the same templates install.sh renders on Linux (embedded from
// vm/, see vm/assets.go) with VirtualBox values, and packs them into an ISO
// 9660 image labelled "cidata" — the NoCloud datasource's well-known volume
// label. Windows has no genisoimage/cloud-localds, hence the Go writer.
package seed

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	diskfs "github.com/diskfs/go-diskfs"
	"github.com/diskfs/go-diskfs/disk"
	"github.com/diskfs/go-diskfs/filesystem"
	"github.com/diskfs/go-diskfs/filesystem/iso9660"

	"github.com/robymus/fclaude/vm"
)

// Params are the per-install values substituted into the templates.
type Params struct {
	VMName         string
	VMUser         string
	VMUID          int
	MaintUser      string
	PubKey         string // authorized_keys line for VMUser
	MaintPubKey    string // authorized_keys line for MaintUser
	AnsibleDir     string // VM path the playbook is written to
	UpdateApt      bool
	MTU            int
	MACNAT         string
	MACHostOnly    string
	VMIP           string
	HostOnlyPrefix int
}

// placeholder matches an unrendered __VAR__ token.
var placeholder = regexp.MustCompile(`__[A-Z][A-Z0-9_]*__`)

// render replaces every __KEY__ in tmpl and fails if any placeholder is left,
// so a template change that adds a variable cannot silently ship unrendered.
func render(name, tmpl string, vars map[string]string) (string, error) {
	keys := make([]string, 0, len(vars))
	for k := range vars {
		keys = append(keys, k)
	}
	sort.Strings(keys) // deterministic replacement order
	for _, k := range keys {
		tmpl = strings.ReplaceAll(tmpl, "__"+k+"__", vars[k])
	}
	if left := placeholder.FindAllString(tmpl, -1); len(left) > 0 {
		return "", fmt.Errorf("seed: %s has unrendered placeholders %v", name, left)
	}
	return tmpl, nil
}

func (p Params) validate() error {
	var errs []error
	for name, v := range map[string]string{
		"VMName": p.VMName, "VMUser": p.VMUser, "MaintUser": p.MaintUser, "PubKey": p.PubKey,
		"MaintPubKey": p.MaintPubKey, "AnsibleDir": p.AnsibleDir, "MACNAT": p.MACNAT,
		"MACHostOnly": p.MACHostOnly, "VMIP": p.VMIP,
	} {
		if v == "" {
			errs = append(errs, fmt.Errorf("seed: %s is empty", name))
		}
		// Values land inside YAML scalars and quoted strings; keep them single-line
		// and free of quotes so they cannot change the document's structure.
		if strings.ContainsAny(v, "\"'\n\r") {
			errs = append(errs, fmt.Errorf("seed: %s contains a quote or newline", name))
		}
	}
	if p.VMUID <= 0 || p.MTU <= 0 || p.HostOnlyPrefix <= 0 {
		errs = append(errs, errors.New("seed: VMUID, MTU and HostOnlyPrefix must be positive"))
	}
	return errors.Join(errs...)
}

// readAsset returns an embedded asset with LF line endings. .gitattributes
// keeps the repo LF, but a binary built from a checkout that converted them
// anyway must still emit a seed cloud-init accepts ("#cloud-config\r" is not
// recognized as cloud-config).
func readAsset(name string) (string, error) {
	b, err := fs.ReadFile(vm.Files, name)
	return strings.ReplaceAll(string(b), "\r\n", "\n"), err
}

// renderPlaybook injects the weekly-update assets into site.yml as base64,
// exactly as install.sh does, and returns the playbook base64-encoded.
func renderPlaybook() (string, error) {
	site, err := readAsset("ansible/site.yml")
	if err != nil {
		return "", err
	}
	vars := map[string]string{}
	for key, file := range map[string]string{
		"APT_UPGRADE_SH_B64":      "update/fclaude-apt-upgrade.sh",
		"APT_UPGRADE_SERVICE_B64": "update/fclaude-apt-upgrade.service",
		"APT_UPGRADE_TIMER_B64":   "update/fclaude-apt-upgrade.timer",
	} {
		b, err := readAsset(file)
		if err != nil {
			return "", err
		}
		vars[key] = base64.StdEncoding.EncodeToString([]byte(b))
	}
	out, err := render("ansible/site.yml", site, vars)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString([]byte(out)), nil
}

// Files renders the three NoCloud files: user-data, meta-data, network-config.
func Files(p Params) (map[string][]byte, error) {
	if err := p.validate(); err != nil {
		return nil, err
	}
	site, err := renderPlaybook()
	if err != nil {
		return nil, err
	}
	updateApt := "0"
	if p.UpdateApt {
		updateApt = "1"
	}
	vars := map[string]string{
		"FCLAUDE_VM_NAME":         p.VMName,
		"FCLAUDE_VM_USER":         p.VMUser,
		"FCLAUDE_VM_UID":          strconv.Itoa(p.VMUID),
		"FCLAUDE_MAINT_USER":      p.MaintUser,
		"FCLAUDE_PUBKEY":          p.PubKey,
		"MAINT_PUBKEY":            p.MaintPubKey,
		"FCLAUDE_VM_ANSIBLE_DIR":  p.AnsibleDir,
		"ANSIBLE_SITE_B64":        site,
		"FCLAUDE_UPDATE_APT":      updateApt,
		"FCLAUDE_PLATFORM":        "virtualbox",
		"USER":                    "", // no host-home bind mount: paths are mapped by the wrapper
		"FCLAUDE_VM_MTU":          strconv.Itoa(p.MTU),
		"FCLAUDE_VM_MAC_NAT":      strings.ToLower(p.MACNAT),
		"FCLAUDE_VM_MAC_HOSTONLY": strings.ToLower(p.MACHostOnly),
		"FCLAUDE_VM_IP":           p.VMIP,
		"FCLAUDE_HOSTONLY_PREFIX": strconv.Itoa(p.HostOnlyPrefix),
	}
	out := map[string][]byte{}
	for name, tmpl := range map[string]string{
		"user-data":      "cloud-init/user-data.tmpl",
		"meta-data":      "cloud-init/meta-data.tmpl",
		"network-config": "cloud-init/network-config-vbox.tmpl",
	} {
		src, err := readAsset(tmpl)
		if err != nil {
			return nil, err
		}
		r, err := render(tmpl, src, vars)
		if err != nil {
			return nil, err
		}
		out[name] = []byte(r + "\n") // install.sh's render() also appends a newline
	}
	return out, nil
}

// VolumeLabel is the NoCloud datasource label.
const VolumeLabel = "cidata"

// WriteISO writes files into a new ISO 9660 image at path (Rock Ridge, so the
// lower-case, hyphenated NoCloud names survive). An existing file is replaced.
func WriteISO(path string, files map[string][]byte) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	var size int64 = 1 << 20 // headroom for ISO structures
	for _, b := range files {
		size += int64(len(b)) + 2048
	}
	img, err := diskfs.Create(path, size, diskfs.SectorSizeDefault)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	// On any failure, release the image file (Windows cannot delete an open
	// file) and remove the partial image.
	done := false
	defer func() {
		if !done {
			_ = img.Close()
			_ = os.Remove(path)
		}
	}()
	img.LogicalBlocksize = 2048
	work, err := os.MkdirTemp("", "fclaude-seed")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(work) }()
	fsys, err := img.CreateFilesystem(disk.FilesystemSpec{
		Partition: 0, FSType: filesystem.TypeISO9660, VolumeLabel: VolumeLabel, WorkDir: work,
	})
	if err != nil {
		return err
	}
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if n != filepath.Base(n) {
			return fmt.Errorf("seed: nested file names are not supported: %q", n)
		}
		f, err := fsys.OpenFile("/"+n, os.O_CREATE|os.O_RDWR)
		if err != nil {
			return err
		}
		if _, err := f.Write(files[n]); err != nil {
			_ = f.Close()
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
	}
	iso, ok := fsys.(*iso9660.FileSystem)
	if !ok {
		return errors.New("seed: unexpected filesystem type")
	}
	if err := iso.Finalize(iso9660.FinalizeOptions{RockRidge: true, VolumeIdentifier: VolumeLabel}); err != nil {
		return err
	}
	done = true
	if err := img.Close(); err != nil {
		_ = os.Remove(path)
		return err
	}
	return nil
}
