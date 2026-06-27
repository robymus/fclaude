#!/usr/bin/env bash
# fclaude — host uninstaller (plan §12).
#
# Reverses everything install.sh did. Asks for heavy confirmation. NEVER deletes
# the VM disk silently — that is a separate, explicit, opt-in final prompt.
set -euo pipefail

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
# shellcheck source=lib/constants.sh
source "${REPO_DIR}/lib/constants.sh"

info() { printf '\033[1;34m[uninstall]\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m[uninstall] WARN:\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[1;31m[uninstall] ERROR:\033[0m %s\n' "$*" >&2; exit 1; }
step() { printf '\n\033[1;32m==>\033[0m %s\n' "$*"; }

VIRSH() { sudo virsh "$@"; }

# --- Confirmations ----------------------------------------------------------
cat <<EOF
This will REMOVE the fclaude VM and all host integration:
  - the libvirt domain '$FCLAUDE_VM_NAME' and its DHCP reservation
  - NFS exports, firewall rules, sudoers drop-in, systemd units
  - wrapper scripts in $FCLAUDE_BIN_DIR and the library in $FCLAUDE_LIB_DIR

The VM disk ($FCLAUDE_DISK) and your config/keys are KEPT unless you opt in below.
EOF
read -rp "Have you backed up the VM disk and any data inside the VM? [y/N] " a
[[ "$a" == [yY] ]] || die "back up first, then re-run."
read -rp "Type 'remove fclaude' to proceed: " a
[[ "$a" == "remove fclaude" ]] || die "confirmation phrase did not match; aborting."

# --- VM ---------------------------------------------------------------------
step "Removing the VM"
if VIRSH dominfo "$FCLAUDE_VM_NAME" >/dev/null 2>&1; then
  VIRSH destroy "$FCLAUDE_VM_NAME" >/dev/null 2>&1 || true
  VIRSH undefine "$FCLAUDE_VM_NAME" --nvram >/dev/null 2>&1 \
    || VIRSH undefine "$FCLAUDE_VM_NAME" >/dev/null 2>&1 || true
  info "domain removed."
else
  info "domain not defined; skipping."
fi
VIRSH net-update "$FCLAUDE_LIBVIRT_NET" delete ip-dhcp-host \
  "<host mac='${FCLAUDE_VM_MAC}'/>" --live --config >/dev/null 2>&1 \
  && info "DHCP reservation removed." || true

# Restore qemu.conf if we appended the managed block.
QEMU_CONF=/etc/libvirt/qemu.conf
if [[ -f "$QEMU_CONF" ]] && sudo grep -q '>>> fclaude managed >>>' "$QEMU_CONF"; then
  sudo sed -i '/# >>> fclaude managed >>>/,/# <<< fclaude managed <<</d' "$QEMU_CONF"
  for u in virtqemud.service libvirtd.service; do sudo systemctl try-restart "$u" 2>/dev/null || true; done
  info "qemu.conf managed block removed."
fi

# --- NFS --------------------------------------------------------------------
step "Removing NFS configuration"
sudo rm -f /etc/exports.d/fclaude.exports /etc/nfs.conf.d/fclaude.conf
sudo exportfs -ra 2>/dev/null || true
# Best-effort: drop any leftover per-session bind mounts under the export root.
if mountpoint -q "$FCLAUDE_EXPORTS_ROOT" 2>/dev/null; then :; fi
while read -r mp; do
  [[ "$mp" == "$FCLAUDE_EXPORTS_ROOT"* ]] && sudo umount -l "$mp" 2>/dev/null || true
done < <(awk '{print $2}' /proc/mounts | grep -F "$FCLAUDE_EXPORTS_ROOT" || true)
sudo systemctl restart nfs-server 2>/dev/null || true
info "NFS exports removed."

# --- Firewall ---------------------------------------------------------------
step "Removing firewall rules"
sudo nft delete table inet fclaude 2>/dev/null && info "nftables table removed." || info "no nftables table present."
sudo rm -f /etc/fclaude/fclaude.nft
sudo rmdir /etc/fclaude 2>/dev/null || true

# Remove ufw rules if we added them (bridge name recorded in ~/.fclaude/vm).
BRIDGE=""
[[ -r "$FCLAUDE_VM_INFO" ]] && BRIDGE="$(awk -F'\"' '/^BRIDGE=/{print $2}' "$FCLAUDE_VM_INFO")"
BRIDGE="${BRIDGE:-virbr0}"
if command -v ufw >/dev/null 2>&1 && sudo ufw status 2>/dev/null | grep -qi '^Status: active'; then
  for proto in tcp udp; do
    sudo ufw delete allow in on "$BRIDGE" proto "$proto" \
      from "$FCLAUDE_SUBNET_CIDR" to "$FCLAUDE_HOST_IP" port 1024:65535 >/dev/null 2>&1 || true
  done
  sudo ufw reload >/dev/null 2>&1 || true
  info "ufw rules removed."
fi

# --- sudoers ----------------------------------------------------------------
step "Removing sudoers drop-in"
sudo rm -f /etc/sudoers.d/fclaude && info "sudoers drop-in removed."

# --- systemd ----------------------------------------------------------------
step "Removing systemd units"
sudo systemctl disable --now fclaude-firewall.service fclaude-exports.service >/dev/null 2>&1 || true
sudo rm -f /etc/systemd/system/fclaude-firewall.service /etc/systemd/system/fclaude-exports.service
sudo rm -f /etc/systemd/system/nfs-server.service.d/fclaude.conf
sudo rmdir /etc/systemd/system/nfs-server.service.d 2>/dev/null || true
sudo systemctl daemon-reload
info "systemd units removed."

# --- Wrappers ---------------------------------------------------------------
step "Removing wrappers + library"
rm -f "${FCLAUDE_BIN_DIR}/fclaude" "${FCLAUDE_BIN_DIR}/fclaude-vscode" "${FCLAUDE_BIN_DIR}/fclaude-grow-disk"
rm -rf "$FCLAUDE_LIB_DIR"
info "wrappers removed."

# --- Optional: state, config, keys, disk ------------------------------------
step "Optional cleanup of ~/.fclaude"
echo "Group membership (libvirt, kvm) was added at install; leaving it in place."
read -rp "Remove config, VM info, ssh keys, refcounts, seed (KEEPS the disk)? [y/N] " a
if [[ "$a" == [yY] ]]; then
  rm -f "$FCLAUDE_CONFIG" "${FCLAUDE_CONFIG}.new" "$FCLAUDE_VM_INFO" "$FCLAUDE_SEED"
  rm -rf "$FCLAUDE_SSH_DIR" "$FCLAUDE_REFCOUNT_DIR" "$FCLAUDE_EXPORTS_ROOT"
  info "config/keys/state removed (disk kept)."
fi

echo
warn "The VM disk is intentionally preserved: $FCLAUDE_DISK"
read -rp "Type 'DELETE DISK' to permanently delete the VM disk, or press Enter to keep it: " a
if [[ "$a" == "DELETE DISK" ]]; then
  rm -f "$FCLAUDE_DISK"
  info "disk deleted."
  rmdir "$FCLAUDE_HOME" 2>/dev/null || true
else
  info "disk kept at $FCLAUDE_DISK"
fi

step "fclaude uninstalled."
