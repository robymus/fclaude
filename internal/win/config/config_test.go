package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultsAreValid(t *testing.T) {
	if err := Defaults().Validate(); err != nil {
		t.Fatalf("defaults invalid: %v", err)
	}
}

func TestParseOverlaysDefaults(t *testing.T) {
	c, err := Parse([]byte(`{"share_root": "d:/work/", "mem_gib": 12}`))
	if err != nil {
		t.Fatal(err)
	}
	if c.ShareRoot != `D:\work` {
		t.Errorf("share_root not cleaned: %q", c.ShareRoot)
	}
	if c.MemGiB != 12 || c.VCPU != Defaults().VCPU {
		t.Errorf("overlay wrong: mem=%d vcpu=%d", c.MemGiB, c.VCPU)
	}
}

func TestParseRejects(t *testing.T) {
	cases := map[string]string{
		`{"share_rot": "C:\\dev"}`:                    "unknown field",
		`{"share_root": "dev"}`:                       "share_root",
		`{"share_root": "C:\\"}`:                      "drive root",
		`{"vm_ip": "192.168.57.10"}`:                  "same /24",
		`{"vm_ip": "192.168.56.1"}`:                   "distinct",
		`{"vm_name": "Fclaude VM"}`:                   "vm_name",
		`{"vm_mount_base": "/home/fclaude/"}`:         "vm_mount_base",
		`{"mem_gib": 1, "vcpu": 0}`:                   "vcpu",
		`{} {}`:                                       "trailing",
		`{"hostonly_prefix": 31}`:                     "hostonly_prefix",
		`{"vm_ip": "fe80::1"}`:                        "IPv4",
		`{"disk_gib": 8, "vm_mtu": 100, "mem_gib":1}`: "disk_gib",
	}
	for in, want := range cases {
		_, err := Parse([]byte(in))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Parse(%s): error %v, want it to mention %q", in, err, want)
		}
	}
}

func TestValidateReportsAllProblems(t *testing.T) {
	c := Defaults()
	c.MemGiB, c.VCPU = 0, 0
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "mem_gib") || !strings.Contains(err.Error(), "vcpu") {
		t.Fatalf("want both problems reported, got %v", err)
	}
}

func TestWriteNoClobber(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", FileName)

	got, err := WriteNoClobber(path, Defaults())
	if err != nil || got != path {
		t.Fatalf("first write: (%q, %v)", got, err)
	}
	orig, _ := os.ReadFile(path)

	c := Defaults()
	c.MemGiB = 16
	got, err = WriteNoClobber(path, c)
	if err != nil || got != path+".new" {
		t.Fatalf("second write should go to .new: (%q, %v)", got, err)
	}
	now, _ := os.ReadFile(path)
	if string(now) != string(orig) {
		t.Fatal("existing config was modified")
	}
	loaded, err := Load(path + ".new")
	if err != nil || loaded.MemGiB != 16 {
		t.Fatalf("round trip: %+v, %v", loaded, err)
	}
	if entries, _ := os.ReadDir(filepath.Dir(path)); len(entries) != 2 {
		t.Fatalf("temp files left behind: %v", entries)
	}
}

func TestWriteNoClobberValidates(t *testing.T) {
	c := Defaults()
	c.ShareRoot = "relative"
	if _, err := WriteNoClobber(filepath.Join(t.TempDir(), FileName), c); err == nil {
		t.Fatal("invalid config must not be written")
	}
}

func TestLoadMissing(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), FileName))
	if err == nil || !strings.Contains(err.Error(), "fclaude-setup") {
		t.Fatalf("got %v", err)
	}
}
