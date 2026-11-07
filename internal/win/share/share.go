// Package share makes a Windows workspace visible inside the VM as a VMware
// shared folder (HGFS) and takes it away again.
//
// Setup and Teardown are the lease.Store Acquire/Release hooks: they run
// under the workspace lock. Like the Linux host's _do_mount, Setup trusts the
// mount table, not the lease count: it re-establishes whatever layer is
// missing (host share, guest mount) and replaces a guest mount whose source
// is wrong, so a VM reboot or a removed share under a live lease heals on the
// next session.
package share

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"strings"

	"github.com/robymus/fclaude/internal/win/lease"
	"github.com/robymus/fclaude/internal/win/winpath"
)

// Host is the hypervisor side (vmw.Client).
type Host interface {
	EnableSharedFolders(ctx context.Context, vmx string) error
	AddShare(ctx context.Context, vmx, name, hostDir string) error
	RemoveShare(ctx context.Context, vmx, name string) error
}

// Guest runs a command in the VM as the fclaude user (vmssh.Client).
type Guest interface {
	Output(ctx context.Context, cmd string) (string, error)
}

// Manager shares workspaces with one VM.
type Manager struct {
	VMX   string
	Host  Host
	Guest Guest
	// LocalNodeModules bind-mounts a VM-local directory over the workspace's
	// top-level node_modules when it has a package.json: native-speed npm,
	// and Linux-only binaries never land in the Windows tree (PLAN §0.7 R2).
	LocalNodeModules bool

	nmStore string // tests only; defaults to nodeModulesStore
}

// nodeModulesStore holds the VM-local node_modules, one directory per workspace key.
const nodeModulesStore = "/var/lib/fclaude/node_modules"

// Workspace validates dir as a workspace under the Windows share root and
// describes it: lease key, share name, and the VM path under vmRoot.
func Workspace(root, vmRoot, dir string) (lease.Workspace, error) {
	rel, err := winpath.WorkspaceRel(root, dir)
	if err != nil {
		return lease.Workspace{}, err
	}
	clean := winpath.Clean(dir)
	key := lease.Key(clean)
	return lease.Workspace{
		Key:     key,
		WinPath: clean,
		VMPath:  path.Join(vmRoot, strings.ReplaceAll(rel, `\`, "/")),
		Share:   "fc-" + key,
	}, nil
}

// Setup makes ws usable in the VM. liveOthers is the number of other live
// lease holders (from lease.Store.Acquire): when Setup fails and nobody else
// holds the workspace, the share it added is removed again.
func (m Manager) Setup(ctx context.Context, ws lease.Workspace, liveOthers int) (err error) {
	if st, statErr := os.Stat(ws.WinPath); statErr != nil || !st.IsDir() {
		return fmt.Errorf("share: workspace %s is not a directory", ws.WinPath)
	}
	// Fast path: already fully set up (the common case for the extension's
	// several processes per tab). One ssh round trip instead of ~1 s per vmrun.
	if _, err := m.Guest.Output(ctx, m.checkScript(ws)); err == nil {
		return nil
	}
	if err := m.Host.EnableSharedFolders(ctx, m.VMX); err != nil {
		return err
	}
	if err := m.Host.AddShare(ctx, m.VMX, ws.Share, ws.WinPath); err != nil {
		return err
	}
	defer func() {
		if err != nil && liveOthers == 0 {
			_ = m.Host.RemoveShare(ctx, m.VMX, ws.Share)
		}
	}()
	if _, err := m.Guest.Output(ctx, m.mountScript(ws)); err != nil {
		return fmt.Errorf("share: mounting %s in the VM: %w", ws.WinPath, err)
	}
	return nil
}

// Teardown unmounts ws in the VM, then removes the share. A guest that cannot
// be reached (VM off) does not stop the share from being removed.
func (m Manager) Teardown(ctx context.Context, ws lease.Workspace) error {
	_, gerr := m.Guest.Output(ctx, m.unmountScript(ws))
	herr := m.Host.RemoveShare(ctx, m.VMX, ws.Share)
	if gerr != nil {
		gerr = fmt.Errorf("share: unmounting %s in the VM: %w", ws.VMPath, gerr)
	}
	return errors.Join(gerr, herr)
}

// checkScript exits 0 only if ws is completely set up: offered by the host,
// mounted from the right share, readable, and (with LocalNodeModules)
// node_modules bound.
func (m Manager) checkScript(ws lease.Workspace) string {
	var b strings.Builder
	fmt.Fprintf(&b, "d=%s\nsrc=%s\nshare=%s\n", q(ws.VMPath), q(".host:/"+ws.Share), q(ws.Share))
	// A share removed on the host leaves an empty but healthy-looking mount;
	// vmware-hgfsclient lists what the host offers right now (~40 ms).
	b.WriteString(`vmware-hgfsclient | grep -qxF "$share" || exit 1
mountpoint -q "$d" && [ "$(findmnt -no SOURCE --mountpoint "$d" | tail -n1)" = "$src" ] && ls "$d" >/dev/null 2>&1 || exit 1
`)
	if m.LocalNodeModules {
		b.WriteString(`[ ! -f "$d/package.json" ] || mountpoint -q "$d/node_modules" || exit 1
`)
	}
	return b.String()
}

// mountScript is idempotent: an existing mount of the right share is kept
// (and re-verified), a mount of anything else at that path is replaced.
// vmhgfs-fuse reports its source as plain "vmhgfs-fuse"; fsname= makes the
// mount table name the share, which is what the source checks compare.
func (m Manager) mountScript(ws lease.Workspace) string {
	d, src := q(ws.VMPath), q(".host:/"+ws.Share)
	var b strings.Builder
	fmt.Fprintf(&b, "set -e\nd=%s\nsrc=%s\n", d, src)
	b.WriteString(`if mountpoint -q "$d"; then
  if [ "$(findmnt -no SOURCE --mountpoint "$d" | tail -n1)" != "$src" ] || ! ls "$d" >/dev/null 2>&1; then
    sudo umount "$d" 2>/dev/null || sudo umount -l "$d"
  fi
fi
if ! mountpoint -q "$d"; then
  mkdir -p "$d"
  sudo vmhgfs-fuse "$src" "$d" -o allow_other,uid="$(id -u)",gid="$(id -g)",fsname="$src"
fi
ls "$d" >/dev/null
`)
	if m.LocalNodeModules {
		store := m.nmStore
		if store == "" {
			store = nodeModulesStore
		}
		fmt.Fprintf(&b, "nm=%s\n", q(path.Join(store, ws.Key)))
		b.WriteString(`if [ -f "$d/package.json" ] && ! mountpoint -q "$d/node_modules"; then
  sudo install -d -o "$(id -u)" -g "$(id -g)" "$nm"
  mkdir -p "$d/node_modules"
  sudo mount --bind "$nm" "$d/node_modules"
fi
`)
	}
	return b.String()
}

func (m Manager) unmountScript(ws lease.Workspace) string {
	var b strings.Builder
	fmt.Fprintf(&b, "d=%s\n", q(ws.VMPath))
	b.WriteString(`for p in "$d/node_modules" "$d"; do
  if mountpoint -q "$p"; then sudo umount "$p" 2>/dev/null || sudo umount -l "$p"; fi
done
rmdir "$d" 2>/dev/null || true
`)
	return b.String()
}

// q single-quotes s for the guest shell.
func q(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
