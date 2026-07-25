#!/usr/bin/env bash
# fclaude-headroom-activate — switch the Headroom proxy to the newest STAGED
# version, but only when it's safe to restart. Runs as root.
#
# Called:
#   1. at boot by fclaude-headroom-activate.service (ordered Before the proxy —
#      the service isn't active yet, so this only repoints the symlink; the proxy
#      then starts on the newest version), and
#   2. on demand by the fclaude wrapper when NO Claude session is running (the
#      "first instance") — so a restart never disrupts a live session.
#
# "Pending" == the newest dir under versions/ differs from `current`. Idempotent.
set -uo pipefail

vroot=/opt/headroom/versions
# shellcheck disable=SC2012  # need `ls -v` version sort; dirs are semver (no odd chars)
newest="$(ls -1v "$vroot" 2>/dev/null | tail -1)"
[ -n "$newest" ] || exit 0
target="$vroot/$newest"

# Keep the CLI symlink pointing THROUGH `current` (stable across activations).
ln -sfn /opt/headroom/current/bin/headroom /usr/local/bin/headroom 2>/dev/null || true

cur="$(readlink -f /opt/headroom/current 2>/dev/null || true)"
if [ "$cur" != "$target" ]; then
  ln -sfn "$target" /opt/headroom/current
  echo "fclaude-headroom-activate: activated ${newest}"
  # Restart only if the proxy is already running (at boot it isn't yet).
  if systemctl is-active --quiet fclaude-headroom; then
    systemctl restart fclaude-headroom
  fi
fi

# Prune all but the two newest versions (keep one rollback target).
# shellcheck disable=SC2012  # version sort; semver dir names
ls -1v "$vroot" 2>/dev/null | head -n -2 | while read -r v; do
  rm -rf "${vroot:?}/${v:?}"
done
exit 0
