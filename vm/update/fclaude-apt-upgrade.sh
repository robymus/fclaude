#!/usr/bin/env bash
# fclaude weekly apt/system-package upgrade (runs IN the VM as root, invoked by
# the fclaude-apt-upgrade.service oneshot behind a weekly timer).
#
# Claude Code is NOT touched here — it updates itself via the native installer's
# background auto-updater. This script keeps the rest of the VM current: apt,
# which also advances Docker (get.docker.com's apt repo) and Node (NodeSource).
#
# Network-speed gated: on a slow/mobile/offline link the (potentially large) apt
# upgrade is skipped so it can't clog the host's uplink — the VM's uplink IS the
# host's connection (NAT), so an in-VM probe reflects real bandwidth. The probe
# is size- AND time-capped, so even on a metered link it costs ~1 MB. Bypass the
# gate with FCLAUDE_UPDATE_FORCE=1 (set by `fclaude --update`).
#
# Config comes from /etc/default/fclaude-update (EnvironmentFile); every value
# has a built-in default here so the script is safe to run standalone.
set -uo pipefail

STAMP=/var/lib/fclaude/last-apt-upgrade    # latest ATTEMPT:  "<outcome> <iso-ts>"
OKSTAMP=/var/lib/fclaude/last-apt-success  # latest SUCCESS:  "<iso-ts>" (drives the staleness warning)
LOG=/var/log/fclaude-apt-upgrade.log

: "${FCLAUDE_UPDATE_MIN_MBIT:=100}"
: "${FCLAUDE_UPDATE_PROBE_URL:=https://speed.cloudflare.com/__down?bytes=10000000}"
: "${FCLAUDE_UPDATE_PROBE_MAXTIME:=5}"
: "${FCLAUDE_UPDATE_FORCE:=0}"

mkdir -p "$(dirname "$STAMP")"
exec > >(tee -a "$LOG") 2>&1
echo "=== fclaude-apt-upgrade $(date -Is) ==="

# --- Network-speed gate ---------------------------------------------------
# Skipped when forced (manual `fclaude --update`) or when the threshold is 0.
if [ "$FCLAUDE_UPDATE_FORCE" != "1" ] && [ "${FCLAUDE_UPDATE_MIN_MBIT:-0}" != "0" ]; then
  # speed_download is bytes/sec over the transfer; range/bytes-capped at ~10 MB
  # and time-capped, whichever hits first. Offline/curl-error -> 0 -> skip.
  bps="$(curl -fsS -o /dev/null --max-time "$FCLAUDE_UPDATE_PROBE_MAXTIME" \
         -w '%{speed_download}' "$FCLAUDE_UPDATE_PROBE_URL" 2>/dev/null || echo 0)"
  mbit="$(awk -v b="${bps:-0}" 'BEGIN{printf "%.0f", (b*8)/1000000}')"
  echo "probe: ${mbit} Mbit/s (threshold ${FCLAUDE_UPDATE_MIN_MBIT} Mbit/s)"
  if [ "${mbit:-0}" -lt "$FCLAUDE_UPDATE_MIN_MBIT" ]; then
    echo "network too slow or offline — skipping apt upgrade (will retry next week / on 'fclaude --update')"
    printf 'skipped(slow:%sMbit) %s\n' "${mbit:-0}" "$(date -Is)" > "$STAMP"
    echo "=== done rc=0 (skipped) ==="
    exit 0   # a skip is success: nothing went wrong, we just deferred
  fi
fi

# Respect cloud-init / any concurrent apt (site.yml's lock-wait idiom).
for _ in $(seq 1 150); do fuser /var/lib/dpkg/lock-frontend >/dev/null 2>&1 || break; sleep 2; done

export DEBIAN_FRONTEND=noninteractive
rc=0
# `upgrade` (not dist-upgrade): no kernel/meta churn that could disturb the tuned
# VM; --force-confold keeps our managed configs (netplan, nfs, sudoers) on prompt.
if apt-get update -qq \
   && apt-get -y -o Dpkg::Options::="--force-confold" upgrade; then
  apt-get -y autoremove --purge >/dev/null 2>&1 || true
  echo "apt: upgrade ok"
else
  echo "apt: upgrade FAILED"
  rc=1
fi

if [ "$rc" -eq 0 ]; then
  printf 'ok %s\n' "$(date -Is)" > "$STAMP"
  date -Is > "$OKSTAMP"   # advance the "last success" clock ONLY on a real upgrade
else
  printf 'error %s\n' "$(date -Is)" > "$STAMP"   # leave $OKSTAMP alone so staleness still trips
fi

# Piggyback the weekly, fast-network moment to STAGE a Headroom update (does not
# touch the running proxy; activated later at boot / next idle session).
if [ -x /usr/local/sbin/fclaude-headroom-update.sh ]; then
  /usr/local/sbin/fclaude-headroom-update.sh || true
fi

echo "=== done rc=$rc ==="
exit "$rc"
