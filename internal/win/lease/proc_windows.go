package lease

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// stillActive is the GetExitCodeProcess value of a running process (STILL_ACTIVE).
const stillActive = 259

// holderOf returns the process creation time (FILETIME, 100ns ticks since
// 1601) as the start-time token.
func holderOf(pid int) (Holder, error) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid)) //nolint:gosec // PIDs fit in uint32 on Windows
	if err != nil {
		return Holder{}, err
	}
	defer func() { _ = windows.CloseHandle(h) }()
	var creation, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(h, &creation, &exit, &kernel, &user); err != nil {
		return Holder{}, err
	}
	started := uint64(creation.HighDateTime)<<32 | uint64(creation.LowDateTime)
	return Holder{PID: pid, Started: started}, nil
}

func alive(h Holder) bool {
	ph, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(h.PID)) //nolint:gosec // see above
	if err != nil {
		// Access denied means the process exists but belongs to someone else —
		// it can't be our wrapper, but be conservative and keep the lease.
		return errors.Is(err, windows.ERROR_ACCESS_DENIED)
	}
	defer func() { _ = windows.CloseHandle(ph) }()
	var code uint32
	if err := windows.GetExitCodeProcess(ph, &code); err != nil || code != stillActive {
		return false
	}
	var creation, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(ph, &creation, &exit, &kernel, &user); err != nil {
		return false
	}
	return uint64(creation.HighDateTime)<<32|uint64(creation.LowDateTime) == h.Started
}

func lockFile(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	ol := new(windows.Overlapped)
	handle := windows.Handle(f.Fd())
	if err := windows.LockFileEx(handle, windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, ol); err != nil {
		_ = f.Close()
		return nil, err
	}
	return func() {
		_ = windows.UnlockFileEx(handle, 0, 1, 0, ol)
		_ = f.Close()
	}, nil
}
