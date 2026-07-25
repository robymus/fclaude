#!/usr/bin/env bash
# fclaude-headroom-update — weekly: STAGE the latest headroom-ai into a new
# versioned venv WITHOUT touching the running proxy or `current`. Activation
# happens later (at boot, or on the next idle first-instance) via
# fclaude-headroom-activate. Running a stale proxy until then is acceptable.
#
# Invoked (best-effort) at the end of the weekly, network-gated apt-upgrade run,
# so it only fetches on a fast link. Runs as root.
set -uo pipefail

LOG=/var/log/fclaude-headroom-update.log
exec > >(tee -a "$LOG") 2>&1
echo "=== fclaude-headroom-update $(date -Is) ==="

command -v python3 >/dev/null 2>&1 || { echo "python3 missing; skip"; exit 0; }

ver="$(python3 - <<'PY'
import json, urllib.request
try:
    with urllib.request.urlopen("https://pypi.org/pypi/headroom-ai/json", timeout=20) as r:
        print(json.load(r)["info"]["version"])
except Exception:
    print("")
PY
)"
[ -n "$ver" ] || { echo "could not resolve latest headroom-ai version; skip"; exit 0; }

dest="/opt/headroom/versions/${ver}"
if [ -x "${dest}/bin/headroom" ]; then
  echo "already staged: ${ver}"
  exit 0
fi

echo "staging headroom-ai[proxy] ${ver} ..."
if python3 -m venv "$dest" \
   && "${dest}/bin/pip" install -q --upgrade pip \
   && "${dest}/bin/pip" install -q "headroom-ai[proxy]==${ver}"; then
  echo "staged ${ver} (pending activation at boot / next idle session)"
else
  rm -rf "$dest"
  echo "staging FAILED for ${ver}"
  exit 1
fi
