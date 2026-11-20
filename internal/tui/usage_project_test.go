package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// queryProjects is the project: qualifiers of m's filter, comma-joined.
func queryProjects(m model) string {
	return strings.Join(parseUsageQuery(m.usageFilter).Projects, ",")
}

func TestFilterUsageProjectMatchesWholeNames(t *testing.T) {
	sessions := []usageSession{
		{ID: "a", WorkDir: "/w/alpha"},
		{ID: "b", WorkDir: "/other/alpha"},
		{ID: "c", WorkDir: "/w/alpha-two"},
		{ID: "d"},
	}
	tests := []struct {
		project string
		want    string
	}{
		{"", "[a b c d]"},
		{"alpha", "[a b]"}, // same name in two places, as the projects panel merges them
		{"alpha-two", "[c]"},
		{"unknown", "[d]"},
		{"alp", "[]"},
		{"ALPHA", "[a b]"},
		{"/w/alpha", "[]"},
	}
	for _, tt := range tests {
		got := []string{}
		var projects []string
		if tt.project != "" {
			projects = []string{tt.project}
		}
		for _, session := range filterUsageProjects(sessions, projects) {
			got = append(got, session.ID)
		}
		if fmt.Sprint(got) != tt.want {
			t.Errorf("project %q = %v, want %s", tt.project, got, tt.want)
		}
	}
	if got := filterUsageProjects(nil, []string{"alpha"}); len(got) != 0 {
		t.Fatalf("nil input = %v", got)
	}
}

func TestUsageProjectRows(t *testing.T) {
	for _, tt := range []struct{ totals, rows, want int }{
		{3, 5, 3}, {5, 5, 5}, {6, 5, 4}, {2, 1, 0}, {1, 1, 1}, {3, 1, 0}, {0, 4, 0}, {3, 0, 0},
	} {
		if got := usageProjectRows(tt.totals, tt.rows); got != tt.want {
			t.Errorf("usageProjectRows(%d, %d) = %d, want %d", tt.totals, tt.rows, got, tt.want)
		}
	}
}

func TestUsageProjectsShownKeepsTheActiveProject(t *testing.T) {
	totals := []usageProjectTotal{{name: "a"}, {name: "b"}, {name: "c"}, {name: "d"}, {name: "e"}}
	tests := []struct {
		rows   int
		active string
		want   string
	}{
		{3, "", "[0 1]"},
		{3, "a", "[0 1]"},
		{3, "b", "[0 1]"},
		{3, "d", "[0 3]"},
		{3, "e", "[0 4]"},
		{3, "missing", "[0 1]"},
		{6, "e", "[0 1 2 3 4]"},
		{1, "e", "[]"},
		{0, "a", "[]"},
	}
	for _, tt := range tests {
		if got := fmt.Sprint(usageProjectsShown(totals, tt.rows, activeList(tt.active))); got != tt.want {
			t.Errorf("rows %d active %q = %s, want %s", tt.rows, tt.active, got, tt.want)
		}
	}
}

func activeList(active string) []string {
	if active == "" {
		return nil
	}
	return []string{active}
}

// projectPanelCell returns a screen cell on the projects panel row naming
// project; the session list on the left may show the same name.
func projectPanelCell(t *testing.T, m model, project string) cell {
	t.Helper()
	left := m.width - usageRightWidth(m.width)
	for y, row := range strings.Split(ansi.Strip(m.screen()), "\n") {
		right := []rune(row)
		if len(right) <= left {
			continue
		}
		if text := string(right[left:]); strings.Contains(text, " "+project+" ") && strings.Contains(text, "×") {
			return cell{left + 3, y}
		}
	}
	t.Fatalf("no projects panel row for %q:\n%s", project, ansi.Strip(m.screen()))
	return cell{}
}

func TestUsageProjectKeyShowsOnlyThatProject(t *testing.T) {
	m := filterTestModel(t)
	m.codexHistoryCursor = 2 // write docs, in auth-service
	m, _ = sendKeys(t, m, keyRunes('p'))
	if queryProjects(m) != "auth-service" {
		t.Fatalf("project = %q", queryProjects(m))
	}
	if got := fmt.Sprint(usageSessionIDs(m)); got != "[id-docs]" {
		t.Fatalf("sessions = %s", got)
	}
	view := ansi.Strip(m.View())
	for _, want := range []string{"Sessions · 1/1 of 4 · project auth-service", "filter project:auth-service", "/ edit · esc clear", "▸ auth-service", "Projects · last 4", "  alpha"} {
		if !strings.Contains(view, want) {
			t.Fatalf("view missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "refactor parser") {
		t.Fatalf("view lists a session from another project:\n%s", view)
	}
	if footer := ansi.Strip(m.usageFooter()); !strings.Contains(footer, "esc clear") {
		t.Fatalf("footer does not say how to clear the project: %q", footer)
	}

	m, _ = sendKeys(t, m, keyRunes('p'))
	if queryProjects(m) != "" || len(usageSessionIDs(m)) != 4 {
		t.Fatalf("second p: project %q, %d sessions", queryProjects(m), len(usageSessionIDs(m)))
	}
	if page := m.usagePage(); page.Sessions[page.Cursor].ID != "id-docs" {
		t.Fatalf("selection moved to %q", page.Sessions[page.Cursor].ID)
	}
	if view := ansi.Strip(m.View()); strings.Contains(view, "project auth-service") || strings.Contains(view, "▸ auth-service") {
		t.Fatalf("cleared project still shown:\n%s", view)
	}
}

func TestUsageProjectComposesWithFilterAndSort(t *testing.T) {
	m := filterTestModel(t)
	m.codexUsage.History = append(m.codexUsage.History,
		codexHistoricalSession{ID: "id-fix-beta", Title: "fix beta flake", WorkDir: "/work/beta", Tokens: 9_000, UpdatedAt: time.Now().Add(-5 * time.Hour)})
	m.codexHistoryCursor = 1 // Fix auth bug, in beta
	m, _ = sendKeys(t, m, keyRunes('p'), keyRunes('t'), keyRunes('/'))
	m, _ = sendKeys(t, m, append(typed("fix"), tea.KeyMsg{Type: tea.KeyEnter})...)
	if got := fmt.Sprint(usageSessionIDs(m)); got != "[id-fix-beta id-fix-auth]" {
		t.Fatalf("sessions = %s", got)
	}
	view := ansi.Strip(m.View())
	for _, want := range []string{`Sessions · 1/2 of 5 · project beta · filter "fix" · most tokens`, "filter project:beta fix", "/ edit · esc clear"} {
		if !strings.Contains(view, want) {
			t.Fatalf("view missing %q:\n%s", want, view)
		}
	}
	// The projects panel keeps every project matching the query to pick.
	if !strings.Contains(view, "▸ beta") || !strings.Contains(view, "  gamma") {
		t.Fatalf("projects panel lost projects:\n%s", view)
	}

	m, _ = sendKeys(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if queryProjects(m) != "" || m.usageFilter != "" || len(usageSessionIDs(m)) != 5 {
		t.Fatalf("Esc left project %q filter %q", queryProjects(m), m.usageFilter)
	}
	if m.usageSort != (usageSort{column: usageSortTokens}) {
		t.Fatalf("Esc changed the order: %+v", m.usageSort)
	}
}

func TestUsageProjectBelowTheFoldStaysInProjectsPanel(t *testing.T) {
	m := filterTestModel(t)
	m.height = 36 // the projects panel lists two projects and "+2 more"
	m.codexHistoryCursor = 1
	m, _ = sendKeys(t, m, keyRunes('p'))
	view := ansi.Strip(m.View())
	if !strings.Contains(view, "▸ beta") || !strings.Contains(view, "+2 more") {
		t.Fatalf("shown project beta is hidden in the projects panel:\n%s", view)
	}
	beta := projectPanelCell(t, m, "beta")
	if m = press(m, beta.x, beta.y); queryProjects(m) != "" {
		t.Fatalf("click on the shown project did not show every project: %q", queryProjects(m))
	}
}

func TestUsageProjectWithoutMatchesSaysSo(t *testing.T) {
	m := filterTestModel(t)
	m.usageFilter = "project:gamma auth"
	view := ansi.Strip(m.View())
	for _, want := range []string{"Sessions · 0 of 4 · project gamma", `No Codex sessions in project gamma match "auth".`} {
		if !strings.Contains(view, want) {
			t.Fatalf("view missing %q:\n%s", want, view)
		}
	}
	// A project another provider has no sessions in: p shows every project.
	m.usageFilter = "project:elsewhere"
	if !strings.Contains(ansi.Strip(m.View()), "No Codex sessions in project elsewhere.") {
		t.Fatalf("empty project not explained:\n%s", ansi.Strip(m.View()))
	}
	m, _ = sendKeys(t, m, keyRunes('p'))
	if queryProjects(m) != "" || len(usageSessionIDs(m)) != 4 {
		t.Fatalf("p on an empty list: project %q", queryProjects(m))
	}
}

func TestUsageProjectKeyOnlyAppliesToUsage(t *testing.T) {
	m := filterTestModel(t)
	m.activeTab = processesTab
	m, _ = sendKeys(t, m, keyRunes('p'))
	if queryProjects(m) != "" {
		t.Fatalf("p outside Usage set project %q", queryProjects(m))
	}
	m = filterTestModel(t)
	m, _ = sendKeys(t, m, keyRunes('/'), keyRunes('p'))
	if queryProjects(m) != "" || m.usageFilter != "p" {
		t.Fatalf("p while typing: project %q filter %q", queryProjects(m), m.usageFilter)
	}
}

func TestClickingProjectsPanelShowsOneProject(t *testing.T) {
	now := fakeClock(t)
	for _, width := range []int{140, 120, 90} {
		m := filterTestModel(t)
		m.width = width
		gamma := projectPanelCell(t, m, "gamma")
		if h := m.hitTest(gamma.x, gamma.y); h.kind != hitUsageProject {
			t.Fatalf("width %d: hit = %+v", width, h)
		}
		m = press(m, gamma.x, gamma.y)
		if queryProjects(m) != "gamma" || fmt.Sprint(usageSessionIDs(m)) != "[id-fix-ci]" {
			t.Fatalf("width %d: click gamma: project %q sessions %v", width, queryProjects(m), usageSessionIDs(m))
		}
		// A quick click on another project switches to it.
		*now = now.Add(50 * time.Millisecond)
		alpha := projectPanelCell(t, m, "alpha")
		m = press(m, alpha.x, alpha.y)
		if queryProjects(m) != "alpha" {
			t.Fatalf("width %d: click alpha: project %q", width, queryProjects(m))
		}
		*now = now.Add(50 * time.Millisecond)
		m = press(m, alpha.x, alpha.y)
		if queryProjects(m) != "" {
			t.Fatalf("width %d: second click alpha: project %q", width, queryProjects(m))
		}
		*now = now.Add(time.Second)
		// The panel border and the session list are not project rows.
		if h := m.hitTest(m.width-1, alpha.y); h.kind == hitUsageProject {
			t.Fatalf("width %d: border hit %+v", width, h)
		}
		if h := m.hitTest(5, rowOf(t, m, "fix ci")); h.kind != hitUsageSession {
			t.Fatalf("width %d: session row hit %+v", width, h)
		}
	}
}

func TestProjectsPanelMoreLineIsNotClickable(t *testing.T) {
	m := filterTestModel(t)
	m.width, m.height = 120, 40
	for i := range 12 {
		m.codexUsage.History = append(m.codexUsage.History, codexHistoricalSession{
			ID: fmt.Sprintf("extra-%02d", i), Title: "extra", WorkDir: fmt.Sprintf("/work/extra-%02d", i), Tokens: int64(i), UpdatedAt: time.Now(),
		})
	}
	more := rowOf(t, m, "more")
	left := m.width - usageRightWidth(m.width)
	if h := m.hitTest(left+3, more); h.kind != hitNone {
		t.Fatalf("+N more line hit %+v", h)
	}
	// Every listed project row maps back to the name drawn there.
	totals := usageProjectTotals(m.usagePage().ProjectSessions)
	lines := strings.Split(ansi.Strip(m.screen()), "\n")
	hits := 0
	for y := range m.height {
		h := m.hitTest(left+3, y)
		if h.kind != hitUsageProject {
			continue
		}
		hits++
		if row := string([]rune(lines[y])[left:]); !strings.Contains(row, " "+totals[h.index].name+" ") {
			t.Fatalf("row %d hit %q but shows %q", y, totals[h.index].name, row)
		}
	}
	if hits == 0 {
		t.Fatal("no project rows were clickable")
	}
}

func TestUsageProjectListIsWidthSafe(t *testing.T) {
	for _, size := range [][2]int{{140, 50}, {120, 40}, {90, 30}, {70, 24}, {40, 24}, {30, 14}} {
		m := filterTestModel(t)
		m.codexUsage.History = append(m.codexUsage.History, codexHistoricalSession{
			ID: "long", Title: "long", WorkDir: "/work/" + strings.Repeat("very-long-project-name-", 6), UpdatedAt: time.Now(),
		})
		m.width, m.height = size[0], size[1]
		m.usageFilter = usageProjectQualifier(usageProjectName(usageSession{WorkDir: "/work/" + strings.Repeat("very-long-project-name-", 6)})) + " lo"
		m.usageFilterEditing = true
		rows := m.height - chromeRows - 1
		lines := strings.Split(m.viewUsagePage(rows), "\n")
		if len(lines) != rows {
			t.Fatalf("%v: rows = %d, want %d", size, len(lines), rows)
		}
		for _, line := range lines {
			if got := ansi.StringWidth(line); got != m.width {
				t.Fatalf("%v: line width = %d", size, got)
			}
		}
		for y := range m.height {
			for x := range m.width {
				_ = m.hitTest(x, y)
			}
		}
		m.usageFilterEditing = false
		if got := ansi.StringWidth(m.usageFooter()); got != m.width {
			t.Fatalf("%v: footer width = %d", size, got)
		}
	}
}
