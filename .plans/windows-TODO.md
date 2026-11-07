# fclaude for Windows (experimental) — TODO

Checklist for [.plans/windows-PLAN.md](windows-PLAN.md).

Legend: `[ ]` todo · `[~]` in progress · `[x]` done. **(L)** = can be done on Linux, **(W)** = needs
the Windows machine. Each phase ends with a verification step.

---

## Phase 0 — Decisions (user)

- [x] Plan §10 decisions: Option A, WSL stays on, SHARE_ROOT in config (default `C:\dev`), 8G/6vCPU,
      prebuilt `fclaude-setup.exe` release that installs everything.
- [x] Branch: `feat/windows` (work stays off main until the Windows E2E passes).

## Phase 1 — (dropped) Linux capture

- [x] Dropped: path forms only show on Windows; capture moved to Phase 5 as a native pass-through
      (PLAN §6 R4).

## Phase 2 — Go scaffolding + CI (L) — done

- [x] `go.mod` (module `github.com/robymus/fclaude`, **Go 1.26**: latest x/sys requires it), `.golangci.yml`.
- [x] `vm/assets.go` + `vm/assets_test.go` (every embedded file present and non-empty).
- [x] `cmd/fclaude-win/main.go`: argv[0] dispatch (`fclaude` / `fclaude-vscode` / `fclaude-setup` /
      `fclaude-capture`), `--version` via ldflags, `--help`. The wrapper personalities take no flags
      of their own. **Not yet:** CLI flags and no-args-from-Explorer → setup.
- [x] CI: `go` job (gofmt, golangci-lint linux+windows, `go test -race`, Windows cross-build) and a
      `go-windows` job (`go test ./...` on windows-latest).
- [x] `.github/workflows/release-windows.yml` (tag `v*-win*`), written early; not exercised yet.
- [x] **Verify:** lint 0 issues on both GOOS; all tests pass; the `.exe` cross-compiles (PE32+ x86-64).
      CI on GitHub: **green on all jobs incl. windows-latest** (4ce09eb).

## Phase 3 — Pure-logic packages (L) — done

- [x] `internal/win/winpath` (added): Windows path semantics as pure strings — 100% coverage.
- [x] `internal/win/config`: JSON over defaults, unknown keys rejected, every error reported, no-clobber write.
- [x] `internal/win/pathmap`: `Mapper` (whole path, embedded in text, file URIs; dot-dir and root
      exclusions) + `Rewriter` (byte-preserving JSON scanner, content-field `Policy`, `Args`, `Pipe`);
      table tests + fuzz (about 5M execs clean). **Pending:** replays of the Windows capture fixtures (Phase 5).
- [x] `internal/win/lease`: lease files, flock / LockFileEx, real liveness (Linux /proc, Windows
      OpenProcess+GetProcessTimes+GetExitCodeProcess), reap, sweep, list; PID-reuse + 40-way concurrency tests.
- [x] `internal/win/vbox`: Runner, Client ops, `--machinereadable` parser, `DefineCommands`,
      `ImportDiskCommands`, `Locate`. **Fixtures are provisional** (PLAN §0.3 #1–2).
- [x] `internal/win/seed`: render (fails on leftover placeholders) + Rock Ridge ISO; checked
      independently with `isoinfo`.
- [x] `internal/win/mcphelper` (port + parity tests), `internal/win/capture` (R4 tool),
      `internal/win/procjob` (Job Object / Pdeathsig; kill-parent test).
- [x] **Verify:** coverage of every package ≥ 82%; lint clean.

## Phase 4 — VM provisioning changes (L) — done

- [x] `vm/cloud-init/user-data.tmpl`: `-e fclaude_platform=__FCLAUDE_PLATFORM__`.
- [x] `lib/constants.sh` `FCLAUDE_PLATFORM="kvm"` + `install.sh` `RENDER_VARS`.
- [x] `vm/ansible/site.yml`: virtualbox-gated tasks (modules-load vboxsf; system git config via
      `git config --system`, so no new collection dependency).
- [x] `vm/cloud-init/network-config-vbox.tmpl`: `nat0` (dhcp) + `host0` (static, no RA), MTU 1400, by MAC.
- [x] CI: Ansible syntax-check also with `-e fclaude_platform=virtualbox`.
- [x] **Verify:** **KVM end-to-end boot with the Go-built seed** (PLAN §0.6). The Linux path only
      gains the extra `fclaude_platform=kvm` arg (intended, not byte-identical); shellcheck clean.

## Phase 5 — Windows environment + spikes (W)

- [x] Check prereqs (PLAN §0.5): all present (VirtualBox 7.2.22, OpenSSH 9.5, Git for Windows 2.56,
      VS Code + extension 2.1.318, Dev Mode on, `C:\dev\base-dev-skills`, 31 GiB RAM). WSL fixes:
      GOPATH was `~/go` = GOROOT (`go env -w GOPATH=~/gopath`); `gcc` + `libc6-dev` installed for
      `-race`. The full gate is green, and **all 11 packages' tests pass as native Windows exes via
      interop** after a test fix (re-exec `os.Executable()`, not the relative `os.Args[0]`).
- [x] Confirm PLAN §0.3 assumptions #1–#3: flags OK, fixtures re-captured, host-only changes need
      admin (UAC; installer default adapter is already correct). **Found the NAT localhost-reachable persistence bug** (PLAN §0.3), so the fix moves to R6.
- [x] **R4 capture:** done; **gate: Option A** (PLAN §0.7). `.cmd` wrapper → `spawn EINVAL`.
      Fixtures in `pathmap/testdata/windows/` with `replay_test.go` (20/27 exact round trips, 6
      drive-case only, 1 `partial_json` fragment left for R5). Fix: Mapper keeps the root's drive
      case + `RootSpelling(root, cwd)`. Queued for Phase 9: `auth status` skips the mount;
      forward `CLAUDE_*` env; `partial_json` decision.
- [x] **R1 — VirtualBox FAILED** (2026-11-06). VirtualBox 7.2.22 on the
      Hyper-V API (NEM "snail mode", WSL2 on) is not viable on this host (Win 11 Home, Ryzen 7840HS):
      under full 6-vCPU load the guest stalls for seconds to minutes (RCU stalls, ssh unreachable;
      a 90 s stress run takes 160 s+). Idle use and provisioning (396 s) work; memory `--verify`
      always passed (no corruption); no AVX-512 is exposed. Load trials (`hack/r1/r1-load.sh`, 15 ssh
      polls under load): feature off + kvm paravirt 15/15 missed; feature off + none 6/15; feature
      off + hyperv 0/15 once (not reproduced); **Windows Hypervisor Platform on** + default 6/15 (20
      warnings), + hyperv 7/15. Guest clocksource fell back to `acpi_pm`. Plus one intermittent
      first-boot initramfs livelock.
      Untested knobs: fewer vCPUs with hyperv paravirt, x2APIC off, guest clocksource override.
- [x] **R1-VMware spike:** VMware Workstation Pro 26.0.1 passes the identical load trial (0/15
      misses, 0 warnings). Live HGFS shares need no admin and self-heal on re-add; the NAT reaches
      nothing on the host. Needs MTU 1500. No symlinks. Details in PLAN §0.7. **Decision (user,
      2026-11-06): switch to VMware** (PLAN §10.6). PLAN §3.1/3.2/3.7/3.8/§4 re-planned.
- [x] **R6** VirtualBox: with `localhostReachable=1` the host's loopback services and 135/445 are
      reachable via `10.0.2.2` (moot after the switch). **VMware: the NAT gateway reaches nothing.**
- [x] **R3** (VMware): remove-while-mounted empties the mount, re-add heals it; order guest
      `umount` → `removeSharedFolder`. (Loop-test ×50 in Phase 6 Verify.)
- [x] **R2** HGFS matrix (2026-11-06, PLAN §0.7): **passes, so HGFS stays (no SMB).** git ok (a
      Windows-git repo shows 0 dirty lines in VM git); `npm ci` fails on symlinks, works with
      `--no-bin-links` (3.4× slower) and at native speed with a VM-local `node_modules` bind mount;
      small files ~40× slower per op (1,322-file scan 0.44 s); 1 GB at 111/430 MB/s write/read;
      case rename, sqlite WAL + rollback journal, create-in-VM → read-on-Windows all ok. Files held
      open by Windows with sharing allowed: all ops work; held exclusively: EACCES (and `mv` says
      ENOENT). **Follow-up (Phase 6 share/Phase 8):** VM-local `node_modules` bind mount.
- [x] **R8** Claude OAuth login over Windows `ssh.exe -t` in Windows Terminal (VMware VM): URL copy, code paste, rendering, resize, Ctrl+C all fine (user, 2026-11-06).
- [x] **Verify:** PLAN updated with results (§0.7); the flipped hypervisor decision is in §3.1/§10.6.

## Phase 6 — Windows-API packages (W, CI on windows-latest)

- [x] `lease` real liveness + `LockFileEx` lock, and `procjob` Job Object: implemented, and tests
      pass on windows-latest CI. (Optionally re-run locally via interop.)
- [x] **`internal/win/vmw`** (done; `vbox` deleted; verified against the real install): `Locate` (registry
      `HKLM\SOFTWARE\VMware, Inc.\VMware Workstation` → `InstallPath`/`ProductVersion`, ≥ 17.5);
      `Runner` over `vmrun -T ws` (start nogui, stop soft/hard, list → state, add/remove/enable
      shared folders); `.vmx` writer (golden-file test; CRLF; hardening keys; shares section;
      `ClearShares` for the cold-boot sweep; seed CD-ROM detach); VMnet discovery from
      `VMnetLib\VMnetConfig\vmnet{1,8}\IPSubnetAddress` (VM IP = subnet `.10`); `vdiskmanager`
      convert/grow commands. Static VMware MACs in `00:50:56:00:00:00–3F:FF:FF`.
- [x] Provisioning for `fclaude_platform=vmware` (done; fresh-VM verify deferred to the first `vmw`-built VM): site.yml (open-vm-tools task; vboxsf task
      dropped; git config for any non-`kvm` platform); rename `network-config-vbox.tmpl` →
      `network-config-win.tmpl`; seed MTU 1500 for VMware; `seed` platform param instead of the
      hard-coded `virtualbox`; CI ansible syntax-check with `vmware`. **Verify** with a fresh VM.
- [x] `internal/win/vmssh`: x/crypto/ssh client, pinned host key (FetchHostKey authenticates first; strict afterwards), bounded dial/handshake, keepalive, stdio Run with cancel. Verified against the real VM.
- [x] `internal/win/share` HGFS impl: fast path (one ssh check: `vmware-hgfsclient` lists the share +
      mount source + readable → **66 ms warm**); full path enable → add → guest `vmhgfs-fuse -o fsname=`
      (without fsname the mount source is just "vmhgfs-fuse", which broke every source check);
      release = guest `umount` (lazy fallback) → `removeSharedFolder`; self-heal; rollback;
      optional VM-local `node_modules` bind. Cold-boot `ClearShares` lives in `vmw` (caller: Phase 8).
- [x] **Verify:** manual share/mount/release loop ×50 clean on the real VM (3.4 s/cycle cold), plus
      hidden-removal self-heal and node_modules; all 13 packages pass as native Windows exes.
      **Pending:** windows-latest CI on the next push.

## Phase 7 — Installer / uninstaller (W)

- [x] Interactive setup wizard: steps 0–11 of PLAN §4 (`install.Setup`, commit ab4ea87). The login
      step is the printed hint "run `fclaude` in a project; Claude asks you to log in" (no launch).
- [x] Upgrade path: newer setup over existing install → binaries only, never VM/disk.
- [x] `install-elevated` + `uninstall` (`fclaude uninstall`: typed `remove fclaude`, VM stopped,
      VM dir removed except the disk, firewall group via UAC `uninstall-elevated`, PATH, VS Code
      setting (JSONC delete, backup), binaries deleted after exit; separate `DELETE DISK`). **Kept
      with the disk: `ssh\` keys, `vm.json` (MACs), `config.json`** — the disk only accepts its
      own keys and its netplan matches its MACs (cloud-init does not re-run: same instance-id),
      so restore = same machine with those kept (PLAN §4). `fclaude grow-disk <GB>` (stop,
      `vdiskmanager -x`, start, in-VM `growpart` + `resize2fs`).
- [x] Tests: disk guard, config-not-overwritten, uninstall never touches disk without the phrase
      (wrong phrase changes nothing; keep-disk → reinstall restores without download/convert),
      JSONC set + delete (comments/trailing commas preserved, backup written).
- [x] **Verify (user's full spin, 2026-11-07, passed):** fresh install from zero; re-run install
      (idempotent); uninstall keeps disk; reinstall onto kept disk (restore question → yes).

## Phase 8 — CLI (W)

- [x] `fclaude` session (lease → share → `ssh.exe -t` in Job → release, also on console close),
      `--dir`, `--no-mount`, `--mount <dir>…`, `--status` (VM state + resources + leases + Claude
      version + last apt; reaps ended sessions), `--cleanup`, `--update`, `--maintenance`,
      `--shutdown` (confirm), staleness warning port. Cold boot: `ClearShares` + lease `Sweep`
      under the store-wide `vm-boot` lock. (`internal/win/cli`)
- [x] **Verify (user, 2026-11-07, passed):** CLI sessions and flags by hand. Found and fixed:
      `sudo` waited 10–15 s on DNS for the hostname (`manage_etc_hosts: localhost`).

## Phase 9 — VS Code wrapper (W)

- [x] Replay the Phase 5 R4 fixtures through the full wrapper (VM-side claude) → **gate: Option A
      (passed in the user's VS Code spin, 2026-11-07)**.
- [ ] **R7** cold-boot timeout behaviour (stderr progress / logon autostart if needed).
- [x] Option A: `fclaude-vscode.exe` (drop argv[1], `--add-dir` folders shared too (outside the
      root: dropped with a warning), MCP-helper + `auth` bypass, Mapper from `RootSpelling(root,
      cwd)`, `CLAUDE_CODE_*`/`CLAUDE_AGENT_SDK_*` env forwarding (path values and `SSE_PORT`
      excluded), lease/share, in-process ssh with `Rewriter` both ways, stderr error messages,
      capture mode kept). `partial_json` deltas are not rewritten (R5 decides).
- [ ] **R5** end-to-end scenario list (chat, @-mention, selection, diff accept/reject, file links,
      second workspace folder (no `/add-dir` in the extension), plan mode, interrupt, close tab).
- [ ] (Only if B) Remote-SSH docs + `fclaude --mount` hold workflow + `.ssh/config` entry generator.
- [x] **Verify (user, 2026-11-07):** VS Code works as expected end to end.

## Phase 10 — E2E, docs, wrap-up (W)

- [x] Firewall matrix from the VM (2026-11-07): 135/139/445/3389/1000 on the VMnet1 and VMnet8
      host addresses blocked; ≥1024 test listener reachable; NAT gateway reaches nothing; DNS +
      internet OK. Found: via vmnat.exe the host's *other* addresses are reachable as loopback
      (no firewall rule applies). LAN devices fixed with an outbound vmnat rule; the
      own-address gap is accepted and documented (PLAN §3.7, §9, windows/README.md).
- [ ] VM reboot under live sessions → self-heal; Windows reboot → cold-boot lease sweep.
- [ ] `windows/README.md` (brother quick start: **VMware Workstation download via Broadcom**, install, login, VS Code setting, known limitations),
      README + CODEBASE Windows sections (CODEBASE section and README pointer exist; keep them current).
- [x] `.github/workflows/release-windows.yml` written (Phase 2); first exercised by the tag below.
- [ ] Cut `v0.1.0-win-experimental`; download it on Windows and install from the release asset.
- [ ] **Verify (done by the user's brother):** brother-style dry run on a clean user profile: download release → double-click
      setup → VS Code session works, following only `windows/README.md` (incl. SmartScreen note).
