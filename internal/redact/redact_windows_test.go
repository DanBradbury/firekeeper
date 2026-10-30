package redact

import (
	"os"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// On Windows, the real home directory in the spellings Windows itself
// produces: its 8.3 short name (when the volume has them), case changes,
// slashes, and JSON's escaped backslashes.
func TestRealWindowsHomeSpellings(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	user := home[strings.LastIndexAny(home, `\/`)+1:]
	spellings := []string{
		home,
		strings.ToLower(home),
		strings.ToUpper(home),
		strings.ReplaceAll(home, `\`, "/"),
		strings.ReplaceAll(home, `\`, `\\`),
		"/" + strings.ToLower(home[:1]) + strings.ReplaceAll(home[2:], `\`, "/"),
	}
	if p, err := windows.UTF16PtrFromString(home); err == nil {
		buf := make([]uint16, windows.MAX_PATH)
		if n, err := windows.GetShortPathName(p, &buf[0], uint32(len(buf))); err == nil && n > 0 && int(n) < len(buf) {
			short := windows.UTF16ToString(buf[:n])
			t.Logf("short name of %s: %s", home, short)
			spellings = append(spellings, short, strings.ToLower(short))
		}
	}
	opts := Options{HomeDir: home}
	for _, s := range spellings {
		got, _ := String("cd "+s+`\src`, opts)
		if !strings.HasPrefix(got, "cd ~") || strings.Contains(strings.ToLower(got), strings.ToLower(user)) {
			t.Errorf("%q -> %q", s, got)
		}
	}
}
