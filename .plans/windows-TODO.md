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
- [ ] **R2** HGFS test matrix (PLAN §6): git clone/status/commit on a Windows-git repo; npm ci with
      `--no-bin-links` vs a VM-local `node_modules`; rename while VS Code holds the file;
      create-in-VM → open in VS Code; 1 GB file; case-only rename; sqlite fsync; small-file perf
      (`git status` on a big tree) → **gate: HGFS vs SMB**. Light load; say so before running.
- [x] **R8** Claude OAuth login over Windows `ssh.exe -t` in Windows Terminal (VMware VM): URL copy, code paste, rendering, resize, Ctrl+C all fine (user, 2026-11-06).
- [ ] **Verify:** PLAN updated with results; any flipped decision noted in §3 with the reason.

## Phase 6 — Windows-API packages (W, CI on windows-latest)

- [x] `lease` real liveness + `LockFileEx` lock, and `procjob` Job Object: implemented, and tests
      pass on windows-latest CI. (Optionally re-run locally via interop.)
- [ ] **`internal/win/vmw`** (replaces `vbox`, which is then deleted): `Locate` (registry
      `HKLM\SOFTWARE\VMware, Inc.\VMware Workstation` → `InstallPath`/`ProductVersion`, ≥ 17.5);
      `Runner` over `vmrun -T ws` (start nogui, stop soft/hard, list → state, add/remove/enable
      shared folders); `.vmx` writer (golden-file test; CRLF; hardening keys; shares section;
      `ClearShares` for the cold-boot sweep; seed CD-ROM detach); VMnet discovery from
      `VMnetLib\VMnetConfig\vmnet{1,8}\IPSubnetAddress` (VM IP = subnet `.10`); `vdiskmanager`
      convert/grow commands. Static VMware MACs in `00:50:56:00:00:00–3F:FF:FF`.
- [ ] Provisioning for `fclaude_platform=vmware`: site.yml (open-vm-tools task; vboxsf task
      dropped; git config for any non-`kvm` platform); rename `network-config-vbox.tmpl` →
      `network-config-win.tmpl`; seed MTU 1500 for VMware; `seed` platform param instead of the
      hard-coded `virtualbox`; CI ansible syntax-check with `vmware`. **Verify** with a fresh VM.
- [ ] `internal/win/vmssh`: x/crypto/ssh client, pinned host key, keepalive, stdio session.
- [ ] `internal/win/share` HGFS impl (or SMB if R2 fails): `addSharedFolder` → guest
      `vmhgfs-fuse` mount → verify; release = guest `umount` (lazy fallback) → `removeSharedFolder`;
      self-heal (re-add heals a live mount); rollback; cold-boot `ClearShares`.
- [ ] **Verify:** windows-latest CI green; manual share/mount/release loop ×50 clean.

## Phase 7 — Installer / uninstaller (W)

- [ ] Interactive setup wizard: steps 0–11 of PLAN §4 (share-root prompt, preflight incl. VMware
      Workstation ≥ 17.5 + VMnet subnets, dirs, guarded disk w/ SHA256 check + vdiskmanager convert,
      keys + ACL, seed, `.vmx`, single UAC `install-elevated`, binaries + PATH, config/vm.json,
      boot+wait w/ progress + host-key pin + seed detach, optional VS Code settings.json edit +
      login launch).
- [ ] Upgrade path: newer setup over existing install → binaries only, never VM/disk.
- [ ] `install-elevated` (firewall rules scoped to both VMnet subnets) + `uninstall` (typed
      confirm, `vmrun stop`, remove the VM dir except the disk, separate `DELETE DISK`) +
      `grow-disk` (stop, `vdiskmanager -x`, start, in-VM grow).
- [ ] Tests: disk guard, config-not-overwritten, uninstall never touches disk without the phrase,
      JSONC settings.json edit (comments/trailing commas preserved, backup written).
- [ ] **Verify:** fresh install from zero; re-run install (idempotent); restore-from-existing-disk
      path; uninstall keeps disk; reinstall onto kept disk.

## Phase 8 — CLI (W)

- [ ] `fclaude` session (lease → share → `ssh.exe -t` in Job → release), `--dir`, `--no-mount`,
      `--mount <dir>…`, `--status` (VM state + leases + Claude version + last apt), `--cleanup`,
      `--update`, `--maintenance`, `--shutdown` (confirm), staleness warning port.
- [ ] **Verify:** each flag by hand; two concurrent CLI sessions on one dir; kill the terminal →
      next `--status` reaps.

## Phase 9 — VS Code wrapper (W)

- [ ] Replay the Phase 5 R4 fixtures through the full wrapper (VM-side claude) → **gate: Option A vs B**.
- [ ] **R7** cold-boot timeout behaviour (stderr progress / logon autostart if needed).
- [ ] Option A: `fclaude-vscode.exe` (drop argv[1], argv rewrite incl. `--add-dir=`, MCP-helper + `auth status` bypass, Mapper from `RootSpelling(root, cwd)`, allowlisted `CLAUDE_*` env forwarding, lease/share,
      in-process ssh with bidirectional `RewriteJSONLine`, stderr error messages, capture mode kept).
- [ ] **R5** end-to-end scenario list (chat, @-mention, selection, diff accept/reject, file links,
      second workspace folder (no `/add-dir` in the extension), plan mode, interrupt, close tab).
- [ ] (Only if B) Remote-SSH docs + `fclaude --mount` hold workflow + `.ssh/config` entry generator.
- [ ] **Verify:** R5 all pass (or gaps listed in README); close VS Code → share released by next run.

## Phase 10 — E2E, docs, wrap-up (W)

- [ ] Firewall matrix from the VM: 135/139/445/3389 on the VMnet1 and VMnet8 host addresses
      blocked; a ≥1024 test listener reachable; NAT gateway still reaches nothing; internet OK.
- [ ] VM reboot under live sessions → self-heal; Windows reboot → cold-boot lease sweep.
- [ ] `windows/README.md` (brother quick start: **VMware Workstation download via Broadcom**, install, login, VS Code setting, known limitations),
      README + CODEBASE Windows sections (CODEBASE section and README pointer exist; keep them current).
- [x] `.github/workflows/release-windows.yml` written (Phase 2); first exercised by the tag below.
- [ ] Cut `v0.1.0-win-experimental`; download it on Windows and install from the release asset.
- [ ] **Verify:** brother-style dry run on a clean user profile: download release → double-click
      setup → VS Code session works, following only `windows/README.md` (incl. SmartScreen note).
