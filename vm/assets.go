// Package vm exposes the guest provisioning assets (cloud-init templates, the
// Ansible playbook and the weekly apt-upgrade units) to Go code.
//
// The Windows host tooling embeds these exact files so the VirtualBox VM is
// provisioned from the same single source of truth as the Linux KVM VM; there
// is no copy to drift.
package vm

import "embed"

// Files holds the embedded provisioning assets, addressed by their path
// relative to the vm/ directory (e.g. "ansible/site.yml").
//
//go:embed ansible/site.yml
//go:embed cloud-init/user-data.tmpl cloud-init/meta-data.tmpl cloud-init/network-config-win.tmpl
//go:embed update/fclaude-apt-upgrade.sh update/fclaude-apt-upgrade.service update/fclaude-apt-upgrade.timer
var Files embed.FS
