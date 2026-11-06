# fclaude for Windows (experimental) — PLAN

Status: **Hypervisor switched to VMware Workstation Pro (2026-11-06, §10.6) after VirtualBox failed R1. §3–§4 describe the VMware design; §0.3/§0.7 keep the VirtualBox findings as history.** 2026-11-07: installer, uninstaller, CLI and VS Code wrapper (Option A) are implemented and unit-tested. The user's install → use → uninstall spin is next (TODO Phases 7–9 Verify). v3. Linux-side groundwork is implemented on branch `feat/windows`; the next work happens on
Windows (see §0).** User decisions are in §10. Every decision below has a default. Items marked
**[R#]** are spikes to run on the Windows machine *before* the code that depends on them. Each one
has a pass/fail criterion and a fallback, so a failed spike changes a known decision rather than
reopening the design.

---

## 0. Handoff: start here in the Windows session

You are a **fresh Claude Code session running inside WSL2 (Ubuntu) on the Windows machine**. This
section is everything the Linux session knew that the code and git history don't say.

### 0.1 What exists (done on Linux, all lint- and test-clean)
**Since 2026-11-06 the hypervisor is VMware Workstation (§10.6).** The `vbox` package and the
`virtualbox` platform value mentioned below are gone: `internal/win/vmw` and `fclaude_platform=vmware`
replaced them (Phase 6).
- The Go module at the repo root, with the single binary `cmd/fclaude-win`. Its personality comes
  from the exe name: `fclaude`, `fclaude-vscode`, `fclaude-setup`, `fclaude-capture`. **Only
  `--version`/`--help` and `fclaude-capture` work**; the other modes exit 2 with "not
  implemented".
- Packages under `internal/win/`, each with tests. See CODEBASE.md, section "Windows host tooling".
  - `winpath`, `config`, `pathmap` (the rewriter, fuzzed), `lease`, `vbox`, `seed`, `capture`,
    `procjob`, `mcphelper`.
  - `vm/assets.go` embeds the provisioning files.
- VM provisioning: `site.yml` has tasks gated on `fclaude_platform == 'virtualbox'` (load
  `vboxsf` at boot; system git config `autocrlf=true`, `checkStat=minimal`, `trustctime=false`).
  `user-data.tmpl` passes `-e fclaude_platform=…` (Linux renders `kvm` via `FCLAUDE_PLATFORM` in
  `lib/constants.sh`). The new `network-config-vbox.tmpl` defines two NICs matched by MAC.
- CI (`.github/workflows/ci.yml`): Go lint for linux and windows, `go test -race`, the Windows
  cross-build, a `windows-latest` test job, and the Ansible syntax-check for both platforms.
  `release-windows.yml` runs on `v*-win*` tags.

### 0.2 Verified facts (don't re-research)
- The VM's stock Ubuntu `-generic` kernel ships `vboxsf.ko` and `vboxguest.ko`
  (`linux-modules-*`). **No Guest Additions are needed.**
- Ubuntu publishes `ubuntu-24.04-server-cloudimg-amd64.vmdk` (plus `.ova`) under
  `https://cloud-images.ubuntu.com/releases/noble/release/`, with `SHA256SUMS` alongside. The
  `.img` is qcow2, which VirtualBox can't use.
- **End-to-end seed boot under KVM passed on Linux.** The Ubuntu noble image booted with a seed
  ISO **built by `internal/win/seed`**:
  - cloud-init accepted the ISO;
  - both NICs were renamed by MAC (`nat0` DHCP plus default route; `host0` static 192.168.56.10/24);
  - the full `site.yml` ran with `fclaude_platform=virtualbox`.

  Details are in §0.6. What remains is VirtualBox specifics, not cloud-init or netplan.
- The installed Claude Code in the VM is the native build (it was 2.1.316 on 2026-11-06), and it
  auto-updates.

- **CI is green on `windows-latest`** (commit 4ce09eb). The Win32 code in `lease` (OpenProcess,
  GetProcessTimes, LockFileEx) and `procjob` (the Job Object kill-on-close test) passes on real
  Windows. That run also caught CRLF in the embedded assets (now prevented by `.gitattributes`
  `eol=lf` plus normalization in `seed`) and an ISO file-handle leak. **Lesson: keep the repo LF;
  if you clone the repo on the Windows side (not in WSL), check `git ls-files --eol`.**

### 0.3 VirtualBox assumptions (historical: VirtualBox was dropped in favour of VMware, §10.6)
These are marked in the code, too. **#1–#3 checked on 2026-11-06** (VirtualBox 7.2.22, Windows 11
26200, from WSL against a throwaway VM `fclaude-spike` with an empty disk):
1. **VBoxManage flag spellings** in `vbox.DefineCommands`: **all accepted, and all took effect**
   (verified in `--machinereadable` and the `.vbox` XML). **But see the NAT bug below.**
2. **`--machinereadable` format:** confirmed: CRLF line endings; quoted values escape `\` and `"`;
   transient-share keys are `SharedFolder{Name,Path}TransientMapping<N>`; non-ASCII paths are
   UTF-8; a trailing `\` on `--hostpath` is dropped. **Exception:** `uartmode1="file,C:\Users\…"`
   is *not* escaped. The parser already handles that (an unknown escape stays literal). Fixtures
   in `internal/win/vbox/testdata/` are now real captures.
   - `showvminfo` issued *during* a poweroff transition fails with `E_ACCESSDENIED` "The object is
     not ready". `State()` must retry briefly on that error rather than report failure.
3. **Host-only adapter changes DO need admin.** VBoxManage, run unelevated, raises its own UAC
   prompt for `hostonlyif create`/`remove` (and probably `ipconfig`), and succeeds only if the
   user approves. (A first reading of "no admin" was wrong: the user had been approving the
   prompts.) So these belong in the single elevated step, as planned. The VirtualBox installer
   already creates `VirtualBox Host-Only Ethernet Adapter` at 192.168.56.1/24, so setup normally
   only *verifies* it (`list hostonlyifs` needs no admin) and elevates only when it is missing
   or misaddressed. The VirtualBox DHCP server on that network is **enabled** by default (range
   .100+, so the static .10 is safe), and step 6 still disables it.

**NAT localhost-reachable bug (security, found 2026-11-06):** `modifyvm --nat-localhostreachable1
off` reads back as `0`, but VirtualBox persists "off" by **omitting** the attribute (`<NAT/>`).
When VBoxSVC restarts (it exits after ~6 s idle, and on every reboot), it reloads a missing
attribute as **true**. The VM then reports `localhostReachable="1"` with no one touching it.
Reproduced deterministically: set off → wait for VBoxSVC to exit → reads 1. So a set-once
install step is worthless. Candidate fixes, to be validated against a real guest in R6:
(a) re-apply `off` and read back `0` immediately before every `startvm` (the VM process keeps
its config once started; VBoxSVC stays alive between the two calls), and refuse to start
otherwise; (b) an explicit NAT-driver extradata key (`VBoxInternal/Devices/virtio-net/0/LUN#0/
AttachedDriver/Config/LocalhostReachable`), which is persisted verbatim. A VM started from the
VirtualBox GUI bypasses (a), so (b), or both, is preferable if R6 shows it works.
4. **Path forms in the extension protocol:** drive-letter case, the `file:///c%3A/` URI form, and
   which keys hold file content. `pathmap.DefaultPolicy` is a first guess; the R4 capture decides.
5. **Can WSL reach the VM** on 192.168.56.10? That matters only for your own testing (the
   product's ssh runs Windows-side). If it can't, test through Windows binaries via interop.

### 0.4 How to work from WSL
- Repo: clone it in the WSL filesystem (e.g. `~/projects/fclaude`), `git checkout feat/windows`.
  Builds are fast there; `/mnt/c` is slow for builds.
- Build Windows binaries by cross-compiling **onto the Windows filesystem**, because VS Code
  (Windows) must be able to spawn them:
  ```bash
  WINHOME=$(wslpath "$(cmd.exe /c 'echo %USERPROFILE%' 2>/dev/null | tr -d '\r')")
  mkdir -p "$WINHOME/.fclaude/bin"
  GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -o "$WINHOME/.fclaude/bin/fclaude-capture.exe" ./cmd/fclaude-win
  ```
- Run Windows programs from WSL through interop:
  `"/mnt/c/Program Files/Oracle/VirtualBox/VBoxManage.exe" list vms`, `powershell.exe -NoProfile
  -Command …`, `cmd.exe /c …`. Their output has CRLF line endings; strip `\r`.
- Windows-API tests: `GOOS=windows go test ./internal/win/lease/ ./internal/win/procjob/` may run
  the test `.exe` directly through interop. If not, `go test -c -o "$WINHOME/x.test.exe"` and run
  that.
- The lint and test gate is the same as CI:
  - `gofmt -l .` (must print nothing);
  - `golangci-lint run ./...` and `GOOS=windows golangci-lint run ./...`;
  - `go test -race ./...`;
  - `shellcheck` for any shell you touch;
  - the Ansible syntax-check exactly as in `ci.yml`.
- Git: work on `feat/windows`, commit and push freely. Don't merge to main without the user. (In
  this repo the user allows commits to main, but this feature stays on its branch until the
  Windows end-to-end passes.)

### 0.5 Prerequisites (the user preinstalls these before the Windows session)
Windows side:
- **VMware Workstation Pro 17.5+** (tested 26.0.1; Broadcom download, free). Its installer creates VMnet1/VMnet8 and enables the Windows Hypervisor Platform.
- **VS Code** with the **Claude Code extension**, signed in, so the extension's bundled `claude.exe`
  exists for the R4 capture.
- **Git for Windows**, for repo-on-NTFS tests (CRLF behaviour, R2).
- **OpenSSH Client** (Settings → Optional features; `ssh -V` in PowerShell should work).
- **Windows Hypervisor Platform** feature enabled (VMware's installer does it; verify in preflight).
- **Developer Mode on** (Settings → System → For developers), so symlinks can be created without
  admin (R2 npm test).
- A test folder `C:\dev` (the default share root) with at least one git repo cloned *by Git for
  Windows* (for example a small Node project).

WSL (Ubuntu) side, where Claude Code runs:
- **Claude Code** CLI, logged in.
- **Go ≥ 1.26** from the go.dev tarball (Ubuntu's apt Go is too old).
- `git`, **`gh`** (authenticated, for push/PR/CI checks), `jq`, `python3`.
- `shellcheck`, `genisoimage` (for `isoinfo`), `ansible-core` (`pipx install ansible-core`, then
  `ansible-galaxy collection install ansible.posix`).
- `golangci-lint` v2.15: `go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0`.
- The repo cloned in the WSL filesystem, on `feat/windows`.
- WSL interop enabled (the default), so `.exe` files run from bash.

### 0.6 KVM seed-boot test (reproducible reference)
This booted the noble `.img` with qemu, `-cpu EPYC-Milan` (needed on the Zen4 Linux host; see
CODEBASE.md), two virtio NICs with the MACs from the seed, and NIC1 user-net with `hostfwd`
2299→22. The seed came from a throwaway `main` that called `seed.Files` and `seed.WriteISO`.
Observed results (2026-11-06):
- The netplan matched §0.2: `nat0` with DHCP and the only default route, `host0` static
  192.168.56.10/24, MTU 1400 on both.
- Provisioning finished in 144 s: `PLAY RECAP ok=23 changed=15 failed=0`, and
  `/var/lib/fclaude/.provisioned` was created.
- Claude Code 2.1.318 (native) and Docker 29.9 were installed.
- `/etc/modules-load.d/fclaude-vboxsf.conf` was written. **After a reboot**, `lsmod` shows `vboxsf`
  (plus `vboxguest`) loaded; on KVM they load without a device. `git config --system -l` shows the
  three settings.
- No `/home/<hostuser>` bind mount (correct: `host_user` is empty on Windows).
- `host0` also picked up IPv6 from qemu's router advertisements. So `accept-ra: false` was added
  to `host0`. That edit was validated with `netplan generate` inside the VM (it produces
  `IPv6AcceptRA=no`), but **not yet booted**; the first VirtualBox boot covers it.
- Heads-up for any manual netplan test: the cloud-init network-config format has `version: 2` at
  top level, and cloud-init adds the `network:` wrapper itself. Raw `netplan generate` needs that
  wrapper.

### 0.7 Phase 5 results (Windows session, 2026-11-06)
Host: Windows 11 26200, **AMD Ryzen 7 7840HS (Zen4), the same laptop as the Linux host**, 31 GiB,
VirtualBox 7.2.22, VS Code + Claude Code extension 2.1.318.

**R4 (capture), done. Gate result: build Option A.** Fixtures are in
`internal/win/pathmap/testdata/windows/`, replayed by `pathmap/replay_test.go`.
- The extension spawns the wrapper as `wrapper <…\native-binary\claude.exe> <flags>` with cwd
  set to the workspace. A **`.cmd` wrapper fails** (`spawn EINVAL`, the CVE-2024-27980 guard), so
  the wrapper must be a real `.exe` (confirmed).
- Each new Claude tab spawns **several** processes through the wrapper: `auth status --json`, the
  stream-json session, a short second stream-json session (`--thinking=disabled`, exits fast), and
  the `--claude-in-chrome-mcp` helper (cwd = the VS Code install dir, outside the share root).
  Every spawn costs an ssh round trip, so a cold VM is paid for on the first spawn (R7).
  `auth status` is workspace-agnostic and should skip the mount, like the MCP helpers (extend
  `mcphelper`).
- **Path forms on the wire:** only backslash drive paths. The drive is **lower-case `c:`** (VS
  Code's fsPath, from the cwd), and sometimes `C:` when the model writes a path itself. Native
  claude mixes both. There are **no `file://` URIs and no forward-slash paths** in stdio.
- **Where paths appear:** whole-string fields (`cwd`, `original_cwd`, `additional_directories`,
  `file_path`, `path`, `filePath`, and the new `wire_tool_inputs.<tool_id>` map, plus hook
  `tool_input`/`tool_response`). Embedded in text: `<ide_selection>`/`<ide_opened_file>` context
  (sent in, and replayed out by `--replay-user-messages`) and tool-result messages. Extension →
  claude also carries `control_response.updatedInput.file_path`.
- A second workspace folder (multi-root) arrives as **`--add-dir=c:\dev\r4-extra`**. The
  extension has no `/add-dir` command.
- `transcript_path`/`memory_paths` point into claude's own config dir
  (`~\.claude\projects\c--dev-r4-scratch\…`), outside the share root. In the VM they become VM
  paths and are not mapped. The extension's diff/hook flow uses `tool_input.file_path`, which
  *is* mapped.
- **Replay result:** of 27 claude→extension lines, **20 round-trip byte-exactly** and 6 differ
  only in drive case (where native claude wrote `C:` itself). This needs the Mapper to keep the
  root's drive case, built from `RootSpelling(shareRoot, cwd)`, which is now implemented.
  Extension→claude: all paths mapped.
- **Open, for R5:** `stream_event` `input_json_delta.partial_json` streams tool input as JSON
  *fragments*, so a path is split across chunks and a line-local rewriter cannot map it. The
  complete input follows in the `assistant` message. If R5 shows the extension renders paths from
  the deltas, add a stateful per-content-block rewriter that holds back a possible root prefix
  until a terminator.
- **Env (Linux has the same gap):** the extension sets `CLAUDE_CODE_ENTRYPOINT=claude-vscode`,
  `CLAUDE_AGENT_SDK_VERSION`, `CLAUDE_CODE_ENABLE_SDK_FILE_CHECKPOINTING`,
  `CLAUDE_CODE_SDK_READS_*`, `CLAUDE_CODE_EMIT_*` and others. ssh does not forward them, and
  neither wrapper does so today. Phase 9: forward an allowlist of `CLAUDE_*` vars (`env K=V` on
  the remote command, since sshd has no `AcceptEnv`).

**R1 (VirtualBox on Hyper-V), FAILED; the port is on hold.** Details and trial matrix in the TODO
(Phase 5, R1). The decision gate in §6 applies: VirtualBox-with-WSL2-on is not viable here. The user
is re-checking whether the WSL2 requirement (§10.2) can be dropped. Without Hyper-V, VirtualBox
uses AMD-V directly, which is its mature path, and the rest of this design is unchanged.

**R1-VMware spike (2026-11-06): VMware Workstation Pro 26.0.1 PASSES where VirtualBox failed.**
It runs on the same Hyper-V API with WSL2 on ("Monitor Mode: ULM"). VM from a hand-written `.vmx`
(text, very scriptable), disk via `vmware-vdiskmanager -r <cloud.vmdk> -t 0` + `-x 32GB`, our own
seed ISO, `vmxnet3` NICs on NAT (vmnet8, 192.168.146.0/24) + host-only (vmnet1, 192.168.201.0/24),
VMware-range static MACs. Driven by `vmrun -T ws` without admin.
- **Stability:** the identical 6-vCPU full-load trial gives **0/15 ssh misses, max 610 ms, the 90 s
  stress run in 90.55 s, 0 kernel warnings**, and memory verify OK. VirtualBox gave 6–15 misses and
  160 s+. The guest clocksource stays `tsc` (VirtualBox fell to `acpi_pm`). No initramfs hang.
  Provisioned with `ok=23 failed=0`. ssh works from Windows and from WSL.
- **MTU:** VMware's NAT (a userspace proxy) ignores the guest's smaller MSS. **With guest MTU
  1400, every HTTPS handshake hangs**; with 1500, all fine. The seed MTU must be per-hypervisor
  (1500 on VMware). The 1400 came from the Linux host's tunnel case.
- **Shared folders:** `vmrun enableSharedFolders` + `addSharedFolder <vmx> <name> <dir>` on a
  **running VM, no admin**. The guest mounts with stock open-vm-tools (`vmhgfs-fuse
  .host:/<name> <dir> -o allow_other,uid=1000,gid=1000`). Read, write, git and Windows visibility
  all OK. **Symlinks: "Operation not supported"** (same risk as vboxsf; npm `.bin` needs
  `--no-bin-links` or a VM-local `node_modules`). Quirk: `touch` of a new empty file gives mtime
  1970 (`utimensat` UTIME_NOW); files written with content, and explicit times, are correct.
- **Release while mounted (R3):** `removeSharedFolder` with the mount up makes the mount empty
  (ENOENT), with no hang. **Re-adding the same name heals the existing mount** with no remount.
  `umount` is clean.
- **NAT side-channel (R6):** the NAT gateway `.146.2` reaches **nothing**, and host loopback-only
  services are unreachable from every address. This is by design, not a fragile setting as in
  VirtualBox. The host's vmnet8/vmnet1 addresses expose 135/139/445/9012 pre-firewall; those
  arrive on real adapters, so the §3.7 rules (scoped to both subnets) apply.
- **Costs:** no winget package. The download needs a Broadcom account, so setup cannot install it
  for the brother; he installs it first. It's free for commercial and personal use since
  2024-11-11.
- **Implication if adopted:** `vbox` becomes a `hv` interface with a VMware implementation (vmx
  writer, vmrun runner); `share` uses HGFS instead of vboxsf; the rest (`seed`, `pathmap`, `lease`,
  `procjob`, wrapper design) is unchanged. Host-only subnet is assigned by VMware (read it from
  the adapter or `vmnetlib`), not fixed at 192.168.56.0/24.

**R2 (HGFS matrix), passed; HGFS stays.** Test dir `C:\dev\r2-hgfs` shared into the VMware VM,
compared with the VM disk:

| Test | HGFS | VM disk |
|---|---|---|
| git clone express / status / commit | 19.9 s / 0.36 s / 0.14 s | 16.7 s / 0.01 s / 0.02 s |
| Git-for-Windows repo, VM `git status` | 0 dirty lines | |
| `npm ci` (typescript, eslint, esbuild) | plain: **fails** (symlinks); `--no-bin-links` 5.2 s; VM-local `node_modules` 1.6 s | 1.5 s |
| 1,322-file scan: find / stat | 0.44 s / 0.90 s | 0.01 s / 0.02 s |
| 1 GB write / read | 111 / 430 MB/s (checksum matches on Windows) | 280 / 1,260 MB/s |
| case-only rename; sqlite WAL + rollback journal; create in VM → read on Windows | all correct | |
| file held open on Windows | sharing allowed: read/write/rename/delete OK; exclusive: EACCES (`mv` reports ENOENT) | |

**`node_modules` design:** a VM-local `node_modules` bind-mounted over the workspace's
(`/var/lib/fclaude/nm/<wskey>` → `<ws>/node_modules`). It's native speed and keeps Linux-only
binaries (esbuild etc.) from overwriting the Windows `node_modules` that the brother's Windows
tools use. The bind mount must be set up before the mount is handed to claude, and released with
the share. The cheap alternative is `/etc/npmrc` `bin-links=false`, but that still clobbers the
Windows tree. Decide the default in Phase 6 (`share`), and document it in the README.

**R6 (NAT `10.0.2.2` reachability), done.** Probed from the provisioned VM via TCP connect. The
targets were real host services: loopback-only `127.0.0.1:1042` and `:9080`; all-interface 135,
445, 5040, 9012; 139 on 192.168.56.1.
- `localhostReachable=0`: **nothing** is reachable via `10.0.2.2`, not even the all-interface
  listeners. The NAT side-channel is fully closed.
- `localhostReachable=1` (the state the VBoxSVC bug in §0.3 silently produces): the guest reaches
  **135, 445, 5040, 9012 and the loopback-only 1042/9080**. The connections come from the VBox
  process, so Windows Firewall cannot filter them. That is a full bypass of the privileged-port
  rule.
- Fix (b), the NAT-driver extradata `LocalhostReachable=0`, **does not work**. VBox.log shows the
  key is added, then the NAT setting's own value is applied after it and wins.
- **Decision:** fix (a). (1) Every `startvm` path re-asserts `--nat-localhostreachable1 off` and
  reads back `localhostReachable="0"` immediately before starting, and refuses to start
  otherwise. (2) Every session start checks the *running* VM's value. If it is `1` (for example,
  started from the VirtualBox GUI), refuse with a message to run `fclaude --shutdown` and retry.
  The guest-side route block stays a documented non-fix (root in the VM can undo it).
- Host-only side without firewall rules (the expected pre-install state): 135, 139, 445, 9012
  open. The §3.7 rules are verified in Phase 10.

**Installer-relevant VirtualBox findings** (fixed in code, or queued in the TODO):
- Seed ISO not a whole number of 2048-byte sectors → VirtualBox refused it as a DVD (qemu doesn't
  care). Fixed in `seed.WriteISO` (trim to the volume size).
- `clonemedium` registers the source VMDK, and the Ubuntu VMDK has a zero UUID, so a second
  import fails. Fixed in `ImportDiskCommands` (close the source before and after).
- `--nat-localhostreachable1 off` does not survive a VBoxSVC restart (§0.3).
- `showvminfo` fails transiently with `E_ACCESSDENIED` during poweroff.

---

## 1. Goal and scope

Port fclaude to a Windows 10/11 host so the user's brother can run Claude Code in yolo mode
inside an isolated VM. He works mainly from **VS Code**, and his projects live on the **Windows
filesystem (NTFS)**.

In scope:
- VM in **VMware Workstation Pro** (Ubuntu 24.04, reusing the existing cloud-init and Ansible provisioning).
- Per-session workspace sharing of NTFS directories into the VM.
- `fclaude-vscode.exe`: a `claudeCode.claudeProcessWrapper` for the Windows VS Code extension.
- `fclaude.exe` CLI with parity for the common commands: session, `--status`, `--maintenance`,
  `--shutdown`, `--update`, `--cleanup`, `--mount`.
- An installer and an uninstaller, with the same disk-safety rules as Linux (never delete the disk
  silently; an existing disk means a restore).
- Lint, tests, and CI for all new code.

Out of scope for v1 (the "experimental" label):
- llm.log integration, a generic multi-hypervisor abstraction, ARM Windows.
- Workspaces outside the one configured share root (`SHARE_ROOT`, e.g. `C:\dev`, chosen at install).
- Running the Linux host code on Windows. The Linux implementation stays **untouched**.

## 2. Architecture

```
VS Code (Windows) ── spawns ──► fclaude-vscode.exe  (Go, native Windows)
                                   │ 1. ensure VM running        (vmrun start <vmx> nogui)
                                   │ 2. acquire workspace lease  (PID-liveness refcount, file lock)
                                   │ 3. share the workspace dir  (vmrun addSharedFolder <vmx> fc-<key> <dir>)
                                   │ 4. mount it in the VM       (ssh: sudo vmhgfs-fuse .host:/fc-<key> …)
                                   │ 5. run claude over SSH      (x/crypto/ssh, stdio)
                                   │    └─ JSON path rewriter    C:\Users\bro\p ⇄ /home/fclaude/p
                                   │ 6. release on exit / reap dead leases on next run
                                   ▼
VMware VM "fclaude" (%USERPROFILE%\.fclaude\vm\fclaude.vmx; Ubuntu 24.04, same site.yml)
   NIC1 NAT (VMnet8)       → internet, MTU 1500
   NIC2 host-only (VMnet1) → <vmnet1 subnet>.10 (static), host is .1
   Windows Firewall: inbound from both VMnet subnets → block 1-1023, allow 1024-65535
```

**WSL is not required by this design.** That choice is deliberate (see §3.2), and it leaves WSL
free for other uses later. **WSL2 stays installed and enabled on the target machine** (the brother
needs it), so Hyper-V is always on, and the hypervisor always runs on top of the Windows
Hypervisor Platform API. Disabling Hyper-V is **not** an option. R1 showed VirtualBox is not viable
in that mode and VMware Workstation is (§0.7, §3.1).

## 3. Key decisions

### 3.1 Hypervisor: **VMware Workstation Pro** (17.5+; tested 26.0.1), driven by `vmrun.exe`

**Switched from VirtualBox on 2026-11-06** (user decision, §10.6). WSL2 stays on, so Hyper-V owns
AMD-V/VT-x and any third-party hypervisor runs on the Windows Hypervisor Platform API.
VirtualBox's backend for that ("NEM snail mode") stalls the guest under multi-vCPU load. VMware's
("Monitor Mode: ULM") passed the identical trial cleanly (§0.7).

- **Prerequisite, installed by the user:** VMware Workstation Pro, free for all use since
  2024-11-11. There is no winget package and the download needs a Broadcom account, so setup
  cannot install it. It checks for it and prints instructions. The VMware installer enables the
  Windows Hypervisor Platform feature itself.
- **Locate:** `HKLM\SOFTWARE\VMware, Inc.\VMware Workstation` → `InstallPath`,
  `ProductVersion` (readable without admin), falling back to
  `C:\Program Files\VMware\VMware Workstation`. Tools used: `vmrun.exe` (`-T ws`) and
  `vmware-vdiskmanager.exe`.
- **VM definition:** we **write the `.vmx` ourselves** (plain `key = "value"` text, CRLF). The VM
  is never registered in the Workstation library; `vmrun` addresses it by `.vmx` path. That makes
  setup idempotent by construction: regenerate the `.vmx`, never edit it in place except for the
  share keys. Key settings: `virtualHW.version`, `guestOS = "ubuntu-64"`, `firmware = "bios"`,
  `memsize`, `numvcpus`, SATA disk + SATA CD-ROM (seed ISO), two `vmxnet3` NICs (+ the
  `pciBridge4..` PCIe root ports they need) with static VMware-range MACs
  (`00:50:56:00:00:00`–`00:50:56:3F:FF:FF`), `serial0` to a file (boot diagnostics), and hardening
  (`usb.present`, `sound.present`, `floppy0.present` off; `isolation.tools.{copy,paste,dnd}.disable`
  on; HGFS left enabled for the shares).
- **Disk:** download `ubuntu-24.04-server-cloudimg-amd64.vmdk` (it's streamOptimized, which VMware
  won't boot), then `vmware-vdiskmanager -r <src> -t 0 <dst>` (monolithic sparse) and
  `-x 32GB <dst>`. Grow later with `-x` while powered off.
- **Lifecycle:** `vmrun start <vmx> nogui`, `vmrun stop <vmx> soft|hard`, `vmrun list` for state
  (a running VM lists its `.vmx` path). None of these need admin.
- Resources default to 8 GiB / 6 vCPU, as on Linux, and are configurable. The guest sees the host
  CPU model (no AVX-512 is exposed under the Hyper-V API) and uses the `tsc` clocksource.

### 3.2 Workspace sharing: **VMware shared folders (HGFS)**, per workspace, added to the running VM

| Option | Verdict | Why |
|---|---|---|
| NFS server in WSL exporting `/mnt/c/...` | **Rejected** | `/mnt/c` is 9p/drvfs; the kernel nfsd cannot export it. Three hops, two VMs. |
| SMB (Windows built-in server) + `cifs` | **Fallback** | Creating shares needs admin, 445 is privileged (conflicts with the firewall rule), and it needs Windows credentials in the VM. |
| **HGFS shared folders** | **Chosen** | `vmrun addSharedFolder <vmx> <name> <dir>` works on a **running VM without admin**, so the VM sees only the open workspaces. The guest's stock open-vm-tools mounts it: `vmhgfs-fuse .host:/<name> <dir> -o allow_other,uid=1000,gid=1000`. No network service, no credentials. |

Verified (§0.7): read, write and git work, and writes are immediately visible on Windows.
`removeSharedFolder` while mounted empties the mount (ENOENT) without hanging, and re-adding the
same name heals the existing mount.

- **Shares persist in the `.vmx`** (`sharedFolderN.*`, `expiration = "never"`, active count in
  `sharedFolder.maxNum`). They are not transient. Consequences: release must remove them, and the
  **cold-boot sweep** (leases deleted when the VM was off, §3.5) also clears every share from the
  `.vmx` before `vmrun start`. No share then survives a crash into the next boot.
- Release order: guest `umount` (lazy fallback) → `vmrun removeSharedFolder`.
- Share names are derived from the workspace key (`fc-<hash>`). The guest mount point is
  `/home/fclaude/<rel>`.
- **Known limitations:** symlink creation fails ("Operation not supported"), so npm's
  `node_modules/.bin` needs `--no-bin-links` or a VM-local `node_modules` (bind mount). R2 decides
  which to document. `touch` on a new empty file sets mtime 1970; files written with content are
  correct. Windows refuses to rename or delete a file another process holds open (to be measured
  in R2).
- Exec bits: everything appears with one mode. That is harmless with `core.filemode=false`, which
  Git for Windows sets.

**Decision gate:** if R2 shows HGFS is unusable for git/npm workloads, switch the `share` package
to the SMB backend. The interface is `Share(dir) (vmPath, release)`.

### 3.3 Host tooling: **Go**, cross-compiled to Windows `.exe`

- The original requirements say "if more complexity is needed, I prefer Go". This port needs it:
  vmrun/.vmx orchestration, Windows process-liveness APIs, a stdio JSON rewriter, ISO generation,
  and ACL setup.
- The wrapper **must be a real `.exe`**. Since the CVE-2024-27980 fix, Node's `spawn()` on
  Windows refuses `.cmd`/`.bat` without `shell: true`, and we cannot control how the extension
  spawns the wrapper. This is verified in [R4].
- It develops and tests on Linux too: `GOOS=windows go build` and pure-logic unit tests run in CI
  on `ubuntu-latest`. Code that uses Windows APIs is tested on a `windows-latest` CI runner.
- Layout: one module at the repo root, one multi-command binary.

```
go.mod                                 module github.com/robymus/fclaude
cmd/fclaude-win/main.go                single binary; argv[0] basename selects mode:
                                         fclaude.exe → CLI, fclaude-vscode.exe → VS Code wrapper
                                         (install copies the same exe under both names)
internal/win/config/                   %USERPROFILE%\.fclaude\config.json load/validate/defaults
internal/win/vmw/                      VMware front end: Locate, vmrun runner, .vmx writer, VMnet discovery, shares, vdiskmanager
internal/win/vmssh/                    x/crypto/ssh client (stdio sessions, exec, keepalive)
internal/win/share/                    Share interface; HGFS impl (+ smb impl only if [R2] fails)
internal/win/lease/                    PID+creation-time leases, LockFileEx-guarded, reaper
internal/win/pathmap/                  Windows⇄VM path mapping + streaming JSON rewriter
internal/win/seed/                     cloud-init NoCloud ISO (label "cidata", Rock Ridge) via diskfs/go-diskfs
internal/win/install/                  install/uninstall steps (idempotent, disk-guarded)
internal/win/procjob/                  Job Object (KILL_ON_JOB_CLOSE) so ssh/children die with us
internal/win/winpath/                  pure-string Windows path semantics (testable on Linux)
internal/win/capture/                  R4 stdio recorder (fclaude-capture.exe)
internal/win/mcphelper/                port of fc_is_mcp_helper
vm/assets.go                           package vm; //go:embed ansible/site.yml cloud-init/*.tmpl update/*
```

`vm/assets.go` embeds the **existing** provisioning files, so the Windows binary is
self-contained and can never drift from what Linux ships. Dependencies, all widely used:
`golang.org/x/crypto/ssh`, `golang.org/x/sys/windows`, `golang.org/x/term`,
`github.com/diskfs/go-diskfs` (ISO writer; chosen over kdomanski/iso9660 for Rock Ridge support, which keeps the lower-case hyphenated NoCloud names intact).

### 3.4 VS Code integration: process wrapper + **path rewriting** (default). Remote-SSH is the alternative.

The core Windows problem: on Linux we `cd` into the *host* absolute path inside the VM, so the
paths claude reports match the editor's paths and file links and diffs work. **On Windows this is
impossible.** Claude in Linux can never report `C:\Users\bro\proj\x.ts`.

**Option A (CHOSEN by user): stdio path-rewriting proxy.** Details are tuned with experiments on Windows ([R4]/[R5]). The extension talks to claude over
line-delimited JSON (stream-json plus control messages). `fclaude-vscode.exe` owns both pipes, so
it rewrites path strings in both directions:
- Extension → VM: `C:\Users\bro\proj\src\a.ts` (also `c:/…`, `c:\\…`, `file:///c%3A/…`) becomes
  `/home/fclaude/proj/src/a.ts`.
- VM → extension: the reverse, emitting the form the extension sent us (backslash, upper-case
  drive).
- Mapping is case-insensitive on the Windows side, and only applies **under SHARE_ROOT**. Other
  strings pass through unchanged.
- The rewriter is **structural**: it parses each JSON line, walks string values, and rewrites only
  values that match a path pattern anchored at the share root. It never does raw text
  substitution. Unparseable lines are forwarded byte-for-byte.
- It also rewrites argv (`--add-dir`, `--mcp-config` paths, and so on) and the `cwd`.

Risk: the extension's protocol changes often. The structural walk needs no field list, so the
real risk is path *forms*. [R4] captures a native Windows session (see §6), producing golden
fixtures that become the rewriter's test suite.

**Option B (fallback only if A proves unworkable): VS Code Remote-SSH into the VM.** The brother opens
`ssh://fclaude/home/fclaude/proj`, and the extension runs *inside* the VM, so all paths are native.
That means no wrapper, no rewriting, and no protocol coupling. Costs:
- The VS Code server and every remote extension run in the VM, where the yolo agent can tamper
  with them. They have a channel back to the Windows UI, which is a weaker boundary than A.
- The workspace mount must exist before the folder is opened (`fclaude.exe --mount <dir>` held
  open, or a persistent share).
- Windows-side tooling, such as Windows git and his other extensions, now runs in the VM.

**Decision gate:** if [R4] shows paths are confined to a handful of well-defined fields, build A.
If they are spread through free text, or A cannot make diffs and file links work in [R5], ship B
and document it. Either way, `fclaude.exe` CLI sessions don't need rewriting (a terminal user
sees VM paths, which is fine).

### 3.5 Session lifetime: **liveness-based leases instead of EXIT-trap refcounts**

On Windows, VS Code ends the wrapper with `TerminateProcess`. That runs no `defer`, no signal
handler, and no cleanup. A Linux-style "decrement on exit" counter would leak on every editor
close. Instead:
- A lease is a file `%USERPROFILE%\.fclaude\leases\<wskey>\<pid>-<creationTime>`. PID plus
  process creation time is safe against PID reuse; it is checked via `OpenProcess` and
  `GetProcessTimes`.
- **Acquire:** under a `LockFileEx` lock on `<wskey>.lock`, reap dead leases, write ours, and
  share and mount if no live lease existed (self-healing, as in Linux `_do_mount`: verify the
  mount table, not the counter).
- **Release:** on a graceful exit (stdin EOF from the extension, Ctrl+C in the CLI) remove our
  lease, and if none remain, unmount in the VM and `sharedfolder remove`.
- **Reap:** every wrapper start, plus `fclaude --status`/`--cleanup`, sweeps all workspaces and
  releases those with no live lease. A killed editor's mount is therefore released on the next
  invocation of anything. Optionally, a single background reaper exists for the VM's lifetime;
  that is deferred and only added if the lazy sweep proves insufficient.
- **Cold boot:** if the VM was off, delete all leases, matching Linux `reset_refcounts_cold_boot`.
- The ssh session is in-process (x/crypto/ssh), so a killed wrapper drops the TCP connection, the
  VM's sshd sends SIGHUP to claude, and there are no orphaned host processes. For the CLI path,
  which uses `ssh.exe -t`, the child is placed in a **Job Object** with `KILL_ON_JOB_CLOSE`.
- MCP-helper invocations (`mcp …`, `--claude-in-chrome-mcp`) skip mounting, the same as Linux
  `fc_is_mcp_helper` (the logic is ported along with its tests).

### 3.6 SSH: in-process for VS Code, `ssh.exe` for the interactive CLI

- **VS Code path:** `x/crypto/ssh` session with stdin, stdout, and stderr wired through the
  rewriter. This needs no PTY, no `ssh.exe`, and no key-ACL problems.
- **CLI interactive (`fclaude`, `--maintenance`):** Windows' built-in
  `C:\Windows\System32\OpenSSH\ssh.exe -t` handles ConPTY, resizing, and colors properly.
  Doing this in-process would mean reimplementing console raw mode and resize polling, which is
  not worth it. Prerequisite: the OpenSSH client (an optional feature, on by default in Win11).
  The installer checks for it and gives the `Add-WindowsCapability` command if it is missing.
- OpenSSH on Windows rejects private keys with loose ACLs. The installer runs
  `icacls <key> /inheritance:r /grant:r "%USERNAME%:R"`.
- Host key: pinned at install time (written to `.fclaude\ssh\known_hosts`). This is stricter
  than Linux's `StrictHostKeyChecking=no`, and cheap to do in Go.

### 3.7 Networking and firewall

- **NIC1: NAT** (`ethernet0.connectionType = "nat"`, VMnet8) for internet access. DHCP and DNS
  come from VMware's NAT service (gateway `.2` of the VMnet8 subnet).
- **NIC2: host-only** (`"hostonly"`, VMnet1). The VM gets a static `.10` in the VMnet1 subnet via
  the cloud-init `network-config` (both NICs matched by MAC). VMware's host-only DHCP hands out
  `.128` and up, so `.10` is safe. Host-to-VM ssh goes over this link.
- **The subnets are chosen by the VMware installer** (here 192.168.201.0/24 and 192.168.146.0/24).
  Setup reads them from `HKLM\SOFTWARE\VMware, Inc.\VMnetLib\VMnetConfig\vmnet{1,8}` →
  `IPSubnetAddress` (no admin) and stores the VM IP in `vm.json`. They are not hard-coded.
- **MTU 1500 on both NICs.** VMware's NAT is a userspace TCP proxy that ignores the guest's smaller
  MSS. With MTU 1400, every HTTPS handshake hangs (verified). The seed's `MTU` parameter is
  therefore per hypervisor (KVM keeps 1400).
- **Firewall (requirement: VM must not reach privileged host ports):** one elevated step adds the
  Windows Defender Firewall rules from the previous design, scoped to `-RemoteAddress` of **both**
  VMnet subnets. Guest traffic to the host's `.1` addresses arrives on these real virtual
  adapters, so the rules apply. Uninstall: `Remove-NetFirewallRule -Group fclaude`.
- **NAT side-channel: absent.** From the guest, the NAT gateway reaches no host port at all, and
  host loopback-only services are unreachable from every address (verified). Unlike VirtualBox's
  `localhostReachable`, this is not a setting that can silently flip.

### 3.8 VM provisioning: reuse `site.yml` almost unchanged

- `host_user` is passed as **empty**, so the `/home/<hostuser>` bind-mount tasks skip themselves.
  The path mapping is done by the rewriter instead.
- `fclaude_platform` becomes **`vmware`** (was `virtualbox`; Linux stays `kvm`). Gated on it:
  - ensure `open-vm-tools` is installed (the cloud image ships it; the task makes it explicit) and
    drop the VirtualBox-only `vboxsf` modules-load task;
  - the system gitconfig for repos created on Windows (`core.autocrlf=true`,
    `core.checkStat=minimal`, `core.trustctime=false`). It now applies to any Windows platform
    (`fclaude_platform != 'kvm'`).
- cloud-init `user-data.tmpl` keeps `-e fclaude_platform=__FCLAUDE_PLATFORM__`. The two-NIC
  `network-config-vbox.tmpl` is hypervisor-neutral (MAC-matched) and is renamed to
  `network-config-win.tmpl`. The KVM `network-config.tmpl` stays byte-identical.
- The weekly apt timer and staleness stamps (`vm/update/*`) are reused verbatim.

### 3.9 Path mapping rules (`internal/win/pathmap`)

```
SHARE_ROOT   = C:\dev                     (config.json; asked at install, default C:\dev)
VM_BASE      = /home/fclaude
C:\dev\work\app\src\x.ts            →  /home/fclaude/work/app/src/x.ts
c:/DEV/work/app                    →  /home/fclaude/work/app           (case-insensitive match)
file:///c%3A/dev/work/app          →  file:///home/fclaude/work/app
/home/fclaude/work/app/x.ts        →  C:\dev\work\app\x.ts            (reverse: root string + VM tail)
```
- The VM-to-Windows direction keeps the tail exactly as claude wrote it, because NTFS is
  case-insensitive. Only the root prefix is replaced.
- Paths outside SHARE_ROOT are left untouched. For example `/tmp/x` in the VM stays as is, and
  the extension won't be able to open it, which is the same as Linux behavior for VM-only paths.

```go
// internal/win/pathmap/pathmap.go (sketch)
type Mapper struct {
    winRoot  string // `C:\Users\Bence`, cleaned, no trailing sep
    vmRoot   string // `/home/fclaude`
}

// ToVM rewrites s if it is (or starts with) a Windows path under winRoot.
func (m *Mapper) ToVM(s string) (string, bool) { … }

// ToWin rewrites s if it is (or starts with) vmRoot + "/" or equals vmRoot.
func (m *Mapper) ToWin(s string) (string, bool) { … }

// RewriteJSONLine parses one NDJSON line and rewrites every string value
// (recursively, map keys untouched) with fn; returns the original bytes on
// parse failure so protocol noise is forwarded unchanged.
func RewriteJSONLine(line []byte, fn func(string) (string, bool)) []byte { … }
```
Strings that *embed* a path mid-text (e.g. `"Edited C:\\Users\\…\\x.ts"`) are handled by a
bounded regex scan per string, anchored on the root. That catches tool results and diff headers.
Test corpus: the fixtures captured in [R4].

## 4. Distribution and install flow

**Distribution (user decision): a prebuilt release.** A GitHub Actions release job on `v*-win*`
tags builds one binary and attaches it as **`fclaude-setup.exe`** plus `SHA256SUMS`. The brother
installs **VMware Workstation Pro first** (README walks through the Broadcom download), then
downloads and double-clicks `fclaude-setup.exe`. A console wizard sets up everything else. It is
the same Go binary: the `fclaude-setup` argv[0] basename (or no arguments from Explorer) selects
interactive install mode.
- Unsigned binary: SmartScreen shows "unknown publisher" (More info → Run anyway); documented.
- Versioning: `-ldflags "-X main.version=<tag>"`. Re-running a newer setup over an existing
  install upgrades the binaries only. It never reprovisions the VM or touches the disk.

Run as a normal user. Only the firewall rules need admin: a single UAC prompt (`ShellExecute
"runas"` on `fclaude.exe install-elevated`).

0. **Ask:** the share root (default `C:\dev`, created if missing after confirmation). Everything
   else uses defaults (8 GiB / 6 vCPU / 32 GiB), changeable later in `config.json`.
1. **Preflight:** Windows build, OpenSSH client, host RAM and disk, `SHARE_ROOT` exists.
   **VMware Workstation ≥ 17.5** found via the registry (else print the download steps and exit).
   VMnet1 and VMnet8 exist with readable subnets.
2. **Dirs:** `%USERPROFILE%\.fclaude\{ssh,leases,bin,vm}`.
3. **Disk (guarded):** if `vm\fclaude-disk.vmdk` exists, prompt "is this a restore from
   backup?". Yes skips the disk step; No exits. Otherwise download the `.vmdk` (verify against
   `SHA256SUMS`), convert it with `vmware-vdiskmanager -r … -t 0`, and grow it with `-x 32GB`.
4. **SSH keys:** ed25519 keypairs for `fclaude` and `maint`, key ACLs fixed (`icacls`).
5. **Seed ISO:** render `user-data` (platform=vmware, host_user=""), `meta-data`, and
   `network-config-win` (MACs, VMnet1 `.10`, MTU 1500). Write `vm\seed.iso` (label `cidata`,
   sector-aligned).
6. **VM:** write `vm\fclaude.vmx` (§3.1) with no shares.
7. **Firewall:** the elevated `install-elevated` step (rules scoped to both VMnet subnets, §3.7).
8. **Binaries:** copy itself to `.fclaude\bin\fclaude.exe`, `fclaude-vscode.exe`,
   `fclaude-setup.exe`, and add `.fclaude\bin` to the user PATH (HKCU `Environment` +
   `WM_SETTINGCHANGE`).
9. **Config + VM info:** `config.json` (never overwritten; `.new` beside it) and `vm.json` (VM
   IP, subnets, MACs, `.vmx` path).
10. **Boot and wait:** `vmrun start … nogui`, wait for `/var/lib/fclaude/.provisioned` over ssh
    with progress (about 6 min). Pin the host key. Then detach the seed ISO (rewrite the `.vmx`
    CD-ROM entry) so a later boot can't re-seed.
11. **Finish:** offer to write `claudeCode.claudeProcessWrapper` into VS Code's user
    `settings.json` (JSONC-aware, backup first, only with consent); then offer the Claude login
    (`fclaude --maintenance` → `claude`).

`fclaude.exe uninstall`: typed-phrase confirmation (`remove fclaude`); `vmrun stop`; remove the
`.vmx` and the VM directory **except the disk**; remove the firewall group (UAC
`uninstall-elevated`), PATH entry, VS Code wrapper setting (consent, backup) and binaries (a
detached `cmd.exe` deletes `bin\` just after exit); a separate typed `DELETE DISK` prompt at the
end. Mirrors `uninstall.sh`.

**What a restore needs (implemented 2026-11-07):** besides the disk, the `ssh\` keys (the VM only
accepts the keys it was provisioned with) and `vm.json` (the MACs its netplan matches). The seed's
`instance-id` is constant, so cloud-init does not re-run on a restored disk and nothing in the
VM is reconfigured. Uninstall therefore keeps `ssh\`, `vm.json` and `config.json` with the disk.
Setup's restore branch reuses them, finds `.provisioned` at once, and warns if the key is
missing. A disk moved to *another* machine also needs the same VMnet1 subnet (its static IP).
This is a documented limitation.

`fclaude.exe grow-disk 64G`: confirm, stop the VM, `vmware-vdiskmanager -x`, start, then
`growpart` + `resize2fs` over ssh.

## 5. VS Code setup (brother's side)

```jsonc
// settings.json (user) — install prints the exact line with his username
"claudeCode.claudeProcessWrapper": "C:\\Users\\<user>\\.fclaude\\bin\\fclaude-vscode.exe",
// + enable the extension's own "skip permissions" setting, as on Linux
```
Workspaces must be under `SHARE_ROOT`. Otherwise the wrapper prints a clear error to stderr
(which the extension shows) and exits non-zero, as on Linux.

## 6. Research spikes (run on Windows before building the dependent parts)

| ID | Question | How | Pass → | Fail → |
|---|---|---|---|---|
| R1 | Is a hypervisor viable next to WSL2 (Hyper-V on)? | Boot headless; 6-vCPU `stress-ng --cpu --vm --verify` while polling ssh; check `dmesg`. | **VirtualBox: FAILED. VMware Workstation: PASSED** (§0.7) | — |
| R2 | Is HGFS good enough? | Share on a running VM; in the VM: `git clone` + `git status` + commit, `npm ci` (symlinks: expected to fail, so test `--no-bin-links` and a VM-local `node_modules`), rename while VS Code has the file open, create in the VM and open in VS Code at once, a 1 GB file, case-only rename, `fsync`-heavy `sqlite3`, and small-file performance (`git status` on a big tree). | **PASSED** (§0.7): HGFS stays | — |
| R3 | Do share add/remove work reliably while mounted? | add → mount → remove-with-mount → re-add loops. | **Done (VMware):** remove empties the mount, re-add heals it. Order: guest `umount` → `removeSharedFolder` | — |
| R4 | How does the Windows extension call the wrapper, and what path forms appear? | Native pass-through capture build (see below): log argv/env/cwd/stdio, run the bundled `claude.exe` natively. Exercise: chat, @-mention a file, selection context, Edit/Write diff accept and reject, open file links, a second workspace folder (multi-root; the extension has no `/add-dir`) and an absolute path outside the workspace, plan mode, interrupt, close tab. Confirm that `.exe` is required (try a `.cmd` once). | Paths in well-defined fields → build Option A | Option B (Remote-SSH) |
| R5 | Does Option A make diffs and file links work end to end? | After building the rewriter: the same scenario list as R4, checked by hand. | Ship A | B, or A with known gaps listed |
| R6 | What can the VM reach on the host through NAT? | TCP probes from the VM to the NAT gateway and the host addresses on real loopback-only and all-interface services. | **Done.** VirtualBox: depends on a flaky setting. **VMware: the NAT gateway reaches nothing** (§0.7) | — |
| R7 | Does the extension time out while the wrapper cold-boots the VM (~30 s)? | Shut the VM off, open the Claude panel. | Fine | Print progress to stderr; optionally autostart the VM at logon (a Task Scheduler entry `fclaude.exe --start`) |
| R8 | Does the Claude login (OAuth paste flow) work over `ssh.exe -t` in Windows Terminal? | `fclaude --maintenance`, then `claude`. | **PASSED** (VMware VM, Windows Terminal, 2026-11-06) | — |

**R4 method (revised): native pass-through capture on Windows, day one.** The extension calls
the wrapper as `wrapper <bundled claude.exe> <flags…>`. `fclaude-capture.exe` (the same binary, renamed; **already implemented**, see
`internal/win/capture`) logs argv, env, cwd, and both stdio streams,
then **runs that bundled `claude.exe` natively on Windows**. It needs no VM, no hypervisor, and no
fclaude infrastructure, so it can run before anything else is built. The result is the ground
truth: exactly what a *native* Windows claude emits and receives. That is the target the
rewriter's VM→Windows output must reproduce, and the extension→claude input it must translate.
The captures become golden fixtures (`internal/win/pathmap/testdata/windows/`), sanitized of
tokens and user content.

A Linux capture was considered and **dropped**. The message structure is the same on both
platforms, but the path *forms*, which are where the risk is, only show on Windows. And the
rewriter walks every string, so it doesn't need a field inventory up front.

Code that can be written and unit-tested **on Linux, before the reboot**: `pathmap` (table tests
for the path forms in §3.9, written from first principles and later validated against the Windows
fixtures), `lease` (with the liveness check abstracted), `config`, `seed`, and the hypervisor
argument builders.

## 7. Testing and CI

- **Go:** `gofmt -l` (must be empty), `go vet ./...` for linux and `GOOS=windows`, and
  `golangci-lint run` (config `.golangci.yml`: errcheck, govet, staticcheck, gosec, revive,
  misspell).
- **Unit tests** (`go test ./...`, with the race detector on linux):
  - `pathmap`: table tests covering every path form in §3.9, plus fixture replays from R4 (the
    rewritten output must equal the golden file, and an unparseable line passes through
    unchanged).
  - `lease`: acquire, release, reap, PID reuse (same PID, different creation time), cold-boot
    sweep, and concurrent acquire. Liveness is behind an interface on Linux; the real
    implementation is tested on `windows-latest`.
  - `vmw`: `.vmx` rendering (golden file), `vmrun` argument construction and `list` parsing,
    VMnet discovery against fake registry values. `vmrun` is behind an `exec` interface.
  - `share`: mount and unmount orchestration with fake vmw and ssh, including rollback on a
    failed VM mount and the self-heal case (a lease is live but the mount is missing).
  - `seed`: the ISO round-trip (write, then read back with iso9660, checking label `cidata`).
  - `install`: the disk guard (existing disk with restore=no means no changes) and
    config-never-overwritten.
  - The MCP-helper detection port (`fc_is_mcp_helper` parity cases).
- **Release:** `.github/workflows/release-windows.yml` on `v*-win*` tags. It runs the full lint
  and test gate, builds `GOOS=windows GOARCH=amd64 CGO_ENABLED=0` with a version ldflag, and
  creates the release with `gh release create` (`fclaude-setup.exe` + `SHA256SUMS`).
- **CI:** a new `go` job on `ubuntu-latest` (lint, test, `GOOS=windows` build), plus a
  `windows-latest` job (`go test ./internal/win/lease/... ./internal/win/procjob/...` against the
  real Win32 APIs). The existing shellcheck and ansible jobs keep running; the ansible
  syntax-check also runs with `-e fclaude_platform=vmware`.
- **Manual end-to-end on Windows** (checklist in the TODO): install, the CLI session, the VS Code
  session, two concurrent sessions on the same dir, killing VS Code then the reaper releasing the
  share, VM reboot, the firewall matrix, and uninstall keeping the disk.

## 8. Files touched

New:
- `go.mod`, `go.sum`, `.golangci.yml`
- `cmd/fclaude-win/main.go`, `internal/win/**` (see §3.3)
- `vm/assets.go` (go:embed)
- `vm/cloud-init/network-config-win.tmpl` (two NICs by MAC; was `network-config-vbox.tmpl`)
- `windows/README.md` (the brother-facing quick start), and `.plans/windows-TODO.md`

Modified (Linux behavior unchanged):
- `vm/ansible/site.yml`: tasks gated on `fclaude_platform` (`vmware`: open-vm-tools; any Windows platform: git
  config).
- `vm/cloud-init/user-data.tmpl`: an `-e fclaude_platform=__FCLAUDE_PLATFORM__` argument.
- `install.sh`: renders `__FCLAUDE_PLATFORM__` as `kvm` (one `RENDER_VARS` entry).
- `.github/workflows/ci.yml`: Go jobs, and the extra ansible syntax-check variant.
- `.github/workflows/release-windows.yml` (new): tagged release of `fclaude-setup.exe`.
- `.gitignore`: `/dist/`, and the capture logs.
- `README.md`, `CODEBASE.md`: a Windows section.

## 9. Trade-offs and known limitations (experimental)

- **Isolation.** It is still a full VM with its own kernel, so the main property holds. VMware's
  NAT exposes no host port (R6), and the host-only/NAT host addresses are covered by the firewall
  rules.
- **Proprietary dependency.** VMware Workstation is closed source, needs a Broadcom account to
  download, and its free licence is a vendor decision that could change. The `vmw` package is
  the only hypervisor-specific code (plus HGFS in `share`), so a later swap stays contained.
- **Performance.** HGFS is slower than NFS for many small files, and any hypervisor on the Hyper-V
  API is slower than native VT-x. That is acceptable for an agent workload; heavy `npm install`
  will be noticeably slower (measured in R2).
- **No symlinks on shares** (HGFS). `node_modules/.bin` needs a workaround (R2).
- **Protocol coupling (Option A).** An extension update could add a path field the rewriter
  doesn't know about. It degrades gracefully (that one link or diff fails), and the capture mode
  stays in the binary (`fclaude-capture.exe`) so new fixtures are cheap to record.
- **Line endings and stat noise** between Windows git and VM git are mitigated by the system
  gitconfig, but not eliminated. The recommendation for the brother: let one side (preferably
  Windows git in VS Code) own commits.
- **One share root** in v1, set in `config.json` (e.g. `C:\dev`; any drive). Adding a list of roots is straightforward later (the mapper becomes a
  list).
- **Disk grow needs a VM restart** (`vmware-vdiskmanager -x` works offline only).

## 10. Decisions (from user review, 2026-11-06)

1. **Option A** (path-rewriting wrapper) is chosen. The rewriter is dialed in with experiments on
   Windows (R4/R5). Remote-SSH (B) is only an emergency fallback.
2. **WSL2 is required on the target and stays enabled.** fclaude doesn't depend on it, which keeps
   it free for future use. As a result the hypervisor always runs on the Hyper-V API; R1 checked
   viability (→ decision 6).
3. **SHARE_ROOT is configurable** (`config.json`), asked at install, default `C:\dev`.
4. **VM defaults 8 GiB / 6 vCPU / 32 GiB** are fine.
5. **Prebuilt release:** `fclaude-setup.exe` on GitHub releases, a self-contained installer that
   sets up the VM, the firewall, the binaries and, optionally, the VS Code setting. (VMware
   Workstation itself is installed by the user first; see decision 6.)
6. **Hypervisor: VMware Workstation Pro** (2026-11-06, after R1). VirtualBox stalls under load
   on the Hyper-V API; VMware passed. The user installs VMware before setup.
