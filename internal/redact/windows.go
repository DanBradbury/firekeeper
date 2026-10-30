package redact

import (
	"fmt"
	"regexp"
	"strings"
	"sync"
	"unicode/utf8"
)

// Windows paths are matched with a pattern, not a fixed string. Windows
// compares paths without regard to case and accepts either separator, and
// transcripts spell one path many ways: C:\Users\Dan, c:\users\dan,
// C:/Users/Dan, /c/Users/Dan (Git Bash), file:///c%3A/Users/Dan,
// \\localhost\c$\Users\Dan and its long-path form
// \\?\UNC\localhost\c$\Users\Dan, WSL's /mnt/c/Users/Dan and Cygwin's
// /cygdrive/c/Users/Dan, JSON text left escaped once more
// (C:\\Users\\Dan), percent-encoded text (C%3A%5CUsers%5CDan, Dan%20Smith),
// and the 8.3 short name (C:\Users\DANBRA~1, or a hashed one such as
// DA1F2B~1). These patterns are built from the path's shape, so they behave
// the same on every operating system; only the transcripts they meet differ.
// On Windows the path's real short name is looked up as well (shortPathName),
// which covers hashed names exactly.

// windowsPath is the compiled matcher for one Windows-shaped path.
type windowsPath struct {
	re *regexp.Regexp
	// hints are lower-case substrings, at least one of which every match
	// contains, so most text skips the regular expression.
	hints []string
}

// windowsSep matches one separator, in any spelling: a slash, a backslash,
// their percent-encodings, or a run of them (escaped backslashes, "\/" from
// JSON encoders).
const windowsSep = `(?:[\\/]|%5c|%2f)+`

// shortPathName returns the 8.3 short form of an existing path, or "" when
// there is none or it can't be looked up. It is a variable so tests on every
// operating system can stand in for Windows.
var shortPathName = osShortPathName

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
		// MSYS), /mnt/c (WSL), /cygdrive/c (Cygwin), and the administrative
		// share \\host\c$, also in its long-path form \\?\UNC\host\c$.
		b.WriteString(`(?:` + d + `(?::|%3a)|(?:/mnt|/cygdrive)?/` + d + `|(?:` + windowsSep + `\?` + windowsSep + `unc)?` + windowsSep + `[a-z0-9._-]+` + windowsSep + d + `(?:\$|%24))`)
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
	// The real short name of each component, when Windows has one that the
	// patterns below might not predict (a hashed DA1F2B~1).
	var real []string
	if !tilde {
		if sp := shortPathName(p); isWindowsDrivePath(sp) {
			real = strings.FieldsFunc(sp[2:], func(r rune) bool { return r == '\\' || r == '/' })
			if len(real) != len(comps) {
				real = nil
			}
		}
	}
	var hints []string
	for i, c := range comps {
		b.WriteString(windowsSep)
		alts := []string{componentPattern(c)}
		short, shortHints := shortNameStem(c)
		if short != "" {
			alts = append(alts, short)
		}
		var realShort string
		if real != nil && !strings.EqualFold(real[i], c) {
			realShort = real[i]
			alts = append(alts, regexp.QuoteMeta(realShort))
		}
		if len(alts) == 1 {
			b.WriteString(alts[0])
		} else {
			b.WriteString(`(?:` + strings.Join(alts, `|`) + `)`)
		}
		if i == len(comps)-1 {
			hints = append(hints, strings.ToLower(c))
			if enc := percentEncode(c); enc != c {
				hints = append(hints, strings.ToLower(enc))
			}
			if strings.Contains(c, " ") {
				hints = append(hints, strings.ToLower(strings.ReplaceAll(c, " ", "+")))
			}
			hints = append(hints, shortHints...)
			if realShort != "" {
				hints = append(hints, strings.ToLower(realShort))
			}
		}
	}
	return &windowsPath{re: regexp.MustCompile(b.String()), hints: hints}
}

// componentPattern matches the path component c literally or with any of
// its characters percent-encoded, as in a file URI or a URL query (a space
// also as "+").
func componentPattern(c string) string {
	var b strings.Builder
	for _, r := range c {
		lit := regexp.QuoteMeta(string(r))
		if isUnreservedURLRune(r) {
			b.WriteString(lit)
			continue
		}
		alt := lit + `|` + regexp.QuoteMeta(percentEncode(string(r)))
		if r == ' ' {
			alt += `|\+`
		}
		b.WriteString(`(?:` + alt + `)`)
	}
	return b.String()
}

// isUnreservedURLRune reports whether r is never percent-encoded in a URL.
func isUnreservedURLRune(r rune) bool {
	return r < utf8.RuneSelf && (isASCIILetter(byte(r)) || '0' <= r && r <= '9' || strings.ContainsRune("-._~", r))
}

// percentEncode encodes every byte of s outside the unreserved set as %XX.
func percentEncode(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if c := s[i]; c < utf8.RuneSelf && isUnreservedURLRune(rune(c)) {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// shortNameStem returns a pattern for the 8.3 short names Windows may give
// a path component that is not a valid 8.3 name itself, such as DANBRA~1
// for danbradbury or PROGRA~1 for "Program Files": up to six characters of
// the name with spaces and dots removed, "~", a number, and the first three
// characters of the extension. From the fifth name sharing those six
// characters Windows hashes instead: the first two characters, four hex
// digits, "~", and a digit (DA1F2B~1). It also returns lower-case hints,
// one of which every match contains. It returns "" for names that need none
// or that it can't predict (non-ASCII ones).
func shortNameStem(c string) (string, []string) {
	base, ext := c, ""
	if i := strings.LastIndexByte(c, '.'); i > 0 {
		base, ext = c[:i], c[i+1:]
	}
	valid := len(base) <= 8 && len(ext) <= 3 && !strings.ContainsAny(c, " +,;=[]") && strings.Count(c, ".") <= 1
	if valid {
		return "", nil
	}
	var stem []byte
	for i := 0; i < len(base) && len(stem) < 6; i++ {
		switch ch := base[i]; {
		case ch >= 0x80:
			return "", nil
		case ch == ' ' || ch == '.':
		case strings.IndexByte("+,;=[]", ch) >= 0:
			stem = append(stem, '_')
		default:
			stem = append(stem, ch)
		}
	}
	if len(stem) == 0 {
		return "", nil
	}
	hashed := stem
	if len(hashed) > 2 {
		hashed = hashed[:2]
	}
	pattern := `(?:` + regexp.QuoteMeta(string(stem)) + `~[0-9]{1,6}|` + regexp.QuoteMeta(string(hashed)) + `[0-9a-f]{4}~[0-9])`
	// Every short name has a "~"; the hashed one shares too little of the
	// name for a better hint.
	hints := []string{strings.ToLower(string(stem)) + "~", "~"}
	ext = strings.ReplaceAll(ext, " ", "")
	if len(ext) > 3 {
		ext = ext[:3]
	}
	for i := 0; i < len(ext); i++ {
		if ext[i] >= 0x80 {
			return "", nil
		}
	}
	if ext != "" {
		pattern += `(?:\.` + regexp.QuoteMeta(ext) + `)?`
	}
	return pattern, hints
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
		if (start > 0 && isKeyByte(s[start-1])) || continuesName(s, end) {
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
