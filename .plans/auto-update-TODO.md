# fclaude auto-update — TODO

Implementation checklist for [.plans/auto-update-PLAN.md](auto-update-PLAN.md).

Legend: `[ ]` todo · `[~]` in progress · `[x]` done. Each phase ends with a verification step.

---

## Phase 0 — Decisions & prerequisites

- [x] **Confirm Node's fate** — **KEEP** Node 22 in the VM (decoupled from Claude, maintained by apt).
- [x] Confirm defaults: `FCLAUDE_UPDATE_MIN_MBIT=100`, probe URL `speed.cloudflare.com`,
      `FCLAUDE_UPDATE_WARN_DAYS=15`, schedule `Sun 04:00`. — confirmed correct.
- [x] Working on branch `fix/nfs-boot-cycle-and-claude-login-path` (feature built here per user).

---

## Phase 1 — Config knobs (`lib/constants.sh` + config template)

- [x] Add to `lib/constants.sh`: `FCLAUDE_UPDATE_APT/_MIN_MBIT/_PROBE_URL/_PROBE_MAXTIME/_WARN_DAYS`.
- [x] Add the same keys to `config.template` (rendered placeholders) so they land in `~/.fclaude/config`.
- [x] Add the five vars to `RENDER_VARS` in `install.sh` so the placeholders resolve.
- [x] `load_config`'s `for v in …` is REQUIRED-var validation only (not an export whitelist); the
      config is `source`d wholesale, so the new vars are available to the wrapper with no change.
- [x] **Verify:** rendering + sourcing checked in Phase 10 lint/verify.

---

## Phase 2 — VM update assets (`vm/update/`)

- [x] `vm/update/fclaude-apt-upgrade.sh` — header, `set -uo pipefail`, stamps/log, default-var
      expansion, network-speed gate (force + threshold-0 bypass, size+time-capped probe, offline→skip,
      `skipped(slow:N)` stamp, exit 0), dpkg-lock wait, `apt-get update`+`upgrade --force-confold`+
      `autoremove`, dual stamps (`$OKSTAMP` only on real success), `chmod +x`.
- [x] `vm/update/fclaude-apt-upgrade.service` — oneshot, `After/Wants=network-online.target`,
      `After=docker.service`, `Nice=10`, `IOSchedulingClass=idle`, `EnvironmentFile=-/etc/default/fclaude-update`.
      (No placeholders → plain `.service`, not `.tmpl`.)
- [x] `vm/update/fclaude-apt-upgrade.timer` — `OnCalendar=Sun 04:00`, `RandomizedDelaySec=1h`,
      `Persistent=true`, `WantedBy=timers.target`.
- [x] **Verify:** shellcheck clean; `systemd-analyze verify` the timer passes (service's VM-only
      ExecStart path warning is expected on the host).

---

## Phase 3 — First-boot provisioning (`vm/ansible/site.yml`)

- [x] **Removed** the `community.general.npm @anthropic-ai/claude-code` task.
- [x] **Node kept** (Phase 0) — NodeSource/`nodejs` tasks retained with a comment they're for user
      projects, not Claude.
- [x] Added native-installer task: `become_user: fclaude`, `curl … | bash -s stable`,
      `creates: ~/.local/bin/claude`.
- [x] Added `~/.claude/settings.json` `{"autoUpdatesChannel":"stable"}` as fclaude via a **jq merge**
      (clobber-safe), `changed_when: false`.
- [x] Write `/etc/default/fclaude-update` (`FCLAUDE_UPDATE_APT` threaded from host via cloud-init
      `-e`; numeric gate knobs left to the script's built-in defaults, commented examples included).
- [x] Install `vm/update/*` script → `/usr/local/sbin/…` (0755) and units → `/etc/systemd/system/`
      via base64 placeholders decoded by `b64decode`.
- [x] `systemd: enable+started fclaude-apt-upgrade.timer` — **gated on `fclaude_update_apt == '1'`**.
- [x] Seed `/var/lib/fclaude/last-apt-success` with the provision timestamp (`creates:` guard).
- [x] **cloud-init embedding:** `install.sh` sed-injects the base64 of each `vm/update/*` file into
      `site.yml`'s `__*_B64__` placeholders before base64-embedding the playbook (single source of truth).
- [x] Thread `fclaude_update_apt=__FCLAUDE_UPDATE_APT__` through the cloud-init runcmd.
- [x] **Verify:** `ansible-playbook --syntax-check` on the rendered playbook passes; base64 round-trip
      is byte-exact through the sed+`b64decode` path.

---

## Phase 4 — Host wrapper: `--update` + startup warning (`host/bin/fclaude`, `lib/common.sh`)

- [x] `lib/common.sh` → `fclaude_update()`: `ensure_vm_running`; `ssh_vm_tty` `claude update` (PTY,
      via `CLAUDE_LOGIN_ENV`); `ssh_vm_tty "sudo env FCLAUDE_UPDATE_FORCE=1 …fclaude-apt-upgrade.sh"`
      (used `sudo env` so the gate-bypass var survives sudo's env policy).
- [x] `lib/common.sh` → `fclaude_staleness_warn()` + pure helpers `_fc_staleness_messages` /
      `_fc_days_ago`: early-return if `WARN_DAYS<=0`; one `ssh_vm` emitting apt-success epoch +
      attempt line + newest `versions/*` mtime; apt branch gated on `FCLAUDE_UPDATE_APT=1` with
      reason (error / skipped-slow / none); best-effort Claude branch; uses `warn`. Arithmetic kept
      inside `if` conditions to stay `set -e`-safe; non-numeric epochs normalized to "-".
- [x] `host/bin/fclaude`: `--update)` case → `fclaude_update; exit $?`; `fclaude_staleness_warn` in the
      normal-run path only (after `ensure_vm_running`, wrapped `set +e`); usage lines for `--update`
      and the staleness warning.
- [x] **Verify:** `bash -n` + shellcheck clean; live end-to-end in Phase 8.

---

## Phase 5 — Migration helper (`host/bin/fclaude-update-setup`)

- [x] Standalone script sourcing the repo's `common.sh` (falls back to installed), `load_config`.
- [x] Remove npm-global Claude via `ssh_vm 'sudo npm uninstall -g … || true'` (fclaude has NOPASSWD sudo).
- [x] Native install **as fclaude** (no sudo) — `ssh_vm_tty` guarded on `~/.local/bin/claude`.
- [x] Merge `settings.json` channel; push `vm/update/*` via `scp_vm` → install to `/usr/local/sbin` +
      `/etc/systemd/system`; write `/etc/default/fclaude-update` from host knobs; seed `last-apt-success`.
- [x] `daemon-reload` + `enable --now` the timer when `FCLAUDE_UPDATE_APT=1` (else disable);
      print `systemctl list-timers`.
- [x] `--now` flag forces one immediate gate-bypassed run; final `claude --version` resolve check.
- [x] `chmod +x`; shellcheck clean.
- [x] **Verify:** live run confirmed in Phase 8 (setup ran clean on the real VM).

---

## Phase 6 — Status output (`lib/common.sh` → `fclaude_status`)

- [x] Added best-effort `Claude:` line (`. ~/.profile; claude --version`) and `Last apt:` line from
      `/var/lib/fclaude/last-apt-upgrade`, inside the running-VM guard (never starts the VM, all `|| true`).
- [x] shellcheck clean.
- [x] **Verify:** live in Phase 8 (--status shows Claude version + Last apt).

---

## Phase 7 — Installer/uninstaller wiring (`install.sh`, `uninstall.sh`)

- [x] `install.sh`: `fclaude-update-setup` added to the wrapper install loop; also injects the
      `vm/update/*` base64 into `site.yml` (Phase 3).
- [x] `uninstall.sh`: best-effort in-VM `disable --now` + removal of the timer/service/script/env,
      `daemon-reload`; removes `~/.local/bin/fclaude-update-setup` on the host.
- [x] Uninstall **leaves Claude Code + auth (`~/.claude`) untouched** — only removes fclaude's own
      timer machinery (documented in the step comment).
- [x] shellcheck clean.

---

## Phase 8 — Migrate & verify the live VM

- [x] Ran `./host/bin/fclaude-update-setup` — VM was **already native** (`~/.local/bin/claude`, no
      npm-global); helper pinned the `stable` channel (jq merge kept existing keys), installed the
      timer/units/env, seeded the stamp, enabled the timer.
- [x] `claude --version` resolves to `~/.local/bin/claude` over ssh; npm-global gone.
- [x] `fclaude --update` updated both layers (`2.1.220 → 2.1.212`, stable channel took effect;
      apt forced, gate bypassed) — auth persisted, no re-login.
- [x] Weekly service run: probe **405 Mbit/s** → apt ran → both stamps advanced; `Result=success`.
- [x] Slow-network SKIP (`FCLAUDE_UPDATE_MIN_MBIT=999999`) → `skipped(slow:325Mbit)` stamp,
      `last-apt-success` **untouched**; FORCE bypass ran apt.
- [x] Backdated `last-apt-success` 20d + skipped attempt → staleness warning with the correct reason;
      restore → no warning. (Fixed a bug: parse the stamp **content**, not file mtime.)
- [x] `systemctl list-timers` shows next run Sun 2026-07-26 04:33 UTC; timer enabled + active.
- [x] Redeployed the updated lib + wrappers into `~/.local` so host-side features are live.

---

## Phase 9 — Docs

- [x] `README.md`: native-install note, `--update` in usage, a "Staying up to date" section (split
      mechanisms, network gate, warning, knobs, `fclaude-update-setup`), layout rows for `vm/update/`
      + `test/`.
- [x] `CODEBASE.md`: architecture line, `common.sh` helpers, `fclaude` flags + `--status` additions,
      `fclaude-update-setup` row, `site.yml`/`user-data` updates, new `vm/update/` + `test/` tables,
      two new design-decision entries, refreshed verification status.
- [x] Node "kept for user projects, not Claude" documented in README + site.yml + CODEBASE.md.

---

## Phase 10 — Quality gates (per global standards)

- [x] Lint: shellcheck clean across the whole repo (added `.shellcheckrc` documenting the two
      intentional, repo-wide disables SC1091/SC2015; targeted inline SC2016 disables for VM-side
      expansion strings).
- [x] Tests: `test/test-staleness.sh` — 15 assertions covering `_fc_days_ago` and
      `_fc_staleness_messages` (reason selection, apt-disabled, claude branch, non-numeric epochs,
      set -e safety). All pass.
- [x] CI: `.github/workflows/ci.yml` runs shellcheck + unit tests + a rendered-playbook
      ansible syntax-check on every push/PR. Lint + test steps verified green locally.
- [x] Self-reviewed against the plan's trade-offs: no defer guard, native per-user Claude (non-root),
      root-only apt script, stamp parsed by content not mtime.

---

## Out of scope / deferred (noted, not doing now)

- Deterministic single-window Claude updates (`DISABLE_AUTOUPDATER` + `claude update` in the timer) —
  rejected in favor of option A.
- `OnFailure=` unit surfacing failures louder than the stamp + startup warning.
- Rollback tooling for Claude versions (manual symlink repoint to a prior `versions/*` suffices).
