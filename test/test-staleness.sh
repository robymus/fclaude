#!/usr/bin/env bash
# Unit tests for the pure update-staleness helpers in lib/common.sh
# (_fc_staleness_messages / _fc_days_ago). No VM or network needed.
#
# Run:  bash test/test-staleness.sh
set -uo pipefail

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck source=../lib/common.sh
source "${REPO_DIR}/lib/common.sh"

pass=0 fail=0
NOW=1700000000              # fixed "now" epoch
DAY=86400
DAYS=15                     # threshold
old=$(( NOW - 20*DAY ))     # 20 days ago -> stale
fresh=$(( NOW - 5*DAY ))    # 5 days ago  -> not stale

# check <description> <expected-substring-or-EMPTY> <actual>
check() {
  local desc="$1" want="$2" got="$3"
  if [[ -z "$want" ]]; then
    if [[ -z "$got" ]]; then pass=$((pass+1)); else
      fail=$((fail+1)); printf 'FAIL: %s\n  wanted: (no output)\n  got:    %s\n' "$desc" "$got"; fi
  else
    if [[ "$got" == *"$want"* ]]; then pass=$((pass+1)); else
      fail=$((fail+1)); printf 'FAIL: %s\n  wanted substring: %s\n  got:              %s\n' "$desc" "$want" "$got"; fi
  fi
}

# --- _fc_days_ago ---------------------------------------------------------
check "days_ago numeric" "20 days" "$(_fc_days_ago "$old" "$NOW")"
check "days_ago unknown" "an unknown time" "$(_fc_days_ago "-" "$NOW")"
check "days_ago non-numeric->unknown" "an unknown time" "$(_fc_days_ago "garbage" "$NOW")"

# --- _fc_staleness_messages ----------------------------------------------
# args: now days apt_enabled apt_ok attempt claude_epoch
check "all fresh -> silent" "" \
  "$(_fc_staleness_messages "$NOW" "$DAYS" 1 "$fresh" "ok" "$fresh")"

check "apt stale, attempt ok -> apt warning, no reason" "system packages not upgraded in 20 days;" \
  "$(_fc_staleness_messages "$NOW" "$DAYS" 1 "$old" "ok" "$fresh")"

check "apt never run -> 'no successful upgrade'" "no successful upgrade recorded yet" \
  "$(_fc_staleness_messages "$NOW" "$DAYS" 1 "-" "-" "$fresh")"

check "apt stale + error attempt -> FAILED reason" "last attempt FAILED" \
  "$(_fc_staleness_messages "$NOW" "$DAYS" 1 "$old" "error" "$fresh")"

check "apt stale + skipped attempt -> slow/offline reason" "slow or offline network" \
  "$(_fc_staleness_messages "$NOW" "$DAYS" 1 "$old" "skipped" "$fresh")"

check "apt disabled -> no apt warning even when stale" "" \
  "$(_fc_staleness_messages "$NOW" "$DAYS" 0 "$old" "error" "$fresh")"

check "claude stale -> claude warning" "Claude Code has not updated in 20 days" \
  "$(_fc_staleness_messages "$NOW" "$DAYS" 1 "$fresh" "ok" "$old")"

check "claude unknown -> no claude warning" "" \
  "$(_fc_staleness_messages "$NOW" "$DAYS" 0 "$fresh" "ok" "-")"

check "non-numeric apt_ok treated as unknown -> warns" "no successful upgrade recorded yet" \
  "$(_fc_staleness_messages "$NOW" "$DAYS" 1 "bogus" "-" "$fresh")"

# both stale -> two lines
both="$(_fc_staleness_messages "$NOW" "$DAYS" 1 "$old" "skipped" "$old")"
check "both stale -> apt line" "system packages not upgraded" "$both"
check "both stale -> claude line" "Claude Code has not updated" "$both"

# set -e safety: a non-stale call must not abort the script
( set -e; _fc_staleness_messages "$NOW" "$DAYS" 1 "$fresh" "ok" "$fresh" >/dev/null; ) \
  && check "set -e safe on non-stale" "" "" \
  || check "set -e safe on non-stale" "NO-ABORT" "ABORTED"

printf '\n%d passed, %d failed\n' "$pass" "$fail"
[[ "$fail" -eq 0 ]]
