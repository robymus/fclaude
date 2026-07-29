# fclaude

Run **Claude Code in yolo mode** (`--dangerously-skip-permissions`, full sudo, free to install
tools and self-manage) confined inside a **hardware-isolated KVM VM**, so a misbehaving or
compromised agent cannot damage the host. Host project directories are mounted on demand for a work
session and unmounted afterwards.

See [.plans/fclaude-PLAN.md](.plans/fclaude-PLAN.md) for the full design and
[CODEBASE.md](CODEBASE.md) for an implementation map.

## Requirements (host)

- Linux with KVM (`/dev/kvm`, user in the `kvm` group), systemd
- `sudo` access (install needs to add packages + firewall/NFS config)
- Packages installed by `install.sh`: `qemu-system-x86`, `libvirt-daemon-system`,
  `libvirt-clients`, `genisoimage`, `nftables`, `nfs-kernel-server`

## Install

```bash
./install.sh
```

This creates a 32 GiB VM disk at `~/.fclaude/fclaude-disk`, boots an Ubuntu 24.04 VM, provisions it
with Ansible (Node + Claude Code + Docker), configures host NFS sharing and firewall rules, and
installs the wrapper scripts into `~/.local/bin`.

To redeploy **only** the host-side scripts after editing a wrapper or pulling a change under
`host/` or `lib/` — no VM, no packages, no system config, no sudo:

```bash
./install.sh --wrappers-only
```

Claude Code is installed with its **native installer** (a self-contained binary under the VM
account's `~/.local`, on the `latest` channel) so it can update itself in the background even
while sessions are running. Node stays for your own project work, not for Claude.

One-time after install — log Claude in inside the VM (persists on the VM disk):

```bash
fclaude --maintenance      # ssh into the VM as the maintenance user
claude                     # authenticate, then exit
```

## Usage

```bash
fclaude                    # mount current dir, run claude there (yolo mode)
fclaude --dir ~/work/proj  # mount and run in a specific dir
fclaude --no-mount         # run claude in /home/fclaude with nothing mounted
fclaude --mount ~/a ~/b    # just mount these dirs in the VM, hold until Ctrl+C, then unmount
fclaude --status           # show VM state, resources, and active workspaces (no start)
fclaude --update           # update Claude Code + system packages now (bypasses the network gate)
fclaude --maintenance      # ssh in as the maintenance (admin) user
fclaude --shutdown         # power off the VM (with confirmation)
```

The current directory must be under `$HOME`. `$HOME/a/b` is shared as `/home/fclaude/a/b` in the VM.
The wrapper never stops the VM on exit — it keeps running between sessions for fast startup.

## Staying up to date

Two independent mechanisms keep the VM current:

- **Claude Code** updates itself via the native installer's background auto-updater (`latest`
  channel). Its versioned install dirs + atomic symlink swap make this **safe while sessions are
  running** — a live session keeps its version; new launches pick up the new one.
- **System packages** (apt → OS, Docker, Node) are refreshed by a **weekly** `systemd` timer in the
  VM (`Sun 04:00`, catches up on next boot if the VM was off). It is **network-speed gated**: if the
  VM's measured downlink is below `FCLAUDE_UPDATE_MIN_MBIT` (default 100 Mbit/s) — e.g. on mobile or
  while travelling — the run is skipped so it can't clog a metered link.

`fclaude --update` forces both now, ignoring the gate. At session start, `fclaude` prints a warning
if the last successful update is older than `FCLAUDE_UPDATE_WARN_DAYS` (default 15) so a stalled
updater doesn't go unnoticed. `fclaude --status` shows the installed Claude version and last apt run.

Tunables live in `~/.fclaude/config` (`FCLAUDE_UPDATE_APT`, `_MIN_MBIT`, `_PROBE_URL`,
`_PROBE_MAXTIME`, `_WARN_DAYS`). After changing them, re-apply to the running VM with:

```bash
./host/bin/fclaude-update-setup        # also retrofits auto-update onto a pre-existing VM
```

### VS Code

Set the Claude extension's **`claudeCode.claudeProcessWrapper`** setting to
`~/.local/bin/fclaude-vscode`, and enable skip-permissions in the extension's own settings. The
wrapper handles VM startup, mounting, and cleanup automatically.

## Grow the disk

```bash
fclaude-grow-disk 64G      # grows the file, partition, and filesystem online
```

## Uninstall

```bash
./uninstall.sh             # reverses everything; keeps the disk unless you confirm deletion
```

## Layout

| Path | Purpose |
|---|---|
| `install.sh` / `uninstall.sh` | host orchestrators |
| `lib/common.sh` | shared bash: config, ssh, locking, refcount, mounting |
| `lib/constants.sh` | single source of truth for IPs/paths/names |
| `host/bin/` | `fclaude`, `fclaude-vscode`, `fclaude-grow-disk`, `fclaude-update-setup` |
| `host/nftables/` | host firewall ruleset template |
| `host/systemd/` | export-root restore unit |
| `vm/cloud-init/` | first-boot user/ssh/ansible bootstrap |
| `vm/libvirt/` | domain XML template |
| `vm/ansible/` | in-VM provisioning playbook |
| `vm/update/` | weekly apt-upgrade script + systemd timer (network-gated) |
| `test/` | unit tests (`bash test/test-staleness.sh`) |
