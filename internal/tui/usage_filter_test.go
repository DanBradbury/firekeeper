package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func filterTestModel(t *testing.T) model {
	t.Helper()
	m := testModel()
	m.activeTab = usageTab
	m.width, m.height = 120, 40
	withUsageData(&m, codexProvider)
	now := time.Now()
	m.codexUsage.History = []codexHistoricalSession{
		{ID: "id-refactor", Title: "refactor parser", WorkDir: "/work/alpha", Tokens: 3_000, UpdatedAt: now.Add(-time.Hour)},
		{ID: "id-fix-auth", Title: "Fix auth bug", WorkDir: "/work/beta", Tokens: 1_000, UpdatedAt: now.Add(-2 * time.Hour)},
		{ID: "id-docs", Title: "write docs", WorkDir: "/work/auth-service", Tokens: 2_000, UpdatedAt: now.Add(-3 * time.Hour)},
		{ID: "id-fix-ci", Title: "fix ci", WorkDir: "/work/gamma", Tokens: 500, UpdatedAt: now.Add(-4 * time.Hour)},
	}
	return m
}

func sendKeys(t *testing.T, m model, keys ...tea.KeyMsg) (model, tea.Cmd) {
	t.Helper()
	var cmd tea.Cmd
	for _, key := range keys {
		var next tea.Model
		next, cmd = m.Update(key)
		m = next.(model)
	}
	return m, cmd
}

func typed(text string) []tea.KeyMsg {
	keys := make([]tea.KeyMsg, 0, len(text))
	for _, r := range text {
		keys = append(keys, keyRunes(r))
	}
	return keys
}

func usageSessionIDs(m model) []string {
	var ids []string
	for _, session := range m.usagePage().Sessions {
		ids = append(ids, session.ID)
	}
	return ids
}

func TestFilterUsageSessionsRanksTitleMatchesFirst(t *testing.T) {
	sessions := []usageSession{
		{ID: "branch", Title: "add tests", Branch: "feature/AUTH-login", WorkDir: "/w/one"},
		{ID: "title", Title: "Fix auth bug", WorkDir: "/w/two"},
		{ID: "project", Title: "write docs", WorkDir: "/w/auth-service"},
		{ID: "none", Title: "refactor", WorkDir: "/w/three"},
		{ID: "title-2", Title: "OAuth refresh", WorkDir: "/w/four"},
	}
	tests := []struct {
		query string
		want  []string
	}{
		{"auth", []string{"title", "title-2", "branch", "project"}},
		{"  AUTH ", []string{"title", "title-2", "branch", "project"}},
		{"docs", []string{"project"}},
		{"three", []string{"none"}},
		{"", []string{"branch", "title", "project", "none", "title-2"}},
		{"   ", []string{"branch", "title", "project", "none", "title-2"}},
		{"zzz", nil},
		{"/w/", nil}, // the path is not searched, only the project name
	}
	for _, tt := range tests {
		var got []string
		for _, session := range filterUsageSessions(sessions, tt.query) {
			got = append(got, session.ID)
		}
		if fmt.Sprint(got) != fmt.Sprint(tt.want) {
			t.Errorf("query %q = %v, want %v", tt.query, got, tt.want)
		}
	}
}

func TestUsageFilterTypingNarrowsListAndSaysSo(t *testing.T) {
	m := filterTestModel(t)
	m, _ = sendKeys(t, m, keyRunes('/'))
	if !m.usageFilterEditing {
		t.Fatal("/ did not start the filter")
	}
	m, cmd := sendKeys(t, m, typed("FIX q")...)
	if cmd != nil {
		t.Fatal("typing q in the filter must not quit")
	}
	if m.usageFilter != "FIX q" {
		t.Fatalf("filter = %q", m.usageFilter)
	}
	m, _ = sendKeys(t, m, tea.KeyMsg{Type: tea.KeyBackspace}, tea.KeyMsg{Type: tea.KeyBackspace})
	if m.usageFilter != "FIX" {
		t.Fatalf("filter after backspace = %q", m.usageFilter)
	}
	if got := fmt.Sprint(usageSessionIDs(m)); got != "[id-fix-auth id-fix-ci]" {
		t.Fatalf("filtered sessions = %s", got)
	}
	view := ansi.Strip(m.View())
	for _, want := range []string{`sessions · 1/2 of 4 · filter "FIX"`, "filter FIX▏", "enter keep · esc clear", "Fix auth bug", "fix ci"} {
		if !strings.Contains(view, want) {
			t.Fatalf("view missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "refactor parser") {
		t.Fatalf("view shows a session the filter excludes:\n%s", view)
	}
	footer := ansi.Strip(m.usageFooter())
	if !strings.Contains(footer, "enter keep") || !strings.Contains(footer, "esc clear") {
		t.Fatalf("editing footer = %q", footer)
	}

	// Enter keeps the filter and stops editing; the list still says so.
	m, cmd = sendKeys(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil || m.chat.open {
		t.Fatal("Enter while typing must keep the filter, not open a chat")
	}
	if m.usageFilterEditing || m.usageFilter != "FIX" {
		t.Fatalf("after Enter: editing=%v filter=%q", m.usageFilterEditing, m.usageFilter)
	}
	view = ansi.Strip(m.View())
	for _, want := range []string{`filter "FIX"`, "filter FIX", "/ edit · esc clear"} {
		if !strings.Contains(view, want) {
			t.Fatalf("view missing %q after Enter:\n%s", want, view)
		}
	}
	if footer := ansi.Strip(m.usageFooter()); !strings.Contains(footer, "esc clear filter") && !strings.Contains(footer, "esc clear") {
		t.Fatalf("footer does not say how to clear the filter: %q", footer)
	}
}

func TestUsageFilterOpensTheSelectedFilteredSession(t *testing.T) {
	m := filterTestModel(t)
	m, _ = sendKeys(t, m, append(append([]tea.KeyMsg{keyRunes('/')}, typed("fix")...), tea.KeyMsg{Type: tea.KeyDown}, tea.KeyMsg{Type: tea.KeyEnter})...)
	if m.codexHistoryCursor != 1 {
		t.Fatalf("cursor = %d, want 1", m.codexHistoryCursor)
	}
	m, cmd := sendKeys(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil || !m.chat.open {
		t.Fatal("Enter did not open the chat")
	}
	if m.chat.meta.ID != "id-fix-ci" {
		t.Fatalf("opened %q, want the second filtered session id-fix-ci", m.chat.meta.ID)
	}
}

func TestUsageFilterEscClears(t *testing.T) {
	m := filterTestModel(t)
	m, _ = sendKeys(t, m, append([]tea.KeyMsg{keyRunes('/')}, typed("docs")...)...)
	m, _ = sendKeys(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.usageFilterEditing || m.usageFilter != "" || len(usageSessionIDs(m)) != 4 {
		t.Fatalf("Esc while typing: editing=%v filter=%q sessions=%d", m.usageFilterEditing, m.usageFilter, len(usageSessionIDs(m)))
	}

	m, _ = sendKeys(t, m, append(append([]tea.KeyMsg{keyRunes('/')}, typed("docs")...), tea.KeyMsg{Type: tea.KeyEnter})...)
	if len(usageSessionIDs(m)) != 1 {
		t.Fatalf("kept filter shows %v", usageSessionIDs(m))
	}
	m, _ = sendKeys(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.usageFilter != "" || len(usageSessionIDs(m)) != 4 {
		t.Fatalf("Esc did not clear the kept filter: %q", m.usageFilter)
	}
	if view := ansi.Strip(m.View()); strings.Contains(view, `filter "`) || strings.Contains(view, "esc clear") {
		t.Fatalf("cleared filter still shown:\n%s", view)
	}
}

func TestUsageFilterWithoutMatchesSaysSo(t *testing.T) {
	m := filterTestModel(t)
	m, _ = sendKeys(t, m, append([]tea.KeyMsg{keyRunes('/')}, typed("nope")...)...)
	view := ansi.Strip(m.View())
	for _, want := range []string{`sessions · 0 of 4 · filter "nope"`, `No Codex sessions match "nope".`} {
		if !strings.Contains(view, want) {
			t.Fatalf("view missing %q:\n%s", want, view)
		}
	}
	m, cmd := sendKeys(t, m, tea.KeyMsg{Type: tea.KeyEnter}, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil || m.chat.open {
		t.Fatal("Enter with no matching session must not open a chat")
	}
}

func TestUsageFilterNeedsASessionList(t *testing.T) {
	m := testModel()
	m.activeTab = usageTab
	withUsageData(&m, codexProvider)
	m, _ = sendKeys(t, m, keyRunes('/'))
	if m.usageFilterEditing {
		t.Fatal("/ started a filter with no sessions to filter")
	}
	processes := filterTestModel(t)
	processes.activeTab = processesTab
	processes, _ = sendKeys(t, processes, keyRunes('/'))
	if processes.usageFilterEditing {
		t.Fatal("/ started a usage filter on the Processes tab")
	}
}

func TestUsageFilterKeepsOnlyPrintableTextAndCapsLength(t *testing.T) {
	m := filterTestModel(t)
	m, _ = sendKeys(t, m, keyRunes('/'), tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a\x1b[31mb\tc\u200bd"), Paste: true})
	if m.usageFilter != "a[31mbcd" {
		t.Fatalf("filter = %q", m.usageFilter)
	}
	m, _ = sendKeys(t, m, tea.KeyMsg{Type: tea.KeyCtrlU}, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(strings.Repeat("x", 500))})
	if got := len([]rune(m.usageFilter)); got != usageFilterMaxRunes {
		t.Fatalf("filter length = %d, want %d", got, usageFilterMaxRunes)
	}
	for _, line := range strings.Split(m.View(), "\n") {
		if got := ansi.StringWidth(line); got != m.width {
			t.Fatalf("line width = %d, want %d", got, m.width)
		}
	}
}

func TestUsageFilterClickSelectsFilteredRowAndEndsTyping(t *testing.T) {
	m := filterTestModel(t)
	m, _ = sendKeys(t, m, append([]tea.KeyMsg{keyRunes('/')}, typed("fix")...)...)
	row := rowOf(t, m, "fix ci")
	if h := m.hitTest(5, row); h.kind != hitUsageSession || h.index != 1 {
		t.Fatalf("hit at fix ci row = %+v, want filtered index 1", h)
	}
	clicked := press(m, 5, row)
	if clicked.codexHistoryCursor != 1 || clicked.usageFilterEditing || clicked.usageFilter != "fix" {
		t.Fatalf("click: cursor=%d editing=%v filter=%q", clicked.codexHistoryCursor, clicked.usageFilterEditing, clicked.usageFilter)
	}
	if h := m.hitTest(5, rowOf(t, m, "filter fix")); h.kind != hitNone {
		t.Fatalf("filter line hit = %+v, want none", h)
	}
	opened, _ := sendKeys(t, clicked, tea.KeyMsg{Type: tea.KeyEnter})
	if opened.chat.meta.ID != "id-fix-ci" {
		t.Fatalf("opened %q", opened.chat.meta.ID)
	}
}

func TestUsageFilterListIsWidthSafe(t *testing.T) {
	for _, editing := range []bool{true, false} {
		for _, size := range [][2]int{{140, 50}, {120, 40}, {90, 30}, {70, 24}, {40, 24}, {30, 14}} {
			m := filterTestModel(t)
			m.width, m.height = size[0], size[1]
			m.usageFilter, m.usageFilterEditing = "a fairly long query that will not fit", editing
			rows := m.height - chromeRows - 1
			lines := strings.Split(m.viewUsagePage(rows), "\n")
			if len(lines) != rows {
				t.Fatalf("%v: rows = %d, want %d", size, len(lines), rows)
			}
			for _, line := range lines {
				if got := ansi.StringWidth(line); got != m.width {
					t.Fatalf("%v editing=%v: line width = %d", size, editing, got)
				}
			}
			if got := ansi.StringWidth(m.usageFooter()); got != m.width {
				t.Fatalf("%v: footer width = %d", size, got)
			}
		}
	}
}

func FuzzFilterUsageSessions(f *testing.F) {
	for _, seed := range []string{"", "auth", "AUTH", " ", "é", "\x00", "/", "fix ci"} {
		f.Add(seed)
	}
	sessions := []usageSession{
		{ID: "a", Title: "Fix auth bug", WorkDir: "/w/alpha"},
		{ID: "b", Title: "", Branch: "feature/auth", WorkDir: "/w/beta"},
		{ID: "c", Title: "Résumé", WorkDir: ""},
		{ID: "d", Title: "fix ci", WorkDir: "/w/auth"},
	}
	f.Fuzz(func(t *testing.T, query string) {
		got := filterUsageSessions(sessions, query)
		if len(got) > len(sessions) {
			t.Fatalf("%d results from %d sessions", len(got), len(sessions))
		}
		needle := strings.ToLower(strings.TrimSpace(query))
		seen := map[string]bool{}
		titles := true
		for _, session := range got {
			if seen[session.ID] {
				t.Fatalf("session %q returned twice", session.ID)
			}
			seen[session.ID] = true
			if needle == "" {
				continue
			}
			title := strings.Contains(strings.ToLower(session.Title), needle)
			if !title && !strings.Contains(strings.ToLower(usageProjectName(session)), needle) && !strings.Contains(strings.ToLower(session.Branch), needle) {
				t.Fatalf("session %q does not match %q", session.ID, query)
			}
			if title && !titles {
				t.Fatalf("title match %q listed after a project or branch match", session.ID)
			}
			titles = titles && title
		}
		if needle == "" && len(got) != len(sessions) {
			t.Fatalf("empty query dropped sessions")
		}
	})
}
