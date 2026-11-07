package lease

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// readStat returns the state letter and start time (clock ticks since boot,
// field 22) of /proc/<pid>/stat. Linux support exists so the package is fully
// testable on CI; the production host is Windows.
func readStat(pid int) (state byte, started uint64, err error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, 0, err
	}
	// comm (field 2) is parenthesised and may contain spaces or ')': split
	// after the LAST ')'. The remaining fields start at field 3 (state).
	s := string(data)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return 0, 0, fmt.Errorf("malformed /proc/%d/stat", pid)
	}
	fields := strings.Fields(s[i+1:])
	const startTimeIdx = 22 - 3
	if len(fields) <= startTimeIdx || fields[0] == "" {
		return 0, 0, fmt.Errorf("short /proc/%d/stat", pid)
	}
	started, err = strconv.ParseUint(fields[startTimeIdx], 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("parse start time of %d: %w", pid, err)
	}
	return fields[0][0], started, nil
}

func holderOf(pid int) (Holder, error) {
	_, started, err := readStat(pid)
	if err != nil {
		return Holder{}, err
	}
	return Holder{PID: pid, Started: started}, nil
}

// alive: the process exists, is not a zombie (it has exited), and its start
// time matches (the PID was not recycled).
func alive(h Holder) bool {
	state, started, err := readStat(h.PID)
	return err == nil && state != 'Z' && started == h.Started
}

func lockFile(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}
