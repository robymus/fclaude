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
- [ ] **R4 capture (first, no VM needed):** cross-build `fclaude-capture.exe` (already implemented)
      into `%USERPROFILE%\.fclaude\bin`, set it as `claudeCode.claudeProcessWrapper`, run the R4
      scenario list in VS Code (logs land in `%USERPROFILE%\.fclaude\captures\`);
      sanitize → `internal/win/pathmap/testdata/windows/`; validate the Phase 3 `pathmap` tables
      against it and fix any path form the tables missed.
- [ ] **R1** VirtualBox on Hyper-V (WSL2 on, running concurrently): boot, 30-min stability, perf →
      record in PLAN. **Gate:** if not viable, stop and raise the Hyper-V-backend alternative with user.
- [ ] **R6** NAT `10.0.2.2` reachability matrix → record; pick §3.7 fallback if needed. Run it
      with `localhostReachable` both 0 and 1 (see the PLAN §0.3 bug), and test the extradata fix.
- [ ] **R2** vboxsf test matrix (git, npm symlinks, locked-file rename, VS Code open-after-create,
      big file, case rename, sqlite fsync, transient-share symlink extradata) → **gate: vboxsf vs SMB**.
- [ ] **R3** transient share add/remove-while-mounted behaviour → fix the release ordering.
- [ ] **R8** Claude OAuth login over `ssh.exe -t`.
- [ ] **Verify:** PLAN updated with results; any flipped decision noted in §3 with the reason.

## Phase 6 — Windows-API packages (W, CI on windows-latest)

- [x] `lease` real liveness + `LockFileEx` lock, and `procjob` Job Object: implemented, and tests
      pass on windows-latest CI. (Optionally re-run locally via interop.)
- [ ] `internal/win/vmssh`: x/crypto/ssh client, pinned host key, keepalive, stdio session.
- [ ] `internal/win/share` vboxsf impl (or SMB if R2 failed): share → VM mount → verify; release
      order from R3; self-heal; rollback.
- [ ] **Verify:** windows-latest CI green; manual share/mount/release loop ×50 clean.

## Phase 7 — Installer / uninstaller (W)

- [ ] Interactive setup wizard: steps 0–12 of PLAN §4 (share-root prompt, preflight + winget
      VirtualBox offer, dirs, guarded disk w/ SHA256 check, keys + ACL, seed, host-only net, VM define,
      single UAC `install-elevated`, binaries + PATH, config/vm.json, boot+wait w/ progress + host-key
      pin, optional VS Code settings.json edit + login launch).
- [ ] Upgrade path: newer setup over existing install → binaries only, never VM/disk.
- [ ] NAT hardening that survives VBoxSVC restarts (fix chosen in R6). Every `startvm` path
      re-asserts the setting and reads back `localhostReachable="0"`, or refuses to start.
      `vbox.State()` retries on `E_ACCESSDENIED` "object is not ready".
- [ ] `install-elevated` (firewall, VirtualBox via winget) + `uninstall` (typed confirm, detach disk before unregister,
      separate `DELETE DISK`) + `grow-disk` (offline resize + in-VM grow).
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
- [ ] Option A: `fclaude-vscode.exe` (drop argv[1], argv rewrite, MCP-helper bypass, lease/share,
      in-process ssh with bidirectional `RewriteJSONLine`, stderr error messages, capture mode kept).
- [ ] **R5** end-to-end scenario list (chat, @-mention, selection, diff accept/reject, file links,
      `/add-dir`, plan mode, interrupt, close tab).
- [ ] (Only if B) Remote-SSH docs + `fclaude --mount` hold workflow + `.ssh/config` entry generator.
- [ ] **Verify:** R5 all pass (or gaps listed in README); close VS Code → share released by next run.

## Phase 10 — E2E, docs, wrap-up (W)

- [ ] Firewall matrix from the VM: 22/135/445/3389 on 192.168.56.1 blocked; 8080 test listener
      reachable; internet OK.
- [ ] VM reboot under live sessions → self-heal; Windows reboot → cold-boot lease sweep.
- [ ] `windows/README.md` (brother quick start: install, login, VS Code setting, known limitations),
      README + CODEBASE Windows sections (CODEBASE section and README pointer exist; keep them current).
- [x] `.github/workflows/release-windows.yml` written (Phase 2); first exercised by the tag below.
- [ ] Cut `v0.1.0-win-experimental`; download it on Windows and install from the release asset.
- [ ] **Verify:** brother-style dry run on a clean user profile: download release → double-click
      setup → VS Code session works, following only `windows/README.md` (incl. SmartScreen note).
