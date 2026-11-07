package lease

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
)

// fakeAlive is a controllable liveness oracle.
type fakeAlive struct {
	mu   sync.Mutex
	dead map[Holder]bool
}

func (f *fakeAlive) alive(h Holder) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return !f.dead[h]
}

func (f *fakeAlive) kill(h Holder) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.dead == nil {
		f.dead = map[Holder]bool{}
	}
	f.dead[h] = true
}

func newTestStore(t *testing.T) (*Store, *fakeAlive) {
	t.Helper()
	fa := &fakeAlive{}
	return &Store{Dir: filepath.Join(t.TempDir(), "leases"), Alive: fa.alive}, fa
}

var ws = Workspace{Key: Key(`C:\dev\app`), WinPath: `C:\dev\app`, VMPath: "/home/fclaude/app", Share: "fc-app"}

func TestKeyCaseInsensitive(t *testing.T) {
	if Key(`C:\dev\App`) != Key(`c:\DEV\app`) || Key(`C:\dev\a`) == Key(`C:\dev\b`) {
		t.Fatal("key must be case-insensitive and distinguish paths")
	}
}

func TestAcquireReleaseLifecycle(t *testing.T) {
	s, _ := newTestStore(t)
	a, b := Holder{PID: 10, Started: 1}, Holder{PID: 11, Started: 1}
	var setups []int
	setup := func(others int) error { setups = append(setups, others); return nil }

	if err := s.Acquire(ws, a, setup); err != nil {
		t.Fatal(err)
	}
	if err := s.Acquire(ws, b, setup); err != nil {
		t.Fatal(err)
	}
	if len(setups) != 2 || setups[0] != 0 || setups[1] != 1 {
		t.Fatalf("setup others counts = %v, want [0 1]", setups)
	}

	torn := 0
	teardown := func(w Workspace) error {
		if w != ws {
			t.Errorf("teardown got %+v", w)
		}
		torn++
		return nil
	}
	if err := s.Release(ws.Key, a, teardown); err != nil || torn != 0 {
		t.Fatalf("first release: err=%v torn=%d (b still holds)", err, torn)
	}
	if err := s.Release(ws.Key, b, teardown); err != nil || torn != 1 {
		t.Fatalf("last release: err=%v torn=%d", err, torn)
	}
	if _, err := os.Stat(s.wsDir(ws.Key)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("workspace record not removed after teardown")
	}
	// Releasing again is harmless.
	if err := s.Release(ws.Key, b, teardown); err != nil || torn != 1 {
		t.Fatalf("double release: err=%v torn=%d", err, torn)
	}
}

func TestReacquireBySameHolderCountsNoOthers(t *testing.T) {
	s, _ := newTestStore(t)
	a := Holder{PID: 10, Started: 1}
	var last int
	setup := func(o int) error { last = o; return nil }
	_ = s.Acquire(ws, a, setup)
	_ = s.Acquire(ws, a, setup)
	if last != 0 {
		t.Fatalf("same holder counted as other: %d", last)
	}
}

func TestSetupFailureRollsBack(t *testing.T) {
	s, _ := newTestStore(t)
	a := Holder{PID: 10, Started: 1}
	if err := s.Acquire(ws, a, func(int) error { return errors.New("mount failed") }); err == nil {
		t.Fatal("setup error not returned")
	}
	if _, err := os.Stat(s.wsDir(ws.Key)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("failed first acquire left a record")
	}

	// With another live holder, a failing setup removes only our lease.
	b := Holder{PID: 11, Started: 1}
	_ = s.Acquire(ws, b, func(int) error { return nil })
	_ = s.Acquire(ws, a, func(int) error { return errors.New("x") })
	st, _ := s.List()
	if len(st) != 1 || len(st[0].Live) != 1 || st[0].Live[0] != b {
		t.Fatalf("rollback with others: %+v", st)
	}
}

func TestReapReleasesDeadHolders(t *testing.T) {
	s, fa := newTestStore(t)
	a, b := Holder{PID: 10, Started: 1}, Holder{PID: 11, Started: 1}
	other := Workspace{Key: Key(`C:\dev\lib`), WinPath: `C:\dev\lib`, Share: "fc-lib"}
	_ = s.Acquire(ws, a, func(int) error { return nil })
	_ = s.Acquire(other, b, func(int) error { return nil })

	fa.kill(a) // editor closed via TerminateProcess
	var torn []string
	released, err := s.Reap(func(w Workspace) error { torn = append(torn, w.Share); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(released) != 1 || released[0].Share != "fc-app" || len(torn) != 1 {
		t.Fatalf("released=%+v torn=%v", released, torn)
	}
	st, _ := s.List()
	if len(st) != 1 || st[0].Workspace.Share != "fc-lib" {
		t.Fatalf("remaining: %+v", st)
	}
}

func TestReapTeardownFailureKeepsRecord(t *testing.T) {
	s, fa := newTestStore(t)
	a := Holder{PID: 10, Started: 1}
	_ = s.Acquire(ws, a, func(int) error { return nil })
	fa.kill(a)
	if _, err := s.Reap(func(Workspace) error { return errors.New("vm unreachable") }); err == nil {
		t.Fatal("teardown error swallowed")
	}
	if _, err := os.Stat(filepath.Join(s.wsDir(ws.Key), "workspace.json")); err != nil {
		t.Fatal("record must survive a failed teardown so a later reap retries")
	}
	if rel, err := s.Reap(func(Workspace) error { return nil }); err != nil || len(rel) != 1 {
		t.Fatalf("retry: %v %v", rel, err)
	}
}

func TestPIDReuseIsDead(t *testing.T) {
	s, _ := newTestStore(t)
	self, err := Self()
	if err != nil {
		t.Fatal(err)
	}
	s.Alive = alive // real OS check
	recycled := Holder{PID: self.PID, Started: self.Started + 1}
	_ = s.Acquire(ws, recycled, func(int) error { return nil })
	st, _ := s.List()
	if len(st) != 1 || len(st[0].Live) != 0 {
		t.Fatalf("recycled PID treated as alive: %+v", st)
	}
}

func TestRealLiveness(t *testing.T) {
	self, err := Self()
	if err != nil {
		t.Fatal(err)
	}
	if !alive(self) {
		t.Fatal("own process reported dead")
	}
	cmd := exec.Command(testExe(t), "-test.run=^$")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	child, err := holderOf(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait() // reaped: the PID no longer exists
	if alive(child) {
		t.Fatal("exited child reported alive")
	}
}

func TestSweep(t *testing.T) {
	s, _ := newTestStore(t)
	_ = s.Acquire(ws, Holder{PID: 1, Started: 1}, func(int) error { return nil })
	n, err := s.Sweep()
	if err != nil || n != 1 {
		t.Fatalf("sweep: n=%d err=%v", n, err)
	}
	if st, _ := s.List(); len(st) != 0 {
		t.Fatalf("not empty after sweep: %+v", st)
	}
	if n, err := (&Store{Dir: filepath.Join(t.TempDir(), "missing")}).Sweep(); err != nil || n != 0 {
		t.Fatalf("sweep of missing dir: %d %v", n, err)
	}
}

func TestConcurrentAcquireRelease(t *testing.T) {
	s, _ := newTestStore(t)
	s.Alive = func(Holder) bool { return true }
	const n = 40
	var mu sync.Mutex
	mounts, unmounts := 0, 0
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func(h Holder) {
			defer wg.Done()
			if err := s.Acquire(ws, h, func(o int) error {
				mu.Lock()
				defer mu.Unlock()
				if o == 0 {
					mounts++
				}
				return nil
			}); err != nil {
				t.Error(err)
			}
			if err := s.Release(ws.Key, h, func(Workspace) error {
				mu.Lock()
				defer mu.Unlock()
				unmounts++
				return nil
			}); err != nil {
				t.Error(err)
			}
		}(Holder{PID: 100 + i, Started: 7})
	}
	wg.Wait()
	if mounts != unmounts || mounts == 0 {
		t.Fatalf("mounts=%d unmounts=%d must balance", mounts, unmounts)
	}
}

func TestParseHolder(t *testing.T) {
	for _, bad := range []string{"workspace.json", "12", "x-1", "0-1", "1-x", "-1-2"} {
		if _, ok := parseHolder(bad); ok {
			t.Errorf("parseHolder(%q) accepted", bad)
		}
	}
	if h, ok := parseHolder("42-99"); !ok || h != (Holder{PID: 42, Started: 99}) {
		t.Errorf("parseHolder valid: %+v %v", h, ok)
	}
}

func TestListIgnoresStrayFilesAndCorruptRecord(t *testing.T) {
	s, _ := newTestStore(t)
	_ = s.Acquire(ws, Holder{PID: 1, Started: 1}, func(int) error { return nil })
	_ = os.WriteFile(filepath.Join(s.wsDir(ws.Key), "junk"), nil, 0o600)
	if st, err := s.List(); err != nil || len(st[0].Live) != 1 {
		t.Fatalf("stray file: %+v %v", st, err)
	}
	_ = os.WriteFile(filepath.Join(s.wsDir(ws.Key), "workspace.json"), []byte("{"), 0o600)
	if _, err := s.List(); err == nil {
		t.Fatal("corrupt record not reported")
	}
}

// testExe is this test binary's absolute path. os.Args[0] can be a bare
// relative name (e.g. when launched through WSL interop), which os/exec
// refuses to run.
func testExe(t *testing.T) string {
	t.Helper()
	p, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestForgetAndWithLock(t *testing.T) {
	s, _ := newTestStore(t)
	_ = s.Acquire(ws, Holder{PID: 1, Started: 1}, func(int) error { return nil })
	ran := false
	if err := s.WithLock("vm-boot", func() error { ran = true; return nil }); err != nil || !ran {
		t.Fatalf("WithLock: ran=%v err=%v", ran, err)
	}
	if err := s.Forget(ws.Key); err != nil {
		t.Fatal(err)
	}
	if st, _ := s.List(); len(st) != 0 {
		t.Fatalf("not empty after Forget (the vm-boot lock file must not count as a workspace): %+v", st)
	}
}
