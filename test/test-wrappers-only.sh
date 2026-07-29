#!/usr/bin/env bash
# Tests for `install.sh --wrappers-only`.
#
# Every fclaude path derives from $HOME (lib/constants.sh), so the whole mode
# can be exercised for real against a throwaway HOME: it must copy the host
# scripts there and do NOTHING else — no sudo, no virsh, no apt, no network, no
# VM disk. That "nothing else" is the entire point of the mode, so it is what
# these tests assert.
#
# Run:  bash test/test-wrappers-only.sh
set -uo pipefail

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
pass=0 fail=0

ok() {
  local desc="$1" cond="$2"
  if [[ "$cond" == "yes" ]]; then pass=$((pass + 1)); else
    fail=$((fail + 1)); printf 'FAIL: %s\n' "$desc"
  fi
}
eq() {
  local desc="$1" want="$2" got="$3"
  if [[ "$got" == "$want" ]]; then pass=$((pass + 1)); else
    fail=$((fail + 1)); printf 'FAIL: %s\n  wanted: %s\n  got:    %s\n' "$desc" "$want" "$got"
  fi
}

FAKE="$(mktemp -d)"
trap 'rm -rf "$FAKE"' EXIT

# Shims that make the test FAIL LOUDLY if the mode reaches for privileged or
# slow machinery. They sit ahead of the real tools on PATH.
shimdir="${FAKE}/shims"
mkdir -p "$shimdir"
for tool in sudo virsh apt-get curl qemu-img ssh ssh-keygen genisoimage cloud-localds; do
  printf '#!/bin/sh\necho "FORBIDDEN: %s called" >&2\nexit 111\n' "$tool" >"${shimdir}/${tool}"
  chmod 755 "${shimdir}/${tool}"
done

run_out="$(HOME="$FAKE" PATH="${shimdir}:$PATH" bash "${REPO_DIR}/install.sh" --wrappers-only 2>&1)"
run_rc=$?

eq "exits 0" "0" "$run_rc"
ok "never invokes a privileged/slow tool" \
  "$([[ "$run_out" != *FORBIDDEN* ]] && echo yes || echo no)"

# --- the files it must install --------------------------------------------
for w in fclaude fclaude-vscode fclaude-grow-disk fclaude-update-setup; do
  ok "installs $w executable" \
    "$([[ -x "${FAKE}/.local/bin/${w}" ]] && echo yes || echo no)"
  ok "installed $w matches the repo copy" \
    "$(cmp -s "${REPO_DIR}/host/bin/${w}" "${FAKE}/.local/bin/${w}" && echo yes || echo no)"
done
for l in common.sh constants.sh; do
  ok "installs lib/$l" \
    "$(cmp -s "${REPO_DIR}/lib/${l}" "${FAKE}/.local/lib/fclaude/${l}" && echo yes || echo no)"
done

# --- what it must NOT touch ------------------------------------------------
# A full install creates these; --wrappers-only must not, or a stray run could
# start conjuring VM state (or a 32 GiB disk) behind the user's back.
for unwanted in .fclaude/fclaude-disk .fclaude/seed.iso .fclaude/ssh .fclaude/config .fclaude/vm; do
  ok "does not create ${unwanted}" \
    "$([[ ! -e "${FAKE}/${unwanted}" ]] && echo yes || echo no)"
done

# --- config drift reporting ------------------------------------------------
ok "warns when there is no config yet" \
  "$([[ "$run_out" == *"no config at"* ]] && echo yes || echo no)"

# With a config that predates a knob, the missing key is named with its default
# so it can be pasted straight in — and a knob that IS present stays quiet.
mkdir -p "${FAKE}/.fclaude"
printf 'FCLAUDE_UPDATE_APT="1"\n' >"${FAKE}/.fclaude/config"
drift_out="$(HOME="$FAKE" PATH="${shimdir}:$PATH" bash "${REPO_DIR}/install.sh" --wrappers-only 2>&1)"
ok "names a knob the config predates" \
  "$([[ "$drift_out" == *"FCLAUDE_UPDATE_WARN_DAYS="* ]] && echo yes || echo no)"
ok "does not re-list a knob the config already has" \
  "$([[ "$drift_out" != *"FCLAUDE_UPDATE_APT="* ]] && echo yes || echo no)"
ok "no longer warns about a missing config" \
  "$([[ "$drift_out" != *"no config at"* ]] && echo yes || echo no)"

# A config carrying every knob must produce no drift noise at all.
sed -n 's/^\([A-Z_][A-Z_0-9]*\)=.*/\1="x"/p' "${REPO_DIR}/host/config.template" \
  >"${FAKE}/.fclaude/config"
full_out="$(HOME="$FAKE" PATH="${shimdir}:$PATH" bash "${REPO_DIR}/install.sh" --wrappers-only 2>&1)"
ok "silent when the config is up to date" \
  "$([[ "$full_out" != *"predates these knobs"* ]] && echo yes || echo no)"

# --- option handling -------------------------------------------------------
help_out="$(HOME="$FAKE" bash "${REPO_DIR}/install.sh" --help 2>&1)"
ok "--help documents the mode" \
  "$([[ "$help_out" == *"--wrappers-only"* ]] && echo yes || echo no)"

bad_out="$(HOME="$FAKE" PATH="${shimdir}:$PATH" bash "${REPO_DIR}/install.sh" --nope 2>&1)"
bad_rc=$?
eq "rejects an unknown option" "1" "$bad_rc"
ok "explains the rejection" \
  "$([[ "$bad_out" == *"unknown option"* ]] && echo yes || echo no)"

printf '\n%d passed, %d failed\n' "$pass" "$fail"
((fail == 0))
