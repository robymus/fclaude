package install

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

var (
	shell32             = windows.NewLazySystemDLL("shell32.dll")
	user32              = windows.NewLazySystemDLL("user32.dll")
	procShellExecuteExW = shell32.NewProc("ShellExecuteExW")
	procSendMessageTOW  = user32.NewProc("SendMessageTimeoutW")
)

// shellExecuteInfo is SHELLEXECUTEINFOW.
type shellExecuteInfo struct {
	cbSize       uint32
	fMask        uint32
	hwnd         uintptr
	lpVerb       *uint16
	lpFile       *uint16
	lpParameters *uint16
	lpDirectory  *uint16
	nShow        int32
	hInstApp     uintptr
	lpIDList     uintptr
	lpClass      *uint16
	hkeyClass    uintptr
	dwHotKey     uint32
	hIcon        uintptr
	hProcess     windows.Handle
}

const (
	seeMaskNoCloseProcess = 0x00000040
	seeMaskNoAsync        = 0x00000100
	swHide                = 0
)

// ErrElevationDeclined means the user answered No to the UAC prompt.
var ErrElevationDeclined = errors.New("administrator permission was declined")

// RunElevated starts exe with args through a UAC prompt ("runas") and waits
// for it. A non-zero exit is an error.
func RunElevated(exe string, args []string) error {
	verb, _ := windows.UTF16PtrFromString("runas")
	file, err := windows.UTF16PtrFromString(exe)
	if err != nil {
		return err
	}
	params, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(args))
	if err != nil {
		return err
	}
	info := shellExecuteInfo{fMask: seeMaskNoCloseProcess | seeMaskNoAsync, lpVerb: verb, lpFile: file, lpParameters: params, nShow: swHide}
	info.cbSize = uint32(unsafe.Sizeof(info))
	if r, _, e := procShellExecuteExW.Call(uintptr(unsafe.Pointer(&info))); r == 0 { //nolint:gosec // Win32 call: pointer to a SHELLEXECUTEINFOW we own
		if errors.Is(e, windows.ERROR_CANCELLED) {
			return ErrElevationDeclined
		}
		return fmt.Errorf("ShellExecuteEx: %w", e)
	}
	defer func() { _ = windows.CloseHandle(info.hProcess) }()
	if _, err := windows.WaitForSingleObject(info.hProcess, windows.INFINITE); err != nil {
		return err
	}
	var code uint32
	if err := windows.GetExitCodeProcess(info.hProcess, &code); err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("elevated step exited with code %d", code)
	}
	return nil
}

// IsElevated reports whether this process runs as administrator.
func IsElevated() bool { return windows.GetCurrentProcessToken().IsElevated() }

// PowerShell runs script with Windows PowerShell and returns its output.
func PowerShell(script string) (string, error) {
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script) //nolint:gosec // scripts are built by this package (FirewallScript etc.)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), &toolError{"powershell", string(out), err}
	}
	return string(out), nil
}

const envKey = `Environment`

// AddToUserPath appends dir to the user's PATH (HKCU\Environment) if it is not
// there yet, and tells running programs. Returns whether it changed anything.
func AddToUserPath(dir string) (bool, error) {
	return editUserPath(func(parts []string) ([]string, bool) {
		for _, p := range parts {
			if strings.EqualFold(strings.TrimRight(p, `\`), strings.TrimRight(dir, `\`)) {
				return parts, false
			}
		}
		return append(parts, dir), true
	})
}

// RemoveFromUserPath removes dir from the user's PATH (uninstall).
func RemoveFromUserPath(dir string) (bool, error) {
	return editUserPath(func(parts []string) ([]string, bool) {
		out, changed := parts[:0:0], false
		for _, p := range parts {
			if strings.EqualFold(strings.TrimRight(p, `\`), strings.TrimRight(dir, `\`)) {
				changed = true
				continue
			}
			out = append(out, p)
		}
		return out, changed
	})
}

func editUserPath(edit func([]string) ([]string, bool)) (bool, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, envKey, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return false, err
	}
	defer func() { _ = k.Close() }()
	cur, typ, err := k.GetStringValue("Path")
	if err != nil && !errors.Is(err, registry.ErrNotExist) {
		return false, err
	}
	var parts []string
	for _, p := range strings.Split(cur, ";") {
		if p != "" {
			parts = append(parts, p)
		}
	}
	parts, changed := edit(parts)
	if !changed {
		return false, nil
	}
	val := strings.Join(parts, ";")
	if typ == registry.SZ {
		err = k.SetStringValue("Path", val)
	} else { // REG_EXPAND_SZ (the default), or missing
		err = k.SetExpandStringValue("Path", val)
	}
	if err != nil {
		return false, err
	}
	broadcastEnvironmentChange()
	return true, nil
}

// broadcastEnvironmentChange is WM_SETTINGCHANGE("Environment"): new
// terminals and Explorer-launched programs see the updated PATH.
func broadcastEnvironmentChange() {
	const hwndBroadcast, wmSettingChange, smtoAbortIfHung = 0xffff, 0x001A, 0x0002
	env, _ := windows.UTF16PtrFromString(envKey)
	var result uintptr
	_, _, _ = procSendMessageTOW.Call(hwndBroadcast, wmSettingChange, 0, uintptr(unsafe.Pointer(env)), smtoAbortIfHung, 5000, uintptr(unsafe.Pointer(&result))) //nolint:gosec // Win32 call: UTF-16 string and result we own
}
