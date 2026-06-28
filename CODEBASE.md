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

VM "fclaude" (Ubuntu 24.04, KVM): uid-1000 fclaude user, sudo NOPASSWD, docker, node+claude-code.
Host firewall (nftables table 'fclaude') blocks VM→host privileged ports (<1024) except 53/67/123.
```

## File-by-file

### Top level
| File | Role |
|---|---|
| `install.sh` | Host installer. 13 ordered steps: preflight → apt → dirs → disk (guarded) → ssh keys → cloud-init seed → libvirt net+DHCP → define/start VM → NFS+firewall+sudoers → systemd units → wrappers → config/vm-info → wait-for-provision. Idempotent where practical. Uses `sudo virsh` during install; renders templates via the `render()` helper. |
| `uninstall.sh` | Reverses install. Heavy confirmation (typed phrase). **Never deletes the disk silently** — separate `DELETE DISK` opt-in prompt at the very end. |
| `README.md` | User-facing quick start. |
| `REQUIREMENTS.md` | Original requirements (source of truth for intent). |

### `lib/`
| File | Role |
|---|---|
| `constants.sh` | **Single source of truth** for names/IPs/MAC/paths/sizes. Sourced by install/uninstall and baked into `~/.fclaude/config`. Change defaults here. |
| `common.sh` | Runtime library sourced by all wrappers. Provides: `load_config`, logging, `ssh_vm`/`ssh_vm_tty`/`ssh_maint`, `vm_state`/`ensure_vm_running`, `with_lock` (flock), `refcount_*`, `relpath_under_home`, and `mount_workspace`/`unmount_workspace` (the reference-counted bind-mount + NFSv4 flow). Host-privileged ops (`mount`/`umount`/`exportfs`) go through `sudo` (NOPASSWD drop-in). |

### `host/bin/` (installed to `~/.local/bin`)
| File | Role |
|---|---|
| `fclaude` | Main CLI. Flags: `--dir`, `--no-mount`, `-m/--maintenance`, `-s/--status`, `--cleanup <ws>`, `--shutdown`, `--`/passthrough. Passes `--dangerously-skip-permissions`. Mounts CWD, runs claude over ssh PTY, unmounts on EXIT trap. `--status` calls `fclaude_status` (in common.sh) — a best-effort report (state/IP/vCPU/mem/uptime/CPU/disk + guest mem/df + active workspace refcounts) that never starts the VM. `--cleanup <ws>` calls `cleanup_workspace` — force-unmounts ONE workspace (VM-side `umount` w/ lazy fallback + host export bind + `exportfs -r` + dir cleanup) and clears its refcount regardless of count, for recovering a leaked mount; accepts a `--status`-style relpath or a real dir under `$HOME`; never starts the VM. |
| `fclaude-vscode` | VS Code `claudeCode.claudeProcessWrapper`. Drops `argv[1]` (the extension's bundled claude path — byte-identical to the VM's native claude) and forwards the rest. **Skips the mount for workspace-agnostic helper invocations** (`fc_is_mcp_helper`: first arg `mcp`, or any `--claude-in-chrome-mcp`) — those run in the VM home with no mount/cd/EXIT-trap, because the extension runs *every* claude process through this wrapper and the chrome-MCP singleton would otherwise pin (leak) the first workspace's mount for the life of the editor; discriminator is **fail-open** (unrecognized → mount). For a real session: mounts CWD, then `cd`s into the **host absolute path** (resolvable in the VM via the `/home/<hostuser>` bind mount) so claude reports host-matching paths back to the editor. stdio (`ssh -T`), **no** skip-permissions flag (extension sets that). **Errors out** if CWD isn't under `$HOME`. |
| `fclaude-grow-disk` | `qemu-img resize` → `virsh blockresize` → in-VM `growpart`+`resize2fs` (online ext4 grow). |
| `fclaude-llmlog-setup` | **Standalone** sudo installer for the llm.log gateway bridge (renders+enables `fclaude-llmlog.{socket,service}` from the repo templates and repoints the VM's `/etc/environment` proxy). Run from a repo checkout to apply the bridge without a full `install.sh`. NOT installed to `~/.local/bin` (it reads the templates relative to itself). |

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
| `cloud-init/user-data.tmpl` | First boot: create `fclaude` (uid 1000, sudo NOPASSWD) + `maint` users, inject pubkeys, growpart/resize, install `ansible`+`git`, drop the playbook via `write_files` (base64), run it locally (`-e host_user=<USER>`). |
| `cloud-init/network-config.tmpl` | NoCloud netplan v2 (rendered into 50-cloud-init.yaml at first boot via `cloud-localds -N`): DHCP on the pinned NIC **plus a conservative `mtu` (`FCLAUDE_VM_MTU`, default 1400)** so the VM tolerates host-side tunnels (e.g. WireGuard ~1420) without black-holing large TLS responses. Baked into the primary file — **never** a second netplan drop-in (a second file re-declaring the interface conflicts with `match`/`set-name`), and **never** applied live with `netplan apply` over ssh (that disrupts the running NIC and needs a reboot to recover). |
| `ansible/site.yml` | In-VM provisioning (runs IN the VM, `-c local`): base tools, **Docker** (get.docker.com, fclaude in docker group), Node 22 + `@anthropic-ai/claude-code`, headless-chrome runtime libs (no browser), `/home/fclaude` ownership, host-home bind mount. Shipped base64-embedded in the seed. |
| `ansible/llmlog.yml` | **Host-run** playbook (control node = host, VM over ssh) for the optional llm.log integration: `ansible.builtin.copy` pushes the host's CA certs into the VM and `blockinfile` sets the proxy/CA env vars in `/etc/environment`. Run from the host (not in-VM) so `copy` can read the host's `~/.llm.log`. install.sh invokes it last (step 14), gated on host certs + `ansible-playbook`. NB: pass ssh `-o` opts in **attached** form (`-oKey=val`) — ansible-core 2.19 argparse-crashes on the space-separated form. |
| `libvirt/domain.xml.tmpl` | KVM domain: 8 GiB / 6 vCPU, q35, **named CPU model `FCLAUDE_VM_CPU_MODEL` (EPYC-Milan) — NOT host-passthrough** (raw Zen4 passthrough corrupts guest memory; see below), raw vda (cache=none, discard=unmap), seed cdrom, virtio NIC with fixed MAC, guest agent, virtio-rng. |

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
- **Reference counting** (flock + counter files) so CLI and VS Code sessions on the same dir coexist; unmount only on last exit. (`lib/common.sh`) A refcount can still leak if a wrapper is `SIGKILL`ed before its EXIT trap, or — the case that motivated `fc_is_mcp_helper` — when a long-lived helper (the chrome-MCP singleton) genuinely outlives its editing session. The first is recovered manually with `fclaude --cleanup <ws>`; the second is now prevented at the source by not mounting for MCP helpers. A liveness/heartbeat reaper was considered and rejected: it can't catch the helper case (that process is genuinely alive), and a `--cleanup` escape hatch covers the SIGKILL case without a background timer.
- **Single source of truth** — all IPs/paths/names in `lib/constants.sh`; everything else is templated.

## Verification status
Static checks performed in development: `bash -n` on every script; `nft`/YAML/XML render validation;
a standalone self-test of the refcount/lock/relpath logic (including a 20-way concurrency test);
full template-render dry run with base64 playbook round-trip. **Live end-to-end checks (VM boot,
Docker-in-VM, firewall matrix, NFS round-trip, online disk grow) require running `install.sh` on the
host with sudo** and are listed in [.plans/fclaude-TODO.md](.plans/fclaude-TODO.md) Phase 8.
