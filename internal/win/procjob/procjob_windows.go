package procjob

import (
	"fmt"
	"os/exec"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Prepare is a no-op on Windows; the job is attached after start.
func Prepare(*exec.Cmd) {}

// Attach places the started cmd in a kill-on-close job. The job handle is
// deliberately never closed: it must live exactly as long as this process.
func Attach(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return fmt.Errorf("procjob: command not started")
	}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return fmt.Errorf("procjob: create job: %w", err)
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
		},
	}
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil { //nolint:gosec // G103: Win32 API takes a struct pointer
		_ = windows.CloseHandle(job)
		return fmt.Errorf("procjob: configure job: %w", err)
	}
	ph, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false,
		uint32(cmd.Process.Pid)) //nolint:gosec // PIDs fit in uint32 on Windows
	if err != nil {
		_ = windows.CloseHandle(job)
		return fmt.Errorf("procjob: open child: %w", err)
	}
	defer func() { _ = windows.CloseHandle(ph) }()
	if err := windows.AssignProcessToJobObject(job, ph); err != nil {
		_ = windows.CloseHandle(job)
		return fmt.Errorf("procjob: assign child: %w", err)
	}
	return nil
}
