package seed

import (
	"bytes"
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func params() Params {
	return Params{
		VMName: "fclaude", VMUser: "fclaude", VMUID: 1000, MaintUser: "maint",
		PubKey: "ssh-ed25519 AAAAC3Nza fclaude", MaintPubKey: "ssh-ed25519 AAAAC3Nzb maint",
		AnsibleDir: "/var/lib/fclaude/ansible", UpdateApt: true, MTU: MTU,
		MACNAT: "52:54:00:FC:1A:DF", MACHostOnly: "52:54:00:fc:1a:e0",
		VMIP: "192.168.56.10", HostOnlyPrefix: 24,
	}
}

func TestFilesRender(t *testing.T) {
	files, err := Files(params())
	if err != nil {
		t.Fatal(err)
	}
	ud := string(files["user-data"])
	if !strings.HasPrefix(ud, "#cloud-config\n") {
		t.Error("user-data must start with #cloud-config")
	}
	for _, want := range []string{"'fclaude_platform=vmware'", "'host_user='", "'fclaude_update_apt=1'",
		`"ssh-ed25519 AAAAC3Nza fclaude"`, "uid: 1000"} {
		if !strings.Contains(ud, want) {
			t.Errorf("user-data missing %q", want)
		}
	}
	nc := string(files["network-config"])
	for _, want := range []string{`"52:54:00:fc:1a:df"`, `"52:54:00:fc:1a:e0"`, `"192.168.56.10/24"`, "mtu: 1500"} {
		if !strings.Contains(nc, want) {
			t.Errorf("network-config missing %q", want)
		}
	}
	for name, b := range files {
		var doc any
		if err := yaml.Unmarshal(b, &doc); err != nil {
			t.Errorf("%s is not valid YAML: %v", name, err)
		}
		if regexp.MustCompile(`__[A-Z][A-Z0-9_]*__`).Match(b) {
			t.Errorf("%s has unrendered placeholders", name)
		}
	}
}

func TestEmbeddedPlaybookCarriesUpdateAssets(t *testing.T) {
	files, err := Files(params())
	if err != nil {
		t.Fatal(err)
	}
	var ud struct {
		WriteFiles []struct {
			Path    string `yaml:"path"`
			Content string `yaml:"content"`
		} `yaml:"write_files"`
	}
	if err := yaml.Unmarshal(files["user-data"], &ud); err != nil {
		t.Fatal(err)
	}
	if len(ud.WriteFiles) != 1 || ud.WriteFiles[0].Path != "/var/lib/fclaude/ansible/site.yml" {
		t.Fatalf("write_files: %+v", ud.WriteFiles)
	}
	site, err := base64.StdEncoding.DecodeString(ud.WriteFiles[0].Content)
	if err != nil {
		t.Fatal(err)
	}
	var plays []map[string]any
	if err := yaml.Unmarshal(site, &plays); err != nil {
		t.Fatalf("embedded playbook not YAML: %v", err)
	}
	if bytes.Contains(site, []byte("__APT_UPGRADE")) {
		t.Fatal("update assets not injected into the playbook")
	}
	if !bytes.Contains(site, []byte("fclaude_platform")) {
		t.Fatal("playbook lacks the VirtualBox-gated tasks")
	}
}

func TestFilesRejectsUnsafeParams(t *testing.T) {
	p := params()
	p.PubKey = "ssh-ed25519 AAA\"\n- injected"
	if _, err := Files(p); err == nil {
		t.Fatal("quote/newline in a value must be rejected")
	}
	p = params()
	p.VMIP = ""
	p.MTU = 0
	if _, err := Files(p); err == nil {
		t.Fatal("empty/zero values must be rejected")
	}
}

func TestRenderDetectsLeftovers(t *testing.T) {
	if _, err := render("t", "a __X__ b __Y__", map[string]string{"X": "1"}); err == nil ||
		!strings.Contains(err.Error(), "__Y__") {
		t.Fatalf("got %v", err)
	}
	if out, err := render("t", "a __X__", map[string]string{"X": "1"}); err != nil || out != "a 1" {
		t.Fatalf("got %q %v", out, err)
	}
}

func TestWriteISORoundTrip(t *testing.T) {
	files, err := Files(params())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "seed.iso")
	if err := os.WriteFile(path, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteISO(path, files); err != nil {
		t.Fatal(err)
	}
	// VirtualBox rejects a DVD image that is not whole sectors; the file must
	// end exactly where the volume does.
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Size()%2048 != 0 || st.Size() > 1<<20 {
		t.Errorf("image size %d: not sector-aligned or untrimmed", st.Size())
	}

	// Independent check with the system ISO tools when present (CI installs
	// genisoimage): the label and the Rock Ridge names cloud-init looks for.
	isoinfo, err := exec.LookPath("isoinfo")
	if err != nil {
		t.Skip("isoinfo not installed; skipping independent ISO verification")
	}
	out, err := exec.Command(isoinfo, "-d", "-i", path).Output()
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`(?m)^Volume id: cidata\s*$`).Match(out) {
		t.Errorf("volume label wrong:\n%s", out)
	}
	if m := regexp.MustCompile(`(?m)^Volume size is: (\d+)`).FindSubmatch(out); m == nil || string(m[1]) != strconv.FormatInt(st.Size()/2048, 10) {
		t.Errorf("volume size %q != file size %d/2048", m, st.Size())
	}
	if !bytes.Contains(out, []byte("Rock Ridge signatures version")) {
		t.Errorf("no Rock Ridge:\n%s", out)
	}
	for name, want := range files {
		got, err := exec.Command(isoinfo, "-R", "-i", path, "-x", "/"+name).Output()
		if err != nil {
			t.Fatalf("extract %s: %v", name, err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s content differs after round trip", name)
		}
	}
}

func TestWriteISORejectsNested(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.iso")
	if err := WriteISO(path, map[string][]byte{"a/b": nil}); err == nil {
		t.Fatal("nested name accepted")
	}
	// The failed image must be closed and removed (an open handle also makes
	// the TempDir cleanup fail on Windows).
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("partial image left behind: %v", err)
	}
}

func TestReadAssetNormalizesCRLF(t *testing.T) {
	ud, err := readAsset("cloud-init/user-data.tmpl")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(ud, "\r") || !strings.HasPrefix(ud, "#cloud-config\n") {
		t.Fatal("asset not LF-normalized")
	}
}

func TestTrimToVolumeRejectsNonISO(t *testing.T) {
	dir := t.TempDir()
	short := filepath.Join(dir, "short")
	if err := os.WriteFile(short, []byte("tiny"), 0o600); err != nil {
		t.Fatal(err)
	}
	notISO := filepath.Join(dir, "zeros")
	if err := os.WriteFile(notISO, make([]byte, 40*2048), 0o600); err != nil {
		t.Fatal(err)
	}
	zeroVol := filepath.Join(dir, "zerovol")
	b := make([]byte, 40*2048)
	b[16*2048] = 1
	copy(b[16*2048+1:], "CD001") // valid signature, volume size 0
	if err := os.WriteFile(zeroVol, b, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{short, notISO, zeroVol, filepath.Join(dir, "missing")} {
		if err := trimToVolume(p); err == nil {
			t.Errorf("%s: no error", filepath.Base(p))
		}
	}
}
