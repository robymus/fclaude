# fclaude for Windows (experimental)

Runs Claude Code with `--dangerously-skip-permissions` inside an isolated Ubuntu VM on your
Windows PC. Your projects stay on Windows. Each project you open is shared into the VM only while
Claude works on it. The VM cannot reach file sharing, RPC or other privileged services on your PC.

Requirements: Windows 10/11 (64-bit), 16 GB RAM or more (the VM uses 8 GB), about 40 GB of free
disk, and the Windows OpenSSH client (installed by default on Windows 11). WSL can stay installed.

## 1. Install VMware Workstation Pro (free)

1. Register or sign in at <https://support.broadcom.com>.
2. Go to **My Downloads → Free Software Downloads → VMware Workstation Pro** and download the
   Windows build (17.5 or newer).
3. Install it with the default options. You never need to open it: fclaude drives it in the
   background.
4. Make sure **Windows Hypervisor Platform** is turned on: *Turn Windows features on or off →
   Windows Hypervisor Platform*. This needs a reboot.

## 2. Install fclaude

1. Download `fclaude-setup.exe` from the release page.
2. Double-click it. Windows SmartScreen may say "Windows protected your PC", because the program
   is not signed. Click **More info → Run anyway**.
3. Answer the questions:
   - **Projects folder** (default `C:\dev`): every project you use with fclaude must be inside it.
   - **Firewall:** Windows asks once for administrator permission. Click **Yes**.
   - **VS Code:** answer **y** to let the Claude extension run inside the VM.
4. Wait about 5 minutes while the VM sets itself up (Ubuntu updates, Node, Claude Code).

## 3. Use it

**Terminal:** open a *new* terminal, go to a project and run `fclaude`:

```
cd C:\dev\myproject
fclaude
```

The first time, Claude asks you to log in. Follow the link it prints.

**VS Code:** restart VS Code, open a folder under `C:\dev`, and use the Claude extension as usual.
Setup has set the extension to skip permission prompts, because Claude runs in the VM, not on
Windows. Uninstall turns that back off.

Other commands (`fclaude --help` lists them all):

| Command | What it does |
|---|---|
| `fclaude --status` | VM state, memory, disk, Claude version, shared projects |
| `fclaude --update` | Update Claude Code and the VM's packages now |
| `fclaude --shutdown` | Turn the VM off (it starts again by itself when needed) |
| `fclaude --maintenance` | A shell in the VM as the admin user |
| `fclaude --cleanup <folder>` | Release a project that stays shared after a crash |
| `fclaude grow-disk 64G` | Make the VM's disk bigger |
| `fclaude uninstall` | Remove fclaude (asks first; keeps the VM disk unless you type `DELETE DISK`) |

## Known limitations

- **`node_modules` stays inside the VM.** In a project with a `package.json`, `node_modules` is a
  VM-local folder, because shared folders cannot hold the symlinks npm creates. On Windows the
  folder looks empty. Turn this off with `"local_node_modules": false` in
  `%USERPROFILE%\.fclaude\config.json`.
- **Symlinks** created in the VM inside a project fail (a shared-folder limit).
- **A new empty file** created in the VM can show a 1970 date on Windows until it is written to.
- **Restoring the VM disk** needs the `ssh` folder and `vm.json` from the same installation. A
  plain `fclaude uninstall` keeps them. When backing up, copy the whole
  `%USERPROFILE%\.fclaude` folder.
- The first Claude start after the PC boots waits while the VM starts.

## Where things are

`%USERPROFILE%\.fclaude\`: `config.json` (your settings), `vm\` (the VM and its disk
`fclaude-disk.vmdk`), `ssh\` (keys), `bin\` (the programs), `leases\` (which projects are in use).
