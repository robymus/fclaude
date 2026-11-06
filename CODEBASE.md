# fclaude — Codebase Map

Sandboxed Claude Code: runs `claude` in yolo mode inside a hardware-isolated KVM VM, mounting host
project directories on demand. See [.plans/fclaude-PLAN.md](.plans/fclaude-PLAN.md) for the full
design rationale and [README.md](README.md) for usage.

## Architecture in one picture

```
host wrapper (~/.local/bin/fclaude)
  → ensure_vm_running (virsh, system libvirt via 'libvirt' group)
  → mount_workspace   (bind-mount $HOME/<rel> into ~/.fclaude/exports, exportfs, NFSv4 mount in VM)
  → ssh fclaude@192.168.122.10 "cd /home/fclaude/<rel> && claude --dangerously-skip-permissions"
  → unmount_workspace on EXIT (reference counted)

VM "fclaude" (Ubuntu 24.04, KVM): uid-1000 fclaude user, sudo NOPASSWD, docker, node, and a
self-updating native Claude Code install (per-user `~/.local`, latest channel).
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
| `common.sh` | Runtime library sourced by all wrappers. Provides: `load_config`, logging, `ssh_vm`/`ssh_vm_tty`/`ssh_maint`, `vm_state`/`ensure_vm_running` (which sweeps all refcounts via `reset_refcounts_cold_boot` after a cold VM start), `with_lock` (flock), `refcount_*`, `relpath_under_home`, `mount_workspace`/`unmount_workspace` (the reference-counted bind-mount + NFSv4 flow), and the update helpers `fclaude_update` (on-demand `claude update` + gate-bypassed apt) / `fclaude_staleness_warn` (session-start warning) with the pure, unit-tested `_fc_staleness_messages`/`_fc_days_ago`. Host-privileged ops (`mount`/`umount`/`exportfs`) go through `sudo` (NOPASSWD drop-in). |

### `host/bin/` (installed to `~/.local/bin`)
| File | Role |
|---|---|
| `fclaude` | Main CLI. Flags: `--dir`, `--no-mount`, `--mount <dir>…`, `-m/--maintenance`, `-s/--status`, `--cleanup <ws>`, `--update`, `--shutdown`, `--`/passthrough. Passes `--dangerously-skip-permissions`. Mounts CWD, runs claude over ssh PTY, unmounts on EXIT trap. On a normal session start it calls `fclaude_staleness_warn` (best-effort, wrapped `set +e`) to flag a stalled updater. `--status` calls `fclaude_status` (in common.sh) — best-effort report (state/IP/vCPU/mem/uptime/CPU/disk + guest mem/df + **installed Claude version + last apt run** + active workspace refcounts) that never starts the VM. `--update` calls `fclaude_update` (Claude + apt now, network gate bypassed). `--mount <dir>…` is a one-shot mode that mounts one or more workspaces (refcounted, same as a session), prints their VM paths, then idles until Ctrl+C and unmounts in reverse order via an EXIT trap — no claude, for when you want a workspace visible in the VM for something else. All paths are validated up front (must exist and be under `$SHARE_ROOT`), repeats are collapsed, and a mount that fails partway still releases the ones already held. `--cleanup <ws>` calls `cleanup_workspace` — force-unmounts ONE workspace (VM-side `umount` w/ lazy fallback + host export bind + `exportfs -r` + dir cleanup) and clears its refcount regardless of count, for recovering a leaked mount; accepts a `--status`-style relpath or a real dir under `$HOME`; never starts the VM. |
| `fclaude-vscode` | VS Code `claudeCode.claudeProcessWrapper`. Drops `argv[1]` (the extension's bundled claude path — byte-identical to the VM's native claude) and forwards the rest. **Skips the mount for workspace-agnostic helper invocations** (`fc_is_mcp_helper`: first arg `mcp`, or any `--claude-in-chrome-mcp`) — those run in the VM home with no mount/cd/EXIT-trap, because the extension runs *every* claude process through this wrapper and the chrome-MCP singleton would otherwise pin (leak) the first workspace's mount for the life of the editor; discriminator is **fail-open** (unrecognized → mount). For a real session: mounts CWD, then `cd`s into the **host absolute path** (resolvable in the VM via the `/home/<hostuser>` bind mount) so claude reports host-matching paths back to the editor. stdio (`ssh -T`), **no** skip-permissions flag (extension sets that). **Errors out** if CWD isn't under `$HOME`. |
| `fclaude-grow-disk` | `qemu-img resize` → `virsh blockresize` → in-VM `growpart`+`resize2fs` (online ext4 grow). |
| `fclaude-llmlog-setup` | **Standalone** sudo installer for the llm.log gateway bridge (renders+enables `fclaude-llmlog.{socket,service}` from the repo templates and repoints the VM's `/etc/environment` proxy). Run from a repo checkout to apply the bridge without a full `install.sh`. NOT installed to `~/.local/bin` (it reads the templates relative to itself). |
| `fclaude-update-setup` | **Standalone** retrofit for auto-update on an already-provisioned VM (installed to `~/.local/bin`). Removes any npm-global Claude, installs/keeps the **native** Claude (as fclaude, no sudo — keeps `~/.local` user-owned), merges `autoUpdatesChannel: latest` into `settings.json` (jq, clobber-safe), pushes `vm/update/*` to `/usr/local/sbin` + `/etc/systemd/system`, writes `/etc/default/fclaude-update` from host knobs, seeds `last-apt-success`, and enables the weekly timer (or disables it when `FCLAUDE_UPDATE_APT=0`). `--now` forces one immediate gate-bypassed apt run. Idempotent; run from a repo checkout. |

### `host/` config templates (rendered + installed by install.sh)
| File | Installed to | Role |
|---|---|---|
| `config.template` | `~/.fclaude/config` | Runtime config the wrappers source. |
| `nftables/fclaude.nft.tmpl` | `/etc/fclaude/fclaude.nft` | Firewall: input hook prio -10, allow 53/67/123 to host, reject/drop other <1024 from VM subnet. Bridge name detected at install. |
| `nfs/fclaude.conf` | `/etc/nfs.conf.d/fclaude.conf` | NFSv4-only (port 2049 only, no rpcbind). |
| `nfs/fclaude.exports.tmpl` | `/etc/exports.d/fclaude.exports` | Single export root `~/.fclaude/exports` (fsid=0, crossmnt, root_squash, sec=sys). |
| `nfs/fclaude-sysctl.conf` | `/etc/sysctl.d/90-fclaude-nfs.conf` | `fs.leases-enable=0`: stops nfsd granting NFSv4 delegations. Write delegations on VM-created files made the host's `open(O_NONBLOCK)` return `EAGAIN`, which VS Code shows as `Unknown (FileSystemError)` on the first open (retry works). Host-wide: also disables `F_SETLEASE`. |
| `sudoers/fclaude.tmpl` | `/etc/sudoers.d/fclaude` | NOPASSWD for `mount`/`umount`/`exportfs` only. |
| `systemd/fclaude-firewall.service` | `/etc/systemd/system/` | Re-applies nft table on boot (after libvirtd). |
| `systemd/fclaude-exports.service` | `/etc/systemd/system/` | Recreates export root + `exportfs -r` on boot. |
| `systemd/nfs-server-after-libvirt.conf` | `/etc/systemd/system/nfs-server.service.d/fclaude.conf` | Drop-in ordering nfs-server after the libvirt network so nfsd can bind to `192.168.122.1`. |
| `systemd/fclaude-llmlog.{socket,service}` | `/etc/systemd/system/` | **llm.log gateway bridge.** Socket-activated `systemd-socket-proxyd` listening on `HOST_IP:port` (the NAT gateway, `FreeBind`) forwarding to host `127.0.0.1:port`. Lets the guest reach the loopback-only llm.log proxy over the NAT — replaces the old per-session `ssh -R`. Installed only when llm.log is enabled and `systemd-socket-proxyd` exists. Re-applied standalone by `host/bin/fclaude-llmlog-setup`. |

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
| `ansible/llmlog.yml` | **Host-run** playbook (control node = host, VM over ssh) for the optional llm.log integration: `ansible.builtin.copy` pushes the host's CA certs into the VM and `blockinfile` sets the proxy/CA env vars in `/etc/environment`. Run from the host (not in-VM) so `copy` can read the host's `~/.llm.log`. install.sh invokes it last (step 14), gated on host certs + `ansible-playbook`. NB: pass ssh `-o` opts in **attached** form (`-oKey=val`) — ansible-core 2.19 argparse-crashes on the space-separated form. |
| `libvirt/domain.xml.tmpl` | KVM domain: 8 GiB / 6 vCPU, q35, **named CPU model `FCLAUDE_VM_CPU_MODEL` (EPYC-Milan) — NOT host-passthrough** (raw Zen4 passthrough corrupts guest memory; see below), raw vda (cache=none, discard=unmap), seed cdrom, virtio NIC with fixed MAC, guest agent, virtio-rng. |

### `vm/update/` (weekly system-package upgrade; injected into site.yml, also pushed by `fclaude-update-setup`)
| File | Installed to (VM) | Role |
|---|---|---|
| `fclaude-apt-upgrade.sh` | `/usr/local/sbin/` | Runs as root. **Network-speed gate** first: a size+time-capped `curl` probe (`FCLAUDE_UPDATE_PROBE_URL`, default Cloudflare `__down`) measures the VM's downlink (= host uplink via NAT); if below `FCLAUDE_UPDATE_MIN_MBIT` (default 100) — or offline — it stamps `skipped(slow:N)` and exits 0 (a skip is success). `FCLAUDE_UPDATE_FORCE=1` bypasses the gate (set by `fclaude --update`). Then dpkg-lock wait → `apt-get update` + `upgrade --force-confold` (not dist-upgrade) + `autoremove`. Writes two stamps under `/var/lib/fclaude/`: `last-apt-upgrade` (every attempt: `ok`/`error`/`skipped(...)`) and `last-apt-success` (**ISO content**, advanced only on a real upgrade — drives the staleness warning). Config from `/etc/default/fclaude-update`; every value has a built-in default. |
| `fclaude-apt-upgrade.service` | `/etc/systemd/system/` | Oneshot; `EnvironmentFile=-/etc/default/fclaude-update`; `Nice=10`, idle IO; after `network-online`+`docker`. |
| `fclaude-apt-upgrade.timer` | `/etc/systemd/system/` | `OnCalendar=Sun 04:00`, `RandomizedDelaySec=1h`, `Persistent=true` (catches up on next boot if the VM was off). |

### `test/`
| File | Role |
|---|---|
| `test-staleness.sh` | Pure-logic unit tests for `_fc_staleness_messages`/`_fc_days_ago` (reason selection, apt-disabled, claude branch, non-numeric epochs, `set -e` safety). `bash test/test-staleness.sh`; run by CI. |
| `test-mount-selfheal.sh` | Unit tests for the refcounted mount flow in `common.sh`, against a fake mount table (`mountpoint`/`sudo`/`ssh_vm` stubbed; no VM, sudo or network). Covers cold mount, a second session joining a live share, **the leaked-refcount regression** (counter alive, share gone → warn + re-seat to 1 + re-share), a VM that lost its mount under a live counter, rollback on a failed VM mount, last-holder unmount, and the **cold-boot sweep** (`ensure_vm_running` with `virsh`/`ssh_vm` stubbed: `shut off` clears every counter, `running`/`paused` keep them). `bash test/test-mount-selfheal.sh`; run by CI. |
| `test-mount-hold.sh` | Unit tests for `fclaude --mount <dir>…` (mount-only hold mode). Runs the real script against a temp config + a stub lib that sources the real `common.sh` and overrides only `ensure_vm_running`/`mount_workspace`/`unmount_workspace`, so path validation is exercised for real. Covers: mount order + reverse-order release on Ctrl+C, repeat/`..` dedup, relative paths, no-dirs error, missing dir and out-of-`$HOME` dir (nothing mounted, VM not started), and release-what-was-held when one mount fails. A watchdog SIGINTs the script once it reports holding (falls back to SIGTERM — same trap — if the suite itself was backgrounded, where POSIX makes async children ignore SIGINT). `bash test/test-mount-hold.sh`; run by CI. |

## Windows host tooling (experimental, Go) — `feat/windows`
Design and status: [.plans/windows-PLAN.md](.plans/windows-PLAN.md), checklist
[.plans/windows-TODO.md](.plans/windows-TODO.md). **Hypervisor: VMware Workstation Pro** (switched
from VirtualBox on 2026-11-06 after it stalled under load on the Hyper-V API; the `vbox` package
below is replaced by `vmw` in Phase 6). Workspaces shared as per-workspace **HGFS** folders, VS Code wrapper that **rewrites paths** on the stdio JSON stream
(`C:\dev\app` ⇄ `/home/fclaude/app`). The Linux code paths are unchanged; the shared VM assets
gain a `fclaude_platform` (`kvm` | `vmware`) switch.

| Path | Role |
|---|---|
| `go.mod` | Module `github.com/robymus/fclaude` (repo root), Go 1.26. |
| `.golangci.yml` | golangci-lint v2 config; CI lints for `GOOS=linux` **and** `GOOS=windows`. |
| `cmd/fclaude-win/` | Single binary; personality from exe name: `fclaude`, `fclaude-vscode`, `fclaude-setup`, `fclaude-capture`. Only `--version`/`--help` and **capture** work so far; the rest exit 2 "not implemented". |
| `vm/assets.go` | `go:embed` of site.yml, cloud-init templates, `vm/update/*` — Windows provisions from the same files as Linux. |
| `vm/cloud-init/network-config-win.tmpl` | Two-NIC netplan (NAT dhcp `nat0` + host-only static `host0`), both matched by MAC, hypervisor-neutral. MTU from the host tooling (`seed.MTU` = 1500 for VMware, whose NAT breaks HTTPS below that). |
| `internal/win/winpath` | Pure-string Windows path semantics (Clean/Rel/WorkspaceRel), identical on Linux CI. Workspaces must be strictly under the share root and not a top-level dot dir. |
| `internal/win/config` | `config.json` (defaults overlay, unknown keys rejected, all errors reported at once), `WriteNoClobber` → `.new`. |
| `internal/win/pathmap` | `Mapper` (whole-path + embedded-in-text, file URIs) and `Rewriter`: byte-preserving JSON scanner, rewrites string *values* only, never file-content fields inside tool inputs (`Policy`); `Args` for argv; `Pipe` for streams. Fuzzed. |
| `internal/win/lease` | Liveness-based refcount: lease file per holder `<pid>-<starttime>`, per-workspace lock (flock / LockFileEx), `Acquire(setup)`/`Release(teardown)`/`Reap`/`Sweep`/`List`. Replaces Linux EXIT-trap counters (Windows kills the wrapper with TerminateProcess). |
| `internal/win/vbox` | VBoxManage front end via `Runner`: state, start/stop, transient shares, `--machinereadable` parser, `DefineCommands`/`ImportDiskCommands`, `Locate` (env → registry → default). Fixtures are provisional until re-captured on Windows. |
| `internal/win/seed` | Renders user-data/meta-data/network-config (port of install.sh `render`, fails on leftover `__VAR__`), writes a Rock Ridge ISO labelled `cidata` (go-diskfs). |
| `internal/win/capture` | R4 research tool: run a command with argv/env(redacted)/cwd/stdio logged as JSONL. |
| `internal/win/procjob` | Child dies with us: Job Object KILL_ON_JOB_CLOSE (Windows) / Pdeathsig (Linux). |
| `internal/win/mcphelper` | Port of `fc_is_mcp_helper`. |
| `.github/workflows/release-windows.yml` | Tag `v*-win*` → lint+test gate → `fclaude-setup.exe` + `SHA256SUMS` prerelease. |

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
- **Optional llm.log proxy** — route claude's API traffic in the VM through the host's llm.log
  (an HTTPS-logging MITM proxy bound to host loopback, hard-coded to `127.0.0.1`). Set
  `LLMLOG_PROXY_PORT` (config) → `install.sh` step 10 installs a **gateway bridge**
  (`fclaude-llmlog.socket`, a socket-activated `systemd-socket-proxyd` on `HOST_IP:<port>` →
  `127.0.0.1:<port>`) and step 14 (host-run `llmlog.yml`) installs the CA + sets the proxy/CA env
  vars in `/etc/environment` with `HTTPS_PROXY=http://HOST_IP:<port>` (picked up by pam_env for the
  wrapper's non-interactive ssh exec). The guest reaches the proxy over the NAT — no per-session
  forward, so concurrent sessions don't collide. **Why not `ssh -R`** (the original approach): a
  reverse forward installs a *single exclusive* listener in the VM, so a second concurrent session's
  `-R` bind fails silently and — worse — when the listener's owning session exits, ssh blocks
  waiting for the *other* session's still-open forwarded connections to drain (the "hang on exit").
  The shared host bridge has none of these failure modes. Apply the bridge alone (no full install)
  with `sudo ./host/bin/fclaude-llmlog-setup`. (`host/systemd/fclaude-llmlog.*`,
  `host/bin/fclaude-llmlog-setup`, `vm/ansible/llmlog.yml`)
- **Reference counting** (flock + counter files) so CLI and VS Code sessions on the same dir coexist; unmount only on last exit. (`lib/common.sh`) A refcount can still leak if a wrapper is `SIGKILL`ed before its EXIT trap, or — the case that motivated `fc_is_mcp_helper` — when a long-lived helper (the chrome-MCP singleton) genuinely outlives its editing session. The second is prevented at the source by not mounting for MCP helpers. A liveness/heartbeat reaper was considered and rejected: it can't catch the helper case (that process is genuinely alive).
- **A cold VM boot invalidates every refcount.** The mounts the counters track live *inside* the VM,
  so a VM coming up from `shut off` (or an unknown state) holds none of them: `ensure_vm_running`
  calls `reset_refcounts_cold_boot` once the guest is reachable, clearing all `*.count`/`*.rel`.
  This is the root-cause fix for counters that outlive a host reboot. A resume from `paused` does
  **not** sweep — those mounts survived. (`lib/common.sh`)
- **The counter is not evidence — the mount table is.** `_do_mount` is idempotent and self-healing: it re-establishes any missing layer of the share (host bind, export, VM NFS mount) *whatever* the counter says, and when nothing is shared it **re-seats a leaked counter to 1** (with a warning) instead of counting on top of it; it also re-verifies the guest mount on every session, so a VM reboot under a live counter heals too. The old `count != 1 → nothing to do` shortcut turned a leaked counter (SIGKILLed terminal, desktop-session logout, host reboot) into a silent no-mount whose only symptom was a bare `cd: /home/fclaude/<ws>: No such file or directory` from the guest — with the host directory perfectly fine and NFS perfectly healthy. `fclaude --status` now also lists counters with no share behind them, and both wrappers replace the guest's raw `cd` failure with a message naming fclaude and the `--cleanup` escape hatch. (`lib/common.sh` `_do_mount`, `test/test-mount-selfheal.sh`)
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
