package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

var allUsageSortColumns = []usageSortColumn{usageSortNone, usageSortTokens, usageSortUpdated, usageSortColumn(99)}

func TestUsageSortNextCyclesDescendingAscendingOff(t *testing.T) {
	tests := []struct {
		from   usageSort
		column usageSortColumn
		want   usageSort
	}{
		{usageSort{}, usageSortTokens, usageSort{column: usageSortTokens}},
		{usageSort{column: usageSortTokens}, usageSortTokens, usageSort{column: usageSortTokens, ascending: true}},
		{usageSort{column: usageSortTokens, ascending: true}, usageSortTokens, usageSort{}},
		{usageSort{}, usageSortUpdated, usageSort{column: usageSortUpdated}},
		{usageSort{column: usageSortUpdated}, usageSortUpdated, usageSort{column: usageSortUpdated, ascending: true}},
		{usageSort{column: usageSortUpdated, ascending: true}, usageSortUpdated, usageSort{}},
		// Another column starts over at descending.
		{usageSort{column: usageSortTokens, ascending: true}, usageSortUpdated, usageSort{column: usageSortUpdated}},
		{usageSort{column: usageSortUpdated}, usageSortTokens, usageSort{column: usageSortTokens}},
		// Columns that cannot be sorted leave the order alone.
		{usageSort{column: usageSortTokens}, usageSortNone, usageSort{column: usageSortTokens}},
		{usageSort{column: usageSortTokens}, usageSortColumn(99), usageSort{column: usageSortTokens}},
		{usageSort{}, usageSortColumn(-1), usageSort{}},
	}
	for _, tt := range tests {
		if got := tt.from.next(tt.column); got != tt.want {
			t.Errorf("%+v.next(%d) = %+v, want %+v", tt.from, tt.column, got, tt.want)
		}
	}
}

func TestUsageSortLabelsArrowsAndKeys(t *testing.T) {
	tests := []struct {
		sort        usageSort
		label       string
		tokens, ago string
	}{
		{usageSort{}, "", "", ""},
		{usageSort{column: usageSortTokens}, "most tokens", "▼", ""},
		{usageSort{column: usageSortTokens, ascending: true}, "fewest tokens", "▲", ""},
		{usageSort{column: usageSortUpdated}, "newest", "", "▲"},
		{usageSort{column: usageSortUpdated, ascending: true}, "oldest", "", "▼"},
		{usageSort{column: usageSortColumn(99)}, "", "", ""},
	}
	for _, tt := range tests {
		if got := tt.sort.label(); got != tt.label {
			t.Errorf("%+v label = %q, want %q", tt.sort, got, tt.label)
		}
		if got := tt.sort.arrow(usageSortTokens); got != tt.tokens {
			t.Errorf("%+v tokens arrow = %q, want %q", tt.sort, got, tt.tokens)
		}
		if got := tt.sort.arrow(usageSortUpdated); got != tt.ago {
			t.Errorf("%+v ago arrow = %q, want %q", tt.sort, got, tt.ago)
		}
		if got := tt.sort.arrow(usageSortNone); got != "" {
			t.Errorf("%+v none arrow = %q", tt.sort, got)
		}
	}
	for _, column := range allUsageSortColumns {
		want := map[usageSortColumn]rune{usageSortTokens: 't', usageSortUpdated: 'a'}[column]
		if got := usageSortKey(column); got != want {
			t.Errorf("usageSortKey(%d) = %q, want %q", column, got, want)
		}
	}
}

func TestSortUsageSessions(t *testing.T) {
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	sessions := []usageSession{
		{ID: "b", Tokens: 50, UpdatedAt: now.Add(-time.Hour)},
		{ID: "a", Tokens: 50, UpdatedAt: now.Add(-time.Hour)},
		{ID: "big", Tokens: 900, UpdatedAt: now.Add(-48 * time.Hour)},
		{ID: "recent", Tokens: 50, UpdatedAt: now},
		{ID: "never", Tokens: 0},
	}
	tests := []struct {
		sort usageSort
		want string
	}{
		{usageSort{}, "[b a big recent never]"},
		{usageSort{column: usageSortColumn(99)}, "[b a big recent never]"},
		// Equal tokens: more recent first, then by id.
		{usageSort{column: usageSortTokens}, "[big recent a b never]"},
		{usageSort{column: usageSortTokens, ascending: true}, "[never recent a b big]"},
		{usageSort{column: usageSortUpdated}, "[recent a b big never]"},
		{usageSort{column: usageSortUpdated, ascending: true}, "[never big a b recent]"},
	}
	for _, tt := range tests {
		input := append([]usageSession(nil), sessions...)
		var got []string
		for _, session := range sortUsageSessions(input, tt.sort) {
			got = append(got, session.ID)
		}
		if fmt.Sprint(got) != tt.want {
			t.Errorf("%+v = %v, want %s", tt.sort, got, tt.want)
		}
		for index := range sessions {
			if input[index].ID != sessions[index].ID {
				t.Fatalf("%+v reordered its input", tt.sort)
			}
		}
	}
	if got := sortUsageSessions(nil, usageSort{column: usageSortTokens}); len(got) != 0 {
		t.Fatalf("nil input = %v", got)
	}
}

func usageListHeader(t *testing.T, m model) string {
	t.Helper()
	return strings.Split(ansi.Strip(m.View()), "\n")[rowOf(t, m, "TITLE")]
}

func TestUsageSortKeysReorderKeepSelectionAndSaySo(t *testing.T) {
	m := filterTestModel(t)
	m.codexHistoryCursor = 2 // write docs
	steps := []struct {
		key    rune
		order  string
		title  string
		header string
	}{
		{'t', "[id-refactor id-docs id-fix-auth id-fix-ci]", "sessions · 2/4 · most tokens", "TOKENS▼"},
		{'t', "[id-fix-ci id-fix-auth id-docs id-refactor]", "sessions · 3/4 · fewest tokens", "TOKENS▲"},
		{'t', "[id-refactor id-fix-auth id-docs id-fix-ci]", "sessions · 3/4─", "TOKENS   AGO"},
		{'a', "[id-refactor id-fix-auth id-docs id-fix-ci]", "sessions · 3/4 · newest", "AGO▲"},
		{'a', "[id-fix-ci id-docs id-fix-auth id-refactor]", "sessions · 2/4 · oldest", "AGO▼"},
	}
	for _, step := range steps {
		m, _ = sendKeys(t, m, keyRunes(step.key))
		if got := fmt.Sprint(usageSessionIDs(m)); got != step.order {
			t.Fatalf("after %q: order = %s, want %s", step.key, got, step.order)
		}
		page := m.usagePage()
		if page.Sessions[page.Cursor].ID != "id-docs" {
			t.Fatalf("after %q: selection moved to %q", step.key, page.Sessions[page.Cursor].ID)
		}
		view := ansi.Strip(m.View())
		if !strings.Contains(view, step.title) {
			t.Fatalf("after %q: title missing %q:\n%s", step.key, step.title, view)
		}
		if header := usageListHeader(t, m); !strings.Contains(header, step.header) {
			t.Fatalf("after %q: header %q missing %q", step.key, header, step.header)
		}
	}
}

func TestUsageSortComposesWithFilter(t *testing.T) {
	m := filterTestModel(t)
	m, _ = sendKeys(t, m, append(append([]tea.KeyMsg{keyRunes('/')}, typed("fix")...), tea.KeyMsg{Type: tea.KeyEnter}, keyRunes('t'), keyRunes('t'))...)
	if got := fmt.Sprint(usageSessionIDs(m)); got != "[id-fix-ci id-fix-auth]" {
		t.Fatalf("filtered and sorted = %s", got)
	}
	// The selection follows id-fix-auth, now second.
	if view := ansi.Strip(m.View()); !strings.Contains(view, `sessions · 2/2 of 4 · filter "fix" · fewest tokens`) {
		t.Fatalf("title does not show both the filter and the order:\n%s", view)
	}
	m, cmd := sendKeys(t, m, tea.KeyMsg{Type: tea.KeyHome}, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil || m.chat.meta.ID != "id-fix-ci" {
		t.Fatalf("opened %q, want the first sorted match id-fix-ci", m.chat.meta.ID)
	}
}

func TestUsageSortKeysOnlyApplyToUsage(t *testing.T) {
	m := filterTestModel(t)
	m.activeTab = processesTab
	m, _ = sendKeys(t, m, keyRunes('t'), keyRunes('a'))
	if m.usageSort != (usageSort{}) {
		t.Fatalf("sort changed outside Usage: %+v", m.usageSort)
	}
	// While a filter is typed, t and a are text.
	m = filterTestModel(t)
	m, _ = sendKeys(t, m, keyRunes('/'), keyRunes('t'), keyRunes('a'))
	if m.usageSort != (usageSort{}) || m.usageFilter != "ta" {
		t.Fatalf("sort=%+v filter=%q", m.usageSort, m.usageFilter)
	}
}

// headerCell returns the screen cell of the first character of label in the
// sessions header row.
func headerCell(t *testing.T, m model, label string) cell {
	t.Helper()
	y := rowOf(t, m, "TITLE")
	row := strings.Split(ansi.Strip(m.screen()), "\n")[y]
	index := strings.Index(row, label)
	if index < 0 {
		t.Fatalf("header row has no %q: %q", label, row)
	}
	return cell{ansi.StringWidth(row[:index]), y}
}

func TestClickingColumnHeadersSorts(t *testing.T) {
	now := fakeClock(t)
	for _, width := range []int{140, 120, 90, 70} {
		m := filterTestModel(t)
		m.width = width
		tokens := headerCell(t, m, "TOKENS")
		m = press(m, tokens.x+2, tokens.y)
		if m.usageSort != (usageSort{column: usageSortTokens}) {
			t.Fatalf("width %d: click TOKENS = %+v", width, m.usageSort)
		}
		// A quick second click reverses the order; it is not a double click.
		*now = now.Add(50 * time.Millisecond)
		m = press(m, tokens.x, tokens.y)
		if m.usageSort != (usageSort{column: usageSortTokens, ascending: true}) {
			t.Fatalf("width %d: second click TOKENS = %+v", width, m.usageSort)
		}
		*now = now.Add(time.Second)
		ago := headerCell(t, m, "AGO")
		m = press(m, ago.x, ago.y)
		if m.usageSort != (usageSort{column: usageSortUpdated}) {
			t.Fatalf("width %d: click AGO = %+v", width, m.usageSort)
		}
		*now = now.Add(time.Second)
		title := headerCell(t, m, "TITLE")
		m = press(m, title.x, title.y)
		if m.usageSort != (usageSort{column: usageSortUpdated}) || m.chat.open {
			t.Fatalf("width %d: click TITLE changed the view: sort %+v", width, m.usageSort)
		}
		if h := m.hitTest(m.width-1, ago.y); h.kind == hitUsageSort {
			t.Fatalf("width %d: right border hit %+v", width, h)
		}
	}
}

func TestRightClickColumnHeaderOffersSorting(t *testing.T) {
	m := filterTestModel(t)
	tokens := headerCell(t, m, "TOKENS")
	next, _ := m.Update(tea.MouseMsg{X: tokens.x, Y: tokens.y, Button: tea.MouseButtonRight, Action: tea.MouseActionPress})
	m = next.(model)
	if m.menu == nil || len(m.menu.items) != 3 || m.menu.items[0].label != "Sort by tokens" || m.menu.items[1].label != "Sort by age" {
		t.Fatalf("menu = %+v", m.menu)
	}
	m, _ = sendKeys(t, m, tea.KeyMsg{Type: tea.KeyDown}, tea.KeyMsg{Type: tea.KeyEnter})
	if m.usageSort != (usageSort{column: usageSortUpdated}) {
		t.Fatalf("menu Sort by age = %+v", m.usageSort)
	}
}

func TestSortedUsageListIsWidthSafe(t *testing.T) {
	for _, order := range []usageSort{{column: usageSortTokens}, {column: usageSortUpdated, ascending: true}} {
		for _, size := range [][2]int{{140, 50}, {120, 40}, {90, 30}, {70, 24}, {40, 24}, {30, 14}} {
			m := filterTestModel(t)
			m.width, m.height = size[0], size[1]
			m.usageSort, m.usageFilter = order, "i"
			rows := m.height - chromeRows - 1
			lines := strings.Split(m.viewUsagePage(rows), "\n")
			if len(lines) != rows {
				t.Fatalf("%v: rows = %d, want %d", size, len(lines), rows)
			}
			for _, line := range lines {
				if got := ansi.StringWidth(line); got != m.width {
					t.Fatalf("%v %+v: line width = %d", size, order, got)
				}
			}
			for y := range m.height {
				for x := range m.width {
					_ = m.hitTest(x, y)
				}
			}
			if got := ansi.StringWidth(m.usageFooter()); got != m.width {
				t.Fatalf("%v: footer width = %d", size, got)
			}
		}
	}
}
