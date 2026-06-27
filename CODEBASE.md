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
| `fclaude` | Main CLI. Flags: `--dir`, `--no-mount`, `-m/--maintenance`, `-s/--status`, `--shutdown`, `--`/passthrough. Passes `--dangerously-skip-permissions`. Mounts CWD, runs claude over ssh PTY, unmounts on EXIT trap. `--status` calls `fclaude_status` (in common.sh) — a best-effort report (state/IP/vCPU/mem/uptime/CPU/disk + guest mem/df + active workspace refcounts) that never starts the VM. |
| `fclaude-vscode` | VS Code `claudeCode.claudeProcessWrapper`. Same mount lifecycle but stdio (`ssh -T`) and **no** skip-permissions flag (extension sets that itself). |
| `fclaude-grow-disk` | `qemu-img resize` → `virsh blockresize` → in-VM `growpart`+`resize2fs` (online ext4 grow). |

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
| `cloud-init/user-data.tmpl` | First boot: create `fclaude` (uid 1000, sudo NOPASSWD) + `maint` users, inject pubkeys, growpart/resize, install `ansible`+`git`, drop the playbook via `write_files` (base64), run it locally. |
| `ansible/site.yml` | In-VM provisioning: base tools, **Docker** (get.docker.com, fclaude in docker group), Node 22 + `@anthropic-ai/claude-code`, headless-chrome runtime libs (no browser), `/home/fclaude` ownership. Shipped base64-embedded in the seed. |
| `libvirt/domain.xml.tmpl` | KVM domain: 8 GiB / 6 vCPU, q35, host-passthrough, raw vda (cache=none, discard=unmap), seed cdrom, virtio NIC with fixed MAC, guest agent, virtio-rng. |

## Runtime state (created on the host by install, never in the repo)
`~/.fclaude/`: `fclaude-disk` (32G raw ext4 VM disk), `seed.iso`, `config`, `vm` (identity),
`ssh/` (keypairs), `exports/` (NFSv4 root; session dirs bind-mounted here), `refcounts/`
(flock-guarded counters).

## Key design decisions (and where they live)
- **Full KVM VM, not a container** — separate kernel for the yolo agent. (`vm/libvirt/`, plan §3)
- **NFSv4-only on 2049** so sharing survives the "<1024 blocked" firewall rule. (`host/nfs/`, plan §8–9)
- **Bind-mount-into-export-root** — VM only sees per-session shared dirs, never all of `$HOME`. (`lib/common.sh` `_do_mount`, plan §9)
- **uid 1000 ↔ 1000** — host `robymus` and VM `fclaude` share uid for transparent NFS ownership.
- **Reference counting** (flock + counter files) so CLI and VS Code sessions on the same dir coexist; unmount only on last exit. (`lib/common.sh`)
- **Single source of truth** — all IPs/paths/names in `lib/constants.sh`; everything else is templated.

## Verification status
Static checks performed in development: `bash -n` on every script; `nft`/YAML/XML render validation;
a standalone self-test of the refcount/lock/relpath logic (including a 20-way concurrency test);
full template-render dry run with base64 playbook round-trip. **Live end-to-end checks (VM boot,
Docker-in-VM, firewall matrix, NFS round-trip, online disk grow) require running `install.sh` on the
host with sudo** and are listed in [.plans/fclaude-TODO.md](.plans/fclaude-TODO.md) Phase 8.
