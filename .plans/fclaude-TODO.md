# fclaude — Implementation TODO

Derived from [.plans/fclaude-PLAN.md](fclaude-PLAN.md). Phases are ordered so each can be tested
before the next. Check items off as completed.

---

## Phase 0 — Repo scaffolding & conventions

- [x] Create repo directory tree per plan §4 (`host/bin`, `host/nftables`, `host/systemd`, `vm/cloud-init`, `vm/libvirt`, `vm/ansible`, `lib/`)
- [x] Add `README.md` with quick-start (install, run, grow, uninstall, maintenance)
- [x] Add `.editorconfig` and choose shell style (bash, `set -euo pipefail`, 2-space indent)
- [x] Add `host/config.template` with all keys from plan §13 (placeholders for paths/IP/keys)
- [x] Decide version/constants file (subnet `192.168.122.0/24`, host IP `.1`, VM IP `.10`, MAC `52:54:00:fc:1a:de`, names) — single source referenced by install + templates

## Phase 1 — Shared bash library (`lib/common.sh`)

- [x] `load_config()` — source `~/.fclaude/config` and `~/.fclaude/vm`, validate required vars
- [x] Logging helpers: `info`/`warn`/`err`/`die` with consistent prefix
- [x] `ssh_vm()` (non-interactive) and `ssh_vm_tty()` (PTY) using `$SSH_KEY`, host-key handling for first connect
- [x] `ensure_vm_running()` — `virsh domstate`; `virsh start` if off; wait-for-ssh loop with timeout
- [x] Locking: `with_lock <key> <cmd>` using `flock` on `~/.fclaude/refcounts/<key>.lock`
- [x] Refcount: `refcount_inc`/`refcount_dec`/`refcount_get` (atomic under lock)
- [x] `relpath_under_home()` — validate dir is under `$HOME`, return `${dir#$HOME/}`, reject otherwise
- [x] `mount_workspace()` / `unmount_workspace()` (plan §9): bind-mount into export root, `exportfs -r`, NFS mount in VM, all refcounted

## Phase 2 — VM image build inputs (cloud-init + libvirt + ansible)

- [x] `vm/cloud-init/meta-data.tmpl` — fixed `instance-id`, hostname
- [x] `vm/cloud-init/user-data.tmpl` (plan §6.1): `fclaude` (uid 1000, sudo NOPASSWD, groups sudo+docker), `maint` user, pubkey placeholders, `growpart`/`resize_rootfs`, install `ansible`+`git`, `runcmd` → `ansible-playbook ... -c local`
- [x] Verify cloud-init copies the Ansible tree into the seed (`/var/lib/fclaude/ansible`) — decide mechanism (write_files vs. extra seed files)
- [x] `vm/libvirt/domain.xml.tmpl` (plan §7): 8 GiB / 6 vCPU, q35, host-passthrough CPU, raw vda (cache=none, discard=unmap), seed cdrom, virtio NIC with fixed MAC, console pty
- [x] `vm/ansible/site.yml` (plan §6.2):
  - [x] apt update + dist-upgrade
  - [x] base packages (nfs-common, curl, ca-certificates, build-essential, unzip, jq, ripgrep)
  - [x] Docker via `get.docker.com`; add `fclaude` to `docker` group
  - [x] Node 22 (NodeSource) + `npm i -g @anthropic-ai/claude-code`
  - [x] headless-chrome runtime libs (libnss3, libgbm1, etc.) — **no browser**
  - [x] ensure `/home/fclaude` owned by fclaude
- [x] Decide Ansible layout: single `site.yml` vs. roles (keep simple: tasks in site.yml first)

## Phase 3 — Host networking & firewall

- [x] `host/nftables/fclaude.nft.tmpl` (plan §8): table `inet fclaude`, input hook prio -10; allow 53/67/123 to host, reject/drop other <1024 from VM subnet to `192.168.122.1`
- [x] Detect actual libvirt bridge name (don't hard-code `virbr0`) and substitute into template
- [x] DHCP reservation for fixed MAC → `192.168.122.10` (`virsh net-update default add ip-dhcp-host ... --live --config`)
- [x] Persist nftables: install rendered file to `/etc/nftables.d/fclaude.nft` + ensure load on boot (systemd drop-in or nftables.conf include)
- [ ] Manual test matrix: from VM — internet OK, host:>=1024 OK, host:<1024 blocked, DNS works, NFS(2049) works

## Phase 4 — NFSv4 workspace sharing (host side)

- [x] `/etc/nfs.conf.d/fclaude.conf` — NFSv4 only (vers3=n, vers4=y)
- [x] `/etc/exports.d/fclaude.exports` — export `~/.fclaude/exports` to subnet (rw, sync, crossmnt, no_subtree_check, root_squash, sec=sys, fsid=0)
- [ ] Create `~/.fclaude/exports` export root; `exportfs -ra`; restart/reload nfs-server
- [x] `host/systemd/fclaude-exports.service` — recreate export root + reload exports on host boot (and clean stale bind mounts)
- [ ] Verify UID 1000 ↔ 1000 mapping: write from VM appears as `robymus` on host and vice-versa
- [ ] Test bind-mount + NFS mount of a sample dir end-to-end; confirm crossmnt descends into the bind submount

## Phase 5 — Wrapper scripts (`host/bin/`)

- [x] `fclaude` (plan §10): arg parsing (`--dir`, `--no-mount`, `--maintenance|-m`, `--shutdown`, `--`/passthrough); ensure VM; mount CWD; run `claude --dangerously-skip-permissions` over ssh; `trap` unmount on EXIT
- [x] `--maintenance` → interactive ssh as `maint` with `$SSH_KEY_MAINT`
- [x] `--shutdown` → confirm prompt then `virsh shutdown`
- [x] `--no-mount` → run claude in `/home/fclaude` (no mount)
- [x] `fclaude-vscode` (plan §11): mount workspace, `exec ssh -T ... claude` **without** skip-permissions flag; `trap` unmount
- [x] `fclaude-grow-disk` (plan §5): `qemu-img resize` → `virsh blockresize` → in-VM `growpart`+`resize2fs`; print `df -h`
- [x] Edge cases: dir not under `$HOME`, VM not running, ssh failure mid-session, Ctrl-C cleanup, concurrent sessions on same dir, concurrent sessions on different dirs

## Phase 6 — `install.sh`

- [x] Pre-flight checks: `/dev/kvm`, kvm group membership, free disk, required host commands
- [x] `sudo apt install qemu-system-x86 libvirt-daemon-system libvirt-clients genisoimage nftables nfs-kernel-server`
- [x] **Disk guard** (plan §5): if `~/.fclaude/fclaude-disk` exists → warn + ask restore?/exit; **never delete**
- [x] Generate ssh keypairs `id_fclaude`, `id_maint` (skip if present)
- [x] Create disk from Ubuntu 24.04 cloud image (skip on restore)
- [x] Render cloud-init templates (inject pubkeys), copy ansible tree, `genisoimage -o ~/.fclaude/seed.iso`
- [x] `virsh net-start default` (+ autostart), add DHCP reservation
- [x] Render + `virsh define` domain; `virsh start fclaude`
- [x] Wait for `cloud-init status --wait` / Ansible completion (poll ssh)
- [x] Apply host config: NFS files + exportfs + restart; nftables load + persist
- [x] Install `fclaude`, `fclaude-vscode`, `fclaude-grow-disk` → `~/.local/bin`; `lib/common.sh` → `~/.local/lib/fclaude/`
- [x] Write `~/.fclaude/config` (from template) and `~/.fclaude/vm` (gathered uuid/mac/ip)
- [x] Idempotency: re-run updates in place (detect existing domain/exports/rules/keys)
- [x] Print next steps (one-time `fclaude -m` → `claude` login; VS Code setting note)

## Phase 7 — `uninstall.sh`

- [x] Multiple explicit confirmations incl. "backed up disk & in-VM data?" requiring typed phrase
- [x] `virsh destroy` + `virsh undefine --nvram`; remove DHCP reservation
- [x] Remove NFS files (`exports.d`, `nfs.conf.d`), `exportfs -ra`, reload
- [x] `nft delete table inet fclaude`; remove persisted nft file + systemd drop-in
- [x] Remove wrappers from `~/.local/bin`, `~/.local/lib/fclaude`, `fclaude-exports.service`
- [x] **Disk kept by default** — separate final explicit prompt (default No) to delete `~/.fclaude/fclaude-disk`; never silent
- [x] Leave `~/.fclaude/config`/`vm`/keys decision documented (prompt to remove)

## Phase 8 — Verification & docs

### Static verification (done in development sandbox)
- [x] `bash -n` syntax check on every script (install, uninstall, lib, all wrappers)
- [x] Refcount/lock/relpath self-test incl. 20-way concurrency (flock correctness) — see scratch harness
- [x] Full template-render dry run: no leftover `__PLACEHOLDERS__`; YAML + XML + config + exports + sudoers valid
- [x] Embedded base64 Ansible playbook round-trips back to valid YAML inside user-data
- [x] Update `README.md` + `CODEBASE.md` with final commands and VS Code `claudeCode.claudeProcessWrapper` instructions

### Live end-to-end (REQUIRES running `install.sh` on the host with sudo — cannot run in this sandbox)
- [ ] Clean run: `install.sh` → VM boots → cloud-init+Ansible green
- [ ] `fclaude` in a real project dir: workspace visible in VM, edits round-trip with correct ownership
- [ ] Docker usable inside VM (`docker run hello-world` as fclaude)
- [ ] Headless browser smoke test (agent installs chromium + screenshots) — confirm libs sufficient
- [ ] Firewall verification matrix: internet OK, host:>=1024 OK, host:<1024 blocked, DNS OK, NFS(2049) OK
- [ ] Refcount/concurrency with live mounts: two `fclaude` + one `fclaude-vscode`; unmount only on last exit
- [ ] `fclaude-grow-disk 64G` grows live; `df -h` confirms
- [ ] `--shutdown`, `--maintenance`, restore-from-backup path, `uninstall.sh` full reverse
- [ ] Reboot host → nftables + exports service restore correctly; `fclaude` still works
- [ ] `nft -c` validation of the rendered ruleset (install.sh already gates on this; couldn't run unprivileged here)

---

## Cross-cutting / code hygiene

- [x] All scripts `set -euo pipefail`, quoted vars, safe `rmdir -p`/`umount` guards
- [x] `shellcheck -x -S warning` clean across install/uninstall/lib/wrappers (only info-level SC1091/SC2015 remain, which are intentional)
- [x] Secrets: ssh keys generated 0600 in `~/.fclaude/ssh`, never logged; no API keys baked into image (claude login happens in-VM, persists on disk)

## Open / verify during build (to confirm during the first live install)
- [x] cloud-init seed delivery of the Ansible tree: resolved via `write_files` base64 (single playbook) — round-trip verified statically
- [ ] Confirm `crossmnt` + NFSv4 `fsid=0` pseudo-root descends into per-session bind mounts on this nfs-kernel-server version
- [ ] Confirm libvirt nftables backend ordering vs. our prio -10 table (no rule shadowing)
