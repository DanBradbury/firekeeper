package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func TestParseUsageQuery(t *testing.T) {
	tests := []struct {
		raw      string
		text     string
		projects string
	}{
		{"", "", "[]"},
		{"   ", "", "[]"},
		{"session", "session", "[]"},
		{"project:firekeeper session", "session", "[firekeeper]"},
		{"session project:firekeeper", "session", "[firekeeper]"},
		{"PROJECT:FireKeeper", "", "[FireKeeper]"},
		{"fix  the   parser", "fix the parser", "[]"},
		{`project:"my app" fix ci`, "fix ci", "[my app]"},
		{`project:"my app`, "", "[my app]"}, // still being typed
		{`project:"a\"b\\c"`, "", `[a"b\c]`},
		{"project:api project:web", "", "[api web]"},
		{`"fix ci" tests`, "fix ci tests", "[]"},
		// Not qualifiers: no value yet, unknown keys, a quoted key.
		{"project:", "", "[]"},
		{`project:""`, "", "[]"},
		{"project: session", "session", "[]"},
		{"fix: parser", "fix: parser", "[]"},
		{"https://example.com", "https://example.com", "[]"},
		{`"project:x"`, "project:x", "[]"},
		{":x", ":x", "[]"},
		{"projects:x", "projects:x", "[]"},
	}
	for _, tt := range tests {
		got := parseUsageQuery(tt.raw)
		if got.Text != tt.text || fmt.Sprint(got.Projects) != tt.projects {
			t.Errorf("parse %q = text %q projects %v, want %q %s", tt.raw, got.Text, got.Projects, tt.text, tt.projects)
		}
	}
}

func TestUsageProjectQualifierRoundTrips(t *testing.T) {
	for _, name := range []string{"firekeeper", "my app", `a"b`, `back\slash`, "tab\there", "ünïcode", "unknown", " lead"} {
		written := usageProjectQualifier(name)
		if got := parseUsageQuery(written).Projects; len(got) != 1 || got[0] != strings.TrimSpace(name) {
			t.Errorf("%q written as %q parses to %q", name, written, got)
		}
	}
	if got := usageProjectQualifier("plain"); got != "project:plain" {
		t.Errorf("plain name written as %q", got)
	}
}

func TestWithUsageProject(t *testing.T) {
	tests := []struct {
		raw, project, want string
	}{
		{"", "beta", "project:beta"},
		{"fix ci", "beta", "project:beta fix ci"},
		{"project:beta", "beta", ""},
		{"project:BETA fix", "beta", "fix"},
		{"project:alpha fix", "beta", "project:beta fix"},
		{"project:alpha project:beta", "beta", "project:beta"},
		{`fix "a b"`, "my app", `project:"my app" fix "a b"`},
		{"project:alpha fix", "", "fix"},
		{"fix", "", "fix"},
	}
	for _, tt := range tests {
		if got := withUsageProject(tt.raw, tt.project); got != tt.want {
			t.Errorf("withUsageProject(%q, %q) = %q, want %q", tt.raw, tt.project, got, tt.want)
		}
	}
}

func TestUsageQueryProjectAndTextTyped(t *testing.T) {
	m := filterTestModel(t)
	m.codexUsage.History = append(m.codexUsage.History,
		codexHistoricalSession{ID: "id-beta-session", Title: "session store", WorkDir: "/work/beta", Tokens: 10, UpdatedAt: time.Now().Add(-6 * time.Hour)},
		codexHistoricalSession{ID: "id-alpha-session", Title: "session cleanup", WorkDir: "/work/alpha", Tokens: 10, UpdatedAt: time.Now().Add(-7 * time.Hour)},
	)
	m, _ = sendKeys(t, m, append([]tea.KeyMsg{keyRunes('/')}, typed("project:BETA")...)...)
	m, _ = sendKeys(t, m, tea.KeyMsg{Type: tea.KeySpace})
	m, _ = sendKeys(t, m, append(typed("session"), tea.KeyMsg{Type: tea.KeyEnter})...)
	if got := fmt.Sprint(usageSessionIDs(m)); got != "[id-beta-session]" {
		t.Fatalf("sessions = %s", got)
	}
	view := ansi.Strip(m.View())
	for _, want := range []string{`sessions · 1/1 of 6 · project BETA · filter "session"`, "filter project:BETA session", "▸ beta"} {
		if !strings.Contains(view, want) {
			t.Fatalf("view missing %q:\n%s", want, view)
		}
	}
	// The projects panel still offers every project with a match for the text.
	if !strings.Contains(view, "  alpha") {
		t.Fatalf("projects panel dropped alpha:\n%s", view)
	}

	// p on the shown project takes the qualifier out and keeps the text.
	m, _ = sendKeys(t, m, keyRunes('p'))
	if m.usageFilter != "session" || fmt.Sprint(usageSessionIDs(m)) != "[id-beta-session id-alpha-session]" {
		t.Fatalf("after p: filter %q sessions %v", m.usageFilter, usageSessionIDs(m))
	}
	// / continues as a new word after a kept query.
	m, _ = sendKeys(t, m, keyRunes('/'))
	if m.usageFilter != "session " {
		t.Fatalf("/ after a kept query: %q", m.usageFilter)
	}
}

func TestUsageQueryMultipleProjectsAndUnknownKeys(t *testing.T) {
	m := filterTestModel(t)
	m.usageFilter = "project:alpha project:gamma"
	if got := fmt.Sprint(usageSessionIDs(m)); got != "[id-refactor id-fix-ci]" {
		t.Fatalf("either project = %s", got)
	}
	view := ansi.Strip(m.View())
	if !strings.Contains(view, "project alpha or gamma") || !strings.Contains(view, "▸ alpha") || !strings.Contains(view, "▸ gamma") {
		t.Fatalf("both projects not shown as applied:\n%s", view)
	}
	m.usageFilter = "nope:beta"
	if got := usageSessionIDs(m); len(got) != 0 {
		t.Fatalf("unknown key is text, so nothing has the title %q: %v", m.usageFilter, got)
	}
	if view := ansi.Strip(m.View()); !strings.Contains(view, `No Codex sessions match "nope:beta".`) {
		t.Fatalf("unknown key not explained:\n%s", view)
	}
}

func TestPaintUsageQueryKeepsTextAndWidth(t *testing.T) {
	for _, raw := range []string{"", "fix", "project:beta fix", `  project:"my app"  x `, `"unterminated`, "fix: x"} {
		painted := paintUsageQuery(raw)
		if got := ansi.Strip(painted); got != raw {
			t.Errorf("paint %q shows %q", raw, got)
		}
	}
}

func FuzzParseUsageQuery(f *testing.F) {
	for _, seed := range []string{"", "project:firekeeper session", `project:"my app" fix`, `project:"a\"b`, "fix: x", `"`, `\`, "project:", "PROJECT:x project:y", "a\u00a0b", "project:\x00"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		query := parseUsageQuery(raw)
		if query.Text != strings.TrimSpace(query.Text) || strings.Contains(query.Text, "  ") && !strings.Contains(raw, `"`) {
			t.Fatalf("text %q is not trimmed and single-spaced", query.Text)
		}
		for _, project := range query.Projects {
			if strings.TrimSpace(project) == "" || project != strings.TrimSpace(project) {
				t.Fatalf("project %q from %q", project, raw)
			}
			// What p writes for a project parses back to it.
			if got := parseUsageQuery(usageProjectQualifier(project)).Projects; len(got) != 1 || got[0] != project {
				t.Fatalf("%q round-trips to %q", project, got)
			}
		}
		for _, word := range splitUsageQuery(raw) {
			if word.start < 0 || word.end > len(raw) || word.start >= word.end {
				t.Fatalf("word span [%d,%d) in %d bytes", word.start, word.end, len(raw))
			}
			if !strings.ContainsFunc(raw[word.start:word.end], func(r rune) bool { return r == '"' }) &&
				strings.ContainsFunc(raw[word.start:word.end], unicode.IsSpace) {
				t.Fatalf("unquoted word %q has a space", raw[word.start:word.end])
			}
		}
		if printable := strings.ToValidUTF8(raw, ""); printable == raw {
			if got := ansi.Strip(paintUsageQuery(raw)); got != raw && !strings.ContainsFunc(raw, func(r rune) bool { return !unicode.IsPrint(r) && !unicode.IsSpace(r) }) {
				t.Fatalf("paint %q shows %q", raw, got)
			}
		}
		// Toggling a project on and off leaves the other words.
		on := withUsageProject(raw, "beta")
		if got := parseUsageQuery(on); len(got.Projects) != 1 || got.Projects[0] != "beta" || got.Text != query.Text {
			t.Fatalf("withUsageProject(%q) = %q parses to %+v", raw, on, got)
		}
		if got := parseUsageQuery(withUsageProject(on, "beta")); len(got.Projects) != 0 || got.Text != query.Text {
			t.Fatalf("toggling beta off %q gives %+v", on, got)
		}
	})
}
