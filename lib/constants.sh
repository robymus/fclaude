#!/usr/bin/env bash
# shellcheck disable=SC2034  # constants are consumed by sourcing scripts/templates
# fclaude — build/runtime constants (single source of truth)
#
# Sourced by install.sh / uninstall.sh and used to render templates.
# Values here are baked into ~/.fclaude/config at install time; the runtime
# wrappers read ~/.fclaude/config (NOT this file) so the user can override them.

# --- Identity ---
FCLAUDE_VM_NAME="fclaude"
FCLAUDE_VM_UUID="fc1aede0-0000-4000-8000-525400fc1ade"  # fixed so redefine is idempotent
FCLAUDE_VM_USER="fclaude"
FCLAUDE_MAINT_USER="maint"
FCLAUDE_VM_UID="1000"            # match host uid for clean NFS ownership

# --- Networking (libvirt default NAT network) ---
FCLAUDE_LIBVIRT_NET="default"
FCLAUDE_SUBNET_CIDR="192.168.122.0/24"
FCLAUDE_HOST_IP="192.168.122.1" # host as seen from the VM (NAT gateway)
FCLAUDE_VM_IP="192.168.122.10"  # pinned via DHCP reservation
FCLAUDE_VM_MAC="52:54:00:fc:1a:de"

# --- VM resources ---
FCLAUDE_VM_MEM_GIB="8"
FCLAUDE_VM_VCPU="6"
FCLAUDE_DISK_SIZE="32G"

# --- Guest image ---
FCLAUDE_CLOUD_IMG_URL="https://cloud-images.ubuntu.com/releases/24.04/release/ubuntu-24.04-server-cloudimg-amd64.img"

# --- Host paths ---
FCLAUDE_HOME="${HOME}/.fclaude"
FCLAUDE_DISK="${FCLAUDE_HOME}/fclaude-disk"
FCLAUDE_SEED="${FCLAUDE_HOME}/seed.iso"
FCLAUDE_EXPORTS_ROOT="${FCLAUDE_HOME}/exports"
FCLAUDE_REFCOUNT_DIR="${FCLAUDE_HOME}/refcounts"
FCLAUDE_SSH_DIR="${FCLAUDE_HOME}/ssh"
FCLAUDE_SSH_KEY="${FCLAUDE_SSH_DIR}/id_fclaude"
FCLAUDE_SSH_KEY_MAINT="${FCLAUDE_SSH_DIR}/id_maint"
FCLAUDE_CONFIG="${FCLAUDE_HOME}/config"
FCLAUDE_VM_INFO="${FCLAUDE_HOME}/vm"

# --- Install targets ---
FCLAUDE_BIN_DIR="${HOME}/.local/bin"
FCLAUDE_LIB_DIR="${HOME}/.local/lib/fclaude"

# --- VM-side paths ---
FCLAUDE_VM_HOME="/home/fclaude"
FCLAUDE_VM_ANSIBLE_DIR="/var/lib/fclaude/ansible"
