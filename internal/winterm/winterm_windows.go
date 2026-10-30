//go:build windows

package winterm

import (
	"fmt"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	swRestore = 9
	gwOwner   = 4
)

var (
	user32            = windows.NewLazySystemDLL("user32.dll")
	procIsIconic      = user32.NewProc("IsIconic")
	procShowWindow    = user32.NewProc("ShowWindow")
	procSetForeground = user32.NewProc("SetForegroundWindow")
	procGetWindow     = user32.NewProc("GetWindow")
	procGetTextLen    = user32.NewProc("GetWindowTextLengthW")
)

// enumState is shared with the EnumWindows callback. Windows callbacks are
// created once and live for the whole process, so the callback reads its
// target from here rather than being rebuilt for each call.
var (
	enumMu        sync.Mutex
	enumTargetPID uint32
	enumFound     windows.HWND
	enumCallback  = windows.NewCallback(func(hwnd windows.HWND, _ uintptr) uintptr {
		var pid uint32
		if _, err := windows.GetWindowThreadProcessId(hwnd, &pid); err != nil || pid != enumTargetPID {
			return 1
		}
		if !windows.IsWindowVisible(hwnd) {
			return 1
		}
		// Owned windows are dialogs and tool windows; the terminal is a
		// top-level window with a title.
		if owner, _, _ := procGetWindow.Call(uintptr(hwnd), gwOwner); owner != 0 {
			return 1
		}
		if length, _, _ := procGetTextLen.Call(uintptr(hwnd)); length == 0 {
			return 1
		}
		enumFound = hwnd
		return 0 // stop
	})
)

// Focus raises the terminal window hosting pids and returns the name of the
// application that owns it.
func Focus(pids []int) (string, error) {
	snapshot, err := snapshotProcesses()
	if err != nil {
		return "", fmt.Errorf("list processes: %w", err)
	}
	exe := make(map[int]string, len(snapshot))
	for _, p := range snapshot {
		exe[p.PID] = p.Exe
	}
	for _, pid := range Candidates(snapshot, pids) {
		hwnd := topLevelWindow(uint32(pid))
		if hwnd == 0 {
			continue
		}
		if iconic, _, _ := procIsIconic.Call(uintptr(hwnd)); iconic != 0 {
			procShowWindow.Call(uintptr(hwnd), swRestore)
		}
		if ok, _, _ := procSetForeground.Call(uintptr(hwnd)); ok == 0 {
			return "", fmt.Errorf("Windows refused to bring %s to the front", AppName(exe[pid]))
		}
		return AppName(exe[pid]), nil
	}
	return "", ErrNoWindow
}

// topLevelWindow finds a visible, titled, unowned window of pid.
func topLevelWindow(pid uint32) windows.HWND {
	enumMu.Lock()
	defer enumMu.Unlock()
	enumTargetPID, enumFound = pid, 0
	// EnumWindows reports an error when the callback stops early.
	_ = windows.EnumWindows(enumCallback, nil)
	return enumFound
}

func snapshotProcesses() ([]Process, error) {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(snapshot)
	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	if err := windows.Process32First(snapshot, &entry); err != nil {
		return nil, err
	}
	var out []Process
	for {
		out = append(out, Process{PID: int(entry.ProcessID), PPID: int(entry.ParentProcessID), Exe: windows.UTF16ToString(entry.ExeFile[:])})
		if err := windows.Process32Next(snapshot, &entry); err != nil {
			if err == windows.ERROR_NO_MORE_FILES {
				break
			}
			return nil, err
		}
	}
	return out, nil
}
