// Package redact scrubs secrets and personal paths from transcript events
// before they leave the machine.
//
// Redaction is best effort. It recognizes common, well-formed secret shapes
// (AWS access key IDs, GitHub tokens, "sk-" style API keys, Bearer tokens,
// PEM private key blocks, JWTs, and KEY=value assignments whose key names
// mention SECRET, TOKEN, PASSWORD, or KEY) and replaces each match with
// "[REDACTED:<kind>]". It cannot recognize every credential: passwords in
// prose, secrets with unusual formats, values split across records, and
// encoded or encrypted data pass through unchanged. Treat redacted output as
// sensitive.
//
// Redaction is idempotent: redacting already-redacted output changes nothing.
package redact

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/DanBradbury/firekeeper/internal/transcript"
)

// Kind names a class of scrubbed content. It appears in the replacement
// marker "[REDACTED:<kind>]".
type Kind string

const (
	KindPrivateKey       Kind = "private_key"
	KindJWT              Kind = "jwt"
	KindGitHubToken      Kind = "github_token"
	KindAWSAccessKey     Kind = "aws_access_key"
	KindAPIKey           Kind = "api_key"
	KindBearerToken      Kind = "bearer_token"
	KindSecretAssignment Kind = "secret_assignment"
	// KindHomeDir counts home directory prefixes rewritten to "~". It is not
	// a secret and has no "[REDACTED:...]" marker.
	KindHomeDir Kind = "home_dir"
)

// Kinds lists every kind in the order redaction applies them.
func Kinds() []Kind {
	return []Kind{
		KindPrivateKey, KindJWT, KindGitHubToken, KindAWSAccessKey,
		KindAPIKey, KindBearerToken, KindSecretAssignment, KindHomeDir,
	}
}

// Marker returns the replacement text for kind.
func Marker(kind Kind) string {
	return "[REDACTED:" + string(kind) + "]"
}

const markerPrefix = "[REDACTED:"

// Counts records how many replacements were made per kind. A secret that
// appears in both Text and Raw is counted once per occurrence.
type Counts map[Kind]int

// Add merges other into c.
func (c Counts) Add(other Counts) {
	for kind, n := range other {
		c[kind] += n
	}
}

// Total returns the number of replacements across all kinds.
func (c Counts) Total() int {
	total := 0
	for _, n := range c {
		total += n
	}
	return total
}

// Options configures redaction.
type Options struct {
	// HomeDir, when set, is rewritten to "~" wherever it appears as a whole
	// path prefix ("/Users/me/src" becomes "~/src"; "/Users/meg" is left
	// alone).
	HomeDir string
}

// Event returns a copy of e with Text and every string in Raw redacted,
// plus the number of replacements per kind. Raw keeps its structure, key
// order, and formatting; only changed strings are re-encoded. Object keys
// are redacted too, since paths and assignments sometimes appear there. If
// Raw is not valid JSON it is redacted as plain text.
func Event(e transcript.Event, opts Options) (transcript.Event, Counts) {
	counts := Counts{}
	e.Text = redactString(e.Text, opts, counts)
	if len(e.Raw) > 0 {
		e.Raw = redactJSON(e.Raw, opts, counts)
	}
	return e, counts
}

// String redacts s and returns the result plus the number of replacements
// per kind.
func String(s string, opts Options) (string, Counts) {
	counts := Counts{}
	return redactString(s, opts, counts), counts
}

// JSON redacts every string in a JSON document, leaving the rest of the
// bytes as they were. The input is never modified. Invalid JSON is redacted
// as plain text.
func JSON(raw []byte, opts Options) ([]byte, Counts) {
	counts := Counts{}
	return redactJSON(raw, opts, counts), counts
}

func redactString(s string, opts Options, counts Counts) string {
	for _, d := range detectors {
		s = d.apply(s, counts)
	}
	s = redactAssignments(s, counts)
	return redactHome(s, opts.HomeDir, counts)
}

// detector replaces regular expression matches with a marker. hints are
// literal substrings, at least one of which every match contains; they let
// typical text skip the regular expression entirely.
type detector struct {
	kind  Kind
	hints []string
	re    *regexp.Regexp
	// group, when non-zero, is the submatch replaced instead of the whole
	// match.
	group int
	// accept, when set, rejects matches that are shaped right but unlikely
	// to be secrets.
	accept func(string) bool
}

var detectors = []detector{
	{
		kind:  KindPrivateKey,
		hints: []string{"PRIVATE KEY-----"},
		// A block with no END line (for example, cut off output) is
		// redacted to the end of the text.
		re: regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----(?:.*?-----END [A-Z0-9 ]*PRIVATE KEY-----|.*)`),
	},
	{
		kind:  KindJWT,
		hints: []string{"eyJ"},
		re:    regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]*`),
	},
	{
		kind:  KindGitHubToken,
		hints: []string{"ghp_", "gho_", "ghu_", "ghs_", "ghr_", "github_pat_"},
		re:    regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9]{36,}|github_pat_[A-Za-z0-9_]{22,})\b`),
	},
	{
		kind:  KindAWSAccessKey,
		hints: []string{"AKIA", "ASIA"},
		re:    regexp.MustCompile(`\b(?:AKIA|ASIA)[A-Z0-9]{16}\b`),
	},
	{
		kind:   KindAPIKey,
		hints:  []string{"sk-", "sk_live_", "sk_test_"},
		re:     regexp.MustCompile(`\b(?:sk-[A-Za-z0-9][A-Za-z0-9_-]{19,}|sk_(?:live|test)_[A-Za-z0-9]{16,})`),
		accept: hasDigit,
	},
	{
		kind:  KindBearerToken,
		hints: []string{"earer"},
		re:    regexp.MustCompile(`\b[Bb]earer\s+([A-Za-z0-9._~+/=-]{8,})`),
		group: 1,
		accept: func(token string) bool {
			return hasDigit(token) || (len(token) >= 20 && hasUpper(token) && hasLower(token))
		},
	},
}

func (d detector) apply(s string, counts Counts) string {
	if !containsAny(s, d.hints) {
		return s
	}
	matches := d.re.FindAllStringSubmatchIndex(s, -1)
	if len(matches) == 0 {
		return s
	}
	marker := Marker(d.kind)
	var b strings.Builder
	last := 0
	for _, m := range matches {
		start, end := m[2*d.group], m[2*d.group+1]
		if start < 0 || (d.accept != nil && !d.accept(s[start:end])) {
			continue
		}
		b.WriteString(s[last:start])
		b.WriteString(marker)
		last = end
		counts[d.kind]++
	}
	if last == 0 {
		return s
	}
	b.WriteString(s[last:])
	return b.String()
}

// secretKeyWords are the key-name fragments that mark an assignment as
// secret, matched case-insensitively anywhere in the key.
var secretKeyWords = []string{"SECRET", "TOKEN", "PASSWORD", "KEY"}

// redactAssignments replaces the value in KEY=value assignments whose key
// mentions a secret word. Unquoted values run to whitespace, a quote, or a
// separator (&, ;, <, >, `). Spaces around "=" (or ":=") are accepted only
// when the value is quoted, which keeps ordinary code comparisons and
// expressions out. Values that reference another variable ($NAME, %NAME%)
// or are already redacted are left alone.
func redactAssignments(s string, counts Counts) string {
	var b strings.Builder
	last := 0
	for pos := 0; pos < len(s); {
		i := strings.IndexByte(s[pos:], '=')
		if i < 0 {
			break
		}
		eq := pos + i
		pos = eq + 1
		if pos < len(s) && s[pos] == '=' {
			pos++ // "==" is a comparison
			continue
		}

		keyEnd := eq
		if keyEnd > 0 && s[keyEnd-1] == ':' {
			keyEnd--
		}
		spacedKey := keyEnd
		for spacedKey > 0 && (s[spacedKey-1] == ' ' || s[spacedKey-1] == '\t') {
			spacedKey--
		}
		valStart := pos
		for valStart < len(s) && (s[valStart] == ' ' || s[valStart] == '\t') {
			valStart++
		}
		spaced := spacedKey != keyEnd || valStart != pos
		keyEnd = spacedKey

		keyStart := keyEnd
		for keyStart > 0 && isKeyByte(s[keyStart-1]) {
			keyStart--
		}
		if keyStart == keyEnd || !isSecretKey(s[keyStart:keyEnd]) {
			continue
		}

		var vStart, vEnd int
		if valStart < len(s) && (s[valStart] == '"' || s[valStart] == '\'') {
			quote := s[valStart]
			vStart = valStart + 1
			vEnd = vStart
			for vEnd < len(s) && s[vEnd] != quote && s[vEnd] != '\n' {
				vEnd++
			}
		} else {
			if spaced {
				continue
			}
			vStart, vEnd = valStart, valStart
			for vEnd < len(s) && !isValueEnd(s[vEnd]) {
				vEnd++
			}
		}
		pos = vEnd
		value := s[vStart:vEnd]
		if value == "" || value[0] == '$' || value[0] == '%' || strings.HasPrefix(value, markerPrefix) {
			continue
		}
		b.WriteString(s[last:vStart])
		b.WriteString(Marker(KindSecretAssignment))
		last = vEnd
		counts[KindSecretAssignment]++
	}
	if last == 0 {
		return s
	}
	b.WriteString(s[last:])
	return b.String()
}

func isSecretKey(key string) bool {
	for _, word := range secretKeyWords {
		if containsFold(key, word) {
			return true
		}
	}
	return false
}

// containsFold reports whether s contains the upper-case ASCII word,
// ignoring ASCII case.
func containsFold(s, word string) bool {
	for i := 0; i+len(word) <= len(s); i++ {
		match := true
		for j := 0; j < len(word); j++ {
			c := s[i+j]
			if 'a' <= c && c <= 'z' {
				c -= 'a' - 'A'
			}
			if c != word[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

func isKeyByte(c byte) bool {
	return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || c == '_' || c == '-' || c == '.'
}

func isValueEnd(c byte) bool {
	switch c {
	case ' ', '\t', '\n', '\r', '"', '\'', '`', '&', ';', '<', '>':
		return true
	}
	return false
}

// redactHome rewrites home to "~" where it is a whole path prefix: not
// preceded by a path-name byte and followed by a separator or a non-path
// byte.
func redactHome(s, home string, counts Counts) string {
	home = strings.TrimRight(home, `/\`)
	if home == "" || !strings.Contains(s, home) {
		return s
	}
	var b strings.Builder
	last := 0
	for pos := 0; pos < len(s); {
		i := strings.Index(s[pos:], home)
		if i < 0 {
			break
		}
		start := pos + i
		end := start + len(home)
		pos = start + 1
		if start > 0 && isKeyByte(s[start-1]) {
			continue
		}
		if end < len(s) && isKeyByte(s[end]) {
			continue
		}
		b.WriteString(s[last:start])
		b.WriteByte('~')
		last = end
		pos = end
		counts[KindHomeDir]++
	}
	if last == 0 {
		return s
	}
	b.WriteString(s[last:])
	return b.String()
}

// redactJSON redacts each string literal in raw. Output is built lazily, so
// a document with nothing to redact is returned as is.
func redactJSON(raw []byte, opts Options, counts Counts) []byte {
	if !json.Valid(raw) {
		return []byte(redactString(string(raw), opts, counts))
	}
	var out []byte
	last := 0
	for pos := 0; pos < len(raw); {
		i := bytes.IndexByte(raw[pos:], '"')
		if i < 0 {
			break
		}
		start := pos + i
		end, escaped := stringEnd(raw, start)
		pos = end

		literal := raw[start:end]
		var value string
		if escaped {
			var ok bool
			if value, ok = unquote(literal); !ok {
				continue
			}
		} else {
			value = string(literal[1 : len(literal)-1])
		}
		redacted := redactString(value, opts, counts)
		if redacted == value {
			continue
		}
		if out == nil {
			out = make([]byte, 0, len(raw)+64)
		}
		out = append(out, raw[last:start]...)
		out = appendJSONString(out, redacted)
		last = end
	}
	if out == nil {
		return raw
	}
	return append(out, raw[last:]...)
}

// stringEnd returns the index just past the string literal opening at
// start, and whether the literal contains escapes.
func stringEnd(raw []byte, start int) (int, bool) {
	escaped := false
	for i := start + 1; i < len(raw); i++ {
		switch raw[i] {
		case '\\':
			escaped = true
			i++
		case '"':
			return i + 1, escaped
		}
	}
	return len(raw), escaped
}

// unquote decodes a JSON string literal that contains escapes. It handles
// the common escapes directly and defers to encoding/json for anything else,
// such as surrogate pairs and invalid UTF-8.
func unquote(literal []byte) (string, bool) {
	body := literal[1 : len(literal)-1]
	if !utf8.Valid(body) {
		return unquoteSlow(literal)
	}
	var b strings.Builder
	b.Grow(len(body))
	for {
		i := bytes.IndexByte(body, '\\')
		if i < 0 {
			b.Write(body)
			return b.String(), true
		}
		b.Write(body[:i])
		if i+1 >= len(body) {
			return "", false
		}
		switch c := body[i+1]; c {
		case '"', '\\', '/':
			b.WriteByte(c)
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		case 'r':
			b.WriteByte('\r')
		case 'b':
			b.WriteByte('\b')
		case 'f':
			b.WriteByte('\f')
		case 'u':
			if i+6 > len(body) {
				return "", false
			}
			r, err := strconv.ParseUint(string(body[i+2:i+6]), 16, 16)
			if err != nil {
				return "", false
			}
			if utf16.IsSurrogate(rune(r)) {
				return unquoteSlow(literal)
			}
			b.WriteRune(rune(r))
			body = body[i+6:]
			continue
		default:
			return "", false
		}
		body = body[i+2:]
	}
}

func unquoteSlow(literal []byte) (string, bool) {
	var value string
	if json.Unmarshal(literal, &value) != nil {
		return "", false
	}
	return value, true
}

func appendJSONString(dst []byte, s string) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s) // encoding a string cannot fail
	return append(dst, bytes.TrimSuffix(buf.Bytes(), []byte("\n"))...)
}

func containsAny(s string, hints []string) bool {
	for _, hint := range hints {
		if strings.Contains(s, hint) {
			return true
		}
	}
	return false
}

func hasDigit(s string) bool {
	return strings.ContainsAny(s, "0123456789")
}

func hasUpper(s string) bool {
	return strings.ContainsAny(s, "ABCDEFGHIJKLMNOPQRSTUVWXYZ")
}

func hasLower(s string) bool {
	return strings.ContainsAny(s, "abcdefghijklmnopqrstuvwxyz")
}
