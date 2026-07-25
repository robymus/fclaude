# fclaude auto-update — PLAN

Keep **Claude Code** (and optionally the whole VM: apt packages, Docker) automatically up to date on
at least a **weekly** cadence — safely, even though **several `claude` instances run continuously**
in the VM.

## The core problem, restated

v1 of this plan reused the current install method (`npm install -g @anthropic-ai/claude-code`) and
guarded updates behind "defer if a `claude` process is running." That guard exists because
`npm install -g` is **not atomic** — it `rm -rf`s the old package dir and extracts the new one in
place; a live session that lazy-loads a bundled asset (ripgrep, a wasm blob, an
occasionally-required module) *during* that window can crash. Real risk, small but not theoretical.

With an **always-on** workflow (multiple persistent sessions), "defer while running" ≈ "never
update." Wrong trade. **The fix is to change the install method, not to defer.**

## Decision: switch to the native Claude Code installer

The native installer is designed for exactly this. Confirmed against current docs
(`code.claude.com/docs/en/setup.md`):

- Installs a self-contained binary to **`~/.local/bin/claude`**, a symlink into
  **`~/.local/share/claude/versions/{VERSION}/`**.
- Updates create a **new versioned directory** and **atomically repoint the symlink**. A process
  already running an old version keeps its files on disk → **concurrent instances are safe by
  design during an update.** No defer guard needed. This is the whole reason to switch.
- **No Node.js dependency** — the binary does not invoke Node. We can drop the NodeSource / Node 22
  install that currently exists *solely* to host the npm global (see trade-off on keeping Node).
- **Per-user** install (`~/.local`, `~/.claude`) — so it must run as the **`fclaude`** user (uid
  1000), not root like today's npm global. Auth in `~/.claude/` persists across the method switch.
- Built-in **background auto-updater**, ON by default; `DISABLE_AUTOUPDATER=1` turns it off;
  `claude update` triggers on demand (exit 0 whether it updated or was already latest).

### Bonus: the PATH plumbing already exists

Every wrapper invocation of `claude` is prefixed with `CLAUDE_LOGIN_ENV` (from the recent
"claude login PATH over ssh" fix, `lib/common.sh:78`):
```sh
CLAUDE_LOGIN_ENV='[ -f "$HOME/.profile" ] && . "$HOME/.profile";'
```
Ubuntu 24.04's stock `~/.profile` prepends `~/.local/bin` to `PATH` when that dir exists. So once
native `claude` lands in `~/.local/bin`, the existing mechanism resolves it over non-interactive ssh
**with no wrapper changes** (`host/bin/fclaude:108,110`, `host/bin/fclaude-vscode:54,73`). This makes
the migration low-risk.

## How Claude Code stays updated — decided

Because the native installer makes hot-updates safe, the "keep Claude updated" problem largely
**dissolves**:

- **Claude Code freshness → the native installer's own background auto-updater** (**chosen: option A**),
  on the **`latest`** release channel (updated from an initial `stable` pin per user preference — the
  VM should track the newest release like the host, no version skew; "on the edge").
  Zero custom code, zero defer logic, upstream-blessed hot-swap. It keeps Claude fresh continuously
  (≥ weekly by definition) and running sessions adopt the new version on their next launch.
- **"All packages" (apt / Docker / OS) → a weekly `systemd` timer**, **network-speed gated** (below).
  This is the only piece with custom machinery, and apt has **no live-`claude` hazard**, so no guard
  is needed there either.

This eliminates the defer-while-running guard entirely, which was the whole point.

## Files to add / modify

```
vm/ansible/site.yml           # replace npm-global install with native installer (as fclaude);
                              #   drop the "Install Claude Code globally" npm task;
                              #   set stable channel + settings.json env; add weekly apt timer
vm/update/fclaude-apt-upgrade.sh          # NEW — weekly system-package upgrade (opt-in)
vm/update/fclaude-apt-upgrade.service.tmpl # NEW — oneshot
vm/update/fclaude-apt-upgrade.timer.tmpl   # NEW — weekly, Persistent
host/bin/fclaude              # NEW --update flag; call fclaude_staleness_warn in startup path
host/bin/fclaude-update-setup             # NEW — retrofit the EXISTING running VM (migration)
lib/constants.sh              # FCLAUDE_UPDATE_APT / _MIN_MBIT / _PROBE_URL / _PROBE_MAXTIME / _WARN_DAYS
lib/common.sh                 # fclaude_update() + fclaude_staleness_warn(); fclaude_status version/stamp lines
install.sh / uninstall.sh     # ship/remove fclaude-update-setup + units
README.md / CODEBASE.md       # document method switch, channel, knobs, --status line
```

## Ansible change — `vm/ansible/site.yml`

Replace the Node-hosted npm global with the native installer, run **as `fclaude`**.

```yaml
    # --- Claude Code (native installer; per-user, hot-update-safe) ---
    - name: Install Claude Code via the native installer (stable channel)
      become: true
      become_user: "{{ fclaude_user }}"
      ansible.builtin.shell: |
        set -o pipefail
        curl -fsSL https://claude.ai/install.sh | bash -s stable
      args:
        creates: "/home/{{ fclaude_user }}/.local/bin/claude"
        executable: /bin/bash

    - name: Ensure Claude settings pin the stable channel (and, if chosen, disable autoupdate)
      become: true
      become_user: "{{ fclaude_user }}"
      ansible.builtin.copy:
        dest: "/home/{{ fclaude_user }}/.claude/settings.json"
        # NOTE: merge, don't clobber, if a settings.json already exists (use a JSON-merge task).
        content: |
          {
            "autoUpdatesChannel": "stable"
          }
        mode: '0644'
```

Notes:
- `~/.local/bin` on PATH is already handled by stock `~/.profile` + `CLAUDE_LOGIN_ENV` (above); no
  extra PATH task needed for the wrappers. (A belt-and-suspenders `.profile` check can be added.)
- **Node question:** the "Install Node.js" + "Add NodeSource repo" tasks currently exist *only* to
  host the npm global. With the native installer they're no longer required **for Claude**. Keep
  Node **only if** the sandboxed agent needs `node`/`npm` for user project work (very likely, given
  it's a general dev sandbox). → Recommend **keep Node** but decouple it conceptually from Claude;
  the npm `@anthropic-ai/claude-code` task is removed regardless. (Decision flagged for you below.)
- If the deterministic alternative is chosen, add `"env": { "DISABLE_AUTOUPDATER": "1" }` to
  settings.json and include `claude update` in the weekly timer script.

## Weekly apt timer (optional "all packages"), network-speed gated

Config knobs in `lib/constants.sh` → baked into `~/.fclaude/config`, read from
`/etc/default/fclaude-update` in the VM:
```bash
FCLAUDE_UPDATE_APT="1"          # 1 = weekly apt upgrade (Docker/OS, and Node if kept); 0 = off
FCLAUDE_UPDATE_MIN_MBIT="100"   # skip apt if measured downlink < this (Mbit/s)
FCLAUDE_UPDATE_PROBE_URL="https://speed.cloudflare.com/__down?bytes=10000000"
FCLAUDE_UPDATE_PROBE_MAXTIME="5"  # seconds; caps probe cost on slow links
FCLAUDE_UPDATE_WARN_DAYS="15"   # warn at session start if last SUCCESSFUL update older than this
```

### The network-speed gate — why this shape

The timer runs **inside the VM**, whose uplink is the host's real connection (NAT through the virtio
NIC), so an **in-VM probe genuinely reflects the host's bandwidth** — it stays self-contained and
still "sees" that you're on mobile/travelling.

The probe is **both size- and time-capped** so it never clogs a metered link: a range/`bytes`-capped
GET (~10 MB) with `--max-time 5`. On a fast link the 10 MB finish in ~1 s and measure high; on a
slow 2 Mbit link `--max-time` aborts after transferring only ~1 MB and measures low; offline → curl
fails → skip. Cloudflare's `speed.cloudflare.com/__down?bytes=N` is a purpose-built, auth-free,
range-capable endpoint (default; overridable). The probe's worst-case cost (~10 MB on a *fast*,
presumably unmetered link) is negligible and far smaller than the apt upgrade it gates.

> Trade-off vs. alternatives: reusing `apt-get update`'s own throughput as the speedometer would cost
> ~zero extra bytes, but many small index files + connection setup make it a noisy meter that
> under-reports on fast links (false skips). A dedicated single-stream probe is predictable; the ~10 MB
> is the price of that predictability. Tune via `FCLAUDE_UPDATE_PROBE_URL`/`_MAXTIME`, or set
> `FCLAUDE_UPDATE_MIN_MBIT=0` to disable the gate.

`vm/update/fclaude-apt-upgrade.sh` (runs as root, no live-claude hazard):
```bash
#!/usr/bin/env bash
set -uo pipefail
STAMP=/var/lib/fclaude/last-apt-upgrade         # latest ATTEMPT: "<outcome> <iso-ts>"
OKSTAMP=/var/lib/fclaude/last-apt-success       # latest SUCCESS: "<iso-ts>" (drives the staleness warning)
LOG=/var/log/fclaude-apt-upgrade.log
: "${FCLAUDE_UPDATE_MIN_MBIT:=100}"
: "${FCLAUDE_UPDATE_PROBE_URL:=https://speed.cloudflare.com/__down?bytes=10000000}"
: "${FCLAUDE_UPDATE_PROBE_MAXTIME:=5}"
mkdir -p "$(dirname "$STAMP")"
exec > >(tee -a "$LOG") 2>&1
echo "=== fclaude-apt-upgrade $(date -Is) ==="

# --- Network-speed gate (bypassed by `fclaude --update`, which sets FCLAUDE_UPDATE_FORCE=1) ---
if [ "${FCLAUDE_UPDATE_FORCE:-0}" != "1" ] && [ "${FCLAUDE_UPDATE_MIN_MBIT:-0}" != "0" ]; then
  bps="$(curl -s -o /dev/null --max-time "$FCLAUDE_UPDATE_PROBE_MAXTIME" \
         -w '%{speed_download}' "$FCLAUDE_UPDATE_PROBE_URL" 2>/dev/null || echo 0)"
  mbit="$(awk -v b="${bps:-0}" 'BEGIN{printf "%.0f", b*8/1000000}')"
  echo "probe: ${mbit} Mbit/s (threshold ${FCLAUDE_UPDATE_MIN_MBIT})"
  if [ "${mbit:-0}" -lt "$FCLAUDE_UPDATE_MIN_MBIT" ]; then
    echo "network too slow / offline — skipping apt upgrade"
    printf 'skipped(slow:%sMbit) %s\n' "${mbit:-0}" "$(date -Is)" > "$STAMP"
    exit 0                                 # success: try again next week / on --update
  fi
fi

# Respect cloud-init / other apt (site.yml's lock-wait idiom).
for _ in $(seq 1 150); do fuser /var/lib/dpkg/lock-frontend >/dev/null 2>&1 || break; sleep 2; done

export DEBIAN_FRONTEND=noninteractive
rc=0
if apt-get update -qq && \
   apt-get -y -o Dpkg::Options::="--force-confold" upgrade; then     # upgrade, NOT dist-upgrade
   apt-get -y autoremove --purge >/dev/null || true
   echo "apt: ok"
else
   echo "apt: FAILED"; rc=1
fi
if [ "$rc" -eq 0 ]; then
  printf 'ok %s\n' "$(date -Is)" > "$STAMP"
  date -Is > "$OKSTAMP"        # advance the "last success" clock ONLY on a real upgrade
else
  printf 'error %s\n' "$(date -Is)" > "$STAMP"
fi
exit "$rc"
```

Note the `skipped(slow…)` path above writes only `$STAMP` (attempt), **not** `$OKSTAMP` — so a long
travel stretch of legitimate skips still trips the staleness warning (you *are* out of date), while
the warning text can read the attempt outcome to explain *why*.

`.service.tmpl` (oneshot) + `.timer.tmpl`:
```ini
# service
[Unit]
Description=fclaude weekly apt upgrade
After=network-online.target
Wants=network-online.target
[Service]
Type=oneshot
ExecStart=/usr/local/sbin/fclaude-apt-upgrade.sh
Nice=10
IOSchedulingClass=idle
```
```ini
# timer
[Unit]
Description=Weekly fclaude apt upgrade
[Timer]
OnCalendar=Sun 04:00
RandomizedDelaySec=1h
Persistent=true          # catch up after --shutdown / long sleep
[Install]
WantedBy=timers.target
```

Ansible tasks append these and `systemctl enable --now fclaude-apt-upgrade.timer` **only when**
`FCLAUDE_UPDATE_APT=1`. `upgrade` (not `dist-upgrade`) avoids kernel/meta churn that could disturb
the tuned VM (CPU model, MTU); NodeSource pinning keeps Node on major 22.

## Manual force — `fclaude --update`

A new flag on the `fclaude` wrapper (`host/bin/fclaude`) to update on demand, **bypassing the network
gate** (you asked for it explicitly, so bandwidth is your call). It updates both layers and streams
output — never starts a session, never mounts:

```bash
# in the arg parser, alongside --status / --shutdown:
  --update)  fclaude_update; exit $? ;;
```
```bash
# helper (lib/common.sh):
fclaude_update() {
  ensure_vm_running                     # same guard --status-adjacent ops use
  echo "==> Claude Code (native auto-updater / on-demand)"
  ssh_vm '. "$HOME/.profile"; claude update'          # runs as fclaude; exit 0 if already latest
  echo "==> System packages (apt, gate bypassed)"
  ssh_maint 'sudo FCLAUDE_UPDATE_FORCE=1 /usr/local/sbin/fclaude-apt-upgrade.sh'
}
```

- `claude update` here is belt-and-suspenders — the background auto-updater normally handles Claude;
  this lets you pull *right now*.
- `FCLAUDE_UPDATE_FORCE=1` makes the apt script skip the speed probe (see gate above). We invoke the
  script directly over ssh (not `systemctl start`) precisely so we can pass that env and stream the
  log live.
- Help text (`host/bin/fclaude` usage block) gains: `--update   Update Claude + apt now (ignores network gate).`

## Migrating the EXISTING running VM — `host/bin/fclaude-update-setup`

`site.yml` only runs at first boot, so the live VM needs a one-shot migration (same standalone shape
as `fclaude-llmlog-setup`). It runs **as the `fclaude` user** for the native install:

```bash
#!/usr/bin/env bash
set -euo pipefail
here="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
source "$here/lib/constants.sh"; source "$here/lib/common.sh"; load_config

# 1. Remove the old npm global (as root) so only one claude remains.
ssh_maint 'sudo npm uninstall -g @anthropic-ai/claude-code 2>/dev/null || true'

# 2. Native install as fclaude (idempotent; skips if already present).
ssh_vm 'command -v claude >/dev/null && claude --version || true'   # before
ssh_vm '[ -x "$HOME/.local/bin/claude" ] || curl -fsSL https://claude.ai/install.sh | bash -s stable'
ssh_vm '. "$HOME/.profile"; claude --version'                       # after — confirm ~/.local/bin wins

# 3. (opt-in) push apt-upgrade script + units, enable the weekly timer (as above via ssh_maint/sudo).
```

Gotchas to verify during migration:
- **Two installs briefly coexist** — the wrapper resolves `~/.local/bin/claude` first via `.profile`
  PATH order; the npm global at `/usr/bin/claude` is removed in step 1, so no ambiguity remains.
- **Auth**: `~/.claude/.credentials.json` for `fclaude` is untouched by the method switch — verify a
  session still runs without re-login. (If login was historically done as a *different* user, that's
  a pre-existing quirk to confirm, not introduced here.)
- **Ownership**: never run the native installer as root — it would create root-owned
  `~/.local/share/claude/versions`, breaking the fclaude auto-updater. Always `ssh_vm` (fclaude), not
  `ssh_maint sudo`.

## Observability — `fclaude --status`

Extend `fclaude_status` (`lib/common.sh`) — best-effort, never starts the VM:
```bash
ver="$(ssh_vm '. "$HOME/.profile"; claude --version 2>/dev/null' 2>/dev/null || true)"
[ -n "$ver" ] && printf '  Claude:      %s\n' "$ver"
apt="$(ssh_vm 'cat /var/lib/fclaude/last-apt-upgrade 2>/dev/null' 2>/dev/null || true)"
[ -n "$apt" ] && printf '  Last apt:    %s\n' "$apt"
```

## Startup staleness warning (session start)

`fclaude` already prints setup lines ("mounting …") as it brings a session up. Add a **staleness
check** in that path so a broken/blocked updater can't fail silently: if the **last successful**
update is older than `FCLAUDE_UPDATE_WARN_DAYS` (15), print a loud, self-explaining warning. It runs
after the VM is up (it's starting anyway) and adds one cheap `ssh_vm` round-trip.

Two independent signals, one warning block:
- **apt** (only when `FCLAUDE_UPDATE_APT=1`): age of `/var/lib/fclaude/last-apt-success`. Also read
  `last-apt-upgrade` so the message can say *why* it's stale — `error` (real failure → check
  `journalctl -u fclaude-apt-upgrade`) vs `skipped(slow…)` (just haven't been on a fast network) vs
  no recent run at all (timer off / VM rarely on).
- **Claude Code** (best-effort, no network): mtime of the newest dir under
  `~/.local/share/claude/versions/` — a proxy for "when Claude last changed version." Claude Code
  releases frequently, so >15 days almost certainly means the background auto-updater is stuck, not
  that upstream went quiet. Clearly labelled best-effort.

```bash
# lib/common.sh — called from fclaude's startup path once the VM is reachable.
fclaude_staleness_warn() {
  local days="${FCLAUDE_UPDATE_WARN_DAYS:-15}" now age_s max_s
  [ "$days" -gt 0 ] 2>/dev/null || return 0
  max_s=$(( days * 86400 ))
  # One round-trip: emit "apt <success-epoch|-> <last-attempt-line>" and "claude <versions-mtime|->".
  local out; out="$(ssh_vm '
    s=$(date -r /var/lib/fclaude/last-apt-success +%s 2>/dev/null || echo -);
    a=$(cat /var/lib/fclaude/last-apt-upgrade 2>/dev/null || echo -);
    v=$(ls -1dt "$HOME/.local/share/claude/versions/"*/ 2>/dev/null | head -1);
    m=$( [ -n "$v" ] && date -r "$v" +%s || echo - );
    printf "APT %s | %s\nCLAUDE %s\n" "$s" "$a" "$m"' 2>/dev/null || true)"
  now="$(date +%s)"
  # --- apt (gated on the feature being enabled) ---
  if [ "${FCLAUDE_UPDATE_APT:-1}" = "1" ]; then
    local s="${...parse APT epoch...}" attempt="${...parse attempt line...}"
    if [ "$s" = "-" ] || { age_s=$(( now - s )); [ "$age_s" -gt "$max_s" ]; }; then
      warn "system packages not upgraded in >${days}d (last attempt: ${attempt:-none}); run: fclaude --update"
    fi
  fi
  # --- claude (best-effort) ---
  local m="${...parse CLAUDE epoch...}"
  if [ "$m" != "-" ] && { age_s=$(( now - m )); [ "$age_s" -gt "$max_s" ]; }; then
    warn "Claude Code hasn't updated in >${days}d — auto-updater may be stuck; run: fclaude --update"
  fi
}
```
(`warn` = the wrapper's existing yellow-stderr helper; the `${...parse...}` bits are `awk`/`case`
extraction, left as pseudo for the plan.)

**Avoiding false alarms on a fresh VM:** `site.yml` seeds `/var/lib/fclaude/last-apt-success` with the
provision timestamp so the 15-day clock starts at install, not at epoch. When `FCLAUDE_UPDATE_APT=0`
the apt branch is skipped entirely (opting out of apt shouldn't nag). The Claude branch only fires
when a versions dir exists (native install present).

## Considerations & trade-offs

- **Install method churn.** One-time migration cost (uninstall npm global, native install, verify
  auth + PATH). Mitigated by the pre-existing `.profile` PATH plumbing and shared `~/.claude`.
- **Auto-latest vs. reproducibility.** `stable` channel is the middle ground — fresh but delayed,
  not bleeding edge. If a release ever breaks, roll back by pointing the symlink at a prior
  `versions/{X}` dir (old versions are retained) — a real, fast rollback the npm method never had.
- **Keeping Node.** Dropping Node removes an update surface, but a dev sandbox almost certainly needs
  `node`/`npm` for user projects. Recommend keeping Node (via apt weekly upgrade) but no longer
  *for Claude*. ← decision point.
- **Determinism.** The background auto-updater (chosen) trades a fixed weekly window for zero
  maintenance and continuous freshness — fine since ≥ weekly is the only hard requirement and you
  always have a session running to carry the background check. `fclaude --update` covers "I want it
  now."
- **Network gate cost & edge cases.** The probe costs up to ~10 MB on a *fast* link (negligible) and
  ~1 MB on a throttled one. A borderline link near 100 Mbit may flip week to week — acceptable; a
  skipped week just updates the next fast-network week, or on demand via `--update`. Captive
  portals / DNS hijacks that return 200 for the probe could mis-measure "fast"; apt then fails
  cleanly on its own and stamps `error`. Set `FCLAUDE_UPDATE_MIN_MBIT=0` to disable gating.
- **VM off for a week+.** The apt timer's `Persistent=true` catches up on next boot (then re-probes
  the network); the Claude auto-updater runs whenever a session next runs — both self-heal without
  host coupling.
- **Security surface.** Native installer + `claude update` run as fclaude (no root); apt timer runs
  root but only fixed package-manager commands from a root-owned `0755` script.
- **Startup-warning cost.** One extra `ssh_vm` round-trip per session start (reads two small stamps +
  a `stat`), a few ms on the already-established VM — negligible, and it fires no network/apt work.
  Purely local (no probe), so it's safe to run even offline.

## Rollout steps (for the TODO)

1. `lib/constants.sh`: add `FCLAUDE_UPDATE_APT` / `_MIN_MBIT` / `_PROBE_URL` / `_PROBE_MAXTIME`; add to `config.template`.
2. `site.yml`: remove the npm `@anthropic-ai/claude-code` task; add native-installer task (as fclaude, `stable`) + `settings.json` `autoUpdatesChannel`; decide Node's fate; add gated apt-timer tasks + `/etc/default/fclaude-update`; **seed `/var/lib/fclaude/last-apt-success` with the provision timestamp** (base64-inline the scripts/units per the cloud-init embedding model).
3. Add `vm/update/*` (apt script + 2 units) and `host/bin/fclaude-update-setup`.
4. `host/bin/fclaude`: add `--update` flag + usage line; call `fclaude_staleness_warn` in the startup path. `lib/common.sh`: add `fclaude_update()` + `fclaude_staleness_warn()`.
5. `install.sh`: install `fclaude-update-setup` to `~/.local/bin`. `uninstall.sh`: disable timer + remove units on the VM.
6. `fclaude_status`: add Claude-version + last-apt lines.
7. **Migrate the live VM:** run `./host/bin/fclaude-update-setup`; verify `claude --version` resolves to `~/.local/bin`, a session runs without re-login, `fclaude --update` works, a slow-network run correctly stamps `skipped`, and an artificially-backdated `last-apt-success` triggers the startup warning.
8. Docs: README (method, channel, `--update`, gate knobs, `--status`, staleness warning), CODEBASE.md (new files/rows).
```
