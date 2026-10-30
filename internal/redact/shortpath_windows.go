package redact

import "golang.org/x/sys/windows"

// osShortPathName returns the 8.3 short form of p from GetShortPathNameW, or
// "" when p does not exist, the volume has no short names, or the call fails.
func osShortPathName(p string) string {
	ptr, err := windows.UTF16PtrFromString(p)
	if err != nil {
		return ""
	}
	buf := make([]uint16, windows.MAX_PATH)
	for {
		n, err := windows.GetShortPathName(ptr, &buf[0], uint32(len(buf)))
		if err != nil || n == 0 {
			return ""
		}
		if int(n) < len(buf) {
			return windows.UTF16ToString(buf[:n])
		}
		if n > 32768 {
			return ""
		}
		buf = make([]uint16, n)
	}
}
