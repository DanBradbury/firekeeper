package redact

import (
	"regexp"
	"strings"
	"sync"
)

// Windows paths are matched with a pattern, not a fixed string. Windows
// compares paths without regard to case and accepts either separator, and
// transcripts spell one path many ways: C:\Users\Dan, c:\users\dan,
// C:/Users/Dan, /c/Users/Dan (Git Bash), file:///c%3A/Users/Dan,
// \\localhost\c$\Users\Dan, JSON text left escaped once more
// (C:\\Users\\Dan), and the 8.3 short name (C:\Users\DANBRA~1). These
// patterns are built from the path's shape, so they behave the same on every
// operating system; only the transcripts they meet differ.

// windowsPath is the compiled matcher for one Windows-shaped path.
type windowsPath struct {
	re *regexp.Regexp
	// hints are lower-case substrings, at least one of which every match
	// contains, so most text skips the regular expression.
	hints []string
}

// windowsSep matches one separator, in any spelling: a slash, a backslash,
// or a run of them (escaped backslashes, "\/" from JSON encoders).
const windowsSep = `[\\/]+`

var windowsPaths sync.Map // string -> *windowsPath (nil when not Windows-shaped)

// isWindowsDrivePath reports whether p is a drive path such as C:\Users\me
// or C:/Users/me.
func isWindowsDrivePath(p string) bool {
	return len(p) >= 3 && isASCIILetter(p[0]) && p[1] == ':' && (p[2] == '\\' || p[2] == '/')
}

// windowsPathFor returns the matcher for p, or nil when p is not a Windows
// drive path or, with tilde set, a "~\..." form below a Windows home.
func windowsPathFor(p string, tilde bool) *windowsPath {
	key := p
	if tilde {
		key = "~" + key
	}
	if v, ok := windowsPaths.Load(key); ok {
		return v.(*windowsPath)
	}
	w := compileWindowsPath(p, tilde)
	windowsPaths.Store(key, w)
	return w
}

func compileWindowsPath(p string, tilde bool) *windowsPath {
	var b strings.Builder
	b.WriteString(`(?i)`)
	var rest string
	switch {
	case tilde && len(p) >= 2 && p[0] == '~' && (p[1] == '\\' || p[1] == '/'):
		b.WriteString(`~`)
		rest = p[1:]
	case !tilde && isWindowsDrivePath(p):
		d := regexp.QuoteMeta(strings.ToLower(p[:1]))
		// C: and c%3A (an encoded colon in a file URI), /c (Git Bash and
		// MSYS), and the administrative share \\host\c$.
		b.WriteString(`(?:` + d + `(?::|%3a)|/` + d + `|` + windowsSep + `[a-z0-9._-]+` + windowsSep + d + `\$)`)
		rest = p[2:]
	default:
		return nil
	}
	var comps []string
	for _, c := range strings.FieldsFunc(rest, func(r rune) bool { return r == '\\' || r == '/' }) {
		comps = append(comps, c)
	}
	if len(comps) == 0 {
		return nil
	}
	var hints []string
	for i, c := range comps {
		b.WriteString(windowsSep)
		alt := regexp.QuoteMeta(c)
		short := shortNameStem(c)
		if short != "" {
			alt = `(?:` + alt + `|` + short + `)`
		}
		b.WriteString(alt)
		if i == len(comps)-1 {
			hints = append(hints, strings.ToLower(c))
			if stem, _, ok := strings.Cut(short, `~`); ok {
				hints = append(hints, strings.ToLower(strings.ReplaceAll(stem, `\`, "")))
			}
		}
	}
	return &windowsPath{re: regexp.MustCompile(b.String()), hints: hints}
}

// shortNameStem returns a pattern for the 8.3 short names Windows may give
// a path component that is not a valid 8.3 name itself, such as DANBRA~1
// for danbradbury or PROGRA~1 for "Program Files": up to six characters of
// the name with spaces and dots removed, "~", a number, and the first three
// characters of the extension. It returns "" for names that need none or
// that it can't predict (non-ASCII ones).
func shortNameStem(c string) string {
	base, ext := c, ""
	if i := strings.LastIndexByte(c, '.'); i > 0 {
		base, ext = c[:i], c[i+1:]
	}
	valid := len(base) <= 8 && len(ext) <= 3 && !strings.ContainsAny(c, " +,;=[]") && strings.Count(c, ".") <= 1
	if valid {
		return ""
	}
	var stem []byte
	for i := 0; i < len(base) && len(stem) < 6; i++ {
		switch ch := base[i]; {
		case ch >= 0x80:
			return ""
		case ch == ' ' || ch == '.':
		case strings.IndexByte("+,;=[]", ch) >= 0:
			stem = append(stem, '_')
		default:
			stem = append(stem, ch)
		}
	}
	if len(stem) == 0 {
		return ""
	}
	pattern := regexp.QuoteMeta(string(stem)) + `~[0-9]{1,6}`
	ext = strings.ReplaceAll(ext, " ", "")
	if len(ext) > 3 {
		ext = ext[:3]
	}
	for i := 0; i < len(ext); i++ {
		if ext[i] >= 0x80 {
			return ""
		}
	}
	if ext != "" {
		pattern += `(?:\.` + regexp.QuoteMeta(ext) + `)?`
	}
	return pattern
}

// redact replaces every whole-path match of w in s, as redactPrefix does
// for a fixed string: a match must not continue a longer name on either
// side.
func (w *windowsPath) redact(s, replacement string, kind Kind, counts Counts) string {
	if !w.mayMatch(s) {
		return s
	}
	var b strings.Builder
	last := 0
	for pos := 0; pos < len(s); {
		loc := w.re.FindStringIndex(s[pos:])
		if loc == nil {
			break
		}
		start, end := pos+loc[0], pos+loc[1]
		if (start > 0 && isKeyByte(s[start-1])) || (end < len(s) && isKeyByte(s[end])) {
			pos = start + 1
			continue
		}
		b.WriteString(s[last:start])
		b.WriteString(replacement)
		counts[kind]++
		last, pos = end, end
	}
	if last == 0 {
		return s
	}
	b.WriteString(s[last:])
	return b.String()
}

func (w *windowsPath) mayMatch(s string) bool {
	lower := strings.ToLower(s)
	for _, h := range w.hints {
		if strings.Contains(lower, h) {
			return true
		}
	}
	return false
}
