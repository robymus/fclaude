# fclaude — Codebase Map

Sandboxed Claude Code: runs `claude` in yolo mode inside a hardware-isolated KVM VM, mounting host
project directories on demand. See [.plans/fclaude-PLAN.md](.plans/fclaude-PLAN.md) for the full
design rationale and [README.md](README.md) for usage.

## Architecture in one picture

```
host wrapper (~/.local/bin/fclaude)
  → ensure_vm_running (virsh, system libvirt via 'libvirt' group)
  → mount_workspace   (bind-mount $HOME/<rel> into ~/.fclaude/exports, exportfs, NFSv4 mount in VM)
  → ssh fclaude@192.168.122.10 "cd /home/fclaude/<rel> || exit 1; <tokensave sync>; exec env ANTHROPIC_BASE_URL=… ANTHROPIC_MODEL=<model>[1m] … claude --settings '{…}' --dangerously-skip-permissions"
  → unmount_workspace on EXIT (reference counted)

VM "fclaude" (Ubuntu 24.04, KVM): uid-1000 fclaude user, sudo NOPASSWD, docker, node, a self-updating
native Claude Code install (per-user `~/.local`, latest channel), a shared Headroom compression proxy
(`:8787`), and a prometheus agent shipping its metrics to Grafana Cloud.
Host firewall (nftables table 'fclaude') blocks VM→host privileged ports (<1024) except 53/67/123.
```

## File-by-file

### Top level
| File | Role |
|---|---|
| `install.sh` | Host installer. 13 ordered steps: preflight → apt → dirs → disk (guarded) → ssh keys → cloud-init seed → libvirt net+DHCP → define/start VM → NFS+firewall+sudoers → systemd units → wrappers → config/vm-info → wait-for-provision. Idempotent where practical. Uses `sudo virsh` during install; renders templates via the `render()` helper. **`--wrappers-only`** runs `install_host_scripts()` (step 11) and nothing else — the host scripts are plain, untemplated copies, so editing a wrapper does not need minutes of VM provisioning to deploy; no sudo, no network, no VM. It also reports config knobs the existing `~/.fclaude/config` predates (a full install never rewrites an existing config either — it drops a `.new` beside it). |
| `uninstall.sh` | Reverses install. Heavy confirmation (typed phrase). **Never deletes the disk silently** — separate `DELETE DISK` opt-in prompt at the very end. |
| `README.md` | User-facing quick start. |
| `REQUIREMENTS.md` | Original requirements (source of truth for intent). |

### `lib/`
| File | Role |
|---|---|
| `constants.sh` | **Single source of truth** for names/IPs/MAC/paths/sizes. Sourced by install/uninstall and baked into `~/.fclaude/config`. Change defaults here. |
| `common.sh` | Runtime library sourced by all wrappers. Provides: `load_config`, logging, `ssh_vm`/`ssh_vm_tty`/`ssh_maint`, `vm_state`/`ensure_vm_running`, `with_lock` (flock), `refcount_*`, `relpath_under_home`, `mount_workspace`/`unmount_workspace` (the reference-counted bind-mount + NFSv4 flow), and the update helpers `fclaude_update` (on-demand `claude update` + gate-bypassed apt) / `fclaude_staleness_warn` (session-start warning) with the pure, unit-tested `_fc_staleness_messages`/`_fc_days_ago`. Host-privileged ops (`mount`/`umount`/`exportfs`) go through `sudo` (NOPASSWD drop-in). |

### `host/bin/` (installed to `~/.local/bin`)
| File | Role |
|---|---|
| `fclaude` | Main CLI. Flags: `--dir`, `--no-mount`, `--mount <dir>…`, `-m/--maintenance`, `-s/--status`, `--cleanup <ws>`, `--update`, `--shutdown`, `--`/passthrough. Passes `--dangerously-skip-permissions`. Mounts CWD, runs claude over ssh PTY, unmounts on EXIT trap. On a normal session start it calls `fclaude_staleness_warn` (best-effort, wrapped `set +e`) to flag a stalled updater. `--status` calls `fclaude_status` (in common.sh) — best-effort report (state/IP/vCPU/mem/uptime/CPU/disk + guest mem/df + **installed Claude version + last apt run** + active workspace refcounts) that never starts the VM. `--update` calls `fclaude_update` (Claude + apt now, network gate bypassed). `--mount <dir>…` is a one-shot mode that mounts one or more workspaces (refcounted, same as a session), prints their VM paths, then idles until Ctrl+C and unmounts in reverse order via an EXIT trap — no claude, for when you want a workspace visible in the VM for something else. All paths are validated up front (must exist and be under `$SHARE_ROOT`), repeats are collapsed, and a mount that fails partway still releases the ones already held. `--cleanup <ws>` calls `cleanup_workspace` — force-unmounts ONE workspace (VM-side `umount` w/ lazy fallback + host export bind + `exportfs -r` + dir cleanup) and clears its refcount regardless of count, for recovering a leaked mount; accepts a `--status`-style relpath or a real dir under `$HOME`; never starts the VM. |
| `fclaude-vscode` | VS Code `claudeCode.claudeProcessWrapper`. Drops `argv[1]` (the extension's bundled claude path — byte-identical to the VM's native claude) and forwards the rest. **Skips the mount for workspace-agnostic helper invocations** (`fc_is_mcp_helper`: first arg `mcp`, or any `--claude-in-chrome-mcp`) — those run in the VM home with no mount/cd/EXIT-trap, because the extension runs *every* claude process through this wrapper and the chrome-MCP singleton would otherwise pin (leak) the first workspace's mount for the life of the editor; discriminator is **fail-open** (unrecognized → mount). For a real session: mounts CWD, then `cd`s into the **host absolute path** (resolvable in the VM via the `/home/<hostuser>` bind mount) so claude reports host-matching paths back to the editor. stdio (`ssh -T`), **no** skip-permissions flag (extension sets that). **Errors out** if CWD isn't under `$HOME`. |
| `fclaude-grow-disk` | `qemu-img resize` → `virsh blockresize` → in-VM `growpart`+`resize2fs` (online ext4 grow). |
| `fclaude-metrics-setup` | **Standalone** host-run installer for the Grafana Cloud metrics agent: `sops -d --extract`s the remote_write url/user/token from `secrets/secrets.sops.yaml`, renders `prometheus.yml`, and delivers them (token → `/etc/fclaude/gc_token` 600 fclaude, config → `/etc/fclaude/prometheus.yml`) into the VM, then (re)starts `fclaude-metrics.service`. The age key never leaves the host. `install.sh` invokes it as step 14; also retrofits a pre-existing VM. |
| `fclaude-update-setup` | **Standalone** retrofit for auto-update on an already-provisioned VM (installed to `~/.local/bin`). Removes any npm-global Claude, installs/keeps the **native** Claude (as fclaude, no sudo — keeps `~/.local` user-owned), merges `autoUpdatesChannel: latest` into `settings.json` (jq, clobber-safe), pushes `vm/update/*` — the apt script/units **and `fclaude-headroom-update.sh`** — to `/usr/local/sbin` + `/etc/systemd/system`, writes `/etc/default/fclaude-update` from host knobs, seeds `last-apt-success`, appends `export TOKENSAVE_UPDATE_CHECK=0` to the VM's `~/.profile` if absent, and enables the weekly timer (or disables it when `FCLAUDE_UPDATE_APT=0`). `--now` forces one immediate gate-bypassed apt run. Idempotent; run from a repo checkout. **This is the only path that refreshes the VM's copy of `vm/update/*`** — site.yml runs at first boot only, so after changing anything under `vm/update/` (or pulling such a change) run this, or the VM keeps executing the version it was built with. |

### `host/` config templates (rendered + installed by install.sh)
| File | Installed to | Role |
|---|---|---|
| `config.template` | `~/.fclaude/config` | Runtime config the wrappers source. |
| `nftables/fclaude.nft.tmpl` | `/etc/fclaude/fclaude.nft` | Firewall: input hook prio -10, allow 53/67/123 to host, reject/drop other <1024 from VM subnet. Bridge name detected at install. |
| `nfs/fclaude.conf` | `/etc/nfs.conf.d/fclaude.conf` | NFSv4-only (port 2049 only, no rpcbind). |
| `nfs/fclaude.exports.tmpl` | `/etc/exports.d/fclaude.exports` | Single export root `~/.fclaude/exports` (fsid=0, crossmnt, root_squash, sec=sys). |
| `sudoers/fclaude.tmpl` | `/etc/sudoers.d/fclaude` | NOPASSWD for `mount`/`umount`/`exportfs` only. |
| `systemd/fclaude-firewall.service` | `/etc/systemd/system/` | Re-applies nft table on boot (after libvirtd). |
| `systemd/fclaude-exports.service` | `/etc/systemd/system/` | Recreates export root + `exportfs -r` on boot. |
| `systemd/nfs-server-after-libvirt.conf` | `/etc/systemd/system/nfs-server.service.d/fclaude.conf` | Drop-in ordering nfs-server after the libvirt network so nfsd can bind to `192.168.122.1`. |

### qemu runs as the invoking user
System libvirt runs qemu as `libvirt-qemu` by default, which **cannot traverse a `0700` home
directory** to open `~/.fclaude/fclaude-disk` (fails with "Could not open ... Permission denied").
install.sh step 8 appends a managed block to `/etc/libvirt/qemu.conf` setting `user = "<you>"` and
`group = "kvm"` (kvm for `/dev/kvm` access), backs up the original, and restarts the qemu driver
(`virtqemud` or `libvirtd`). This keeps the disk owned by you (no ownership churn, so
`fclaude-grow-disk` works). uninstall.sh removes the block and restarts the daemon.

### Host firewall interaction (ufw / nft base chains)
Our `inet fclaude` nft table runs at input-hook priority −10 and **rejects guest→host `<1024`
(terminal) while merely `accept`-ing `>=1024` (non-terminal)**. That non-terminal accept does NOT
override a `drop` in another base chain at the same hook — in nftables, a `drop` anywhere is final.

If the host runs **ufw** (its `ip filter INPUT` chain has `policy drop`), guest→host traffic to
`>=1024` (including NFS 2049) is dropped by ufw despite our accept — only libvirt's `LIBVIRT_INP`
(DNS 53 / DHCP 67) passes. install.sh therefore detects an active ufw and adds:
`ufw allow in on <bridge> proto {tcp,udp} from <subnet> to <host_ip> port 1024:65535`. uninstall.sh
removes them (bridge read from `~/.fclaude/vm`). `<1024` stays blocked by ufw's default-deny plus our
nft reject (defense in depth). When ufw is inactive, host INPUT defaults to accept and the nft table
alone enforces the policy.

### Bridge / gateway IP persistence
`virbr0` and its `192.168.122.1` address are owned by the libvirt **network** `default`, not by the
VM. install.sh runs `virsh net-autostart default` + `virsh net-start default` (step 7), so the bridge
and gateway IP come up at host boot and stay up **whether or not the VM is running** — host services
can bind to `192.168.122.1` with the VM shut off. The nfs-server drop-in above guarantees nfsd starts
only after that address exists.

### `vm/` guest provisioning
| File | Role |
|---|---|
| `cloud-init/meta-data.tmpl` | instance-id + hostname. |
| `cloud-init/user-data.tmpl` | First boot: create `fclaude` (uid 1000, sudo NOPASSWD) + `maint` users, inject pubkeys, growpart/resize, install `ansible`+`git`, drop the playbook via `write_files` (base64), run it locally (`-e host_user=<USER> -e fclaude_update_apt=<0\|1>`). |
| `cloud-init/network-config.tmpl` | NoCloud netplan v2 (rendered into 50-cloud-init.yaml at first boot via `cloud-localds -N`): DHCP on the pinned NIC **plus a conservative `mtu` (`FCLAUDE_VM_MTU`, default 1400)** so the VM tolerates host-side tunnels (e.g. WireGuard ~1420) without black-holing large TLS responses. Baked into the primary file — **never** a second netplan drop-in (a second file re-declaring the interface conflicts with `match`/`set-name`), and **never** applied live with `netplan apply` over ssh (that disrupts the running NIC and needs a reboot to recover). |
| `ansible/site.yml` | In-VM provisioning (runs IN the VM, `-c local`): base tools, **Docker** (get.docker.com, fclaude in docker group), **Node 22 (for user projects, not Claude)**, **native Claude Code** (`claude.ai/install.sh \| bash`, run as fclaude) + `autoUpdatesChannel: latest` merged into `settings.json`, headless-chrome runtime libs (no browser), `/home/fclaude` ownership, host-home bind mount, and the **weekly apt-upgrade** assets (script + service + timer, enabled when `fclaude_update_apt=1`) + seeded `last-apt-success`. Shipped base64-embedded in the seed; the `vm/update/*` assets are injected into `__*_B64__` placeholders by install.sh before embedding. |
| `headroom/` | Headroom proxy assets embedded into `site.yml` by install.sh: `fclaude-headroom.service` (persistent proxy on loopback `:8787`, `--no-http2`), `fclaude-headroom-activate.{sh,service}` (repoint `current` + restart at boot / when idle). |
| `metrics/` | `prometheus.yml.tmpl` (5m scrape of `:8787`, remote_write, `password_file`) + `fclaude-metrics.service` (`prometheus --agent`, gated on the delivered secret). |
| `libvirt/domain.xml.tmpl` | KVM domain: 8 GiB / 6 vCPU, q35, **named CPU model `FCLAUDE_VM_CPU_MODEL` (EPYC-Milan) — NOT host-passthrough** (raw Zen4 passthrough corrupts guest memory; see below), raw vda (cache=none, discard=unmap), seed cdrom, virtio NIC with fixed MAC, guest agent, virtio-rng. |

### `vm/update/` (weekly system-package upgrade; injected into site.yml, also pushed by `fclaude-update-setup`)
| File | Installed to (VM) | Role |
|---|---|---|
| `fclaude-apt-upgrade.sh` | `/usr/local/sbin/` | Runs as root. **Network-speed gate** first: a size+time-capped `curl` probe (`FCLAUDE_UPDATE_PROBE_URL`, default Cloudflare `__down`) measures the VM's downlink (= host uplink via NAT); if below `FCLAUDE_UPDATE_MIN_MBIT` (default 100) — or offline — it stamps `skipped(slow:N)` and exits 0 (a skip is success). `FCLAUDE_UPDATE_FORCE=1` bypasses the gate (set by `fclaude --update`). Then dpkg-lock wait → `apt-get update` + `upgrade --force-confold` (not dist-upgrade) + `autoremove`. Writes two stamps under `/var/lib/fclaude/`: `last-apt-upgrade` (every attempt: `ok`/`error`/`skipped(...)`) and `last-apt-success` (**ISO content**, advanced only on a real upgrade — drives the staleness warning). Config from `/etc/default/fclaude-update`; every value has a built-in default. |
| `fclaude-apt-upgrade.service` | `/etc/systemd/system/` | Oneshot; `EnvironmentFile=-/etc/default/fclaude-update`; `Nice=10`, idle IO; after `network-online`+`docker`. |
| `fclaude-apt-upgrade.timer` | `/etc/systemd/system/` | `OnCalendar=Sun 04:00`, `RandomizedDelaySec=1h`, `Persistent=true` (catches up on next boot if the VM was off). |
| `fclaude-headroom-update.sh` | `/usr/local/sbin/` | Runs as root, chained (best-effort) from the tail of `fclaude-apt-upgrade.sh`, so it only fetches on a link the gate already approved. **Stages** the latest `headroom-ai[proxy]` into `/opt/headroom/versions/<ver>` without touching the running proxy (activation is `fclaude-headroom-activate`'s job), and **upgrades tokensave** (`tokensave upgrade` as the `fclaude` user, then `tokensave reinstall` on a version bump) — both skipped while `pgrep -x claude` finds a live session. Logs to `/var/log/fclaude-headroom-update.log`. |

### `test/`
| File | Role |
|---|---|
| `test-staleness.sh` | Pure-logic unit tests for `_fc_staleness_messages`/`_fc_days_ago` (reason selection, apt-disabled, claude branch, non-numeric epochs, `set -e` safety). `bash test/test-staleness.sh`; run by CI. |
| `test-mount-hold.sh` | Unit tests for `fclaude --mount <dir>…` (mount-only hold mode). Runs the real script against a temp config + a stub lib that sources the real `common.sh` and overrides only `ensure_vm_running`/`mount_workspace`/`unmount_workspace`, so path validation is exercised for real. Covers: mount order + reverse-order release on Ctrl+C, repeat/`..` dedup, relative paths, no-dirs error, missing dir and out-of-`$HOME` dir (nothing mounted, VM not started), and release-what-was-held when one mount fails. A watchdog SIGINTs the script once it reports holding (falls back to SIGTERM — same trap — if the suite itself was backgrounded, where POSIX makes async children ignore SIGINT). `bash test/test-mount-hold.sh`; run by CI. |

## Runtime state (created on the host by install, never in the repo)
`~/.fclaude/`: `fclaude-disk` (32G raw ext4 VM disk), `seed.iso`, `config`, `vm` (identity),
`ssh/` (keypairs), `exports/` (NFSv4 root; session dirs bind-mounted here), `refcounts/`
(flock-guarded counters).

## Key design decisions (and where they live)
- **Full KVM VM, not a container** — separate kernel for the yolo agent. (`vm/libvirt/`, plan §3)
- **NFSv4-only on 2049** so sharing survives the "<1024 blocked" firewall rule. (`host/nfs/`, plan §8–9)
- **Bind-mount-into-export-root** — VM only sees per-session shared dirs, never all of `$HOME`. (`lib/common.sh` `_do_mount`, plan §9)
- **uid 1000 ↔ 1000** — host `robymus` and VM `fclaude` share uid for transparent NFS ownership.
- **Host-path bind mount** — the VM bind-mounts `/home/fclaude` onto `/home/<hostuser>` so claude's
  getcwd-derived **absolute paths match the host** (a symlink would canonicalize back to
  `/home/fclaude` and break VS Code file links; verified on the live VM). The `shared` root
  propagates each per-session NFS mount through the bind automatically. Set up idempotently in
  `vm/ansible/site.yml` (host login passed from cloud-init as `-e host_user=<USER>`). The
  `fclaude-vscode` wrapper `cd`s into the host absolute path so paths round-trip to the editor.
- **Conservative NIC MTU (1400)** — baked into the cloud-init network config so the VM advertises a
  small TCP MSS and survives host tunnels (WireGuard etc.) that would otherwise PMTU-blackhole large
  responses (symptom: `claude` hangs at startup, big fetches time out). Set it once at the netplan
  source; do not add a second netplan file or run a live `netplan apply`. (`vm/cloud-init/network-config.tmpl`, `FCLAUDE_VM_MTU`)
- **Named CPU model, not `host-passthrough`** — passing this AMD Zen4 (Ryzen 7840HS) laptop CPU
  through raw caused guest-wide memory corruption: reserved-bit page faults and random
  multi-process SIGSEGV (incl. PID 1), VM dying every few minutes. Traced to an AVX-512/Zen4 path
  miscomputing under KVM; a KVM-validated model without AVX-512 (`EPYC-Milan`) ran 80+ min clean.
  (`FCLAUDE_VM_CPU_MODEL`, `vm/libvirt/domain.xml.tmpl`)
- **Headroom compression proxy (in-VM), driven by env — NEVER `headroom wrap`** — Claude's API traffic
  runs through a shared in-VM Headroom proxy (`fclaude-headroom.service`, loopback `:8787`). The
  wrappers point Claude at it themselves and **must not** call `headroom wrap claude`: wrap
  unconditionally writes `env.ANTHROPIC_BASE_URL` into `<cwd>/.claude/settings.local.json`, plus a
  `SessionStart` self-heal hook and a `.headroom_wrap_marker.json` sidecar (headroom 0.32.1
  `wrap.py:4630`/`:4646`, their #951/#2221), and offers **no flag or env var to turn it off**. Our cwd
  is an NFS share of a **host** directory, so those writes land in the user's real repo — where a
  `claude` running *outside* the VM reads `ANTHROPIC_BASE_URL=127.0.0.1:8787` and dies with
  ConnectionRefused. It fired even when Claude was never engaged: the VS Code extension runs
  `fclaude-vscode` for *every* claude process, so just opening the folder poisoned the repo. On clean
  exit wrap restores the env key but **leaves the hook**; only `headroom unwrap` removes it.
  What the launch line reproduces instead (all verified against wrap.py and on the wire in
  `~/.headroom/logs/proxy.log`): `ANTHROPIC_BASE_URL`; `ANTHROPIC_MODEL=<model>[1m]`, which is the
  *entirety* of `--1m` (`wrap.py:4676`→`:229`) — Claude Code converts the `[1m]` suffix into the
  `context-1m-2025-08-07` beta header, and the env overrides settings.json, so the model is a config
  knob (`FCLAUDE_HEADROOM_MODEL`, default `claude-opus-5`); `ENABLE_TOOL_SEARCH=true` (#746, below);
  and `ANTHROPIC_CUSTOM_HEADERS: X-Headroom-Project` for per-project savings attribution
  (percent-encoded exactly like headroom's `quote(name, safe="-_.() ")`). The base URL is *also*
  passed as inline `--settings` JSON — the one job the settings file did, since cc-daemon spawns
  conversation workers that re-read settings rather than inherit the environment (#951); `--settings`
  takes a JSON **string**, so no file is written anywhere. MCP registration (headroom retrieve +
  tokensave) is durable user-scope state in the VM's `~/.claude.json` that wrap only re-asserted —
  `site.yml` provisions it once. The one genuinely per-session job wrap did, the tokensave code-graph
  refresh (`wrap.py:1542`), is now `_tokensave_prepare_cmd`. Replaces the old llm.log proxy entirely (no
  `HTTPS_PROXY`, no host bridge); removal strips any stale llm.log block from the VM's
  `/etc/environment`. (`vm/headroom/`, `lib/common.sh` `_headroom_launch`, `test/test-headroom-launch.sh`)
- **tokensave is per-project, and must be `init`ed there** — the code-graph store is
  `<workspace>/.tokensave/` (`tokensave.db` + `config.json` + `branch-meta.json`); the VM home's
  `~/.tokensave/` is a *different* thing, the global store (config, the savings ledger `global.db`,
  mmaps). `tokensave sync` refuses to run on an uninitialized project, and with no local store
  tokensave resolves **upward to a parent project** — so a stray index at `/home/fclaude` (one was
  created by a 7.0.2-era sync that walked all of `$HOME`, 204 MB) can silently capture every repo
  mounted beneath it. `_tokensave_prepare_cmd` is immune by construction: `init` when `.tokensave/` is
  absent, `sync` when present, so each workspace is self-contained. Cost is a one-time full index on
  the first launch in a repo (capped by `FCLAUDE_TOKENSAVE_TIMEOUT`, silent, non-fatal — tokensave
  finishes lazily on demand). Measured on 7.7.0: `sync` of an unchanged project is ~12 ms.
- **Keeping the code-graph store out of git — global ignore first, repo `.gitignore` as fallback** —
  7.0.x appended a `.gitignore` entry for its own store, 7.7.0 does not, so a 200 MB+ binary index
  otherwise sits untracked in a fresh repo, one `git add -A` from being committed. Preferred fix is a
  **global** ignore: git reads `$XDG_CONFIG_HOME/git/ignore` (→ `~/.config/git/ignore`) **by default**,
  with no `core.excludesFile` needed. `site.yml` provisions it in the VM (`lineinfile`, so a user's own
  entries are never clobbered); the host side is the user's own one-liner (see README) — install.sh
  deliberately does NOT edit a personal git config. When neither covers it, `_tokensave_prepare_cmd`
  appends the entry to the repo. `git check-ignore` is the test rather than a grep for the literal, so
  a global ignore, `.git/info/exclude`, a parent `.gitignore` or any other pattern form all count as
  already-handled — which is what makes the fallback **stand down** the moment a global ignore exists,
  leaving the repo completely untouched. Outside a git repo, or before a store exists, it writes
  nothing. Appends only to the `.gitignore` of the directory holding the store, and never joins its
  entry onto an unterminated last line. Note for anyone extending `test-headroom-launch.sh`: those
  cases run git under `HOME`/`XDG_CONFIG_HOME`/`GIT_CONFIG_*` overrides, because a developer machine
  that globally ignores `.tokensave/` would otherwise make every "not ignored yet" case test the
  opposite of what it claims.
- **tokensave telemetry is off** — it ships with `upload_enabled = true`, reporting token-savings
  counts to a worldwide counter. `site.yml` disables it via `tokensave disable-upload-counter` (the
  CLI, not a hand-written `~/.tokensave/config.toml`, so tokensave keeps ownership of that file's
  other defaults). This is VM-local state, so provisioning has to own it or a rebuild silently opts
  back in. Re-enable per VM with `tokensave enable-upload-counter`.
- **tokensave never self-updates** — headroom's `ensure_tokensave()` returns early whenever the binary
  exists, so it installs ONCE and then stays pinned forever; ours sat at 7.0.2 while 7.7.0 shipped.
  The weekly `fclaude-headroom-update.sh` now runs `tokensave upgrade`, guarded by the same
  `pgrep -x claude` idle rule as `fclaude-headroom-activate` (the upgrade replaces a binary a live
  `tokensave serve` is executing). A version bump is followed by `tokensave reinstall`, because a
  release that adds MCP tools leaves them unpermitted until the agent integration is refreshed
  (7.0.2 → 7.7.0 added 81, and Claude Code warns on every call until then). `reinstall` is run from
  `$HOME` and without `--local`, so it only ever touches the VM's USER-scope config — never a mounted
  repo. Install and upgrade are two different mechanisms with different trust models: headroom's
  `ensure_tokensave()` fetches the GitHub **release tarball** and verifies it against a **pinned
  SHA-256** (never `cargo install` — a multi-minute compile in the launch path), while `tokensave
  upgrade` is the vendor's own self-updater and pins nothing beyond the release channel.
  (`vm/update/fclaude-headroom-update.sh`)
- **`vm/update/*` only reaches an existing VM through `fclaude-update-setup`** — site.yml runs at
  first boot, so editing a script under `vm/update/` changes nothing on a VM that is already
  provisioned. That is exactly how the tokensave upgrade above silently did not happen: the script
  landed in the repo, the VM kept the copy it was built with, and `fclaude --update` (which chains
  apt → `fclaude-headroom-update.sh`) dutifully ran the old one. `fclaude-update-setup` now pushes
  that script alongside the apt assets; **run it after any `vm/update/` change**.
- **tokensave's update notice is silenced (`TOKENSAVE_UPDATE_CHECK=0`)** — this is not noise
  suppression. Its MCP server PREPENDS "⚠️ tokensave vX is installed, but vY is available…" as an
  extra content block to **every tool result** (`mcp/server.rs check_version_update`) — in the
  payload, not on stderr — so one minor release upstream corrupts every tokensave tool result until
  the binary is upgraded. The flag disables only the *passive* notice (`init`/`sync`/`status` + the
  MCP server); explicit `tokensave upgrade` and `doctor` ignore it, so weekly upgrades still work.
  Set in **two** places, deliberately: the wrappers export it on the session line
  (`_tokensave_prepare_cmd`), which covers every VM regardless of vintage and cannot be undone by
  `tokensave reinstall` (which rewrites the MCP registration, so a per-server `env` would be lost);
  and the VM's `~/.profile` (site.yml, retrofit by `fclaude-update-setup`) covers sessions the
  wrappers did not launch. claude's MCP servers and tokensave's own hooks inherit it either way.
- **`HEADROOM_TOOL_SEARCH=off` — the two tool-deferral layers must not stack** — Claude Code already
  defers its own tool schemas and resolves them **client-side** through its own `ToolSearch` tool;
  `headroom wrap` keeps that on by setting `ENABLE_TOOL_SEARCH=true` (needed because Claude Code
  disables deferral whenever `ANTHROPIC_BASE_URL` is custom — headroom #746). Measured on the wire:
  deferral on = 11 tools / 40 KB of schema per request, off = 147 tools / 170 KB. The proxy has a
  *second*, server-side layer that marks tools `defer_loading` and injects an Anthropic
  `tool_search_tool_regex`; it fired on every request (`transforms=… router:tool_search_deferral:15tools`
  in `~/.headroom/logs/proxy.log`) even though its source calls it opt-in. Stacked on top of Claude
  Code's own deferral it breaks tool loading two ways: the model's searches come back as
  `server_tool_use` / `tool_search_tool_result` blocks, which the **VS Code extension's webview cannot
  render** (`Unsupported content type: server_tool_use` — the string lives in the extension bundle, not
  in `claude`, which is why the CLI path survives; cf. claude-code#64158); and the injected regex only
  indexes the ~15 tools present in the request, while every real deferred tool (MCP servers, WebSearch,
  …) lives in Claude Code's client-side registry — so every lookup answers "no tools match". Headroom's
  keep-resident allowlist is lowercase opencode names (`bash`, `read`, `edit`), so no Claude Code tool
  ever matches it and even `ToolSearch` itself gets deferred. Pinned off explicitly in the unit rather
  than trusting the documented opt-in default. (`vm/headroom/fclaude-headroom.service`)
- **Headroom updates never restart a live proxy** — `fclaude-headroom-update.sh` (weekly, piggybacks
  the network-gated apt run) STAGES the latest `headroom-ai[proxy]` into a new versioned venv without
  touching the running proxy; `fclaude-headroom-activate` repoints `/opt/headroom/current` + restarts
  only at boot (Before the proxy) or when the wrapper sees no Claude running (`headroom_activate_if_idle`,
  flock-guarded). Stale-until-safe is acceptable. Same versioned-dir + symlink discipline as native Claude.
- **Grafana Cloud metrics via in-VM `prometheus --agent`** — scrapes headroom `:8787/metrics` every 5m
  and `remote_write`s to Grafana Cloud (`host`/`vm` external labels from config). Creds come from a
  **SOPS**-encrypted secret (`secrets/secrets.sops.yaml`, age recipients in `.sops.yaml`); the age key
  stays on the host and only the derived token (`/etc/fclaude/gc_token`, fclaude-readable) + rendered
  `prometheus.yml` reach the VM (accepted: a metrics-write-only token lives in the sandbox). Delivered
  by `host/bin/fclaude-metrics-setup` (install.sh step 14). (`vm/metrics/`, `secrets/`)
- **Reference counting** (flock + counter files) so CLI and VS Code sessions on the same dir coexist; unmount only on last exit. (`lib/common.sh`) A refcount can still leak if a wrapper is `SIGKILL`ed before its EXIT trap, or — the case that motivated `fc_is_mcp_helper` — when a long-lived helper (the chrome-MCP singleton) genuinely outlives its editing session. The first is recovered manually with `fclaude --cleanup <ws>`; the second is now prevented at the source by not mounting for MCP helpers. A liveness/heartbeat reaper was considered and rejected: it can't catch the helper case (that process is genuinely alive), and a `--cleanup` escape hatch covers the SIGKILL case without a background timer.
- **Native Claude install + split update strategy** — Claude Code uses the **native installer** (not
  the npm global) specifically because its per-version dirs (`~/.local/share/claude/versions/*`) +
  atomic symlink swap make **updating safe while sessions run** (a live process keeps its version's
  files; the npm global's in-place `rm -rf`+extract could yank a lazy-loaded asset from a running
  session). So Claude keeps *itself* current via its background auto-updater on the `latest` channel —
  no host machinery, no "defer while a session is running" guard (which, with always-on sessions,
  would mean *never* updating). Only **system packages** need the custom weekly timer, and apt has no
  live-`claude` hazard. Installed **per-user as fclaude** (never root) so the auto-updater can write
  `~/.local`. PATH over non-interactive ssh already works via the wrapper's `CLAUDE_LOGIN_ENV`
  sourcing `~/.profile`. (`vm/ansible/site.yml`, `vm/update/`, `lib/common.sh`,
  `host/bin/fclaude-update-setup`)
- **Update staleness surfaced, not hidden** — the weekly apt run may legitimately *skip* for weeks on
  slow/mobile networks, so `fclaude_staleness_warn` keys off the **last successful** update
  (`last-apt-success`, parsed by **content** not mtime so a disk restore can't mask it) and, at
  session start, warns with the reason (failed / skipped-slow / never-run). Claude staleness is a
  best-effort check on the newest `versions/*` dir mtime. (`lib/common.sh`)
- **Single source of truth** — all IPs/paths/names in `lib/constants.sh`; everything else is templated.

## Verification status
Static checks performed in development: `bash -n` and **shellcheck** on every script (`.shellcheckrc`
records the two intentional repo-wide disables); `nft`/YAML/XML render validation; a standalone
self-test of the refcount/lock/relpath logic (including a 20-way concurrency test); full
template-render dry run with base64 playbook round-trip. **CI** (`.github/workflows/ci.yml`) enforces
shellcheck + `test/*.sh` (staleness + `--mount` hold mode) + a rendered-playbook ansible syntax-check
on every push/PR.

Auto-update (`.plans/auto-update-*`) was verified **live** on the running VM: native install +
`latest`-channel pin, weekly service run (probe 405 Mbit/s → apt → both stamps advanced),
slow-network skip preserving `last-apt-success`, force bypass, `fclaude --update` and channel-switch
(`stable` 2.1.212 → `latest` 2.1.220), `--status` version/apt lines, and the backdated-stamp
staleness warning. Other **live end-to-end
checks (VM boot, Docker-in-VM, firewall matrix, NFS round-trip, online disk grow) require running
`install.sh` on the host with sudo** and are listed in
[.plans/fclaude-TODO.md](.plans/fclaude-TODO.md) Phase 8.
