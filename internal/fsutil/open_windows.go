//go:build windows

package fsutil

import (
	"os"

	"golang.org/x/sys/windows"
)

// Open opens name for reading and lets other processes rename, replace, or
// delete it while it is open. os.Open on Windows does not, so a reader of a
// log or state file would make the writer's rotation or atomic replace fail
// with a sharing violation.
func Open(name string) (*os.File, error) {
	path, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: name, Err: err}
	}
	const share = windows.FILE_SHARE_READ | windows.FILE_SHARE_WRITE | windows.FILE_SHARE_DELETE
	handle, err := windows.CreateFile(path, windows.GENERIC_READ, share, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: name, Err: err}
	}
	return os.NewFile(uintptr(handle), name), nil
}
