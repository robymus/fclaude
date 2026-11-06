// Package lease implements liveness-based workspace reference counting.
//
// On Linux the wrappers decrement a counter from an EXIT trap. On Windows the
// VS Code extension ends the wrapper with TerminateProcess, which runs no
// cleanup at all, so a counter would leak on every editor close. Instead each
// session records a lease naming its own process (PID + start time, so a
// recycled PID is not mistaken for the original). A lease whose process is
// gone is dead, and is reaped — with the workspace's share torn down when no
// live lease remains — by whichever fclaude invocation runs next.
//
// Layout under Store.Dir:
//
//	<key>.lock              per-workspace lock file (never removed)
//	<key>/workspace.json    what is shared (so a reaper can tear it down)
//	<key>/<pid>-<start>     one empty file per lease holder
package lease

import (
	"crypto/sha1" //nolint:gosec // non-cryptographic: a stable short directory name
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Holder identifies a process holding a lease.
type Holder struct {
	PID int
	// Started is an OS-specific process start-time token; together with PID it
	// uniquely identifies a process across PID reuse.
	Started uint64
}

func (h Holder) fileName() string { return fmt.Sprintf("%d-%d", h.PID, h.Started) }

func parseHolder(name string) (Holder, bool) {
	p, s, ok := strings.Cut(name, "-")
	if !ok {
		return Holder{}, false
	}
	pid, err1 := strconv.Atoi(p)
	started, err2 := strconv.ParseUint(s, 10, 64)
	if err1 != nil || err2 != nil || pid <= 0 {
		return Holder{}, false
	}
	return Holder{PID: pid, Started: started}, true
}

// Self returns the Holder for the current process.
func Self() (Holder, error) { return holderOf(os.Getpid()) }

// Workspace describes one shared workspace.
type Workspace struct {
	Key     string `json:"key"`
	WinPath string `json:"win_path"`
	VMPath  string `json:"vm_path"`
	Share   string `json:"share"` // VirtualBox shared-folder name
}

// Key returns the stable key for a cleaned Windows workspace path. NTFS is
// case-insensitive, so the key is too.
func Key(cleanWinPath string) string {
	sum := sha1.Sum([]byte(strings.ToLower(cleanWinPath))) //nolint:gosec // see import
	return hex.EncodeToString(sum[:8])
}

// Store manages leases in a directory.
type Store struct {
	Dir string
	// Alive reports whether a holder's process still runs. Defaults to the OS check.
	Alive func(Holder) bool
}

// NewStore returns a Store using the OS liveness check.
func NewStore(dir string) *Store { return &Store{Dir: dir, Alive: alive} }

func (s *Store) wsDir(key string) string { return filepath.Join(s.Dir, key) }

// withLock runs fn while holding the workspace's exclusive lock.
func (s *Store) withLock(key string, fn func() error) error {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	unlock, err := lockFile(filepath.Join(s.Dir, key+".lock"))
	if err != nil {
		return fmt.Errorf("lock workspace %s: %w", key, err)
	}
	defer unlock()
	return fn()
}

// reapLocked removes dead holder files and returns the live ones. Lock held.
func (s *Store) reapLocked(key string) ([]Holder, error) {
	entries, err := os.ReadDir(s.wsDir(key))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var live []Holder
	for _, e := range entries {
		h, ok := parseHolder(e.Name())
		if !ok {
			continue
		}
		if s.Alive(h) {
			live = append(live, h)
			continue
		}
		if err := os.Remove(filepath.Join(s.wsDir(key), e.Name())); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	return live, nil
}

// Acquire records h as a holder of ws. setup runs under the lock with the
// number of OTHER live holders; it must make the share usable (and should
// verify the real mount state rather than trust that count — a live lease
// does not prove the VM still has the mount). If setup fails, h's lease is
// rolled back.
func (s *Store) Acquire(ws Workspace, h Holder, setup func(liveOthers int) error) error {
	return s.withLock(ws.Key, func() error {
		live, err := s.reapLocked(ws.Key)
		if err != nil {
			return err
		}
		others := 0
		for _, l := range live {
			if l != h {
				others++
			}
		}
		if err := os.MkdirAll(s.wsDir(ws.Key), 0o700); err != nil {
			return err
		}
		if err := writeJSON(filepath.Join(s.wsDir(ws.Key), "workspace.json"), ws); err != nil {
			return err
		}
		holderPath := filepath.Join(s.wsDir(ws.Key), h.fileName())
		if err := os.WriteFile(holderPath, nil, 0o600); err != nil {
			return err
		}
		if err := setup(others); err != nil {
			_ = os.Remove(holderPath)
			if others == 0 {
				_ = os.RemoveAll(s.wsDir(ws.Key))
			}
			return err
		}
		return nil
	})
}

// Release drops h's lease on the workspace key. If no live holder remains,
// teardown runs under the lock; on success the workspace record is removed.
func (s *Store) Release(key string, h Holder, teardown func(Workspace) error) error {
	return s.withLock(key, func() error {
		err := os.Remove(filepath.Join(s.wsDir(key), h.fileName()))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return s.teardownIfIdleLocked(key, teardown)
	})
}

func (s *Store) teardownIfIdleLocked(key string, teardown func(Workspace) error) error {
	live, err := s.reapLocked(key)
	if err != nil || len(live) > 0 {
		return err
	}
	ws, err := readWorkspace(s.wsDir(key))
	if errors.Is(err, os.ErrNotExist) {
		return os.RemoveAll(s.wsDir(key))
	}
	if err != nil {
		return err
	}
	if err := teardown(ws); err != nil {
		return fmt.Errorf("release %s: %w", ws.WinPath, err)
	}
	return os.RemoveAll(s.wsDir(key))
}

// Reap tears down every workspace that has no live holder left and returns
// the workspaces released. Errors on one workspace do not stop the others.
func (s *Store) Reap(teardown func(Workspace) error) ([]Workspace, error) {
	keys, err := s.keys()
	if err != nil {
		return nil, err
	}
	var released []Workspace
	var errs []error
	for _, key := range keys {
		err := s.withLock(key, func() error {
			live, err := s.reapLocked(key)
			if err != nil || len(live) > 0 {
				return err
			}
			ws, rerr := readWorkspace(s.wsDir(key))
			if err := s.teardownIfIdleLocked(key, teardown); err != nil {
				return err
			}
			if rerr == nil {
				released = append(released, ws)
			}
			return nil
		})
		if err != nil {
			errs = append(errs, err)
		}
	}
	return released, errors.Join(errs...)
}

// Sweep forgets every lease without tearing anything down. Only valid right
// after a cold VM boot: the mounts the leases describe lived inside the VM
// and are gone, and transient shared folders do not survive a VM power-off.
func (s *Store) Sweep() (int, error) {
	keys, err := s.keys()
	if err != nil {
		return 0, err
	}
	n := 0
	var errs []error
	for _, key := range keys {
		errs = append(errs, s.withLock(key, func() error {
			n++
			return os.RemoveAll(s.wsDir(key))
		}))
	}
	return n, errors.Join(errs...)
}

// Status is the state of one workspace, for `fclaude --status`.
type Status struct {
	Workspace Workspace
	Live      []Holder
}

// List reports every recorded workspace with its live holders, reaping dead
// holder files on the way (but never tearing a share down).
func (s *Store) List() ([]Status, error) {
	keys, err := s.keys()
	if err != nil {
		return nil, err
	}
	var out []Status
	for _, key := range keys {
		err := s.withLock(key, func() error {
			live, err := s.reapLocked(key)
			if err != nil {
				return err
			}
			ws, err := readWorkspace(s.wsDir(key))
			if errors.Is(err, os.ErrNotExist) {
				ws = Workspace{Key: key}
			} else if err != nil {
				return err
			}
			out = append(out, Status{Workspace: ws, Live: live})
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// keys lists workspace directories, sorted for deterministic output.
func (s *Store) keys() ([]string, error) {
	entries, err := os.ReadDir(s.Dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var keys []string
	for _, e := range entries {
		if e.IsDir() {
			keys = append(keys, e.Name())
		}
	}
	sort.Strings(keys)
	return keys, nil
}

func readWorkspace(dir string) (Workspace, error) {
	var ws Workspace
	data, err := os.ReadFile(filepath.Join(dir, "workspace.json"))
	if err != nil {
		return ws, err
	}
	if err := json.Unmarshal(data, &ws); err != nil {
		return ws, fmt.Errorf("corrupt %s: %w", filepath.Join(dir, "workspace.json"), err)
	}
	return ws, nil
}

func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
