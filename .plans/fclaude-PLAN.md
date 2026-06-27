# fclaude — Sandboxed Claude Code VM: Implementation Plan

## 1. Goal

Run Claude Code in **yolo mode** (`--dangerously-skip-permissions`, full sudo, free to install
tools and self-manage) but confined inside a **hardware-isolated VM**, so a misbehaving or
compromised agent cannot damage the host. Host workspaces are mounted on demand for a work
session and unmounted afterwards.

## 2. Host facts (probed on this machine)

| Property | Value | Consequence |
|---|---|---|
| OS / init | Debian 13 trixie (MX 25 base) / **systemd** | services manageable via `systemctl` |
| CPU virt | AMD `svm`, `/dev/kvm` present, user in `kvm` group | **full KVM VM is the right choice** |
| RAM / disk free | 30 GiB / 341 GiB free on `/` | VM gets 4–8 GiB RAM, 32 GiB disk easily |
| Already installed | `docker` 29.5, `sshfs`, `nfs-kernel-server`, `nft`, `genisoimage`, `qemu-img`, `resize2fs`, `growpart` | minimal extra host install |
| To install on host | `qemu-system-x86`, `libvirt-daemon-system`, `libvirt-clients` (apt) | |
| Network | wlan0 `10.0.163.248`; libvirt `192.168.122.0/24` is **free** (Docker uses 172.17–21) | no subnet conflict |
| Host user | `robymus` uid/gid **1000/1000** | make VM `fclaude` uid 1000 → clean NFS mapping |

## 3. Architecture decision: full QEMU/KVM VM via libvirt

**Chosen: a full KVM virtual machine managed by libvirt**, booting an Ubuntu 24.04 cloud image.

Why not a system container (incus/LXD/Docker)?
- Containers **share the host kernel**. An agent with full root + `--dangerously-skip-permissions`
  is exactly the threat model where a shared kernel is the weak link (container escape, kernel
  exploit). A full VM gives a separate kernel and a real hypervisor boundary — the strongest
  isolation for "yolo mode, but safe."
- The user explicitly listed full VMs as acceptable and pre-empted the NFS-sharing approach
  ("it's okay to install nfs server on the host"), which maps naturally to a VM.
- Docker-in-VM (a requirement) works perfectly inside a KVM VM and is itself *more* isolated than
  Docker-in-container.

Trade-offs accepted: a VM uses more RAM (~1–2 GiB idle) and boots slower (~15–25 s) than a
container. Mitigated by keeping the VM running between sessions (the wrapper never stops it unless
asked) and pinning a static IP.

```
┌─ HOST (robymus, Debian 13) ───────────────────────────────────────────────┐
│  ~/.local/bin/fclaude ── ssh ──▶ libvirt VM "fclaude" (Ubuntu 24.04)       │
│  ~/.fclaude/fclaude-disk (32G raw, ext4)  ──attached as vda──▶ VM root     │
│                                                                            │
│  NFSv4 export root  ~/.fclaude/exports  ──(virbr0, 2049)──▶ VM mounts      │
│     └─ bind-mount of $HOME/<relpath> (per session, refcounted)            │
│                                                                            │
│  nftables table "fclaude": drop VM→host:<1024 (except 53/67/123)          │
│  libvirt default NAT (virbr0 192.168.122.1) → internet + host:>=1024       │
└────────────────────────────────────────────────────────────────────────────┘
                  VM "fclaude": uid-1000 fclaude user, sudo NOPASSWD,
                  docker, node+claude-code, nfs-common, ansible, headless-chrome-ready
```

## 4. Repository / file layout

```
fclaude/
  install.sh                 # host-side orchestrator (apt, disk, VM, firewall, wrappers)
  uninstall.sh               # reverse everything, heavy confirmation, never deletes disk silently
  lib/common.sh              # shared bash: config load, logging, ssh helpers, refcount, locking
  host/
    bin/
      fclaude                # main CLI wrapper  → ~/.local/bin/fclaude
      fclaude-vscode         # VS Code process wrapper → ~/.local/bin/fclaude-vscode
      fclaude-grow-disk      # online disk grow → ~/.local/bin/fclaude-grow-disk
    nftables/fclaude.nft.tmpl
    systemd/fclaude-exports.service   # re-create export root mount on host boot
    config.template
  vm/
    cloud-init/{user-data.tmpl,meta-data.tmpl}
    libvirt/domain.xml.tmpl
    ansible/site.yml         # node+claude, docker, nfs-common, chrome deps, users
  .plans/fclaude-PLAN.md     # this file
```

Runtime state (created by install, never in repo):

```
~/.fclaude/
  fclaude-disk          # 32G raw VM disk (the "loopback file")
  config                # user-editable config (see §11)
  vm                    # VM identity: name, uuid, mac, ip, ssh user/port (see §10)
  ssh/{id_fclaude,id_fclaude.pub}   # host→VM keypair
  exports/              # NFSv4 fsid=0 root; session dirs bind-mounted in here
  refcounts/            # flock-guarded per-path mount counters
  seed.iso              # cloud-init seed (kept for reference)
```

## 5. The virtual disk (`~/.fclaude/fclaude-disk`)

- **Format: raw**, 32 GiB, single GPT partition, **ext4** root.
  - raw = transparent (loop-mountable on host for disaster recovery), trivial to grow.
  - **ext4 chosen specifically because it grows online** with `resize2fs` while mounted — directly
    satisfies "filesystem that supports growing easily." (xfs also grows online but can't shrink;
    ext4 is the simplest ubiquitous choice.)
- Built in `install.sh` from the Ubuntu cloud image:

```bash
# install.sh (excerpt) — create the disk ONLY if absent
DISK=~/.fclaude/fclaude-disk
if [[ -e "$DISK" ]]; then
  echo "WARNING: $DISK already exists."
  read -rp "Is this a RESTORE from backup? Continue setup WITHOUT touching the disk? [y/N] " a
  [[ "$a" == [yY] ]] || { echo "Refusing to overwrite. Delete it manually to start fresh."; exit 1; }
  RESTORE=1
else
  IMG=$(mktemp --tmpdir fclaude-cloud-XXXX.img)
  curl -fL -o "$IMG" \
    https://cloud-images.ubuntu.com/releases/24.04/release/ubuntu-24.04-server-cloudimg-amd64.img
  qemu-img convert -O raw "$IMG" "$DISK"     # qcow2 cloud image -> raw
  qemu-img resize -f raw "$DISK" 32G          # extend file to 32 GiB
  rm -f "$IMG"
fi
```

cloud-init's `growpart` + `resize2fs` modules (enabled by default in Ubuntu cloud images) grow the
root partition to fill 32 GiB on first boot — no manual partitioning needed.

### Growing later — `fclaude-grow-disk <new-size>` (online)

```bash
#!/usr/bin/env bash
# ~/.local/bin/fclaude-grow-disk  e.g.  fclaude-grow-disk 64G
set -euo pipefail; source ~/.fclaude/config
new="$1"
qemu-img resize -f raw "$FCLAUDE_DISK" "$new"          # 1) grow the physical file
virsh blockresize fclaude vda "$new"                   # 2) tell the running VM the disk grew
ssh_vm "sudo growpart /dev/vda 1 && sudo resize2fs /dev/vda1"  # 3) grow partition + fs, online
ssh_vm "df -h /"
```

All three steps work with the VM running (ext4 online resize). If the VM is off, step 1 + next boot
(cloud-init) is enough.

## 6. VM provisioning: cloud-init → Ansible

Per the requirement, **cloud-init does the minimum** (users, ssh, install Ansible), then **Ansible
does everything else**.

### 6.1 cloud-init `user-data` (templated, secrets filled by install.sh)

```yaml
#cloud-config
hostname: fclaude
users:
  - name: fclaude
    uid: 1000                      # match host robymus uid → clean NFS ownership
    groups: [sudo, docker]
    sudo: ["ALL=(ALL) NOPASSWD:ALL"]
    shell: /bin/bash
    ssh_authorized_keys: ["__FCLAUDE_PUBKEY__"]
  - name: maint                    # maintenance user, separate key
    groups: [sudo]
    sudo: ["ALL=(ALL) NOPASSWD:ALL"]
    shell: /bin/bash
    ssh_authorized_keys: ["__MAINT_PUBKEY__"]
growpart: {mode: auto, devices: ['/']}
resize_rootfs: true
package_update: true
packages: [ansible, git]           # Ansible first, per requirement
runcmd:
  # Pull the playbook baked into the seed and apply it locally (ansible runs ON the VM)
  - [ "ansible-playbook", "/var/lib/fclaude/ansible/site.yml", "-c", "local" ]
```

`meta-data` supplies a fixed `instance-id` and the static network is provided by libvirt DHCP
reservation (see §8). The Ansible tree is copied into the seed ISO under `/var/lib/fclaude/ansible`.

### 6.2 Ansible `site.yml` (runs inside the VM)

```yaml
- hosts: localhost
  become: true
  tasks:
    - apt: {update_cache: true, upgrade: dist}
    - name: Base tools
      apt:
        name: [nfs-common, curl, ca-certificates, build-essential, unzip, jq, ripgrep]
        state: present
    # --- Docker (requirement: claude in VM can use docker) ---
    - name: Docker apt repo + engine
      shell: curl -fsSL https://get.docker.com | sh
      args: {creates: /usr/bin/docker}
    - user: {name: fclaude, groups: docker, append: true}
    # --- Node + Claude Code ---
    - name: NodeSource 22.x
      shell: curl -fsSL https://deb.nodesource.com/setup_22.x | bash -
      args: {creates: /etc/apt/sources.list.d/nodesource.list}
    - apt: {name: nodejs, state: present, update_cache: true}
    - npm: {name: "@anthropic-ai/claude-code", global: true}
    # --- Headless browser readiness (infra only; do NOT install the browser) ---
    - name: Chromium runtime deps (so agent can `apt install chromium` / use puppeteer later)
      apt:
        name: [libnss3, libatk-bridge2.0-0, libgbm1, libasound2t64, libgtk-3-0, fonts-liberation]
        state: present
    # --- Workspace mount base ---
    - file: {path: /home/fclaude, owner: fclaude, group: fclaude, state: directory}
```

> Auth note: `claude` login state lives in `/home/fclaude/.claude`, which is on the persistent
> disk. Log in once via maintenance ssh (`fclaude --maintenance` → `claude` → authenticate); it
> persists across sessions and reboots.

## 7. libvirt domain

Defined by `install.sh` (via `virsh define domain.xml`). Key bits of `domain.xml.tmpl`:

```xml
<domain type='kvm'>
  <name>fclaude</name>
  <memory unit='GiB'>8</memory>
  <vcpu>6</vcpu>
  <os><type arch='x86_64' machine='q35'>hvm</type><boot dev='hd'/></os>
  <features><acpi/><apic/></features>
  <cpu mode='host-passthrough'/>
  <devices>
    <disk type='file' device='disk'>
      <driver name='qemu' type='raw' cache='none' discard='unmap'/>
      <source file='__HOME__/.fclaude/fclaude-disk'/>
      <target dev='vda' bus='virtio'/>
    </disk>
    <disk type='file' device='cdrom'>
      <source file='__HOME__/.fclaude/seed.iso'/><target dev='sda' bus='sata'/><readonly/>
    </disk>
    <interface type='network'>
      <source network='default'/>
      <mac address='52:54:00:fc:1a:de'/>      <!-- fixed MAC → DHCP reservation -->
      <model type='virtio'/>
    </interface>
    <console type='pty'/>
  </devices>
</domain>
```

`autostart` is **off** — the wrapper starts the VM on demand; it stays up between sessions.

## 8. Networking & firewall

- **libvirt `default` NAT network** (`virbr0`, `192.168.122.1`) gives the VM internet via
  masquerade. We add a **DHCP reservation** pinning the fixed MAC to `192.168.122.10`:

```bash
virsh net-update default add ip-dhcp-host \
  "<host mac='52:54:00:fc:1a:de' name='fclaude' ip='192.168.122.10'/>" --live --config
```

- **Host firewall (nftables)** — a dedicated table so uninstall removes it cleanly. Goal: VM may
  reach the **internet** and **host unprivileged ports (≥1024)**, but **not host privileged ports
  (<1024)**, except the infra ports libvirt itself needs (DNS 53, DHCP 67, NTP 123).

```nft
# host/nftables/fclaude.nft.tmpl  → loaded with `nft -f`
table inet fclaude {
  chain host_guard {
    type filter hook input priority -10; policy accept;   # runs before libvirt's rules (prio 0)
    iifname "virbr0" ip saddr 192.168.122.0/24 ip daddr 192.168.122.1 meta l4proto {tcp,udp} th dport 53  accept
    iifname "virbr0" ip saddr 192.168.122.0/24 ip daddr 192.168.122.1 udp dport 67  accept
    iifname "virbr0" ip saddr 192.168.122.0/24 ip daddr 192.168.122.1 udp dport 123 accept
    # everything else below 1024 to the host is denied:
    iifname "virbr0" ip saddr 192.168.122.0/24 ip daddr 192.168.122.1 tcp dport 1-1023 reject with tcp reset
    iifname "virbr0" ip saddr 192.168.122.0/24 ip daddr 192.168.122.1 udp dport 1-1023 drop
  }
}
```

Why this works: VM→host traffic terminates on the host and hits the **input** hook at
`192.168.122.1`; VM→internet and VM→other go through **forward** (untouched, so internet works).
**NFS is reachable** because NFSv4 uses **port 2049 (≥1024)** — see §9. Persisted via
`/etc/nftables.d/fclaude.nft` + a systemd drop-in so it survives reboot.

## 9. Workspace sharing — NFSv4 + bind-mount export root (refcounted)

**Design: a single stable NFSv4 export root with per-session bind mounts.** This limits the VM's
reach to exactly the directories shared this session (the agent has sudo in the VM and *could*
otherwise mount anything exported), and avoids editing `/etc/exports` per session.

- One persistent export: `~/.fclaude/exports` as the NFSv4 root (`fsid=0`, `crossmnt` so it
  descends into bind submounts), exported only to the VM subnet:

```
# /etc/exports.d/fclaude.exports  (written by install.sh)
/home/robymus/.fclaude/exports 192.168.122.0/24(rw,sync,crossmnt,no_subtree_check,root_squash,sec=sys)
```

- **Use NFSv4-only** (disable v3/rpcbind) so only port 2049 is needed — keeping it inside the
  "unprivileged ports" firewall allowance:

```
# /etc/nfs.conf.d/fclaude.conf
[nfsd]
vers3=n
vers4=y
```

- UID mapping: host files are `1000`, VM `fclaude` is `1000`, `sec=sys` passes uid 1000 → ownership
  is transparent both ways. No squashing surprises.

### Mount flow (host side), reference-counted

To share `$HOME/path1/path2` → VM `/home/fclaude/path1/path2`:

```bash
# lib/common.sh (excerpt)
mount_workspace() {          # $1 = absolute host dir under $HOME
  local rel="${1#$HOME/}"                      # path1/path2
  local key; key=$(echo -n "$rel" | sha1sum | cut -c1-16)
  with_lock "$key" '
    n=$(refcount_inc "$key")
    if [[ "$n" == 1 ]]; then
      mkdir -p "$HOME/.fclaude/exports/$rel"
      mount --bind "$HOME/$rel" "$HOME/.fclaude/exports/$rel"
      exportfs -r
      ssh_vm "sudo mkdir -p /home/fclaude/$rel && \
              sudo mount -t nfs4 192.168.122.10-host:/$rel /home/fclaude/$rel" # see note
    fi
  '
}
unmount_workspace() {
  local rel="${1#$HOME/}"; local key; key=$(echo -n "$rel" | sha1sum | cut -c1-16)
  with_lock "$key" '
    n=$(refcount_dec "$key")
    if [[ "$n" == 0 ]]; then
      ssh_vm "sudo umount /home/fclaude/$rel || true"
      umount "$HOME/.fclaude/exports/$rel" || true
      rmdir -p "$HOME/.fclaude/exports/$rel" 2>/dev/null || true
    fi
  '
}
```

> The VM mounts from the host's NAT gateway address `192.168.122.1` (`mount -t nfs4
> 192.168.122.1:/$rel ...`). Refcount files live in `~/.fclaude/refcounts/<key>`, guarded by
> `flock` so concurrent `fclaude` invocations (and `fclaude-vscode`) count correctly.

Trade-offs: bind-mount-into-export-root adds a host mount per session but gives least-privilege
exposure and a single static export. Alternative (export `$HOME` wholesale, mount subdirs) is
simpler but lets the in-VM agent mount your entire home — rejected for the sandbox use case.

## 10. The `fclaude` wrapper (host CLI)

Default behaviour: ensure VM up → mount CWD (refcounted) → run claude **with
`--dangerously-skip-permissions`** in that dir over ssh → unmount on exit.

```bash
#!/usr/bin/env bash
# ~/.local/bin/fclaude
set -euo pipefail; source ~/.fclaude/config; source ~/.local/lib/fclaude/common.sh

DIR="$PWD"; DO_MOUNT=1
while [[ $# -gt 0 ]]; do case "$1" in
  --no-mount)     DO_MOUNT=0; shift;;
  --dir)          DIR="$2"; shift 2;;
  --maintenance|-m) ensure_vm_running; exec ssh -t -i "$SSH_KEY_MAINT" maint@"$VM_IP"; ;;
  --shutdown)     read -rp "Shut down the fclaude VM? [y/N] " a
                  [[ "$a" == [yY] ]] && virsh shutdown fclaude; exit 0;;
  --) shift; break;;
  *)  break;;
esac; done

ensure_vm_running                          # virsh start + wait-for-ssh if needed

if [[ "$DO_MOUNT" == 1 ]]; then
  [[ "$DIR" == "$HOME"* ]] || { echo "Dir must be under \$HOME to share"; exit 1; }
  rel="${DIR#$HOME/}"
  mount_workspace "$DIR"
  trap 'unmount_workspace "$DIR"' EXIT      # unmount even on Ctrl-C / error
  ssh_vm_tty "cd /home/fclaude/$rel && claude --dangerously-skip-permissions $*"
else
  ssh_vm_tty "cd /home/fclaude && claude --dangerously-skip-permissions $*"
fi
```

Arguments summary:
- *(none)* → mount CWD, run claude there.
- `--dir <path>` → mount and run in a specific dir instead of CWD.
- `--no-mount` → run claude in `/home/fclaude` without mounting anything.
- `--maintenance` / `-m` → interactive ssh as the `maint` sudo user (VM admin).
- `--shutdown` → confirm + power off the VM (the wrapper otherwise never stops it).
- Anything after `--` (or trailing args) is passed through to `claude`.

`ensure_vm_running` reads identity from `~/.fclaude/vm`:

```
VM_NAME=fclaude
VM_UUID=...                 # from virsh
VM_MAC=52:54:00:fc:1a:de
VM_IP=192.168.122.10
SSH_USER=fclaude
SSH_KEY=~/.fclaude/ssh/id_fclaude
SSH_KEY_MAINT=~/.fclaude/ssh/id_maint
```

## 11. `fclaude-vscode` (VS Code extension wrapper)

The VS Code Claude extension launches a `claude` process and talks to it over stdio. We point the
extension's Claude executable at `fclaude-vscode`, a **thin wrapper** that runs the real `claude`
inside the VM with stdio forwarded, mounting the workspace folder for the lifetime of the process.

Unlike the CLI wrapper, `fclaude-vscode` does **not** pass `--dangerously-skip-permissions` — the
extension exposes that as its own setting, which the user enables in the VS Code UI.

```bash
#!/usr/bin/env bash
# ~/.local/bin/fclaude-vscode  — extension cwd = workspace folder
set -euo pipefail; source ~/.fclaude/config; source ~/.local/lib/fclaude/common.sh
ensure_vm_running
DIR="$PWD"; rel="${DIR#$HOME/}"
if [[ "$DIR" == "$HOME"* ]]; then
  mount_workspace "$DIR"; trap 'unmount_workspace "$DIR"' EXIT
  exec ssh -T -i "$SSH_KEY" "$SSH_USER@$VM_IP" \
       "cd /home/fclaude/$rel && exec claude $*"
else
  exec ssh -T -i "$SSH_KEY" "$SSH_USER@$VM_IP" "exec claude $*"
fi
```

Set the extension's **`claudeCode.claudeProcessWrapper`** setting to `~/.local/bin/fclaude-vscode`.
`install.sh` does **not** touch VS Code settings — the user sets this in the VS Code UI themselves
(along with the skip-permissions option). Shared refcounting means CLI and VS Code sessions on the
same folder coexist safely. `ssh -T` forwards stdio transparently (no PTY needed for the
extension's JSON protocol).

## 12. Install / uninstall

### `install.sh` order of operations
1. Pre-flight: confirm `/dev/kvm`, kvm group, free disk; `sudo apt install qemu-system-x86
   libvirt-daemon-system libvirt-clients genisoimage nftables nfs-kernel-server`.
2. **Disk guard** (§5): if `~/.fclaude/fclaude-disk` exists → warn, ask restore?/exit; **never delete**.
3. Generate ssh keypairs (`id_fclaude`, `id_maint`).
4. Create disk from cloud image (unless restore).
5. Render cloud-init seed (inject pubkeys, copy `vm/ansible/`), `genisoimage -o seed.iso`.
6. `virsh net-start default` + DHCP reservation; `virsh define domain.xml`; `virsh start fclaude`.
7. Wait for cloud-init/Ansible to finish (poll ssh + `cloud-init status --wait`).
8. Host config: write `/etc/exports.d/fclaude.exports`, `/etc/nfs.conf.d/fclaude.conf`,
   `exportfs -ra`, restart nfs; load `host/nftables/fclaude.nft`, persist it.
9. Install wrappers → `~/.local/bin/`, `lib/common.sh` → `~/.local/lib/fclaude/`.
10. Write `~/.fclaude/config` (from template) and `~/.fclaude/vm` (gathered identity).
11. Print next steps (e.g. one-time `fclaude -m` then `claude` login).

Idempotent: re-running detects existing domain/exports/rules and updates in place.

### `uninstall.sh`
- Multiple explicit confirmations; asks **"Have you backed up `~/.fclaude/fclaude-disk` and any
  in-VM data?"** and requires typing a confirmation phrase.
- `virsh destroy fclaude; virsh undefine fclaude --nvram`; remove DHCP reservation.
- Remove `/etc/exports.d/fclaude.exports`, `/etc/nfs.conf.d/fclaude.conf`, `exportfs -ra`.
- `nft delete table inet fclaude`; remove persisted nft file + systemd drop-in.
- Remove wrappers from `~/.local/bin`, `~/.local/lib/fclaude`.
- **The disk is kept by default.** Deleting `~/.fclaude/fclaude-disk` is a separate, explicit
  final prompt (default No). The script never silently removes the disk.

## 13. Config template (`~/.fclaude/config`)

```bash
# ~/.fclaude/config — edit to taste
FCLAUDE_DISK="$HOME/.fclaude/fclaude-disk"
SHARE_ROOT="$HOME"                 # host base; $SHARE_ROOT/a/b -> /home/fclaude/a/b
VM_MOUNT_BASE="/home/fclaude"
VM_NAME="fclaude"
VM_IP="192.168.122.10"
VM_MEM_GIB=8
VM_VCPU=6
SSH_USER="fclaude"
SSH_KEY="$HOME/.fclaude/ssh/id_fclaude"
SSH_KEY_MAINT="$HOME/.fclaude/ssh/id_maint"
```

## 14. Browser automation readiness

The VM is headless. Ansible installs **Chromium runtime libraries** (not the browser) so the agent
can later `sudo apt install chromium` or use Puppeteer/Playwright in **headless** mode for
screenshots — no X server required. Documented but left for the agent to install on demand, per
requirement.

## 15. Security considerations / trade-offs (summary)

- **Isolation:** full KVM boundary; agent root is confined to the guest. Strongest option for yolo mode.
- **Blast radius of file access:** only per-session bind-mounted dirs are reachable over NFS, not all of `$HOME`.
- **Privileged host ports blocked** by nftables; NFS works because v4 = port 2049.
- **Docker-in-VM** is namespaced inside the guest — no host daemon exposure.
- **Persistence:** disk holds OS, `/home/fclaude`, and claude login; survives reboots and is the single backup artifact.
- **Residual risk:** the VM has outbound internet (required) — exfiltration is possible by design; mitigated only by the VM boundary, not the network. Note for the user if tighter egress control is wanted later (could add nft egress allowlist).
- **Maintenance** via separate `maint` user keeps the agent's `fclaude` identity distinct from admin access.

## 16. Decisions locked in

- **VM size:** 8 GiB RAM / 6 vCPU / 32 GiB disk.
- **Egress:** VM internet fully open (VM boundary is the protection; egress allowlist deferred).
- **Wrapper language:** Bash.
- **Guest OS:** Ubuntu 24.04 LTS.

### Resolved
- **VS Code integration:** the extension setting is **`claudeCode.claudeProcessWrapper`**. `install.sh`
  does **not** set it — the user points it at `~/.local/bin/fclaude-vscode` in the VS Code UI, and
  enables skip-permissions there too (so the vscode wrapper does not pass the flag). See §11.
- **VM pre-bake scope:** keep the image minimal — only node / claude-code / docker plus the §6.2
  base (nfs-common, build tools, headless-chrome runtime libs). **No browser or extra language
  toolchains are pre-installed**; the agent installs whatever it needs inside the VM on demand.
