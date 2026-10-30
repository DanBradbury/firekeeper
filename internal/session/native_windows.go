//go:build windows

package session

import (
	"bytes"
	"errors"
	"fmt"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	processQueryLimitedInformation = 0x1000
	processVMRead                  = 0x0010

	// maxRemoteString bounds a command line or directory read from another
	// process. The Windows limit is 32,767 characters.
	maxRemoteString = 64 << 10
)

// nativeCommand replaces ps and lsof with Win32 calls: Windows has neither,
// and starting PowerShell on every two-second poll would be slow.
func nativeCommand(name string, args []string) ([]byte, bool, error) {
	switch name {
	case "ps":
		out, err := windowsProcessList(time.Now())
		return out, true, err
	case "lsof":
		return emulateLsof(args, windowsProcessCWD), true, nil
	}
	return nil, false, nil
}

// windowsProcessList lists every process in ps's column layout. Command
// lines come from each process's own memory, so a process owned by another
// user, or elevated, falls back to its executable name.
func windowsProcessList(now time.Time) ([]byte, error) {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, fmt.Errorf("list processes: %w", err)
	}
	defer windows.CloseHandle(snapshot)

	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	if err := windows.Process32First(snapshot, &entry); err != nil {
		return nil, fmt.Errorf("list processes: %w", err)
	}
	var out bytes.Buffer
	for {
		if entry.ProcessID != 0 {
			info := inspectProcess(entry.ProcessID, false)
			command := info.commandLine
			if command == "" {
				command = windows.UTF16ToString(entry.ExeFile[:])
			}
			var elapsed time.Duration
			if !info.created.IsZero() {
				elapsed = now.Sub(info.created)
			}
			out.WriteString(psLine(int(entry.ProcessID), int(entry.ParentProcessID), elapsed, command))
		}
		if err := windows.Process32Next(snapshot, &entry); err != nil {
			if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
				break
			}
			return nil, fmt.Errorf("list processes: %w", err)
		}
	}
	return out.Bytes(), nil
}

// windowsProcessCWD returns pid's current directory, or "" when it cannot
// be read.
func windowsProcessCWD(pid int) string {
	return inspectProcess(uint32(pid), true).cwd
}

type processDetails struct {
	created     time.Time
	commandLine string
	cwd         string
}

// inspectProcess reads what ps and lsof would report for pid. Every field is
// best effort: a process that denies access yields the zero value.
func inspectProcess(pid uint32, wantCWD bool) processDetails {
	var details processDetails
	handle, err := windows.OpenProcess(processQueryLimitedInformation|processVMRead, false, pid)
	if err != nil {
		return details
	}
	defer windows.CloseHandle(handle)

	var created, exited, kernel, user windows.Filetime
	if windows.GetProcessTimes(handle, &created, &exited, &kernel, &user) == nil {
		details.created = time.Unix(0, created.Nanoseconds())
	}
	params, ok := remoteProcessParameters(handle)
	if !ok {
		return details
	}
	details.commandLine = readRemoteString(handle, params.CommandLine)
	if wantCWD {
		details.cwd = trimWindowsDir(readRemoteString(handle, params.CurrentDirectory.DosPath))
	}
	return details
}

// remoteProcessParameters reads the RTL_USER_PROCESS_PARAMETERS block from
// the process environment block of another process. Only the prefix up to
// the command line is read, which every supported Windows version lays out
// the same way.
func remoteProcessParameters(handle windows.Handle) (windows.RTL_USER_PROCESS_PARAMETERS, bool) {
	var params windows.RTL_USER_PROCESS_PARAMETERS
	var info windows.PROCESS_BASIC_INFORMATION
	var returned uint32
	if err := windows.NtQueryInformationProcess(handle, windows.ProcessBasicInformation, unsafe.Pointer(&info), uint32(unsafe.Sizeof(info)), &returned); err != nil {
		return params, false
	}
	if info.PebBaseAddress == nil {
		return params, false
	}
	var peb windows.PEB
	pebPrefix := unsafe.Offsetof(peb.ProcessParameters) + unsafe.Sizeof(peb.ProcessParameters)
	if !readRemote(handle, uintptr(unsafe.Pointer(info.PebBaseAddress)), unsafe.Pointer(&peb), pebPrefix) || peb.ProcessParameters == nil {
		return params, false
	}
	paramsPrefix := unsafe.Offsetof(params.CommandLine) + unsafe.Sizeof(params.CommandLine)
	if !readRemote(handle, uintptr(unsafe.Pointer(peb.ProcessParameters)), unsafe.Pointer(&params), paramsPrefix) {
		return params, false
	}
	return params, true
}

func readRemote(handle windows.Handle, address uintptr, destination unsafe.Pointer, size uintptr) bool {
	var read uintptr
	if err := windows.ReadProcessMemory(handle, address, (*byte)(destination), size, &read); err != nil {
		return false
	}
	return read == size
}

func readRemoteString(handle windows.Handle, s windows.NTUnicodeString) string {
	if s.Length == 0 || s.Buffer == nil || int(s.Length) > maxRemoteString {
		return ""
	}
	buffer := make([]uint16, s.Length/2)
	if !readRemote(handle, uintptr(unsafe.Pointer(s.Buffer)), unsafe.Pointer(&buffer[0]), uintptr(s.Length)) {
		return ""
	}
	return windows.UTF16ToString(buffer)
}
