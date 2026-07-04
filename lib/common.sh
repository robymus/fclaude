#!/usr/bin/env bash
# fclaude — shared library for the host-side wrapper scripts.
#
# Sourced by fclaude, fclaude-vscode, fclaude-grow-disk. Provides config
# loading, logging, ssh helpers, VM lifecycle, locking, refcounting, and the
# reference-counted workspace mount/unmount flow (plan §9).
#
# Requires bash 4+. Callers should run `set -euo pipefail` themselves.
#
# Host privileged operations (bind mount, umount, exportfs) go through `sudo`.
# install.sh adds a NOPASSWD sudoers drop-in for exactly those commands so
# sessions never prompt for a password. VM control uses `virsh` against the
# system libvirt via the `libvirt` group (no sudo needed).

export LIBVIRT_DEFAULT_URI="${LIBVIRT_DEFAULT_URI:-qemu:///system}"

# ---------------------------------------------------------------------------
# Logging
# ---------------------------------------------------------------------------
_fc_tag="fclaude"
info() { printf '[%s] %s\n' "$_fc_tag" "$*" >&2; }
warn() { printf '[%s] WARN: %s\n' "$_fc_tag" "$*" >&2; }
err()  { printf '[%s] ERROR: %s\n' "$_fc_tag" "$*" >&2; }
die()  { err "$*"; exit 1; }

# ---------------------------------------------------------------------------
# Config
# ---------------------------------------------------------------------------
# Load ~/.fclaude/config (and ~/.fclaude/vm if present), then validate that the
# variables the wrappers depend on are set.
load_config() {
  local cfg="${FCLAUDE_CONFIG_FILE:-$HOME/.fclaude/config}"
  [[ -r "$cfg" ]] || die "config not found at $cfg — run install.sh first"
  # shellcheck disable=SC1090
  source "$cfg"

  local vm_info="${FCLAUDE_HOME:-$HOME/.fclaude}/vm"
  if [[ -r "$vm_info" ]]; then
    # shellcheck disable=SC1090
    source "$vm_info"
  fi

  local missing=()
  local v
  for v in VM_NAME VM_IP HOST_IP SSH_USER SSH_KEY EXPORTS_ROOT REFCOUNT_DIR \
           VM_MOUNT_BASE SHARE_ROOT; do
    [[ -n "${!v:-}" ]] || missing+=("$v")
  done
  ((${#missing[@]} == 0)) || die "config missing required vars: ${missing[*]}"

  mkdir -p "$REFCOUNT_DIR"
}

# ---------------------------------------------------------------------------
# SSH helpers
# ---------------------------------------------------------------------------
# Common ssh options: use the fclaude key, don't pollute the user's known_hosts,
# and don't prompt on first connect (the VM is local and ephemeral).
_ssh_opts() {
  printf '%s\0' \
    -i "$SSH_KEY" \
    -o StrictHostKeyChecking=no \
    -o UserKnownHostsFile=/dev/null \
    -o LogLevel=ERROR \
    -o ConnectTimeout=5
}

# Prefix that makes a remote `claude` invocation honor the VM account's login
# environment. `ssh host "cmd"` runs cmd in a NON-login, NON-interactive shell,
# which reads neither ~/.profile nor ~/.bashrc (and Ubuntu's ~/.bashrc `return`s
# early when non-interactive anyway). The login PATH — where native `claude
# install` puts ~/.local/bin and its ~/.local/bin/env script — lives in
# ~/.profile, so without this `exec claude` fails with "claude: not found".
# We source ~/.profile rather than hardcode a path, so whatever the account's
# profile configures (nvm, a different prefix, …) is respected. Sourced in the
# SAME shell (one parse), so %q-quoted claude args appended after it stay intact.
# Single-quoted so $HOME is expanded by the VM shell, not the host.
CLAUDE_LOGIN_ENV='[ -f "$HOME/.profile" ] && . "$HOME/.profile";'

# Run a command in the VM, no PTY (for scripted/stdio use).
# The command string is intentionally assembled on the client and run by the
# VM's shell, so client-side expansion (SC2029) is the desired behaviour.
#
# `-n` redirects this ssh's stdin from /dev/null. This is critical: helper calls
# (mount/unmount/start probes) run while the wrapper has inherited the caller's
# stdin. Without -n, an ssh_vm command would consume bytes the caller still needs
# — e.g. the VS Code extension's stdio control handshake — and the real claude
# ssh would then hang waiting for input that was already swallowed.
ssh_vm() {
  local -a opts=()
  mapfile -d '' -t opts < <(_ssh_opts)
  # shellcheck disable=SC2029
  ssh -n "${opts[@]}" "${SSH_USER}@${VM_IP}" "$@"
}

# The guest reaches the host's llm.log proxy via the host-side gateway bridge
# (fclaude-llmlog.socket: HOST_IP:port -> 127.0.0.1:port), so the VM just points
# HTTPS_PROXY at HOST_IP and no per-session ssh forward is needed. (Earlier this
# used `ssh -R`, but that installs a single exclusive listener in the VM:
# concurrent sessions collide on the bind, and the listener's owner blocks on
# exit waiting for the other session's forwarded connections to drain.)

# Run a command in the VM with a PTY (for interactive claude / shells).
ssh_vm_tty() {
  local -a opts=()
  mapfile -d '' -t opts < <(_ssh_opts)
  ssh -t "${opts[@]}" "${SSH_USER}@${VM_IP}" "$@"
}

# Run a command in the VM as the maintenance user (interactive).
ssh_maint() {
  ssh -t \
    -i "${SSH_KEY_MAINT:?SSH_KEY_MAINT not set}" \
    -o StrictHostKeyChecking=no \
    -o UserKnownHostsFile=/dev/null \
    -o LogLevel=ERROR \
    "${MAINT_USER:?MAINT_USER not set}@${VM_IP}" "$@"
}

# ---------------------------------------------------------------------------
# VM lifecycle
# ---------------------------------------------------------------------------
vm_state() { virsh domstate "$VM_NAME" 2>/dev/null || echo "undefined"; }

# Start the VM if it isn't running and wait until ssh answers.
ensure_vm_running() {
  local state
  state="$(vm_state)"
  case "$state" in
    running) ;;
    "shut off"|crashed) info "starting VM $VM_NAME..."; virsh start "$VM_NAME" >/dev/null ;;
    paused)  info "resuming VM $VM_NAME...";  virsh resume "$VM_NAME" >/dev/null ;;
    undefined) die "VM $VM_NAME is not defined — run install.sh first" ;;
    *) info "VM state '$state', attempting start..."; virsh start "$VM_NAME" >/dev/null 2>&1 || true ;;
  esac

  local timeout="${FCLAUDE_SSH_TIMEOUT:-90}"
  local waited=0
  while ! ssh_vm true 2>/dev/null; do
    sleep 2
    waited=$((waited + 2))
    ((waited < timeout)) || die "VM did not become reachable over ssh within ${timeout}s"
  done
}

# ---------------------------------------------------------------------------
# Status report (does NOT start the VM) — plan §10 / fclaude --status
# ---------------------------------------------------------------------------
_human() { numfmt --to=iec --suffix=B "${1:-0}" 2>/dev/null || printf '%sB' "${1:-0}"; }

# Pull one "Field: value" line out of `virsh dominfo`.
_dominfo_field() { virsh dominfo "$VM_NAME" 2>/dev/null | awk -F': *' -v k="$1" '$1==k{print $2; exit}'; }

# Print a status summary. Best-effort throughout: every external call is guarded
# with `|| true` so a single failure never aborts the report (callers run under
# `set -e`). Does not start the VM.
fclaude_status() {
  local state; state="$(vm_state || true)"
  printf 'fclaude VM "%s"\n' "$VM_NAME"
  printf '  State:       %s\n' "$state"
  printf '  IP:          %s\n' "$VM_IP"
  printf '  Autostart:   %s\n' "$(_dominfo_field 'Autostart' || true)"
  printf '  vCPUs:       %s\n' "$(_dominfo_field 'CPU(s)' || true)"

  local maxmem_kib; maxmem_kib="$(_dominfo_field 'Max memory' | awk '{print $1}' || true)"
  [[ -n "$maxmem_kib" ]] && printf '  Memory:      %s configured\n' "$(_human $((maxmem_kib * 1024)))"

  if [[ "$state" == "running" ]]; then
    local pid et cput
    pid="$(pgrep -f "guest=$VM_NAME" 2>/dev/null | head -1 || true)"
    [[ -n "$pid" ]] && et="$(ps -o etime= -p "$pid" 2>/dev/null | tr -d ' ' || true)"
    [[ -n "${et:-}" ]] && printf '  Uptime:      %s\n' "$et"
    cput="$(virsh domstats "$VM_NAME" --cpu-total 2>/dev/null | awk -F= '/cpu.time=/{print $2}' || true)"
    [[ -n "$cput" ]] && printf '  CPU time:    %s s\n' "$((cput / 1000000000))"
  fi

  local cap alloc
  cap="$(virsh domblkinfo "$VM_NAME" vda 2>/dev/null | awk '/^Capacity:/{print $2}' || true)"
  alloc="$(virsh domblkinfo "$VM_NAME" vda 2>/dev/null | awk '/^Allocation:/{print $2}' || true)"
  [[ -n "$cap" ]] && printf '  Disk (vda):  %s allocated of %s\n' "$(_human "$alloc")" "$(_human "$cap")"

  # Guest-side memory + disk (one ssh call each, best-effort, fast timeout).
  if [[ "$state" == "running" ]]; then
    local mem df
    mem="$(ssh_vm 'free -m' 2>/dev/null | awk '/^Mem:/{print $3" "$2}' || true)"
    [[ -n "$mem" ]] && printf '  Guest mem:   %s MiB used of %s MiB\n' \
      "$(awk '{print $1}' <<<"$mem")" "$(awk '{print $2}' <<<"$mem")"
    df="$(ssh_vm 'df -h /' 2>/dev/null | awk 'NR==2{print $3" "$2" "$5}' || true)"
    [[ -n "$df" ]] && printf '  Guest /:     %s used of %s (%s)\n' \
      "$(awk '{print $1}' <<<"$df")" "$(awk '{print $2}' <<<"$df")" "$(awk '{print $3}' <<<"$df")"
  fi

  printf '\nActive workspaces (host bind mounts):\n'
  local found=0 t rel
  while IFS= read -r t; do
    [[ -z "$t" ]] && continue
    rel="${t#"${EXPORTS_ROOT%/}"/}"
    printf '  %-44s refs=%s\n' "$rel" "$(refcount_get "$(_key_for "$rel")" || true)"
    found=1
  done < <(findmnt -rno TARGET 2>/dev/null | grep "^${EXPORTS_ROOT%/}/" || true)
  ((found)) || printf '  (none)\n'
}

# ---------------------------------------------------------------------------
# Locking & refcounting
# ---------------------------------------------------------------------------
# Stable key for a workspace relative path (filename-safe).
_key_for() { printf '%s' "$1" | sha1sum | cut -c1-16; }

# with_lock <key> <command...> : run command holding an exclusive flock.
with_lock() {
  local key="$1"; shift
  local lockfile="${REFCOUNT_DIR}/${key}.lock"
  exec {_fc_lock_fd}>"$lockfile"
  flock "$_fc_lock_fd"
  "$@"
  local rc=$?
  flock -u "$_fc_lock_fd"
  exec {_fc_lock_fd}>&-
  return $rc
}

_count_file() { printf '%s/%s.count' "$REFCOUNT_DIR" "$1"; }

refcount_get() {
  local f; f="$(_count_file "$1")"
  [[ -r "$f" ]] && cat "$f" || echo 0
}

# Increment and echo the new value. Caller must hold the lock for <key>.
refcount_inc() {
  local key="$1" f n
  f="$(_count_file "$key")"
  n=$(( $(refcount_get "$key") + 1 ))
  printf '%s' "$n" >"$f"
  echo "$n"
}

# Decrement (floor 0) and echo the new value. Caller must hold the lock.
refcount_dec() {
  local key="$1" f n
  f="$(_count_file "$key")"
  n=$(( $(refcount_get "$key") - 1 ))
  ((n < 0)) && n=0
  if ((n == 0)); then
    rm -f "$f"
  else
    printf '%s' "$n" >"$f"
  fi
  echo "$n"
}

# ---------------------------------------------------------------------------
# Path validation
# ---------------------------------------------------------------------------
# Echo the path relative to $SHARE_ROOT, or die if it isn't under it.
relpath_under_home() {
  local dir="$1"
  # Resolve to an absolute, symlink-free path.
  dir="$(cd "$dir" 2>/dev/null && pwd -P)" || die "directory does not exist: $1"
  local root; root="$(cd "$SHARE_ROOT" && pwd -P)"
  [[ "$dir" == "$root" || "$dir" == "$root"/* ]] \
    || die "directory must be under $SHARE_ROOT to be shared: $dir"
  if [[ "$dir" == "$root" ]]; then
    echo "."
  else
    echo "${dir#"$root"/}"
  fi
}

# ---------------------------------------------------------------------------
# Workspace mount/unmount (reference counted) — plan §9
# ---------------------------------------------------------------------------
# Internal: do the actual mount work; assumes lock held and rel computed.
_do_mount() {
  local rel="$1" key="$2"
  local n; n="$(refcount_inc "$key")"
  if [[ "$n" == "1" ]]; then
    local host_src="${SHARE_ROOT%/}/${rel}"
    local export_dir="${EXPORTS_ROOT%/}/${rel}"
    info "sharing $host_src"
    mkdir -p "$export_dir"
    if ! mountpoint -q "$export_dir"; then
      sudo mount --bind "$host_src" "$export_dir"
    fi
    sudo exportfs -r
    ssh_vm "sudo mkdir -p ${VM_MOUNT_BASE%/}/${rel} && \
            ( mountpoint -q ${VM_MOUNT_BASE%/}/${rel} || \
              sudo mount -t nfs4 -o vers=4,sec=sys ${HOST_IP}:/${rel} ${VM_MOUNT_BASE%/}/${rel} )" \
      || { _do_unmount "$rel" "$key" >/dev/null 2>&1 || true; die "NFS mount in VM failed for /${rel}"; }
  fi
}

# Internal: tear down the mount; assumes lock held.
_do_unmount() {
  local rel="$1" key="$2"
  local n; n="$(refcount_dec "$key")"
  if [[ "$n" == "0" ]]; then
    local export_dir="${EXPORTS_ROOT%/}/${rel}"
    info "unsharing /${rel}"
    ssh_vm "sudo umount ${VM_MOUNT_BASE%/}/${rel} 2>/dev/null || true; \
            sudo rmdir -p ${VM_MOUNT_BASE%/}/${rel} 2>/dev/null || true" || true
    if mountpoint -q "$export_dir"; then
      sudo umount "$export_dir" 2>/dev/null || sudo umount -l "$export_dir" 2>/dev/null || true
    fi
    sudo exportfs -r 2>/dev/null || true
    # Remove now-empty export dirs up to (not including) EXPORTS_ROOT.
    ( cd "$EXPORTS_ROOT" 2>/dev/null && rmdir -p "$rel" 2>/dev/null ) || true
  fi
}

# Public: mount the given host directory into the VM (refcounted).
mount_workspace() {
  local dir="$1" rel key
  rel="$(relpath_under_home "$dir")"
  [[ "$rel" == "." ]] && die "refusing to share all of $SHARE_ROOT; pick a subdirectory"
  key="$(_key_for "$rel")"
  with_lock "$key" _do_mount "$rel" "$key"
}

# Public: unmount the given host directory from the VM (refcounted).
unmount_workspace() {
  local dir="$1" rel key
  # Don't re-resolve via cd (dir may be gone); compute rel directly.
  local root; root="$(cd "$SHARE_ROOT" && pwd -P)"
  dir="${dir%/}"
  if [[ "$dir" == "$root" ]]; then rel="."; else rel="${dir#"$root"/}"; fi
  key="$(_key_for "$rel")"
  with_lock "$key" _do_unmount "$rel" "$key"
}

# Internal: force teardown ignoring the refcount; assumes lock held.
_do_cleanup() {
  local rel="$1" key="$2"
  local export_dir="${EXPORTS_ROOT%/}/${rel}"
  info "force-unmounting /${rel}"
  # Best-effort VM-side umount (lazy fallback). VM may be off — ssh_vm just fails.
  ssh_vm "sudo umount ${VM_MOUNT_BASE%/}/${rel} 2>/dev/null || \
          sudo umount -l ${VM_MOUNT_BASE%/}/${rel} 2>/dev/null || true; \
          sudo rmdir -p ${VM_MOUNT_BASE%/}/${rel} 2>/dev/null || true" || true
  if mountpoint -q "$export_dir"; then
    sudo umount "$export_dir" 2>/dev/null || sudo umount -l "$export_dir" 2>/dev/null || true
  fi
  sudo exportfs -r 2>/dev/null || true
  ( cd "$EXPORTS_ROOT" 2>/dev/null && rmdir -p "$rel" 2>/dev/null ) || true
  rm -f "$(_count_file "$key")"   # reset the (now stale) refcount
}

# Public: forcibly tear down ONE workspace mount and clear its refcount,
# regardless of the current count. For manual recovery of a mount whose session
# leaked (e.g. a wrapper killed before its EXIT trap, or a long-lived helper that
# outlived its editing session). `rel` is a path relative to $SHARE_ROOT, exactly
# as `fclaude --status` prints it. Does not start the VM.
cleanup_workspace() {
  local rel="$1" key
  rel="${rel#/}"; rel="${rel%/}"
  [[ -n "$rel" && "$rel" != "." ]] \
    || die "cleanup needs a workspace path under $SHARE_ROOT (see 'fclaude --status')"
  key="$(_key_for "$rel")"
  with_lock "$key" _do_cleanup "$rel" "$key"
}
