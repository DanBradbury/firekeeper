package tui

import (
	"strings"
	"unicode"
)

// usage_query.go parses the Usage tab's / filter. A query is words separated
// by spaces. A word of the form key:value with a known key is a qualifier;
// every other word is text, searched as one substring the way the filter
// always has (titles first, then project and branch). Values with spaces are
// quoted: project:"my app". An unknown key such as "fix:" stays text, so a
// title with a colon can still be found.
//
//	project:firekeeper session   sessions in firekeeper whose title has "session"
//	project:api project:web      sessions in either project
//
// A new qualifier adds a usageQueryKey, a field on usageQuery, a case in
// parseUsageQuery and a match in filterUsageSessions.

// usageQueryKey is a qualifier name, matched case-insensitively.
type usageQueryKey string

const (
	// usageQueryProject keeps sessions whose project, the working directory
	// name the projects panel groups by, equals the value, ignoring case.
	usageQueryProject usageQueryKey = "project"
)

// usageQuery is a parsed filter.
type usageQuery struct {
	// Text is searched as one substring; words are joined by single spaces.
	Text string
	// Projects are the project: values; a session in any of them matches.
	Projects []string
}

// empty reports whether the query keeps every session.
func (q usageQuery) empty() bool { return q.Text == "" && len(q.Projects) == 0 }

// usageQueryWord is one word of a raw query: its byte span, and the
// qualifier it sets when it is one.
type usageQueryWord struct {
	start, end int
	key        usageQueryKey
	value      string
	text       string
}

func (w usageQueryWord) qualifier() bool { return w.key != "" }

// splitUsageQuery splits raw into words at unquoted whitespace. A double
// quote groups spaces into a word until the next unescaped quote, or the end
// of the query while it is still being typed; a backslash in quotes escapes
// the next character.
func splitUsageQuery(raw string) []usageQueryWord {
	var words []usageQueryWord
	start, quoted, escaped := -1, false, false
	var unquoted strings.Builder
	flush := func(end int) {
		if start < 0 {
			return
		}
		words = append(words, newUsageQueryWord(raw[start:end], unquoted.String(), start, end))
		start = -1
		unquoted.Reset()
	}
	for index, r := range raw {
		switch {
		case escaped:
			escaped = false
			unquoted.WriteRune(r)
		case quoted && r == '\\':
			escaped = true
		case r == '"':
			if start < 0 {
				start = index
			}
			quoted = !quoted
		case !quoted && unicode.IsSpace(r):
			flush(index)
		default:
			if start < 0 {
				start = index
			}
			unquoted.WriteRune(r)
		}
	}
	flush(len(raw))
	return words
}

// newUsageQueryWord classifies one word. raw is as typed; unquoted has the
// quotes and escapes removed.
func newUsageQueryWord(raw, unquoted string, start, end int) usageQueryWord {
	word := usageQueryWord{start: start, end: end, text: unquoted}
	name, value, ok := strings.Cut(unquoted, ":")
	if !ok {
		return word
	}
	// The key itself is never quoted: "project:x" in quotes is text.
	if rawName, _, _ := strings.Cut(raw, ":"); rawName != name {
		return word
	}
	switch key := usageQueryKey(strings.ToLower(name)); key {
	case usageQueryProject:
		word.key, word.value = key, value
	}
	return word
}

// parseUsageQuery turns a raw / filter into a query. A qualifier with no
// value yet, such as "project:" while it is typed, is left out.
func parseUsageQuery(raw string) usageQuery {
	var query usageQuery
	var text []string
	for _, word := range splitUsageQuery(raw) {
		switch word.key {
		case usageQueryProject:
			if value := strings.TrimSpace(word.value); value != "" {
				query.Projects = append(query.Projects, value)
			}
		default:
			text = append(text, word.text)
		}
	}
	query.Text = strings.TrimSpace(strings.Join(text, " "))
	return query
}

// usageProjectQualifier writes a project: word that parses back to name.
func usageProjectQualifier(name string) string {
	if name != "" && !strings.ContainsFunc(name, func(r rune) bool { return unicode.IsSpace(r) || r == '"' || r == '\\' }) {
		return string(usageQueryProject) + ":" + name
	}
	escaped := strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(name)
	return string(usageQueryProject) + `:"` + escaped + `"`
}

// withUsageProject rewrites raw to show only project, or every project when
// it is already the only one shown or is empty. Other words are kept as
// typed.
func withUsageProject(raw, project string) string {
	query := parseUsageQuery(raw)
	only := len(query.Projects) == 1 && strings.EqualFold(query.Projects[0], project)
	var kept []string
	if project != "" && !only {
		kept = append(kept, usageProjectQualifier(project))
	}
	for _, word := range splitUsageQuery(raw) {
		if word.key != usageQueryProject {
			kept = append(kept, raw[word.start:word.end])
		}
	}
	return strings.Join(kept, " ")
}

// paintUsageQuery colours raw for the filter line: qualifiers teal, text
// orange. raw holds printable runes only (appendUsageFilter).
func paintUsageQuery(raw string) string {
	painted := ""
	last := 0
	for _, word := range splitUsageQuery(raw) {
		painted += dashPaint(raw[last:word.start], usageMutedColor, false)
		if word.qualifier() {
			key, value, _ := strings.Cut(raw[word.start:word.end], ":")
			painted += dashPaint(key+":", dashTeal, false) + dashPaint(value, dashTeal, true)
		} else {
			painted += dashPaint(raw[word.start:word.end], dashOrange, true)
		}
		last = word.end
	}
	return painted + dashPaint(raw[last:], usageMutedColor, false)
}
