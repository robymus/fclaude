package vm

import (
	"io/fs"
	"testing"
)

// Every embedded asset must be present and non-empty: a missing or emptied
// file would only surface as a broken first boot on Windows.
func TestEmbeddedAssets(t *testing.T) {
	want := []string{
		"ansible/site.yml",
		"cloud-init/user-data.tmpl", "cloud-init/meta-data.tmpl", "cloud-init/network-config-vbox.tmpl",
		"update/fclaude-apt-upgrade.sh", "update/fclaude-apt-upgrade.service", "update/fclaude-apt-upgrade.timer",
	}
	for _, name := range want {
		b, err := fs.ReadFile(Files, name)
		if err != nil || len(b) == 0 {
			t.Errorf("%s: missing or empty (%v)", name, err)
		}
	}
}
