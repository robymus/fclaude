#!/usr/bin/env bash
# Tests that NFSv4 delegations stay disabled on the host.
#
# The behaviour itself needs a live nfsd, so this guards the wiring: the sysctl
# drop-in must exist, be applied BEFORE nfs-server restarts (so held delegations
# go away too), and be reverted by uninstall.
#
# Run:  bash test/test-nfs-delegations.sh
set -uo pipefail

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
pass=0 fail=0

ok() {
  local desc="$1" cond="$2"
  if [[ "$cond" == "yes" ]]; then pass=$((pass + 1)); else
    fail=$((fail + 1)); printf 'FAIL: %s\n' "$desc"
  fi
}

first_line() { grep -n -m1 -F -- "$1" "$2" | cut -d: -f1; }

dropin="${REPO_DIR}/host/nfs/fclaude-sysctl.conf"
install_sh="${REPO_DIR}/install.sh"
uninstall_sh="${REPO_DIR}/uninstall.sh"
target="/etc/sysctl.d/90-fclaude-nfs.conf"

ok "drop-in disables leases" \
  "$(grep -Eq '^fs\.leases-enable[[:space:]]*=[[:space:]]*0$' "$dropin" && echo yes || echo no)"
ok "install deploys the drop-in" \
  "$(grep -q "fclaude-sysctl.conf\" ${target}" "$install_sh" && echo yes || echo no)"

load_at="$(first_line "sysctl --quiet --load ${target}" "$install_sh")"
restart_at="$(first_line 'systemctl restart nfs-server' "$install_sh")"
ok "install loads the sysctl before restarting nfs-server" \
  "$([[ -n "$load_at" && -n "$restart_at" && "$load_at" -lt "$restart_at" ]] && echo yes || echo no)"

ok "uninstall removes the drop-in" \
  "$(grep -qF "$target" "$uninstall_sh" && echo yes || echo no)"
ok "uninstall restores leases" \
  "$(grep -qF 'fs.leases-enable=1' "$uninstall_sh" && echo yes || echo no)"

printf '%d passed, %d failed\n' "$pass" "$fail"
((fail == 0))
