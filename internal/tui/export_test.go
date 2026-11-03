package tui

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DanBradbury/firekeeper/internal/serverbin"
	"github.com/DanBradbury/firekeeper/internal/session"
	"github.com/DanBradbury/firekeeper/pkg/event"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// shareServerEnv makes the test binary act as `firekeeper-server share`:
// "ok" writes a page summarizing what it read, "old" fails as a server from
// before share does, and "fail" reports an error.
const shareServerEnv = "FIREKEEPER_TEST_SHARE_SERVER"

func runShareServerHelper() {
	mode := os.Getenv(shareServerEnv)
	if mode == "" || len(os.Args) != 2 || os.Args[1] != "share" {
		return
	}
	switch mode {
	case "old":
		fmt.Fprintln(os.Stderr, `firekeeper-server: unknown command "share"`)
		os.Exit(2)
	case "fail":
		fmt.Fprintln(os.Stderr, "firekeeper-server share: read event 3: bad input\nsecond line")
		os.Exit(1)
	}
	dec := json.NewDecoder(bufio.NewReader(os.Stdin))
	var header shareHeader
	if err := dec.Decode(&header); err != nil {
		os.Exit(3)
	}
	var texts []string
	for {
		var e event.Event
		if err := dec.Decode(&e); err != nil {
			break
		}
		texts = append(texts, e.SessionID+"|"+e.Text)
	}
	out, _ := json.Marshal(struct {
		Header shareHeader `json:"header"`
		Texts  []string    `json:"texts"`
	}{header, texts})
	fmt.Printf("<!doctype html><script type=\"application/json\">%s</script>", out)
	os.Exit(0)
}

var noServerLookup = serverbin.Lookup{
	Getenv: func(string) string { return "" },
	Executable: func() (string, error) {
		return filepath.Join(os.TempDir(), "firekeeper-no-such-dir", "firekeeper"), nil
	},
	LookPath: func(string) (string, error) { return "", exec.ErrNotFound },
}

// useShareServer points the export at the test binary in mode and at a
// temporary home, which it returns.
func useShareServer(t *testing.T, mode string, downloads bool) string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	if downloads {
		if err := os.Mkdir(filepath.Join(home, "Downloads"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	savedLookup, savedHome := exportLookup, exportHome
	t.Cleanup(func() { exportLookup, exportHome = savedLookup, savedHome })
	exportLookup = noServerLookup
	exportLookup.Getenv = func(k string) string {
		if k == serverbin.Env {
			return self
		}
		return ""
	}
	exportHome = func() (string, error) { return home, nil }
	t.Setenv(shareServerEnv, mode)
	return home
}

// exportTestModel opens the chat test session, whose transcript and cwd sit
// under home, and adds a file edit to it.
func exportTestModel(t *testing.T, home string) model {
	t.Helper()
	m, path := chatTestModel(t)
	edit := `{"type":"assistant","timestamp":"2026-10-13T09:01:00Z","message":{"id":"msg_edit","role":"assistant","content":[{"type":"tool_use","id":"t2","name":"Edit","input":{"file_path":"` +
		filepath.ToSlash(filepath.Join(home, "src", "demo", "main.go")) + `"}}]}}` + "\n"
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	file.WriteString(edit)
	file.Close()
	m.processGroups[0].sessions[0].cwd = filepath.Join(home, "src", "demo")
	m.processGroups[0].sessions[0].model = "claude-test"
	m.processGroups[0].sessions[0].state = session.SessionStateWaiting
	return openTestChat(t, m)
}

func pressExport(t *testing.T, m model) (model, exportResultMsg) {
	t.Helper()
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	m = updated.(model)
	if cmd == nil {
		t.Fatal("e started no export")
	}
	if !strings.Contains(m.terminalStatus, "exporting") {
		t.Fatalf("status = %q, want an exporting notice", m.terminalStatus)
	}
	msg := exportResult(t, cmd)
	updated, _ = m.Update(msg)
	return updated.(model), msg
}

// exportResult runs cmd, which Update batches with the status expiry timer,
// and returns the export's result without waiting for the timer.
func exportResult(t *testing.T, cmd tea.Cmd) exportResultMsg {
	t.Helper()
	results := make(chan tea.Msg, 8)
	var run func(tea.Cmd)
	run = func(c tea.Cmd) {
		msg := c()
		if batch, ok := msg.(tea.BatchMsg); ok {
			for _, c := range batch {
				if c != nil {
					go run(c)
				}
			}
			return
		}
		results <- msg
	}
	go run(cmd)
	timeout := time.After(time.Minute)
	for {
		select {
		case msg := <-results:
			if result, ok := msg.(exportResultMsg); ok {
				return result
			}
		case <-timeout:
			t.Fatal("export did not finish")
		}
	}
}

func TestChatExportWritesPageToDownloads(t *testing.T) {
	home := useShareServer(t, "ok", true)
	m := exportTestModel(t, home)
	m, msg := pressExport(t, m)

	result := msg
	if result.err != nil {
		t.Fatalf("export failed: %v", result.err)
	}
	want := filepath.Join(home, "Downloads", "firekeeper-claude-"+chatTestSessionID+".html")
	if result.path != want {
		t.Fatalf("saved to %q, want %q", result.path, want)
	}
	if !strings.Contains(m.terminalStatus, "saved "+filepath.Join("~", "Downloads")) {
		t.Fatalf("status = %q", m.terminalStatus)
	}
	if m.chat.exporting {
		t.Fatal("export still marked running")
	}
	page, err := os.ReadFile(want)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Header shareHeader `json:"header"`
		Texts  []string    `json:"texts"`
	}
	body := strings.TrimSuffix(strings.TrimPrefix(string(page), `<!doctype html><script type="application/json">`), "</script>")
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("page is not what the server wrote: %v", err)
	}
	s := got.Header.Session
	if s.SessionID != chatTestSessionID || s.Provider != "claude" || s.Title != "Chat test" || s.Model != "claude-test" ||
		s.State != session.SessionStateWaiting || s.Project != "demo" || s.Origin != "local" {
		t.Fatalf("session header = %+v", s)
	}
	if s.EventCount != int64(len(got.Texts)) || s.EventCount < 4 || s.StartedAt == nil || s.LastActivityAt == nil {
		t.Fatalf("event count %d for %d events, times %v %v", s.EventCount, len(got.Texts), s.StartedAt, s.LastActivityAt)
	}
	if s.CWD != filepath.Join("~", "src", "demo") && s.CWD != "~/src/demo" {
		t.Fatalf("cwd %q not redacted to ~", s.CWD)
	}
	if len(got.Header.Files) != 1 || got.Header.Files[0].Changes != 1 || strings.Contains(got.Header.Files[0].Path, home) {
		t.Fatalf("files = %+v", got.Header.Files)
	}
	if !strings.Contains(strings.Join(got.Texts, "\n"), chatTestSessionID+"|Please add a chat view") {
		t.Fatalf("events missing the user prompt or session id: %q", got.Texts)
	}
	leftovers, _ := filepath.Glob(filepath.Join(home, "Downloads", ".firekeeper-export-*"))
	if len(leftovers) > 0 {
		t.Fatalf("temporary files left behind: %v", leftovers)
	}
}

func TestChatExportWithoutDownloadsUsesFirekeeperFolder(t *testing.T) {
	home := useShareServer(t, "ok", false)
	_, msg := pressExport(t, exportTestModel(t, home))
	result := msg
	if result.err != nil || filepath.Dir(result.path) != filepath.Join(home, ".firekeeper", "exports") {
		t.Fatalf("export = %q, %v", result.path, result.err)
	}
}

func TestChatExportFailures(t *testing.T) {
	tests := []struct {
		mode, want string
	}{
		{"old", "cannot export; run `firekeeper serve` to update it"},
		{"fail", "export failed: firekeeper-server share: read event 3: bad input"},
	}
	for _, tt := range tests {
		t.Run(tt.mode, func(t *testing.T) {
			home := useShareServer(t, tt.mode, true)
			m, _ := pressExport(t, exportTestModel(t, home))
			if !strings.Contains(m.terminalStatus, tt.want) || strings.Contains(m.terminalStatus, "second line") {
				t.Fatalf("status = %q, want %q", m.terminalStatus, tt.want)
			}
			entries, _ := os.ReadDir(filepath.Join(home, "Downloads"))
			if len(entries) != 0 {
				t.Fatalf("failed export left %d files", len(entries))
			}
		})
	}
}

func TestChatExportWithoutServer(t *testing.T) {
	home := t.TempDir()
	savedHome := exportHome
	t.Cleanup(func() { exportHome = savedHome })
	exportHome = func() (string, error) { return home, nil }
	m, _ := pressExport(t, exportTestModel(t, home))
	if !strings.Contains(m.terminalStatus, "needs firekeeper-server") {
		t.Fatalf("status = %q", m.terminalStatus)
	}
}

func TestChatExportIgnoresRepeatWhileRunning(t *testing.T) {
	home := useShareServer(t, "ok", true)
	m := exportTestModel(t, home)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	if _, cmd := updated.(model).Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}}); cmd != nil {
		t.Fatal("second e started another export")
	}
}

func TestExportFilename(t *testing.T) {
	tests := []struct {
		meta session.Meta
		want string
	}{
		{session.Meta{ID: "abc-123", Provider: "codex"}, "firekeeper-codex-abc-123.html"},
		{session.Meta{ID: "../x y", Provider: "co/dex"}, "firekeeper-codex-..-x-y.html"},
		{session.Meta{}, "firekeeper-session.html"},
		{session.Meta{ID: strings.Repeat("a", 80), Provider: "claude"}, "firekeeper-claude-" + strings.Repeat("a", 64) + ".html"},
	}
	for _, tt := range tests {
		if got := exportFilename(tt.meta); got != tt.want {
			t.Errorf("exportFilename(%+v) = %q, want %q", tt.meta, got, tt.want)
		}
	}
}

// clickFooter left-clicks the middle of the footer row.
func clickFooter(m model) (model, tea.Cmd) {
	updated, cmd := m.Update(tea.MouseMsg{X: m.width / 2, Y: m.height - 1, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	return updated.(model), cmd
}

func stubOpenLocalFile(t *testing.T, err error) *[]string {
	t.Helper()
	var opened []string
	saved := openLocalFile
	t.Cleanup(func() { openLocalFile = saved })
	openLocalFile = func(path string) error {
		opened = append(opened, path)
		return err
	}
	return &opened
}

func TestClickingExportNoticeOpensPage(t *testing.T) {
	home := useShareServer(t, "ok", true)
	opened := stubOpenLocalFile(t, nil)
	m, result := pressExport(t, exportTestModel(t, home))
	if !strings.Contains(ansi.Strip(m.View()), "click to open") {
		t.Fatalf("footer does not offer to open the page:\n%s", ansi.Strip(m.View()))
	}

	// A click above the footer does not open it.
	updated, _ := m.Update(tea.MouseMsg{X: 5, Y: m.height - 2, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	if m = updated.(model); len(*opened) != 0 {
		t.Fatal("a click outside the footer opened the page")
	}

	m, cmd := clickFooter(m)
	if cmd == nil {
		t.Fatal("clicking the notice did nothing")
	}
	updated, _ = m.Update(cmd())
	m = updated.(model)
	if len(*opened) != 1 || (*opened)[0] != result.path {
		t.Fatalf("opened %q, want %q", *opened, result.path)
	}
	if m.terminalStatus != "" {
		t.Fatalf("status = %q after opening", m.terminalStatus)
	}

	// The notice is gone, so a second click opens nothing.
	if _, cmd := clickFooter(m); cmd != nil {
		cmd()
	}
	if len(*opened) != 1 {
		t.Fatal("a click after the notice went opened the page again")
	}
}

func TestExportNoticeOpensOnlyWhileShown(t *testing.T) {
	home := useShareServer(t, "ok", true)
	opened := stubOpenLocalFile(t, nil)
	m, _ := pressExport(t, exportTestModel(t, home))

	// The Usage tab's footer has no status, so the notice is not there.
	usage := m
	usage.chat = chatState{}
	usage.activeTab = usageTab
	if usage.exportNoticeShown() {
		t.Fatal("notice counted as shown on the Usage tab")
	}

	// Another status replaces the notice.
	m.terminalStatus = "switched to Ghostty"
	if _, cmd := clickFooter(m); cmd != nil {
		cmd()
	}
	if len(*opened) != 0 {
		t.Fatal("clicking another status opened the export")
	}
}

func TestExportNoticeOutlastsOtherStatuses(t *testing.T) {
	home := useShareServer(t, "ok", true)
	m := exportTestModel(t, home)
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	m = updated.(model)
	result := exportResult(t, cmd)
	updated, _ = m.Update(result)
	m = updated.(model)
	if m.terminalStatus != exportSavedStatus(result.path) || exportNoticeLifetime <= terminalStatusLifetime {
		t.Fatalf("status = %q", m.terminalStatus)
	}
	// An expiry timer from the "exporting" notice must not clear it.
	updated, _ = m.Update(terminalStatusExpiredMsg{seq: m.terminalStatusSeq - 1})
	if updated.(model).terminalStatus == "" {
		t.Fatal("an older status's timer cleared the export notice")
	}
}

func TestExportOpenFailureShowsStatus(t *testing.T) {
	home := useShareServer(t, "ok", true)
	stubOpenLocalFile(t, errors.New("no application knows this file"))
	m, _ := pressExport(t, exportTestModel(t, home))
	m, cmd := clickFooter(m)
	updated, _ := m.Update(cmd())
	if status := updated.(model).terminalStatus; !strings.Contains(status, "could not open the export: no application") {
		t.Fatalf("status = %q", status)
	}
}

func TestOpenFileCommand(t *testing.T) {
	tests := []struct {
		goos, name string
		args       []string
	}{
		{"darwin", "open", []string{"/x/a b.html"}},
		{"linux", "xdg-open", []string{"/x/a b.html"}},
		{"windows", "rundll32", []string{"url.dll,FileProtocolHandler", "/x/a b.html"}},
	}
	for _, tt := range tests {
		name, args := openFileCommand(tt.goos, "/x/a b.html")
		if name != tt.name || strings.Join(args, "|") != strings.Join(tt.args, "|") {
			t.Errorf("%s: %s %q, want %s %q", tt.goos, name, args, tt.name, tt.args)
		}
	}
}

func TestOKeyOpensShownExport(t *testing.T) {
	home := useShareServer(t, "ok", true)
	opened := stubOpenLocalFile(t, nil)
	m, result := pressExport(t, exportTestModel(t, home))
	if !strings.Contains(m.terminalStatus, "o or click to open") {
		t.Fatalf("status = %q", m.terminalStatus)
	}
	o := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'o'}}

	// From the chat view.
	updated, cmd := m.Update(o)
	if cmd == nil {
		t.Fatal("o in the chat view did nothing")
	}
	updated, _ = updated.(model).Update(cmd())
	if len(*opened) != 1 || (*opened)[0] != result.path || updated.(model).terminalStatus != "" {
		t.Fatalf("opened %q, status %q", *opened, updated.(model).terminalStatus)
	}
	// With the notice gone, o does nothing.
	if _, cmd := updated.(model).Update(o); cmd != nil {
		cmd()
	}
	if len(*opened) != 1 {
		t.Fatal("o opened the page after its notice went")
	}

	// From Processes, after leaving the chat while the notice shows.
	back, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if !back.(model).exportNoticeShown() {
		t.Fatal("notice not shown on Processes after leaving the chat")
	}
	updated, cmd = back.(model).Update(o)
	if cmd == nil {
		t.Fatal("o on Processes did nothing")
	}
	updated.(model).Update(cmd())
	if len(*opened) != 2 {
		t.Fatalf("opened %q, want a second open from Processes", *opened)
	}
}
