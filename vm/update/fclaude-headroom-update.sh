#!/usr/bin/env bash
# fclaude-headroom-update — weekly maintenance for the Headroom toolchain:
#
#   1. STAGE the latest headroom-ai into a new versioned venv WITHOUT touching
#      the running proxy or `current`. Activation happens later (at boot, or on
#      the next idle first-instance) via fclaude-headroom-activate. Running a
#      stale proxy until then is acceptable.
#   2. UPGRADE the tokensave code-graph binary, but only while no session is
#      running (it replaces a binary a live MCP server is executing).
#
# Invoked (best-effort) at the end of the weekly, network-gated apt-upgrade run,
# so it only fetches on a fast link. Runs as root.
set -uo pipefail

VM_USER=fclaude   # owns ~/.local/bin/tokensave; matches fclaude-headroom.service

LOG=/var/log/fclaude-headroom-update.log
exec > >(tee -a "$LOG") 2>&1
echo "=== fclaude-headroom-update $(date -Is) ==="

# --- 1. Headroom proxy (stage only) -----------------------------------------
stage_headroom() {
  command -v python3 >/dev/null 2>&1 || { echo "python3 missing; skip"; return 0; }

  local ver dest
  ver="$(python3 - <<'PY'
import json, urllib.request
try:
    with urllib.request.urlopen("https://pypi.org/pypi/headroom-ai/json", timeout=20) as r:
        print(json.load(r)["info"]["version"])
except Exception:
    print("")
PY
  )"
  [ -n "$ver" ] || { echo "could not resolve latest headroom-ai version; skip"; return 0; }

  dest="/opt/headroom/versions/${ver}"
  if [ -x "${dest}/bin/headroom" ]; then
    echo "already staged: ${ver}"
    return 0
  fi

  echo "staging headroom-ai[proxy] ${ver} ..."
  if python3 -m venv "$dest" \
     && "${dest}/bin/pip" install -q --upgrade pip \
     && "${dest}/bin/pip" install -q "headroom-ai[proxy]==${ver}"; then
    echo "staged ${ver} (pending activation at boot / next idle session)"
  else
    rm -rf "$dest"
    echo "staging FAILED for ${ver}"
    return 1
  fi
}

# --- 2. tokensave code-graph binary -----------------------------------------
# tokensave is installed ONCE — headroom's ensure_tokensave() returns early
# whenever the binary is already present — so without this it stays pinned at
# whatever version first landed (ours sat seven minor releases behind, on an
# indexer whose whole job is graph quality). It self-updates from GitHub.
#
# Run as $VM_USER: the binary lives in that account's ~/.local/bin, and a login
# shell is what puts it on PATH. Skipped while any Claude session is running —
# the upgrade would replace the binary a live `tokensave serve` MCP process is
# executing. Same idle rule as fclaude-headroom-activate; a skipped week is
# harmless, the next run catches it.
upgrade_tokensave() {
  if pgrep -x claude >/dev/null 2>&1; then
    echo "tokensave: Claude session active; skipping upgrade"
    return 0
  fi
  local as_user=(runuser -u "$VM_USER" -- bash -lc)
  if ! "${as_user[@]}" 'command -v tokensave' >/dev/null 2>&1; then
    echo "tokensave: not installed; nothing to upgrade"
    return 0
  fi
  local before after
  before="$("${as_user[@]}" 'tokensave --version' 2>/dev/null)"
  echo "tokensave: ${before} — checking for a newer release ..."
  if ! timeout 300 "${as_user[@]}" 'tokensave upgrade'; then
    echo "tokensave: upgrade failed or timed out (non-fatal)"
    return 0
  fi
  after="$("${as_user[@]}" 'tokensave --version' 2>/dev/null)"
  echo "tokensave: now ${after}"
  [ "$before" != "$after" ] || return 0

  # A release that adds MCP tools leaves them unpermitted until the agent
  # integration is refreshed — 7.0.2 → 7.7.0 added 81 of them, and Claude Code
  # warns on every call until this runs. `reinstall` targets the USER-scope
  # config (~/.claude/settings.json in the VM); `--local`, the project-scoped
  # variant, is opt-in and deliberately never used here. Non-TTY, so its
  # optional git post-commit hook prompt silently skips.
  echo "tokensave: refreshing agent integration after upgrade ..."
  # shellcheck disable=SC2016  # intentional: $HOME must expand in $VM_USER's
  # login shell (we run as root), and cd'ing there keeps reinstall away from any
  # mounted workspace — i.e. away from the user's real repos.
  timeout 120 "${as_user[@]}" 'cd "$HOME" && tokensave reinstall' >/dev/null 2>&1 \
    && echo "tokensave: agent integration refreshed" \
    || echo "tokensave: reinstall failed (tools may warn until it is rerun)"
}

rc=0
stage_headroom || rc=1
upgrade_tokensave
exit "$rc"
