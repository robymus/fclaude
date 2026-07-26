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
# shellcheck disable=SC2016  # intentional: the VM shell expands $HOME, not us
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

# Project label for the X-Headroom-Project header. Percent-encodes exactly what
# Headroom's own `urllib.parse.quote(name, safe="-_.() ")` leaves alone, so the
# proxy's per-project savings attribution keys stay identical to the ones `wrap`
# produced (and the header stays visible-ASCII, RFC 7230). Iterates BYTES —
# LC_ALL=C makes ${#s} / ${s:i:1} byte-wise — so a multi-byte name encodes to
# %XX per byte like the Python side.
_headroom_project_label() {
  local s="$1" out="" c i
  local LC_ALL=C
  for ((i = 0; i < ${#s}; i++)); do
    c="${s:i:1}"
    case "$c" in
      [A-Za-z0-9._~-] | ' ' | '(' | ')') out+="$c" ;;
      *) out+="$(printf '%%%02X' "'${c}")" ;;
    esac
  done
  printf '%s' "$out"
}

# Headroom launch prefix for the wrappers. Claude is launched DIRECTLY, with the
# environment `headroom wrap claude` would have set — NOT through `wrap` itself.
#
# Why not `wrap`: it unconditionally writes `env.ANTHROPIC_BASE_URL` into
# `<cwd>/.claude/settings.local.json`, plus a SessionStart self-heal hook and a
# `.headroom_wrap_marker.json` sidecar (headroom 0.32.1 `wrap.py:4630`/`:4646`,
# their issues #951/#2221). There is no flag or env var that disables it. Our cwd
# is an NFS share of a HOST directory, so those writes land in the real repo:
# a `claude` running OUTSIDE the VM then reads ANTHROPIC_BASE_URL=127.0.0.1:8787
# and dies with ConnectionRefused — the proxy only exists in the VM. Merely
# opening the repo in VS Code (which runs fclaude-vscode for every claude
# process) was enough to break a host-side session. wrap also leaves the hook
# behind on clean exit; only `headroom unwrap` removes it.
#
# Everything wrap contributed for us is reproduced here (checked against wrap.py):
#   ANTHROPIC_BASE_URL        the shared in-VM proxy — `--no-proxy` already meant
#                             wrap never started one of its own.
#   ANTHROPIC_MODEL=<m>[1m]   the ENTIRETY of `--1m` (wrap.py:4676 → :229): Claude
#                             Code turns the `[1m]` suffix into the `context-1m`
#                             beta header, which is what actually unlocks the 1M
#                             window. Verified on the wire in the proxy log.
#   ENABLE_TOOL_SEARCH=true   Claude Code disables its client-side tool deferral
#                             behind a custom base URL (headroom #746).
#   ANTHROPIC_CUSTOM_HEADERS  X-Headroom-Project, for per-project savings
#                             attribution in the Grafana dashboards.
# The base URL is ALSO passed as inline `--settings` JSON, because that is the
# one thing the settings file was for: cc-daemon spawns conversation workers that
# re-read settings instead of inheriting the launcher's environment (#951).
# `--settings` accepts a JSON *string*, so it needs no file, anywhere.
#
# MCP registration (headroom retrieve + tokensave) is durable USER-scope state in
# the VM's ~/.claude.json, provisioned once by site.yml — wrap only re-asserted
# it on every start. The one genuinely per-session job it did is the tokensave
# code-graph refresh; see _tokensave_sync_cmd.
#
# Emits the command word(s) that PRECEDE the claude args; the caller does
# `… ; exec $(_headroom_launch "$dir") <claude-args>`. A caller-supplied
# `--settings` in <claude-args> wins (Claude Code takes the last one).
#   $1 = workspace dir, for the project label (defaults to the VM mount base).
#   FCLAUDE_HEADROOM_ENABLE=0 → plain `claude` (bypass Headroom entirely).
_headroom_launch() {
  local dir="${1:-${VM_MOUNT_BASE:-}}"
  if [[ "${FCLAUDE_HEADROOM_ENABLE:-1}" != "1" ]]; then
    printf 'claude'
    return
  fi
  local proxy="http://127.0.0.1:${FCLAUDE_HEADROOM_PORT:-8787}"
  local -a kv=("ANTHROPIC_BASE_URL=${proxy}" "ENABLE_TOOL_SEARCH=true")
  [[ -n "${FCLAUDE_HEADROOM_MODEL:-}" ]] &&
    kv+=("ANTHROPIC_MODEL=${FCLAUDE_HEADROOM_MODEL}[1m]")
  local label
  label="$(_headroom_project_label "$(basename -- "${dir:-/}")")"
  [[ -n "$label" ]] && kv+=("ANTHROPIC_CUSTOM_HEADERS=X-Headroom-Project: ${label}")

  printf 'env'
  printf ' %q' "${kv[@]}"
  printf ' claude --settings %q' "{\"env\":{\"ANTHROPIC_BASE_URL\":\"${proxy}\"}}"
}

# Remote-shell snippet that prepares the tokensave code graph for the workspace
# we are about to launch in. Emits a `;`-terminated snippet to run AFTER the cd
# into the workspace — both steps are cwd-relative, like tokensave itself.
#
# 1. Refresh the graph. `headroom wrap` did this on every start (wrap.py:1542)
#    and it is the ONLY per-session work we give up by not calling wrap. The
#    store is per-project (`<workspace>/.tokensave/`), `sync` refuses to run on
#    an uninitialized project, and with no local store tokensave resolves UPWARD
#    to a parent project — so init-when-absent is what keeps each workspace
#    bound to its own index instead of some ancestor's.
#
# 2. Make the store git-ignore itself. tokensave 7.0.x appended the entry; 7.7.0
#    does not, so an unignored 200 MB+ binary index now shows up as untracked in
#    a fresh repo, one `git add -A` away from being committed. A one-line
#    .gitignore diff is the cheaper of the two. `git check-ignore` is the test
#    rather than grepping for a literal: it honours the global ignore file,
#    .git/info/exclude and any pattern form, so we never add a duplicate. Only
#    ever appends to the .gitignore of the directory the store lives in.
#
# Silent and non-fatal by construction: any stray stdout would corrupt the VS
# Code stdio JSON protocol, and tokensave re-checks staleness on demand anyway,
# so a failure here costs nothing. Emits nothing at all when disabled.
_tokensave_prepare_cmd() {
  [[ "${FCLAUDE_TOKENSAVE_SYNC:-1}" == "1" ]] || return 0
  # Quoted heredoc: every $ and \ below belongs to the REMOTE shell, not to us.
  # __T__ is the only thing we substitute. $() strips the trailing newline, so
  # the snippet ends exactly at `fi` and the `;` lets `exec …` follow it.
  local snippet
  snippet="$(cat <<'REMOTE'
if command -v tokensave >/dev/null 2>&1; then
  if [ -d .tokensave ]; then timeout __T__ tokensave sync; else timeout __T__ tokensave init; fi >/dev/null 2>&1 || true
fi
if [ -d .tokensave ] && git rev-parse --is-inside-work-tree >/dev/null 2>&1 && ! git check-ignore -q .tokensave 2>/dev/null; then
  { [ -s .gitignore ] && [ -n "$(tail -c1 .gitignore)" ] && printf '\n'
    printf '# tokensave code-graph index (local, binary)\n.tokensave/\n'
  } >>.gitignore 2>/dev/null || true
fi
REMOTE
  )"
  printf '%s;' "${snippet//__T__/${FCLAUDE_TOKENSAVE_TIMEOUT:-60}}"
}

# Activate a newer STAGED Headroom version, but only when this is the "first
# instance" (no Claude running in the VM) so a proxy restart never disrupts a live
# session. Best-effort and cheap; safe to call on every session start. Guarded by a
# host-side flock so two concurrent first-starts don't both restart.
headroom_activate_if_idle() {
  [[ "${FCLAUDE_HEADROOM_ENABLE:-1}" == "1" ]] || return 0
  with_lock "headroom-activate" _headroom_activate_if_idle
}
_headroom_activate_if_idle() {
  # `pgrep -x claude` in the VM: if none, we're the first instance → safe to swap.
  if ! ssh_vm 'pgrep -x claude >/dev/null 2>&1'; then
    ssh_vm 'sudo /usr/local/sbin/fclaude-headroom-activate >/dev/null 2>&1' || true
  fi
}

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

    # Update status (best-effort): installed Claude version + last weekly apt run.
    local cver apt
    # shellcheck disable=SC2016  # intentional: the VM shell sources its own ~/.profile
    cver="$(ssh_vm '[ -f "$HOME/.profile" ] && . "$HOME/.profile"; claude --version 2>/dev/null' 2>/dev/null || true)"
    [[ -n "$cver" ]] && printf '  Claude:      %s\n' "$cver"
    apt="$(ssh_vm 'cat /var/lib/fclaude/last-apt-upgrade 2>/dev/null' 2>/dev/null || true)"
    [[ -n "$apt" ]] && printf '  Last apt:    %s\n' "$apt"
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
# Updates — on-demand + session-start staleness warning (plan: auto-update)
# ---------------------------------------------------------------------------
# Claude Code updates itself via the native installer's background auto-updater;
# apt/system packages are refreshed by a weekly, network-gated systemd timer in
# the VM. These helpers let the user force an update now and get warned at
# session start if updates have silently stalled.

# On-demand: update Claude Code and system packages right now, bypassing the
# weekly apt run's network-speed gate (an explicit request means bandwidth is
# the user's call). Streams output over a PTY.
fclaude_update() {
  ensure_vm_running
  info "updating Claude Code (native auto-updater, on demand)..."
  ssh_vm_tty "${CLAUDE_LOGIN_ENV} claude update" \
    || warn "claude update returned non-zero (already latest is reported as success)"
  info "updating system packages (apt; network gate bypassed)..."
  ssh_vm_tty "sudo env FCLAUDE_UPDATE_FORCE=1 /usr/local/sbin/fclaude-apt-upgrade.sh" \
    || warn "apt upgrade returned non-zero (see /var/log/fclaude-apt-upgrade.log in the VM)"
}

# Pure helper: human "N days" for an epoch (or a placeholder for "-"/unknown).
_fc_days_ago() {
  local t="$1" now="$2"
  [[ "$t" =~ ^[0-9]+$ ]] || { printf 'an unknown time'; return; }
  printf '%d days' "$(( (now - t) / 86400 ))"
}

# Pure helper (unit-tested): emit zero or more warning lines for stale updates.
# Args: now_epoch days apt_enabled apt_ok_epoch apt_attempt_first_token claude_epoch
# apt_ok_epoch / claude_epoch may be "-" (unknown); non-numeric is treated as "-".
_fc_staleness_messages() {
  local now="$1" days="$2" apt_enabled="$3" apt_ok="$4" apt_attempt="$5" claude_epoch="$6"
  local max=$(( days * 86400 ))
  [[ "$apt_ok" =~ ^[0-9]+$ ]] || apt_ok="-"
  [[ "$claude_epoch" =~ ^[0-9]+$ ]] || claude_epoch="-"

  if [[ "$apt_enabled" == "1" ]]; then
    local stale=0
    if [[ "$apt_ok" == "-" ]]; then stale=1
    elif (( now - apt_ok > max )); then stale=1; fi
    if (( stale )); then
      local reason
      case "$apt_attempt" in
        error*)   reason="last attempt FAILED — check: journalctl -u fclaude-apt-upgrade" ;;
        skipped*) reason="recent runs skipped — slow or offline network" ;;
        ok*)      reason="" ;;
        *)        reason="no successful upgrade recorded yet" ;;
      esac
      printf 'system packages not upgraded in %s%s; run: fclaude --update\n' \
        "$(_fc_days_ago "$apt_ok" "$now")" "${reason:+ ($reason)}"
    fi
  fi

  if [[ "$claude_epoch" != "-" ]] && (( now - claude_epoch > max )); then
    printf 'Claude Code has not updated in %s — auto-updater may be stuck; run: fclaude --update\n' \
      "$(_fc_days_ago "$claude_epoch" "$now")"
  fi
}

# Session-start check: one best-effort ssh round-trip (purely local in the VM,
# no network probe), then warn via the pure helper above. Never fails the caller.
fclaude_staleness_warn() {
  local days="${FCLAUDE_UPDATE_WARN_DAYS:-15}"
  [[ "$days" =~ ^[0-9]+$ ]] && (( days > 0 )) || return 0
  local apt_enabled="${FCLAUDE_UPDATE_APT:-1}"

  local raw
  # The apt-success stamp's CONTENT is the ISO timestamp of the last real upgrade
  # (parse it, not the file mtime — a disk restore would reset mtime). The Claude
  # versions dir has no such content, so its install time IS its mtime (date -r).
  # shellcheck disable=SC2016  # intentional: the VM shell expands $s/$v/$HOME, not us
  raw="$(ssh_vm '
    s=$(date -d "$(cat /var/lib/fclaude/last-apt-success 2>/dev/null)" +%s 2>/dev/null || echo -);
    a=$(head -1 /var/lib/fclaude/last-apt-upgrade 2>/dev/null || true);
    v=$(ls -1dt "$HOME/.local/share/claude/versions/"*/ 2>/dev/null | head -1);
    if [ -n "$v" ]; then m=$(date -r "$v" +%s 2>/dev/null || echo -); else m=-; fi;
    printf "APT_OK=%s\nATTEMPT=%s\nCLAUDE=%s\n" "${s:--}" "${a:--}" "${m:--}"
  ' 2>/dev/null || true)"
  [[ -n "$raw" ]] || return 0

  local apt_ok attempt claude_epoch
  apt_ok="$(sed -n 's/^APT_OK=//p' <<<"$raw" | head -1)"
  attempt="$(sed -n 's/^ATTEMPT=//p' <<<"$raw" | head -1)"
  claude_epoch="$(sed -n 's/^CLAUDE=//p' <<<"$raw" | head -1)"

  local now msg
  now="$(date +%s)"
  while IFS= read -r msg; do
    [[ -n "$msg" ]] && warn "$msg"
  done < <(_fc_staleness_messages "$now" "$days" "$apt_enabled" \
             "${apt_ok:--}" "${attempt%% *}" "${claude_epoch:--}")
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
