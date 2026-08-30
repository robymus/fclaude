#!/usr/bin/env bash
# Unit tests for the reference-counted mount flow in lib/common.sh, focused on
# the self-healing behaviour: a refcount left behind by a session that was
# killed before its EXIT trap must NOT suppress the mount.
#
# Regression: a leaked counter made `_do_mount` take the "already mounted"
# branch, so nothing was shared and the session landed in a VM directory that
# did not exist — surfacing only as `cd: /home/fclaude/<ws>: No such file or
# directory` from the guest shell.
#
# The real lib is loaded; only the side effects (sudo mount/umount/exportfs, the
# ssh call into the VM, and mountpoint(1)) are stubbed against a fake mount
# table. No VM, sudo or network.
#
# Run:  bash test/test-mount-selfheal.sh
set -uo pipefail

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

TMP="$(cd "$(mktemp -d)" && pwd -P)"   # -P: relpath_under_home compares real paths
trap 'rm -rf "$TMP"' EXIT

SHARE="$TMP/home"
WS="$SHARE/ws"
mkdir -p "$WS" "$TMP/exports" "$TMP/refcount"

REC="$TMP/rec"        # stubbed privileged/ssh calls of the current case
STATE="$TMP/mounted"  # fake host mount table: one mounted path per line
: >"$REC"; : >"$STATE"

cat >"$TMP/config" <<EOF
VM_NAME=testvm
VM_IP=192.0.2.2
HOST_IP=192.0.2.1
SSH_USER=fclaude
SSH_KEY=$TMP/key
EXPORTS_ROOT=$TMP/exports
REFCOUNT_DIR=$TMP/refcount
VM_MOUNT_BASE=/home/fclaude
SHARE_ROOT=$SHARE
EOF
export FCLAUDE_CONFIG_FILE="$TMP/config"

# Written to disk so each case can source it in its own subshell (a die() in the
# lib calls exit, which must not take the whole suite down with it).
cat >"$TMP/stubs.sh" <<'STUBS'
source "${REPO_DIR}/lib/common.sh"
load_config

# mountpoint -q <path> against the fake table.
mountpoint() { grep -qxF "${2:-}" "$STATE" 2>/dev/null; }

# sudo mount --bind SRC DST | sudo umount [-l] DST | sudo exportfs -r
sudo() {
  printf 'sudo %s\n' "$*" >>"$REC"
  case "${1:-}" in
    mount)  printf '%s\n' "${!#}" >>"$STATE" ;;
    umount) grep -vxF "${!#}" "$STATE" >"$STATE.new" 2>/dev/null || :
            mv "$STATE.new" "$STATE" ;;
  esac
  return 0
}

ssh_vm() {
  printf 'ssh_vm %s\n' "$*" >>"$REC"
  [[ "${SSH_VM_FAIL:-0}" == 1 ]] && return 1
  return 0
}
STUBS

KEY="$(printf '%s' ws | sha1sum | cut -c1-16)"
COUNT="$TMP/refcount/${KEY}.count"
RELF="$TMP/refcount/${KEY}.rel"

pass=0 fail=0
check() {
  local desc="$1" want="$2" got="$3"
  if [[ "$got" == *"$want"* ]]; then
    pass=$((pass + 1))
  else
    fail=$((fail + 1))
    printf 'FAIL: %s\n  wanted substring: %s\n  got:\n%s\n' "$desc" "$want" "$got"
  fi
}
check_not() {
  local desc="$1" unwanted="$2" got="$3"
  if [[ "$got" != *"$unwanted"* ]]; then
    pass=$((pass + 1))
  else
    fail=$((fail + 1))
    printf 'FAIL: %s\n  unwanted substring: %s\n  got:\n%s\n' "$desc" "$unwanted" "$got"
  fi
}
check_eq() {
  local desc="$1" want="$2" got="$3"
  if [[ "$got" == "$want" ]]; then
    pass=$((pass + 1))
  else
    fail=$((fail + 1))
    printf 'FAIL: %s\n  wanted: [%s]\n  got:    [%s]\n' "$desc" "$want" "$got"
  fi
}

count_now() { cat "$COUNT" 2>/dev/null || echo "-"; }

# run <mount|unmount> — one wrapper-like invocation in its own subshell.
run() {
  : >"$REC"
  ( set -euo pipefail
    export REPO_DIR REC STATE
    # shellcheck disable=SC1090,SC1091
    source "$TMP/stubs.sh"
    case "$1" in
      mount)   mount_workspace "$WS" ;;
      unmount) unmount_workspace "$WS" ;;
    esac
  ) 2>&1
}

# --- 1. cold start: nothing shared, no counter ------------------------------
out="$(run mount)"; rc=$?
check_eq "cold mount succeeds"            "0"  "$rc"
check    "cold mount binds into exports"  "sudo mount --bind $WS $TMP/exports/ws" "$(cat "$REC")"
check    "cold mount mounts in the VM"    "ssh_vm"  "$(cat "$REC")"
check_eq "cold mount sets refcount to 1"  "1"  "$(count_now)"
check_eq "cold mount records the workspace" "ws" "$(cat "$RELF" 2>/dev/null || echo -)"

# --- 2. second session joins the live share ---------------------------------
out="$(run mount)"
check_eq "join increments the refcount"   "2"  "$(count_now)"
check_not "join does not re-bind"         "mount --bind" "$(cat "$REC")"
check    "join still verifies the VM mount" "ssh_vm" "$(cat "$REC")"

# --- 3. the regression: counter alive, share gone ---------------------------
# Simulate the killed-session leak: the counter survives, the mounts do not
# (host reboot, desktop-session logout, SIGKILLed terminal).
: >"$STATE"
printf '2' >"$COUNT"
out="$(run mount)"
check    "stale counter is reported"      "stale refcount for /ws" "$out"
check    "stale counter re-establishes the share" "sudo mount --bind $WS $TMP/exports/ws" "$(cat "$REC")"
check_eq "stale counter is re-seated to 1" "1" "$(count_now)"

# --- 4. host share live but the VM lost its mount ---------------------------
# (guest rebooted under a perfectly live counter) — the ssh mount must still run.
out="$(run mount)"
check    "live share still re-checks the VM" "ssh_vm" "$(cat "$REC")"
check_eq "live share increments"          "2"  "$(count_now)"
printf '1' >"$COUNT"   # drop back to a single holder for the cases below

# --- 5. a failing VM mount rolls everything back ----------------------------
: >"$STATE"; rm -f "$COUNT" "$RELF"
out="$( : >"$REC"
        ( set -euo pipefail
          export REPO_DIR REC STATE SSH_VM_FAIL=1
          # shellcheck disable=SC1090,SC1091
          source "$TMP/stubs.sh"
          mount_workspace "$WS" ) 2>&1 )"
rc=$?
check_eq "VM mount failure aborts"        "1"  "$rc"
check    "VM mount failure explains itself" "NFS mount of /ws in the VM failed" "$out"
check_eq "VM mount failure clears the refcount" "-" "$(count_now)"
check_eq "VM mount failure releases the host bind" "" "$(cat "$STATE")"

# --- 6. last holder unmounts ------------------------------------------------
run mount >/dev/null
out="$(run unmount)"
check_eq "unmount clears the refcount"    "-"  "$(count_now)"
check_eq "unmount drops the host bind"    ""   "$(cat "$STATE")"
check_eq "unmount removes the workspace note" "-" "$(cat "$RELF" 2>/dev/null || echo -)"

# --- 7. a cold VM boot invalidates every refcount ---------------------------
# The mounts they count live inside the VM, so a VM coming up from "shut off"
# holds none of them. Anything left on disk is a session that never unmounted.
# `virsh`/`ssh_vm` are stubbed so ensure_vm_running runs without a VM.
: >"$STATE"; rm -f "$COUNT" "$RELF"
run_boot() {  # run_boot <domstate>
  ( set -euo pipefail
    export REPO_DIR REC STATE
    # shellcheck disable=SC1090,SC1091
    source "$TMP/stubs.sh"
    # shellcheck disable=SC2317  # both are called from ensure_vm_running
    virsh() { case "${1:-}" in domstate) printf '%s\n' "$BOOT_STATE" ;; *) : ;; esac; }
    # shellcheck disable=SC2317
    ssh_vm() { return 0; }   # reachable immediately: no wait loop
    BOOT_STATE="$1"
    ensure_vm_running
  ) 2>&1
}

printf '3' >"$COUNT"; printf 'ws' >"$RELF"
printf '1' >"$TMP/refcount/deadbeefdeadbeef.count"
out="$(run_boot "shut off")"
check    "cold boot reports the sweep"    "cleared 2 workspace refcount(s)" "$out"
check_eq "cold boot clears the counter"   "-"  "$(count_now)"
check_eq "cold boot clears the sidecar"   "-"  "$(cat "$RELF" 2>/dev/null || echo -)"
check_eq "cold boot clears every workspace" "0" \
  "$(find "$TMP/refcount" -name '*.count' | wc -l)"

# A VM that was already up keeps its counters: those sessions are still live.
printf '2' >"$COUNT"; printf 'ws' >"$RELF"
out="$(run_boot running)"
check_not "running VM does not sweep"     "cleared" "$out"
check_eq  "running VM keeps the counter"  "2" "$(count_now)"

# A resume from pause keeps them too — the mounts survived the pause.
out="$(run_boot paused)"
check_not "resume does not sweep"         "cleared" "$out"
check_eq  "resume keeps the counter"      "2" "$(count_now)"
rm -f "$COUNT" "$RELF"

printf '\n%d passed, %d failed\n' "$pass" "$fail"
((fail == 0))
