# fclaude Headroom proxy + Grafana Cloud metrics — PLAN

Replace the optional **llm.log** HTTPS-proxy integration with **Headroom**
([headroomlabs-ai/headroom](https://github.com/headroomlabs-ai/headroom)) — a context-compression
proxy — running **inside the VM**. Point the Claude processes that the fclaude wrappers launch at it
via `ANTHROPIC_BASE_URL`, keep the proxy updated **without restarting it under load**, and export its
Prometheus metrics to **Grafana Cloud** (5-min resolution) with credentials held in a **SOPS**-encrypted
secret.

## What Headroom is (confirmed from the repo)

- Python package (Rust-accelerated, `abi3` wheels) published to PyPI as **`headroom-ai`**; latest
  **v0.32.0**, released frequently. Install via `pip`/`uv`/`pipx`. **No standalone binary** — so "update
  the binary" below means "install the new wheel into a staged venv."
- Runs as a local proxy: **`headroom proxy --port 8787`**. Clients set **`ANTHROPIC_BASE_URL`** at it;
  it forwards `/v1/messages` to `api.anthropic.com`, compressing tool-output/JSON/code payloads.
- **Prometheus** metrics at **`http://<proxy>:8787/metrics`** (same port as the proxy), e.g.
  `headroom_requests_total`, `headroom_tokens_saved_total`, `headroom_compression_ratio_bucket`,
  `headroom_latency_seconds_bucket`, `headroom_cache_hits_total`.
- Useful env vars: `HEADROOM_UPDATE_CHECK=off` (we manage updates), `HEADROOM_OUTPUT_SHAPER`,
  `HEADROOM_TLS_STRICT`.

> ⚠️ **Two things to verify during implementation (flagged, not assumed):**
> 1. **OAuth pass-through.** Claude Code authenticates to Anthropic with an OAuth **`Authorization:
>    Bearer sk-ant-oat…`** header (not `x-api-key`). The README hints bearer forwarding is supported
>    ("OAuth bearer forwarded"), but this MUST be smoke-tested with a real session. Headroom advertises
>    **`headroom wrap claude`** as the "just works" path — it runs/attaches the proxy and sets the env
>    for you. Smoke-test both: (a) our architecture — standalone `headroom proxy` service +
>    `ANTHROPIC_BASE_URL`; (b) `headroom wrap claude`. If (a) fails auth but (b) works, `wrap` is doing
>    extra glue (header/token handling) — then adopt the `wrap` exec strategy (below). Last-resort
>    fallback: set `ANTHROPIC_API_KEY` in the VM instead of OAuth-through-proxy.
> 2. **Bind flag (minor).** `--port` is documented; a `--host`/`--bind` flag is not. Not blocking —
>    the chosen **in-VM** metrics agent scrapes `127.0.0.1:8787` regardless. We'll pin `127.0.0.1` if a
>    bind flag exists (defense in depth), else rely on the default loopback bind.

---

## Part A — Remove the llm.log integration

Delete the files and every reference:

| Remove | Notes |
|---|---|
| `host/systemd/fclaude-llmlog.socket` / `.service` | host gateway bridge |
| `vm/ansible/llmlog.yml` | host-run CA/proxy-env playbook |
| `host/bin/fclaude-llmlog-setup` | standalone bridge installer |
| `lib/constants.sh` → `FCLAUDE_LLMLOG_*` | replaced by `FCLAUDE_HEADROOM_*` / `FCLAUDE_METRICS_*` |
| `host/config.template` → `LLMLOG_PROXY_PORT` block | replaced by Headroom/metrics knobs |
| `lib/common.sh` → llm.log comment block (l.96–101) | keep `CLAUDE_LOGIN_ENV` (PATH, unrelated) |
| `install.sh` → step 10 llmlog socket render, step 14 llmlog ansible, `FCLAUDE_LLMLOG_PORT`/`FCLAUDE_SOCKET_PROXYD` in `RENDER_VARS`, proxyd detection | replace with Headroom+metrics steps |
| `uninstall.sh` → llmlog unit disable/removal | replace with Headroom+metrics teardown |
| README / CODEBASE | rewrite the "llm.log" sections |

No `HTTPS_PROXY` / `/etc/environment` proxy edits anymore — Headroom is reached per-process via
`ANTHROPIC_BASE_URL` set by the wrappers (below), which the user specifically wants.

---

## Part B — Headroom proxy inside the VM

### Versioned venv layout (enables update-without-restart, Part C)

```
/opt/headroom/
  versions/<ver>/           # a python venv per Headroom version (pip install headroom-ai==<ver>)
  current -> versions/<ver> # symlink the service runs; swapped only on a safe restart
```

The systemd unit runs `…/current/bin/headroom proxy` so activating a new version = repoint the
symlink + restart. A running proxy keeps its own version's files (its venv dir isn't deleted) — the
same hot-swap discipline used for native Claude.

### Provisioning (append to `vm/ansible/site.yml`)

```yaml
    # --- Headroom context-compression proxy (per-process ANTHROPIC_BASE_URL) ---
    - name: Install python venv + pipx tooling for Headroom
      ansible.builtin.apt:
        name: [python3-venv, python3-pip]
        state: present

    - name: Install the initial Headroom version into a versioned venv
      ansible.builtin.shell: |
        set -euo pipefail
        ver="$(python3 - <<'PY'
        import json,urllib.request
        r=json.load(urllib.request.urlopen("https://pypi.org/pypi/headroom-ai/json"))
        print(r["info"]["version"])
        PY
        )"
        dest="/opt/headroom/versions/${ver}"
        if [ ! -x "${dest}/bin/headroom" ]; then
          python3 -m venv "${dest}"
          "${dest}/bin/pip" install --upgrade pip >/dev/null
          "${dest}/bin/pip" install "headroom-ai==${ver}"      # base only; [all] (HF text model) deferred
        fi
        ln -sfn "${dest}" /opt/headroom/current
        ln -sfn "${dest}/bin/headroom" /usr/local/bin/headroom   # CLI on PATH for `headroom wrap`
      args: {executable: /bin/bash, creates: /usr/local/bin/headroom}

    - name: Install the Headroom systemd unit
      ansible.builtin.copy:
        dest: /etc/systemd/system/fclaude-headroom.service
        mode: '0644'
        content: "{{ '__HEADROOM_SERVICE_B64__' | b64decode }}"

    - name: Enable + start Headroom
      ansible.builtin.systemd:
        name: fclaude-headroom.service
        daemon_reload: true
        enabled: true
        state: started
```

### The service — `vm/headroom/fclaude-headroom.service`

```ini
[Unit]
Description=fclaude Headroom context-compression proxy
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
Environment=HEADROOM_UPDATE_CHECK=off
# Loopback bind is the default; if a bind flag exists, pin 127.0.0.1 explicitly.
ExecStart=/opt/headroom/current/bin/headroom proxy --port 8787
Restart=on-failure
RestartSec=2
# Least-privilege hardening (proxy holds no creds; it forwards the client's auth):
DynamicUser=no
User=fclaude
NoNewPrivileges=yes

[Install]
WantedBy=multi-user.target
```

### Launch Claude via `headroom wrap` (chosen)

**Decided:** the wrappers exec **`headroom wrap claude …`** against the **shared persistent `headroom
proxy` service**. `wrap` is Headroom's advertised "just works" path for Claude — it attaches to the
running proxy (hot-syncs settings via loopback POST) and sets up the client env/auth itself, so we
don't hand-manage `ANTHROPIC_BASE_URL` or worry about OAuth header quirks. One shared proxy still
serves `/metrics` and the cross-session cache; concurrent sessions all attach to it.

`headroom` must be resolvable in the VM: `fclaude-headroom-activate` (Part C) maintains a
`/usr/local/bin/headroom` symlink → `/opt/headroom/current/bin/headroom`. `claude` resolves via
`~/.profile` PATH as today.

A prefix analogous to `CLAUDE_LOGIN_ENV`, set **only** on the processes we launch (never globally),
toggled by config so the integration can be disabled instantly:

```bash
# lib/common.sh
# Wrap wrapper-launched Claude with Headroom. FCLAUDE_HEADROOM_ENABLE=0 → plain claude.
HEADROOM_WRAP=''
[[ "${FCLAUDE_HEADROOM_ENABLE:-1}" == "1" ]] && HEADROOM_WRAP='headroom wrap'
```

Every wrapper exec becomes `… exec ${HEADROOM_WRAP} claude …`:

```bash
# host/bin/fclaude  (mounted + --no-mount)
ssh_vm_tty "${CLAUDE_LOGIN_ENV} cd … && exec ${HEADROOM_WRAP} claude --dangerously-skip-permissions ${extra}"
# host/bin/fclaude-vscode  (real-session + MCP-helper branches)
"${CLAUDE_LOGIN_ENV} … exec ${HEADROOM_WRAP} claude ${extra}"
```

Runs under the wrapper's PTY (`ssh_vm_tty`), so `wrap` inherits the TTY and Claude stays interactive.
`claude update` runs elsewhere (not under wrap) and hits `downloads.claude.ai`, unaffected.

> **Smoke test must confirm** (rollout step 1): (a) `headroom wrap claude` carries Claude's OAuth and
> runs a real session; (b) with the `headroom proxy` service already running, `wrap` **attaches to it**
> and does **not** spawn a second proxy (check `ss -ltnp`/`pgrep` for a single listener on 8787, and
> that metrics reflect the wrapped traffic). If (b) fails — `wrap` spawns its own per session — fall
> back to the explicit form: keep the shared service and set `ANTHROPIC_BASE_URL=http://127.0.0.1:8787`
> in the exec prefix instead of `headroom wrap`. This is the only branch point left; everything else is
> identical.

---

## Part C — Update Headroom without restarting the running proxy

Requirement: stage the new version now, **don't** restart the live proxy; let it restart at a safe
moment — **VM boot**, or when the fclaude wrapper starts a session and **no Claude is currently
running** (the "first instance"). Running stale until then is fine.

### Weekly staging (new `vm/update/fclaude-headroom-update.sh`, driven by the existing weekly timer)

```bash
#!/usr/bin/env bash
set -uo pipefail
# Resolve latest headroom-ai; if not already installed, build its venv. Do NOT
# touch /opt/headroom/current or restart the service — activation happens later.
ver="$(python3 - <<'PY'
import json,urllib.request
print(json.load(urllib.request.urlopen("https://pypi.org/pypi/headroom-ai/json"))["info"]["version"])
PY
)"
dest="/opt/headroom/versions/${ver}"
if [ ! -x "${dest}/bin/headroom" ]; then
  python3 -m venv "${dest}" && "${dest}/bin/pip" install -q --upgrade pip \
    && "${dest}/bin/pip" install -q "headroom-ai==${ver}" \
    && echo "staged headroom ${ver} (pending activation)" \
    || { rm -rf "${dest}"; echo "headroom ${ver} staging FAILED"; exit 1; }
fi
```

Reuse the network-speed gate context: call this from the weekly `fclaude-apt-upgrade` flow (or a
sibling timer) so it only runs on a fast link. A newly-staged version simply sits in `versions/`
until activated — no pending-flag file needed; "pending" == "newest `versions/*` ≠ `current`".

### Safe activation (new `/usr/local/sbin/fclaude-headroom-activate`)

```bash
#!/usr/bin/env bash
set -uo pipefail
newest="$(ls -1v /opt/headroom/versions 2>/dev/null | tail -1)"
[ -n "$newest" ] || exit 0
ln -sfn "/opt/headroom/versions/${newest}/bin/headroom" /usr/local/bin/headroom   # keep the CLI symlink fresh
cur="$(readlink -f /opt/headroom/current 2>/dev/null)"
[ "$cur" = "/opt/headroom/versions/${newest}" ] && exit 0   # already newest → nothing to do
ln -sfn "/opt/headroom/versions/${newest}" /opt/headroom/current
systemctl is-active --quiet fclaude-headroom && systemctl restart fclaude-headroom
# prune old versions, keep the two newest
ls -1v /opt/headroom/versions | head -n -2 | while read -r v; do rm -rf "/opt/headroom/versions/$v"; done
```

Triggers:
1. **VM boot** — a oneshot `fclaude-headroom-activate.service` ordered `Before=fclaude-headroom.service`
   repoints the symlink (service not yet active → no restart), so boot always starts the newest.
2. **First-instance, wrapper-driven** — before launching a session, if no Claude runs in the VM, the
   wrapper calls activate (which restarts only if the symlink actually moved). A live session is never
   disturbed because we only restart when the process count is zero.

```bash
# lib/common.sh — called from the wrapper startup path (guarded by a host-side flock)
headroom_activate_if_idle() {
  [[ -n "${HEADROOM_PROXY_URL:-}" ]] || return 0
  # "first instance" == no claude running in the VM right now
  if ! ssh_vm 'pgrep -x claude >/dev/null 2>&1'; then
    ssh_vm 'sudo /usr/local/sbin/fclaude-headroom-activate' || true
  fi
}
```

Race note: two near-simultaneous first sessions could both see zero and both restart — harmless (a
second restart of a just-restarted proxy). A host-side `with_lock headroom …` around the check
removes even that.

---

## Part D — Export metrics to Grafana Cloud (5-min) — **in-VM `prometheus --agent`** (decided)

The agent runs **in the VM** (robust; works with the proxy's default loopback bind) and is
**Prometheus in agent mode** (single official binary, tiny YAML config, minimal footprint). It
scrapes `127.0.0.1:8787/metrics` every 5m and `remote_write`s to Grafana Cloud (basic auth: username
= numeric instance id, password = access-policy token). Accepted cost of in-VM: the write token lives
in the sandbox → use a **metrics-write-only** access-policy token (blast radius = junk metrics).

**Install** the official binary pinned to a version (Ubuntu's apt `prometheus` is the full server and
may predate agent mode): fetch `prometheus-<ver>.linux-amd64.tar.gz`, install the `prometheus` binary
to `/usr/local/bin`. Stale is fine; a version bump is a manual retarget.

### Config — `vm/metrics/prometheus.yml.tmpl` (url/user/labels rendered; token via `password_file`)

Prometheus doesn't expand env vars in its config, so the **secret token** is referenced by
`password_file` (mode 600), while the non-secret URL/username/labels are rendered in:

```yaml
global:
  scrape_interval: 5m
  external_labels:
    host: __FCLAUDE_METRICS_HOST_LABEL__     # "darkness"
    vm:   __FCLAUDE_METRICS_VM_LABEL__       # "fclaude"
scrape_configs:
  - job_name: headroom
    static_configs:
      - targets: ['127.0.0.1:8787']
remote_write:
  - url: __GC_PROM_URL__                     # from the decrypted secret (endpoint, non-secret)
    basic_auth:
      username: __GC_PROM_USER__             # numeric instance id
      password_file: /etc/fclaude/gc_token   # the access-policy token, mode 600 root
```

### Service — `vm/metrics/fclaude-metrics.service`

```ini
[Unit]
Description=fclaude metrics agent (Headroom -> Grafana Cloud, prometheus agent mode)
After=network-online.target fclaude-headroom.service
Wants=network-online.target
[Service]
User=fclaude
ExecStart=/usr/local/bin/prometheus --agent \
  --config.file=/etc/fclaude/prometheus.yml \
  --storage.agent.path=/var/lib/fclaude/prom-agent \
  --web.listen-address=127.0.0.1:0
Restart=on-failure
[Install]
WantedBy=multi-user.target
```
(`--agent` on recent Prometheus; on older builds `--enable-feature=agent` — pin a version where the
chosen form works.) The host renders `prometheus.yml` (url/user/labels) and writes
`/etc/fclaude/gc_token` during decrypt-and-deliver (Part E). 5-min `scrape_interval` = the required
resolution.

---

## Part E — SOPS-managed Grafana Cloud secret (mirror `~/projects/vps-config`)

Reuse the exact SOPS pattern from `vps-config`: a root `.sops.yaml` with **both** age recipients so
either key (laptop or claude-runner) can decrypt, and a single encrypted YAML holding the
`gc_prom_*` keys. `sops`/`age` are already on the host, and the host already holds a private key that
decrypts vps-config's secret — so I can **copy the real Grafana Cloud creds over** (authorized) and
commit the encrypted file; no placeholders needed.

### `.sops.yaml` (copied recipients from vps-config)
```yaml
# age private keys live OUTSIDE the repo (~/.config/sops/age/keys.txt); only PUBLIC keys here.
#   - robymus (laptop):  age1tz8yj4w6dfcjeq006yxdn5nnw7xq04s6wxm5m7jvqmjpcfk6ra0s7kwk4p
#   - claude runner:     age19yrsp0tjvx6f03hzj3qx0khlwqvmjs7g23eh7vutnfy9sgpseslqjl45sh
creation_rules:
  - path_regex: secrets/.*\.sops\.ya?ml$
    age: >-
      age1tz8yj4w6dfcjeq006yxdn5nnw7xq04s6wxm5m7jvqmjpcfk6ra0s7kwk4p,age19yrsp0tjvx6f03hzj3qx0khlwqvmjs7g23eh7vutnfy9sgpseslqjl45sh
```

### `secrets/secrets.sops.yaml` (encrypted; plaintext shape shown)
```yaml
gc_prom_url:   https://prometheus-prod-XX-REGION.grafana.net/api/prom/push
gc_prom_user:  "000000"      # numeric instance id
gc_prom_token: glc_...       # access-policy token (metrics:write only, ideally a dedicated fclaude one)
```
Created by copying vps-config's values, then `sops -e` (recipients from `.sops.yaml`). Editable later
with `sops secrets/secrets.sops.yaml`.

> **Note:** vps-config's token may be scoped to that VPS. Recommend a **dedicated fclaude access-policy
> token** (metrics:write) so it can be rotated independently and, since it lives in the sandbox, has
> minimal blast radius. If you'd rather reuse the same token, that's fine — I'll copy it verbatim.

### Decrypt-and-deliver (host-run; standalone `host/bin/fclaude-metrics-setup`, also called by install.sh)
```bash
# Host holds the age key; extract each field and deliver to the VM.
url="$(sops -d --extract '["gc_prom_url"]'   secrets/secrets.sops.yaml)"
user="$(sops -d --extract '["gc_prom_user"]'  secrets/secrets.sops.yaml)"
sops -d --extract '["gc_prom_token"]' secrets/secrets.sops.yaml | \
  ssh_vm 'sudo install -m600 /dev/stdin /etc/fclaude/gc_token'      # ssh WITHOUT -n so stdin flows
# render prometheus.yml (url/user/labels) locally, push it, restart the agent
render_prom_yml "$url" "$user" "$HOST_LABEL" "$VM_LABEL" | \
  ssh_vm 'sudo install -m644 /dev/stdin /etc/fclaude/prometheus.yml'
ssh_vm 'sudo systemctl restart fclaude-metrics.service'
```

The **age private key never leaves the host**; only the derived token/config reach the VM. `.gitignore`
already needs no plaintext entry (the only on-disk secret is the encrypted `*.sops.yaml`).

---

## Config knobs (`lib/constants.sh` → `~/.fclaude/config`)

```bash
# Headroom proxy (in-VM). 0 disables: the wrappers then exec plain `claude`.
FCLAUDE_HEADROOM_ENABLE="1"
FCLAUDE_HEADROOM_PORT="8787"                          # the shared proxy service's port
# Metrics export
FCLAUDE_METRICS_ENABLE="1"
FCLAUDE_METRICS_HOST_LABEL="darkness"
FCLAUDE_METRICS_VM_LABEL="fclaude"
```

Runtime config gets `FCLAUDE_HEADROOM_ENABLE` (consumed by `HEADROOM_WRAP` in the wrappers) + the
metrics label knobs. (If the smoke test forces the explicit-`ANTHROPIC_BASE_URL` fallback, add
`FCLAUDE_HEADROOM_PROXY_URL="http://127.0.0.1:8787"` and use it in the exec prefix instead.)

## Files added / modified

```
DELETE  host/systemd/fclaude-llmlog.{socket,service}, vm/ansible/llmlog.yml, host/bin/fclaude-llmlog-setup
NEW     vm/headroom/fclaude-headroom.service
NEW     vm/headroom/fclaude-headroom-activate.service        (boot activation oneshot)
NEW     vm/update/fclaude-headroom-update.sh                 (weekly staging)
NEW     vm/metrics/prometheus.yml.tmpl                       (agent config; url/user/labels rendered)
NEW     vm/metrics/fclaude-metrics.service
NEW     host/bin/fclaude-metrics-setup                       (decrypt+deliver secret, render cfg, (re)start agent)
NEW     .sops.yaml, secrets/secrets.sops.yaml                (recipients + keys mirror vps-config)
MODIFY  vm/ansible/site.yml   (Headroom venv+units, activate unit, prometheus-agent install+unit)
MODIFY  vm/cloud-init/user-data.tmpl (thread metrics-enable + labels if provisioned at first boot)
MODIFY  install.sh            (base64-inject new assets into site.yml; drop llmlog steps; add metrics-setup to wrappers + a host-run secret-delivery step)
MODIFY  uninstall.sh          (teardown headroom + metrics units; drop llmlog teardown)
MODIFY  lib/constants.sh, host/config.template   (drop LLMLOG_*, add HEADROOM_/METRICS_ knobs)
MODIFY  lib/common.sh         (HEADROOM_WRAP prefix; headroom_activate_if_idle; drop llmlog comment)
MODIFY  host/bin/fclaude, host/bin/fclaude-vscode  (exec ${HEADROOM_WRAP} claude; call headroom_activate_if_idle)
MODIFY  README.md, CODEBASE.md
MODIFY  .github/workflows/ci.yml  (shellcheck the new scripts; validate the .alloy render)
```

## Considerations & trade-offs

- **OAuth through the proxy (top risk).** If Headroom doesn't forward Claude Code's `Authorization:
  Bearer` OAuth token, sessions 401. Verify with a real session first; fallback is an
  `ANTHROPIC_API_KEY` in the VM. Gate rollout on this smoke test.
- **Token in the sandbox (security, accepted).** The in-VM agent (your choice) puts a Grafana Cloud
  write token in `/etc/fclaude/gc_token` inside a yolo VM whose agent has `sudo`. Mitigate with a
  **dedicated, metrics-write-only** access-policy token so worst case is junk metrics, not account
  compromise — recommended over reusing the vps-config token.
- **Compression footprint (base install, your choice).** Base `headroom-ai` gives JSON/code
  compression + metrics and is light (no torch). `[all]` (HF text embedder) would pull torch + models
  — deferred. `HEADROOM_UPDATE_CHECK=off` stops its own updater fighting ours.
- **Correctness of compression for a coding agent.** Headroom rewrites payloads; if it ever corrupts a
  tool result the agent misbehaves subtly. `HEADROOM_OUTPUT_HOLDOUT` gives a measured control group.
  Keep the `--no-headroom` escape (empty `FCLAUDE_HEADROOM_PORT`) to bypass instantly.
- **Update model.** Staged venv + symlink swap mirrors native-Claude: the running proxy is never
  mutated in place; activation only at boot or a zero-session first-instance. Stale-until-safe is
  acceptable per the requirement.
- **Extra ssh at session start.** `headroom_activate_if_idle` adds one `ssh_vm pgrep` round-trip;
  fold it into the existing `fclaude_staleness_warn` probe to keep session start to a single call.
- **5-min resolution.** Coarsens histogram/counter rates but matches the requirement; keeps Grafana
  Cloud active-series/DPM low.

## Resolved decisions
1. **Exec strategy:** wrappers run **`headroom wrap claude`** against a shared persistent `headroom
   proxy` service (no hand-managed `ANTHROPIC_BASE_URL`). ✔
2. **Metrics agent placement:** in-VM. ✔
3. **Headroom install:** base `headroom-ai` (no `[all]`). ✔
4. **Metrics agent:** `prometheus --agent`. ✔
5. **Grafana Cloud token:** dedicated `metrics:write` access-policy token (you mint it in the portal). ✔
6. **SOPS:** mirror `vps-config` `.sops.yaml` (laptop + claude-runner recipients); copy `gc_prom_url`/
   `gc_prom_user` over + the new dedicated token. ✔

Still to **verify during implementation** (not blocking the plan): (a) `headroom wrap` carries Claude's
OAuth AND attaches to the shared proxy without spawning its own — explicit-`ANTHROPIC_BASE_URL` is the
only fallback; (b) the exact `prometheus` agent-mode flag for the pinned version.

## Rollout steps (for the TODO)
0. **Grafana Cloud token:** you mint a dedicated `metrics:write` access-policy token in the portal
   (or hand me a management token to script it); I copy it + the `gc_prom_url/user` into
   `secrets/secrets.sops.yaml`.
1. **OAuth smoke test** in the VM before wiring anything (gate the rest on it): install base
   `headroom-ai`, then test **both** `headroom proxy` + `ANTHROPIC_BASE_URL` **and** `headroom wrap
   claude`. Pick the exec strategy from the result (explicit-env preferred; `wrap` if it's required).
2. Part A removal (llmlog) — keep the branch green each step.
3. Config knobs; `HEADROOM_ENV` in wrappers; Headroom venv+service in site.yml; smoke a session end-to-end.
4. Update staging (`fclaude-headroom-update.sh`) + activate (boot unit + `headroom_activate_if_idle`).
5. SOPS (`.sops.yaml` + `secrets/secrets.sops.yaml` with copied creds); `fclaude-metrics-setup`;
   prometheus-agent install + unit + rendered config; confirm series land in Grafana Cloud with the labels.
6. install.sh/uninstall.sh wiring; base64 injection of the new VM assets; host-run secret delivery.
7. Docs (README/CODEBASE) + CI (shellcheck new scripts, validate prometheus.yml render) + live verify on the VM.
