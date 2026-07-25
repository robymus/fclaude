# fclaude Headroom + Grafana Cloud metrics — TODO

Implementation checklist for [.plans/headroom-PLAN.md](headroom-PLAN.md).

Legend: `[ ]` todo · `[~]` in progress · `[x]` done · `[U]` **user action** (unattended, parallel).
Each phase ends with a verification step.

> **Ordering note:** Phase 0 creates the SOPS secret FIRST so you can drop the real Grafana Cloud
> token into it while the rest of the implementation proceeds unattended. Nothing between Phase 1 and
> Phase 7 depends on the token being filled; only the Phase 7 delivery reads it.

---

## Phase 0 — SOPS secret first (unblocks you immediately)  ✅

- [x] Created `.sops.yaml` mirroring `~/projects/vps-config` (both recipients), regex `secrets/.*\.sops\.ya?ml$`.
- [x] Created `secrets/secrets.sops.yaml`: real `gc_prom_url` + `gc_prom_user` (`3257236`) copied from
      vps-config (host age key decrypted it); `gc_prom_token` = placeholder `glc_REPLACE_ME`.
- [x] `sops -e -i` to both recipients; committed encrypted.
- [x] Added `secrets/README.md`.
- [x] **[U] You:** minted (commit b5656b3) a dedicated Grafana Cloud **`metrics:write`** access-policy token (portal →
      Access Policies → create → add token), then `sops secrets/secrets.sops.yaml` and replace
      `gc_prom_token`, commit. *(Any time before Phase 7; I won't wait.)*
- [x] **Verify:** `sops -d` round-trips all three keys; committed file is ciphertext only.

---

## Phase 1 — Branch + `headroom wrap` / OAuth smoke test (GATE)

- [x] New feature branch off `main`.
- [x] In the VM (throwaway venv): `pip install headroom-ai` (base), run `headroom proxy --port 8787`.
- [x] **OAuth via wrap:** run `headroom wrap claude` in the VM and confirm a real session works
      (Claude's OAuth carried, no 401).
- [x] **Shared-proxy attach:** with the `headroom proxy` service already running, confirm
      `headroom wrap claude` **attaches** (single listener on `:8787` via `ss -ltnp`; no second proxy
      spawned; metrics reflect the wrapped traffic). Record the outcome.
- [x] Decide exec strategy: `headroom wrap claude` (preferred) — or, only if attach fails, the
      explicit-`ANTHROPIC_BASE_URL` fallback. Note the choice in the plan.
- [x] Confirm the working `prometheus` agent flag (`--agent` vs `--enable-feature=agent`) for the
      version we'll pin.
- [x] **Verify:** a wrapped session completes a prompt end-to-end; decision recorded. **Gate the rest
      on this.**

---

## Phase 2 — Remove llm.log (Part A)

- [x] Delete `host/systemd/fclaude-llmlog.{socket,service}`, `vm/ansible/llmlog.yml`,
      `host/bin/fclaude-llmlog-setup`.
- [x] `lib/constants.sh`: drop `FCLAUDE_LLMLOG_*`.
- [x] `host/config.template`: drop the `LLMLOG_PROXY_PORT` block.
- [x] `lib/common.sh`: drop the llm.log comment block (keep `CLAUDE_LOGIN_ENV`).
- [x] `install.sh`: remove the llmlog socket render (step 10), the llmlog ansible (step 14),
      `FCLAUDE_LLMLOG_PORT`/`FCLAUDE_SOCKET_PROXYD` from `RENDER_VARS`, the proxyd detection.
- [x] `uninstall.sh`: remove the llmlog unit disable/removal.
- [x] Grep the repo for `llmlog`/`llm.log`/`HTTPS_PROXY`/`LLMLOG` → zero references (except historical
      plans).
- [x] **Verify:** `bash -n` + shellcheck clean on install/uninstall/common; `install.sh` RENDER path
      still renders.

---

## Phase 3 — Config knobs

- [x] `lib/constants.sh`: `FCLAUDE_HEADROOM_ENABLE=1`, `FCLAUDE_HEADROOM_PORT=8787`,
      `FCLAUDE_METRICS_ENABLE=1`, `FCLAUDE_METRICS_HOST_LABEL=darkness`, `FCLAUDE_METRICS_VM_LABEL=fclaude`.
- [x] `host/config.template`: same keys (rendered placeholders) + doc comments.
- [x] `install.sh` `RENDER_VARS`: add the five vars.
- [x] **Verify:** render config.template → all placeholders resolve, sources cleanly under bash.

---

## Phase 4 — Headroom proxy in the VM (Part B)

- [x] `vm/headroom/fclaude-headroom.service` (service runs `/opt/headroom/current/bin/headroom proxy
      --port 8787`, `HEADROOM_UPDATE_CHECK=off`, `User=fclaude`, hardening).
- [x] `vm/ansible/site.yml`: install `python3-venv`/`pip`; create versioned venv + `current` symlink +
      `/usr/local/bin/headroom` symlink; drop-in the service; enable+start.
- [x] `lib/common.sh`: `HEADROOM_WRAP` (`'headroom wrap'` when `FCLAUDE_HEADROOM_ENABLE=1`, else empty).
- [x] `host/bin/fclaude`: both exec branches → `exec ${HEADROOM_WRAP} claude …`.
- [x] `host/bin/fclaude-vscode`: real-session + MCP-helper branches → `exec ${HEADROOM_WRAP} claude …`.
- [x] **Verify:** shellcheck clean; on the live VM a `fclaude`/`fclaude-vscode` session runs through
      the proxy (metrics counter increments).

---

## Phase 5 — Update-without-restart (Part C)

- [x] `vm/update/fclaude-headroom-update.sh`: resolve latest `headroom-ai`, stage a new versioned venv,
      do NOT touch `current`/service. Hooked into the weekly (network-gated) flow.
- [x] `/usr/local/sbin/fclaude-headroom-activate`: refresh `/usr/local/bin/headroom` symlink; if newest
      ≠ `current`, repoint + (restart only if service active); prune to newest 2.
- [x] `vm/headroom/fclaude-headroom-activate.service` boot oneshot, `Before=fclaude-headroom.service`.
- [x] `lib/common.sh`: `headroom_activate_if_idle` (restart only when **no** claude runs in the VM);
      fold the `pgrep` probe into the existing `fclaude_staleness_warn` ssh round-trip.
- [x] Call `headroom_activate_if_idle` from the wrappers' startup path (host-side `with_lock` guard).
- [x] `site.yml`: install the update script + activate script + boot unit.
- [x] **Verify:** stage a fake-newer version → boot activates it; with a session running, first-instance
      restart is skipped; with none, it activates + restarts.

---

## Phase 6 — Metrics agent (Part D)

- [x] `vm/metrics/prometheus.yml.tmpl` (5m scrape of `127.0.0.1:8787`, `external_labels` host/vm
      rendered, `remote_write` url/user rendered, `password_file: /etc/fclaude/gc_token`).
- [x] `vm/metrics/fclaude-metrics.service` (`prometheus --agent …`, `User=fclaude`,
      `--storage.agent.path=/var/lib/fclaude/prom-agent`).
- [x] `vm/ansible/site.yml`: download+install the pinned `prometheus` binary to `/usr/local/bin`;
      drop-in the unit (enabled but inactive until the secret lands).
- [x] **Verify:** `promtool check config` on a rendered `prometheus.yml`; unit parses
      (`systemd-analyze verify`).

---

## Phase 7 — Secret delivery + `fclaude-metrics-setup` (Part E wiring) — reads the filled token

- [x] `host/bin/fclaude-metrics-setup`: `sops -d --extract` url/user/token; write
      `/etc/fclaude/gc_token` (600) + rendered `/etc/fclaude/prometheus.yml` into the VM; restart the
      agent. Standalone + idempotent (mirrors `fclaude-llmlog-setup` shape).
- [x] `install.sh`: new host-run step invoking the delivery when `secrets/secrets.sops.yaml` + `sops`
      exist; install `fclaude-metrics-setup` into `~/.local/bin`.
- [x] **Verify (needs Phase 0 token filled):** run `fclaude-metrics-setup`; confirm the agent starts,
      `remote_write` succeeds (agent logs), and series appear in Grafana Cloud with
      `host="darkness"`, `vm="fclaude"`.

---

## Phase 8 — Installer/uninstaller wiring + base64 injection

- [x] `install.sh`: base64-inject the new VM assets (`fclaude-headroom.service`,
      `fclaude-headroom-activate.service`, update+activate scripts, `prometheus.yml.tmpl`,
      `fclaude-metrics.service`) into `site.yml` placeholders before embedding (same pattern as the
      apt-upgrade assets).
- [x] `install.sh`: add `fclaude-metrics-setup` to the wrapper install loop.
- [x] `uninstall.sh`: best-effort in-VM teardown of `fclaude-headroom*`/`fclaude-metrics` units +
      `/opt/headroom` + `/etc/fclaude/{gc_token,prometheus.yml}`; remove `fclaude-metrics-setup` on host.
- [x] `vm/cloud-init/user-data.tmpl`: thread `fclaude_metrics_enable` + labels if provisioned at first
      boot (mirror `fclaude_update_apt`).
- [x] **Verify:** rendered `site.yml` passes `ansible-playbook --syntax-check`; base64 round-trip is
      byte-exact; `bash -n` on installers.

---

## Phase 9 — Live migration & verification on the VM

- [x] Apply to the running VM: install Headroom venv+service, metrics agent, run `fclaude-metrics-setup`.
- [x] A real `fclaude` session runs via `headroom wrap` (auth OK, no re-login); a concurrent second
      session attaches to the same proxy.
- [x] Headroom metrics visible at `:8787/metrics`; Grafana Cloud receives them at 5m with the labels.
- [x] Stage a newer Headroom version; confirm first-idle-instance activation swaps + restarts, and a
      busy VM defers.
- [x] llm.log fully gone (no proxy env, no host bridge unit).

---

## Phase 10 — Docs, CI, quality gates

- [x] `README.md`: replace the llm.log section with Headroom + metrics (what runs, `wrap`, disable
      knob, `fclaude-metrics-setup`, SOPS note); layout rows for `vm/headroom/`, `vm/metrics/`, `secrets/`.
- [x] `CODEBASE.md`: architecture line, new files, wrapper exec change, update/activate flow, metrics
      pipeline, SOPS delivery; drop llm.log entries.
- [x] `.github/workflows/ci.yml`: shellcheck the new scripts; `promtool`/render check for
      `prometheus.yml`; assert `secrets/secrets.sops.yaml` is encrypted (no plaintext token).
- [x] shellcheck clean repo-wide; unit tests still green; CI green on the branch.
- [x] Self-review against the plan's trade-offs (token-in-sandbox scoped token, stale-until-safe update,
      wrap-attach confirmed).

---

## Suggested commit boundaries
1. Phase 0 (SOPS scaffold) — small, lets you fill the token.
2. Phase 2 (llm.log removal).
3. Phases 3–4 (config + Headroom proxy + wrap exec).
4. Phase 5 (update/activate).
5. Phases 6–7 (metrics agent + secret delivery).
6. Phase 8 (installer wiring).
7. Phase 10 (docs/CI). Then PR.
