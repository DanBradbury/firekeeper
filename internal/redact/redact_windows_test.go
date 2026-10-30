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
		// SEC-26-5 (#31): WSL, Cygwin, long-path UNC, and percent-encoded.
		"/mnt/" + strings.ToLower(home[:1]) + strings.ReplaceAll(home[2:], `\`, "/"),
		"/cygdrive/" + strings.ToLower(home[:1]) + strings.ReplaceAll(home[2:], `\`, "/"),
		`\\?\UNC\localhost\` + home[:1] + "$" + home[2:],
		strings.ReplaceAll(percentEncode(home), "%3A", "%3a"),
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
		if strings.Contains(s, "%") {
			// Percent-encoded text continues with an encoded separator.
			got, _ := String("cd "+s+"%5Csrc", opts)
			if !strings.HasPrefix(got, "cd ~") || strings.Contains(strings.ToLower(got), strings.ToLower(user)) {
				t.Errorf("%q -> %q", s, got)
			}
			continue
		}
		got, _ := String("cd "+s+`\src`, opts)
		if !strings.HasPrefix(got, "cd ~") || strings.Contains(strings.ToLower(got), strings.ToLower(user)) {
			t.Errorf("%q -> %q", s, got)
		}
	}
}

// The real short-name lookup (GetShortPathNameW) feeds the matcher, so a
// directory's actual 8.3 name is redacted even when it is a hashed one no
// pattern predicts (SEC-26-5, #31).
func TestRealWindowsShortPathLookup(t *testing.T) {
	if got := osShortPathName(`C:\no\such\firekeeper\path`); got != "" {
		t.Errorf("short name of a missing path = %q", got)
	}
	var dir, short string
	parents := []string{t.TempDir()}
	if home, err := os.UserHomeDir(); err == nil {
		parents = append(parents, home)
	}
	for _, parent := range parents {
		d, err := os.MkdirTemp(parent, "Firekeeper Long Name ")
		if err != nil {
			continue
		}
		t.Cleanup(func() { os.RemoveAll(d) })
		if s := osShortPathName(d); s != "" && !strings.EqualFold(s, d) {
			dir, short = d, s
			break
		}
	}
	if dir == "" {
		t.Skip("no volume here keeps 8.3 short names")
	}
	t.Logf("short name of %s: %s", dir, short)
	leaf := dir[strings.LastIndexAny(dir, `\/`)+1:]
	opts := Options{HomeDir: `C:\Users\someone`, Paths: []string{dir}}
	for _, s := range []string{short, strings.ToLower(short), strings.ReplaceAll(short, `\`, "/")} {
		got, _ := String("open "+s+`\notes.txt`, opts)
		if !strings.Contains(got, Marker(KindPath)) || strings.Contains(strings.ToLower(got), strings.ToLower(leaf)) {
			t.Errorf("%q -> %q", s, got)
		}
	}
}
