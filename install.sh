#!/usr/bin/env bash
# fclaude — host installer (plan §12).
#
# Creates the sandbox VM and wires up the host: packages, disk, cloud-init seed,
# libvirt domain + NAT reservation, NFS sharing, firewall, sudoers, and the
# wrapper scripts. Safe to re-run (idempotent where practical).
set -euo pipefail

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
# shellcheck source=lib/constants.sh
source "${REPO_DIR}/lib/constants.sh"

# --- tiny logging -----------------------------------------------------------
info() { printf '\033[1;34m[install]\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m[install] WARN:\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[1;31m[install] ERROR:\033[0m %s\n' "$*" >&2; exit 1; }
step() { printf '\n\033[1;32m==>\033[0m %s\n' "$*"; }

VIRSH() { sudo virsh "$@"; }   # use system libvirt during install

# --- template rendering -----------------------------------------------------
# render <template-file> <output-file> : replace every __VAR__ with $VAR.
# Extra dynamic vars (pubkeys, base64 playbook, bridge, HOME) are exported below.
render() {
  local tmpl="$1" out="$2" content var
  content="$(cat "$tmpl")"
  for var in "${RENDER_VARS[@]}"; do
    content="${content//__${var}__/${!var}}"
  done
  printf '%s\n' "$content" >"$out"
}

# ===========================================================================
step "1/13  Pre-flight checks"
[[ "$(id -u)" -ne 0 ]] || die "run as your normal user (not root); the script uses sudo where needed"
[[ -e /dev/kvm ]] || die "/dev/kvm not present — KVM/virtualization unavailable"
command -v sudo >/dev/null || die "sudo is required"
grep -Eq '(vmx|svm)' /proc/cpuinfo || die "CPU virtualization (vmx/svm) not available"
avail_kib="$(df --output=avail -k "$HOME" | tail -1 | tr -d ' ')"
(( avail_kib > 35 * 1024 * 1024 )) || warn "less than ~35 GiB free in \$HOME; the 32 GiB disk may not fit"
info "host looks suitable for KVM."

# ===========================================================================
step "2/13  Install host packages"
PKGS=(qemu-system-x86 qemu-utils libvirt-daemon-system libvirt-clients
      genisoimage nftables nfs-kernel-server cloud-image-utils)
missing=()
for p in "${PKGS[@]}"; do dpkg -s "$p" >/dev/null 2>&1 || missing+=("$p"); done
if ((${#missing[@]})); then
  info "installing: ${missing[*]}"
  sudo apt-get update -qq
  sudo apt-get install -y "${missing[@]}"
else
  info "all host packages already present."
fi
sudo systemctl enable --now libvirtd >/dev/null 2>&1 || true
sudo systemctl enable --now nfs-server >/dev/null 2>&1 || true

# Group membership for password-less virsh in the wrappers (needs re-login).
RELOGIN=0
for g in libvirt kvm; do
  if ! id -nG "$USER" | tr ' ' '\n' | grep -qx "$g"; then
    sudo usermod -aG "$g" "$USER" && RELOGIN=1
    info "added $USER to group '$g'"
  fi
done

# ===========================================================================
step "3/13  Create state directories"
mkdir -p "$FCLAUDE_HOME" "$FCLAUDE_EXPORTS_ROOT" "$FCLAUDE_REFCOUNT_DIR" \
         "$FCLAUDE_SSH_DIR" "$FCLAUDE_BIN_DIR" "$FCLAUDE_LIB_DIR"
chmod 700 "$FCLAUDE_SSH_DIR"

# ===========================================================================
step "4/13  Virtual disk"
if [[ -e "$FCLAUDE_DISK" ]]; then
  warn "disk already exists: $FCLAUDE_DISK"
  read -rp "Is this a RESTORE from backup? Continue setup WITHOUT touching the disk? [y/N] " a
  [[ "$a" == [yY] ]] || die "refusing to overwrite existing disk. Delete it manually to start fresh."
  info "restore mode: keeping existing disk."
else
  tmpimg="$(mktemp --tmpdir fclaude-cloud-XXXXXX.img)"
  info "downloading Ubuntu 24.04 cloud image..."
  curl -fL --progress-bar -o "$tmpimg" "$FCLAUDE_CLOUD_IMG_URL"
  info "converting to raw and resizing to $FCLAUDE_DISK_SIZE..."
  qemu-img convert -O raw "$tmpimg" "$FCLAUDE_DISK"
  qemu-img resize -f raw "$FCLAUDE_DISK" "$FCLAUDE_DISK_SIZE"
  rm -f "$tmpimg"
  info "disk created: $FCLAUDE_DISK"
fi

# ===========================================================================
step "5/13  SSH keys"
[[ -f "$FCLAUDE_SSH_KEY" ]]      || ssh-keygen -t ed25519 -N '' -C fclaude      -f "$FCLAUDE_SSH_KEY"      >/dev/null
[[ -f "$FCLAUDE_SSH_KEY_MAINT" ]] || ssh-keygen -t ed25519 -N '' -C fclaude-maint -f "$FCLAUDE_SSH_KEY_MAINT" >/dev/null
FCLAUDE_PUBKEY="$(cat "${FCLAUDE_SSH_KEY}.pub")"
MAINT_PUBKEY="$(cat "${FCLAUDE_SSH_KEY_MAINT}.pub")"

# Export everything templates may reference.
export HOME FCLAUDE_PUBKEY MAINT_PUBKEY
ANSIBLE_SITE_B64="$(base64 -w0 "${REPO_DIR}/vm/ansible/site.yml")"
export ANSIBLE_SITE_B64
RENDER_VARS=(
  HOME FCLAUDE_DISK FCLAUDE_SEED FCLAUDE_HOME FCLAUDE_EXPORTS_ROOT FCLAUDE_REFCOUNT_DIR
  FCLAUDE_SSH_KEY FCLAUDE_SSH_KEY_MAINT FCLAUDE_VM_NAME FCLAUDE_VM_UUID FCLAUDE_VM_USER FCLAUDE_VM_UID
  FCLAUDE_MAINT_USER FCLAUDE_VM_IP FCLAUDE_HOST_IP FCLAUDE_VM_MAC FCLAUDE_VM_MEM_GIB
  FCLAUDE_VM_VCPU FCLAUDE_LIBVIRT_NET FCLAUDE_VM_HOME FCLAUDE_VM_ANSIBLE_DIR
  FCLAUDE_SUBNET_CIDR FCLAUDE_PUBKEY MAINT_PUBKEY ANSIBLE_SITE_B64 USER
)

# ===========================================================================
step "6/13  Build cloud-init seed"
seeddir="$(mktemp -d)"
render "${REPO_DIR}/vm/cloud-init/meta-data.tmpl" "${seeddir}/meta-data"
render "${REPO_DIR}/vm/cloud-init/user-data.tmpl" "${seeddir}/user-data"
if command -v cloud-localds >/dev/null 2>&1; then
  cloud-localds "$FCLAUDE_SEED" "${seeddir}/user-data" "${seeddir}/meta-data"
else
  genisoimage -output "$FCLAUDE_SEED" -volid cidata -joliet -rock \
    "${seeddir}/user-data" "${seeddir}/meta-data" >/dev/null 2>&1
fi
rm -rf "$seeddir"
info "seed image: $FCLAUDE_SEED"

# ===========================================================================
step "7/13  libvirt NAT network + DHCP reservation"
if ! VIRSH net-info "$FCLAUDE_LIBVIRT_NET" >/dev/null 2>&1; then
  die "libvirt '$FCLAUDE_LIBVIRT_NET' network missing; expected with libvirt-daemon-system"
fi
VIRSH net-autostart "$FCLAUDE_LIBVIRT_NET" >/dev/null 2>&1 || true
VIRSH net-start "$FCLAUDE_LIBVIRT_NET" >/dev/null 2>&1 || true
# Pin VM MAC -> IP (idempotent: delete any prior entry first).
VIRSH net-update "$FCLAUDE_LIBVIRT_NET" delete ip-dhcp-host \
  "<host mac='${FCLAUDE_VM_MAC}'/>" --live --config >/dev/null 2>&1 || true
VIRSH net-update "$FCLAUDE_LIBVIRT_NET" add ip-dhcp-host \
  "<host mac='${FCLAUDE_VM_MAC}' name='${FCLAUDE_VM_NAME}' ip='${FCLAUDE_VM_IP}'/>" \
  --live --config >/dev/null
BRIDGE="$(VIRSH net-info "$FCLAUDE_LIBVIRT_NET" | awk '/^Bridge:/{print $2}')"
[[ -n "$BRIDGE" ]] || die "could not determine libvirt bridge name"
export FCLAUDE_BRIDGE="$BRIDGE"
RENDER_VARS+=(FCLAUDE_BRIDGE)
info "network bridge: $BRIDGE, VM pinned to $FCLAUDE_VM_IP"

# ===========================================================================
step "8/13  Define and start the VM"

# System libvirt runs qemu as 'libvirt-qemu' by default, which cannot traverse
# your home directory to open ~/.fclaude/fclaude-disk. Run qemu as your user
# (group 'kvm' for /dev/kvm access) so the disk opens natively and ownership
# never churns — this also keeps fclaude-grow-disk working without chowns.
QEMU_CONF=/etc/libvirt/qemu.conf
if [[ -f "$QEMU_CONF" ]] && ! sudo grep -q '>>> fclaude managed >>>' "$QEMU_CONF"; then
  if sudo grep -Eq '^[[:space:]]*user[[:space:]]*=' "$QEMU_CONF"; then
    warn "qemu.conf already sets 'user'; appending fclaude override (last definition wins)."
  fi
  sudo cp -n "$QEMU_CONF" "${QEMU_CONF}.fclaude.bak" || true
  printf '\n# >>> fclaude managed >>>\nuser = "%s"\ngroup = "kvm"\n# <<< fclaude managed <<<\n' "$USER" \
    | sudo tee -a "$QEMU_CONF" >/dev/null
  # qemu.conf is read by virtqemud (modular) or libvirtd (monolithic); restart
  # whichever is actually running so the new user/group takes effect.
  for u in virtqemud.service libvirtd.service; do sudo systemctl try-restart "$u" 2>/dev/null || true; done
  info "qemu configured to run as $USER:kvm (backup at ${QEMU_CONF}.fclaude.bak)."
else
  info "qemu.conf already managed by fclaude; leaving as-is."
fi

domxml="$(mktemp --suffix=.xml)"
render "${REPO_DIR}/vm/libvirt/domain.xml.tmpl" "$domxml"
dom_state="$(VIRSH domstate "$FCLAUDE_VM_NAME" 2>/dev/null || true)"
if [[ "$dom_state" == "running" ]]; then
  info "domain already running; leaving definition untouched."
elif VIRSH dominfo "$FCLAUDE_VM_NAME" >/dev/null 2>&1; then
  # A stopped domain may carry a different (auto-generated) UUID than our fixed
  # one; undefine it first so the new definition can apply cleanly.
  info "domain exists and is stopped; redefining."
  VIRSH undefine "$FCLAUDE_VM_NAME" --nvram >/dev/null 2>&1 \
    || VIRSH undefine "$FCLAUDE_VM_NAME" >/dev/null 2>&1 || true
  VIRSH define "$domxml" >/dev/null
else
  VIRSH define "$domxml" >/dev/null
  info "domain defined."
fi
rm -f "$domxml"
if [[ "$(VIRSH domstate "$FCLAUDE_VM_NAME" 2>/dev/null)" != "running" ]]; then
  VIRSH start "$FCLAUDE_VM_NAME" >/dev/null
fi
info "VM started."

# ===========================================================================
step "9/13  NFS export root + firewall + sudoers (host config)"
# NFS
sudo install -d -m 755 /etc/nfs.conf.d /etc/exports.d
tmpf="$(mktemp)"; render "${REPO_DIR}/host/nfs/fclaude.conf"        "$tmpf"; sudo install -m 644 "$tmpf" /etc/nfs.conf.d/fclaude.conf; rm -f "$tmpf"
tmpf="$(mktemp)"; render "${REPO_DIR}/host/nfs/fclaude.exports.tmpl" "$tmpf"; sudo install -m 644 "$tmpf" /etc/exports.d/fclaude.exports; rm -f "$tmpf"
sudo systemctl restart nfs-server
sudo exportfs -ra
info "NFSv4 export root configured: $FCLAUDE_EXPORTS_ROOT"

# Firewall
sudo install -d -m 755 /etc/fclaude
tmpf="$(mktemp)"; render "${REPO_DIR}/host/nftables/fclaude.nft.tmpl" "$tmpf"
sudo nft -c -f "$tmpf" || die "nftables ruleset failed validation"
sudo install -m 644 "$tmpf" /etc/fclaude/fclaude.nft; rm -f "$tmpf"
sudo nft -f /etc/fclaude/fclaude.nft
info "firewall rules applied."

# ufw integration: if ufw is the active host firewall, its INPUT policy is
# 'drop', which blocks guest->host traffic (including NFS 2049) that our nft
# table merely 'accept's (non-terminally). Open the VM subnet to host
# unprivileged ports via ufw; <1024 stays denied by ufw default + our nft reject.
if command -v ufw >/dev/null 2>&1 && sudo ufw status 2>/dev/null | grep -qi '^Status: active'; then
  for proto in tcp udp; do
    sudo ufw allow in on "$BRIDGE" proto "$proto" \
      from "$FCLAUDE_SUBNET_CIDR" to "$FCLAUDE_HOST_IP" port 1024:65535 \
      comment 'fclaude' >/dev/null
  done
  sudo ufw reload >/dev/null 2>&1 || true
  info "ufw active: allowed guest->host unprivileged ports on $BRIDGE."
else
  info "ufw not active: relying on the inet fclaude nft table only."
fi

# sudoers
tmpf="$(mktemp)"; render "${REPO_DIR}/host/sudoers/fclaude.tmpl" "$tmpf"
sudo visudo -cf "$tmpf" >/dev/null || die "generated sudoers file failed validation"
sudo install -m 440 "$tmpf" /etc/sudoers.d/fclaude; rm -f "$tmpf"
info "sudoers drop-in installed."

# ===========================================================================
step "10/13  systemd units (boot persistence)"
tmpf="$(mktemp)"; render "${REPO_DIR}/host/systemd/fclaude-exports.service" "$tmpf"
sudo install -m 644 "$tmpf" /etc/systemd/system/fclaude-exports.service; rm -f "$tmpf"
sudo install -m 644 "${REPO_DIR}/host/systemd/fclaude-firewall.service" /etc/systemd/system/fclaude-firewall.service
# Order nfs-server after the libvirt network so nfsd can bind to 192.168.122.1.
sudo install -d -m 755 /etc/systemd/system/nfs-server.service.d
sudo install -m 644 "${REPO_DIR}/host/systemd/nfs-server-after-libvirt.conf" \
  /etc/systemd/system/nfs-server.service.d/fclaude.conf
sudo systemctl daemon-reload
sudo systemctl enable fclaude-exports.service fclaude-firewall.service >/dev/null 2>&1 || true
info "boot-persistence units enabled."

# ===========================================================================
step "11/13  Install wrappers + library"
install -m 644 "${REPO_DIR}/lib/common.sh"    "${FCLAUDE_LIB_DIR}/common.sh"
install -m 644 "${REPO_DIR}/lib/constants.sh" "${FCLAUDE_LIB_DIR}/constants.sh"
for w in fclaude fclaude-vscode fclaude-grow-disk; do
  install -m 755 "${REPO_DIR}/host/bin/${w}" "${FCLAUDE_BIN_DIR}/${w}"
done
info "wrappers installed to $FCLAUDE_BIN_DIR"

# ===========================================================================
step "12/13  Write config + VM info"
tmpf="$(mktemp)"; render "${REPO_DIR}/host/config.template" "$tmpf"
if [[ -e "$FCLAUDE_CONFIG" ]]; then
  info "config exists; leaving it untouched (new template at ${FCLAUDE_CONFIG}.new)"
  cp "$tmpf" "${FCLAUDE_CONFIG}.new"
else
  cp "$tmpf" "$FCLAUDE_CONFIG"
fi
rm -f "$tmpf"

VM_UUID="$(VIRSH domuuid "$FCLAUDE_VM_NAME" 2>/dev/null | tr -d '[:space:]')"
cat >"$FCLAUDE_VM_INFO" <<EOF
# fclaude VM identity (generated by install.sh — do not edit by hand)
VM_NAME="$FCLAUDE_VM_NAME"
VM_UUID="$VM_UUID"
VM_MAC="$FCLAUDE_VM_MAC"
VM_IP="$FCLAUDE_VM_IP"
BRIDGE="$BRIDGE"
LIBVIRT_NET="$FCLAUDE_LIBVIRT_NET"
EOF
info "wrote $FCLAUDE_CONFIG and $FCLAUDE_VM_INFO"

# ===========================================================================
step "13/13  Wait for VM provisioning (cloud-init + Ansible)"
info "waiting for the VM to come up on ssh (this includes apt + Ansible; may take several minutes)..."
deadline=$(( SECONDS + 900 ))
SSH_OPTS=(-i "$FCLAUDE_SSH_KEY" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null
          -o LogLevel=ERROR -o ConnectTimeout=5)
until ssh "${SSH_OPTS[@]}" "${FCLAUDE_VM_USER}@${FCLAUDE_VM_IP}" \
        'test -f /var/lib/fclaude/.provisioned' 2>/dev/null; do
  (( SECONDS < deadline )) || { warn "provisioning not confirmed within timeout; check 'fclaude --maintenance' and 'cloud-init status'"; break; }
  sleep 5
done
ssh "${SSH_OPTS[@]}" "${FCLAUDE_VM_USER}@${FCLAUDE_VM_IP}" 'test -f /var/lib/fclaude/.provisioned' 2>/dev/null \
  && info "VM provisioned successfully." || true

# ===========================================================================
printf '\n\033[1;32mfclaude install complete.\033[0m\n'
cat <<EOF

Next steps:
  1. $( ((RELOGIN)) && echo "Log out and back in (group changes) BEFORE using fclaude." || echo "(no re-login needed)" )
  2. One-time Claude login inside the VM (persists on the VM disk):
         fclaude --maintenance        # or: fclaude -m
         claude                        # authenticate, then exit
  3. Use it:
         cd ~/some/project && fclaude

  VS Code: set 'claudeCode.claudeProcessWrapper' to:
         ${FCLAUDE_BIN_DIR}/fclaude-vscode
     and enable skip-permissions in the extension settings.

  Config: $FCLAUDE_CONFIG
  VM info: $FCLAUDE_VM_INFO
EOF
