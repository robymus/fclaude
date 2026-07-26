#!/usr/bin/env bash
# Unit tests for `fclaude --mount <dir>...` (mount, hold until Ctrl+C, unmount).
#
# The real lib is loaded, so config loading and path validation
# (relpath_under_home) run for real; only the side-effecting pieces (VM startup,
# NFS mount/unmount) are stubbed and recorded to a file. No VM, sudo or network.
#
# Run:  bash test/test-mount-hold.sh
set -uo pipefail

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
FCLAUDE="${REPO_DIR}/host/bin/fclaude"

TMP="$(cd "$(mktemp -d)" && pwd -P)"   # -P: relpath_under_home compares real paths
trap 'rm -rf "$TMP"' EXIT

SHARE="$TMP/home"
mkdir -p "$SHARE/a" "$SHARE/b" "$SHARE/fail" "$TMP/outside"

REC="$TMP/rec"        # stubbed mount/unmount calls, in order
LOG="$TMP/log"        # everything fclaude printed
PIDFILE="$TMP/pid"    # pid of the fclaude under test

# --- fake config + stub lib ------------------------------------------------
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

mkdir -p "$TMP/lib"
cat >"$TMP/lib/common.sh" <<EOF
source "${REPO_DIR}/lib/common.sh"

ensure_vm_running() {
  printf 'vm-up\n' >>"$REC"
  printf '%s\n' "\$\$" >"$PIDFILE"
}

# Record the call; a workspace named "fail" simulates a mount that dies, so we
# can check that the mounts already held are still released.
mount_workspace() {
  printf 'mount %s\n' "\$1" >>"$REC"
  [[ "\$1" == */fail ]] && die "simulated mount failure: \$1"
  return 0
}
unmount_workspace() { printf 'unmount %s\n' "\$1" >>"$REC"; }
EOF

export FCLAUDE_CONFIG_FILE="$TMP/config"
export FCLAUDE_LIB_DIR="$TMP/lib"

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

reset_run() { : >"$REC"; : >"$LOG"; rm -f "$PIDFILE"; }

# run_fail <args...> — run to completion (used for the paths that must abort).
run_fail() {
  reset_run
  bash "$FCLAUDE" "$@" >"$LOG" 2>&1
}

# run_hold <args...> — run fclaude in the FOREGROUND (so SIGINT isn't ignored by
# POSIX's async-child rule) with a background watchdog that Ctrl+Cs it once it
# reports that it is holding the mounts.
run_hold() {
  reset_run
  (
    local i pid=""
    for ((i = 0; i < 300; i++)); do
      if [[ -s "$PIDFILE" ]] && grep -q 'Press Ctrl+C' "$LOG" 2>/dev/null; then
        pid="$(cat "$PIDFILE")"; break
      fi
      sleep 0.05
    done
    [[ -n "$pid" ]] || exit 0
    kill -INT "$pid" 2>/dev/null
    for ((i = 0; i < 60; i++)); do
      kill -0 "$pid" 2>/dev/null || exit 0
      sleep 0.05
    done
    # SIGINT ignored — happens when the suite itself was started in the
    # background. Same trap, same teardown, so the assertions still hold.
    kill -TERM "$pid" 2>/dev/null
  ) &
  local wd=$!
  bash "$FCLAUDE" "$@" >"$LOG" 2>&1
  local rc=$?
  wait "$wd" 2>/dev/null
  return $rc
}

# --- happy path: two dirs, held, then released in reverse order ------------
run_hold --mount "$SHARE/a" "$SHARE/b"
rc=$?
rec="$(cat "$REC")"; out="$(cat "$LOG")"
check "two dirs: exits cleanly on Ctrl+C" "0" "$rc"
check "two dirs: first mounted" "mount $SHARE/a" "$rec"
check "two dirs: second mounted" "mount $SHARE/b" "$rec"
check "two dirs: VM path reported" "/home/fclaude/a" "$out"
check "two dirs: waits for Ctrl+C" "Press Ctrl+C" "$out"
check "two dirs: released in reverse order" \
  "unmount $SHARE/b
unmount $SHARE/a" "$rec"

# --- repeats collapse to a single mount ------------------------------------
run_hold --mount "$SHARE/a" "$SHARE/a" "$SHARE/a/../a"
rec="$(cat "$REC")"; out="$(cat "$LOG")"
check "repeats: one workspace reported" "1 workspace(s) mounted" "$out"
check "repeats: exactly one mount call" "1" "$(grep -c "^mount " "$REC")"
check "repeats: exactly one unmount call" "1" "$(grep -c "^unmount " "$REC")"

# --- relative paths resolve against the cwd --------------------------------
( cd "$SHARE" && run_hold --mount a )
check "relative path: resolved to abs" "mount $SHARE/a" "$(cat "$REC")"

# --- no directories --------------------------------------------------------
run_fail --mount
rc=$?
check "no dirs: exits non-zero" "1" "$rc"
check "no dirs: explains itself" "needs at least one directory" "$(cat "$LOG")"

# --- nonexistent dir: fails before anything is mounted ---------------------
run_fail --mount "$SHARE/a" "$SHARE/nope"
rec="$(cat "$REC")"
check "missing dir: reported" "does not exist" "$(cat "$LOG")"
check_not "missing dir: nothing mounted" "mount " "$rec"
check_not "missing dir: VM not started" "vm-up" "$rec"

# --- dir outside the share root is refused ---------------------------------
run_fail --mount "$TMP/outside"
check "outside share root: refused" "must be under" "$(cat "$LOG")"
check_not "outside share root: nothing mounted" "mount " "$(cat "$REC")"

# --- a mount failing partway releases what was already held ----------------
run_fail --mount "$SHARE/a" "$SHARE/fail"
rec="$(cat "$REC")"
check "partial failure: first was mounted" "mount $SHARE/a" "$rec"
check "partial failure: reported" "simulated mount failure" "$(cat "$LOG")"
check "partial failure: first released" "unmount $SHARE/a" "$rec"
check_not "partial failure: failed one not released" "unmount $SHARE/fail" "$rec"

printf '\n%d passed, %d failed\n' "$pass" "$fail"
[[ "$fail" -eq 0 ]]
