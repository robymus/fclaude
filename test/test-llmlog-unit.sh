#!/usr/bin/env bash
# Tests for the llm.log bridge systemd unit templates.
#
# The socket unit exists to expose llm.log (host loopback only, hard-coded) on
# the libvirt gateway so the VM can reach it. Its one recurring failure mode is
# an ORDERING CYCLE, and it has bitten twice:
#
#   sockets.target -> fclaude-llmlog.socket -> <some service>
#                  -> basic.target -> sockets.target
#
# A socket unit is Before=sockets.target by default, and every service is
# After=basic.target which is After=sockets.target — so ordering this socket
# after ANY service closes the loop. systemd breaks the cycle by deleting one
# job in it, chosen effectively at random per boot: once the victim was
# nfs-server.service, once the socket's own start job (leaving it "enabled" but
# dead, and the VM unable to reach llm.log). Both are silent at the point of
# use, which is why this is a test and not just a comment in the unit.
#
# Run:  bash test/test-llmlog-unit.sh
set -uo pipefail

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SOCKET="${REPO_DIR}/host/systemd/fclaude-llmlog.socket"
SERVICE="${REPO_DIR}/host/systemd/fclaude-llmlog.service"
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

# Directives only: strip comments and blank lines, so the prose above a unit's
# [Unit] section (which deliberately NAMES the forbidden directives) can never
# trip these assertions.
directives() { grep -vE '^[[:space:]]*(#|;)' "$1" | grep -vE '^[[:space:]]*$'; }

ok "socket template exists"  "$([[ -r "$SOCKET"  ]] && echo yes || echo no)"
ok "service template exists" "$([[ -r "$SERVICE" ]] && echo yes || echo no)"

sock_d="$(directives "$SOCKET")"

# --- The cycle guard -------------------------------------------------------
# Any After=/Wants=/Requires=/BindsTo= naming a .service (or network-online,
# which pulls one in via its own Wants=) re-creates the cycle.
offenders="$(printf '%s\n' "$sock_d" \
  | grep -iE '^[[:space:]]*(After|Wants|Requires|Requisite|BindsTo|PartOf)=' \
  | grep -iE '\.service|network-online' || true)"
eq "socket orders against no service unit (ordering-cycle guard)" "" "$offenders"

# --- What makes that safe --------------------------------------------------
# FreeBind is the reason no libvirt ordering is needed: it permits binding the
# gateway address before the bridge exists. Without it, dropping the ordering
# would trade a cycle for a bind failure at boot.
ok "socket sets FreeBind=true" \
  "$(printf '%s\n' "$sock_d" | grep -qiE '^[[:space:]]*FreeBind=(true|yes|1)[[:space:]]*$' && echo yes || echo no)"

ok "socket is WantedBy=sockets.target" \
  "$(printf '%s\n' "$sock_d" | grep -qE '^[[:space:]]*WantedBy=sockets\.target[[:space:]]*$' && echo yes || echo no)"

# --- Exposure ---------------------------------------------------------------
# Listen on the gateway placeholder ONLY. A wildcard bind would put llm.log on
# the host LAN, not just the VM subnet.
ok "socket listens on the gateway placeholder" \
  "$(printf '%s\n' "$sock_d" \
     | grep -qE '^[[:space:]]*ListenStream=__FCLAUDE_HOST_IP__:__FCLAUDE_LLMLOG_PORT__[[:space:]]*$' && echo yes || echo no)"

wildcard="$(printf '%s\n' "$sock_d" \
  | grep -iE '^[[:space:]]*ListenStream=' \
  | grep -E '=(0\.0\.0\.0|\[?::\]?)?:|=[0-9]+[[:space:]]*$' || true)"
eq "socket never binds a wildcard address" "" "$wildcard"

# --- Service side -----------------------------------------------------------
# The service is socket-activated; it must stay bound to the socket and proxy
# to host loopback (llm.log's hard-coded bind).
svc_d="$(directives "$SERVICE")"

ok "service requires its socket" \
  "$(printf '%s\n' "$svc_d" | grep -qE '^[[:space:]]*Requires=fclaude-llmlog\.socket[[:space:]]*$' && echo yes || echo no)"

ok "service proxies to host loopback" \
  "$(printf '%s\n' "$svc_d" \
     | grep -qE '^[[:space:]]*ExecStart=__FCLAUDE_SOCKET_PROXYD__ 127\.0\.0\.1:__FCLAUDE_LLMLOG_PORT__[[:space:]]*$' && echo yes || echo no)"

# The service is NOT Before=sockets.target, so ordering it after the socket is
# correct and cycle-free — the asymmetry with the socket unit is deliberate.
ok "service may order after its socket" \
  "$(printf '%s\n' "$svc_d" | grep -qE '^[[:space:]]*After=fclaude-llmlog\.socket[[:space:]]*$' && echo yes || echo no)"

# --- Placeholders ------------------------------------------------------------
# fclaude-llmlog-setup substitutes exactly these; an unrendered leftover would
# ship a literal placeholder into /etc/systemd/system.
for tmpl in "$SOCKET" "$SERVICE"; do
  leftover="$(grep -oE '__[A-Z_]+__' "$tmpl" | sort -u \
    | grep -vE '^__(FCLAUDE_HOST_IP|FCLAUDE_LLMLOG_PORT|FCLAUDE_SOCKET_PROXYD)__$' || true)"
  eq "$(basename "$tmpl"): no unknown placeholders" "" "$leftover"
done

printf '\n%d passed, %d failed\n' "$pass" "$fail"
[[ "$fail" -eq 0 ]]
