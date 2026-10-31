package tui

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DanBradbury/firekeeper/internal/reporter"
	"github.com/DanBradbury/firekeeper/internal/session"
	"github.com/DanBradbury/firekeeper/internal/transcript"
	"github.com/DanBradbury/firekeeper/internal/transcript/antigravity"
	"github.com/DanBradbury/firekeeper/internal/transcript/grok"
	"github.com/DanBradbury/firekeeper/internal/transcript/reader"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

type localUsageEnumerator struct {
	metas []session.Meta
	err   error
}

func (s localUsageEnumerator) Enumerate(ctx context.Context, opts reader.EnumerateOptions) ([]session.Meta, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var metas []session.Meta
	for _, meta := range s.metas {
		if opts.Skip == nil || !opts.Skip(meta) {
			metas = append(metas, meta)
		}
	}
	return metas, s.err
}

func TestLocalUsageGrokHistoryAndPrivacy(t *testing.T) {
	root := t.TempDir()
	cwd := filepath.Join(root, "repo")
	id := "0199c3d4-e5f6-7a8b-9c0d-1e2f3a4b5c6d"
	path := filepath.Join(root, "sessions", url.PathEscape(cwd), id, "updates.jsonl")
	write := func(path, data string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(path, fmt.Sprintf(`{"timestamp":1793429000,"method":"session/update","params":{"sessionId":%q,"update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"Synthetic answer"}}}}`+"\n", id))
	write(filepath.Join(filepath.Dir(path), "usage.json"), fmt.Sprintf(`{"sessionId":%q,"session":{"inputTokens":100,"outputTokens":20,"cachedReadTokens":30,"cacheCreationTokens":10,"usageIsIncomplete":true},"turns":[{"turnNumber":1,"endedAt":"2026-10-30T12:00:00Z","inputTokens":40,"outputTokens":5,"cachedReadTokens":10}]}`, id))
	source := &grok.Source{Home: root}
	skip := func(meta session.Meta) bool { return reporter.ExclusionReason(meta, nil) != "" }
	snapshot, err := fetchLocalUsage(context.Background(), transcript.ProviderGrok, source, skip)
	if err != nil || len(snapshot.Sessions) != 1 || snapshot.Input != 60 ||
		snapshot.Output != 20 || snapshot.Cache != 40 {
		t.Fatalf("session totals or cache accounting differ: error=%v", err)
	}
	var dated int64
	for _, count := range snapshot.Daily {
		dated += count
	}
	if dated != 45 || snapshot.Sessions[0].Tokens != 120 {
		t.Fatal("turn deltas added to cumulative totals or assigned to the wrong date")
	}
	if !strings.Contains(snapshot.Sessions[0].Details[3].Value, "incomplete") {
		t.Fatal("incomplete provider usage not surfaced")
	}
	write(filepath.Join(cwd, ".firekeeper-ignore"), "")
	write(path, "{invalid}\n")
	snapshot, err = fetchLocalUsage(context.Background(), transcript.ProviderGrok, source, skip)
	if err != nil || len(snapshot.Sessions) != 0 {
		t.Fatal("ignored transcript was opened")
	}
}

func TestLocalUsagePartialFailureCancellationAndOverflow(t *testing.T) {
	for _, provider := range []transcript.Provider{transcript.ProviderGrok, transcript.ProviderAntigravity} {
		s := localUsageEnumerator{metas: []session.Meta{{ID: "missing", CWD: t.TempDir()}}}
		snapshot, err := fetchLocalUsage(context.Background(), provider, s, nil)
		if err == nil || len(snapshot.Sessions) != 1 || !strings.Contains(err.Error(), "token history unavailable") {
			t.Fatal("unavailable usage lost usable history or failed silently")
		}
	}
	s := localUsageEnumerator{metas: []session.Meta{{Input: math.MaxInt64, Output: 1}}}
	if _, err := fetchLocalUsage(context.Background(), transcript.ProviderCursor, s, nil); err == nil {
		t.Fatal("overflow accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := fetchLocalUsage(ctx, transcript.ProviderGrok, s, nil); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation identity lost")
	}
}

func TestAntigravityUsagePageReadsDatedDatabaseTurns(t *testing.T) {
	root, cwd := t.TempDir(), t.TempDir()
	dir := filepath.Join(root, "antigravity-cli")
	path := filepath.Join(dir, "brain", "fixture", ".system_generated", "logs", "transcript.jsonl")
	for _, parent := range []string{filepath.Dir(path), filepath.Join(dir, "cache"), filepath.Join(dir, "conversations")} {
		if err := os.MkdirAll(parent, 0700); err != nil {
			t.Fatal(err)
		}
	}
	log := `{"step_index":0,"type":"USER_INPUT","source":"USER_EXPLICIT","status":"DONE","content":"Synthetic request"}` + "\n"
	if err := os.WriteFile(path, []byte(log), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cache", "last_conversations.json"), []byte(fmt.Sprintf("{%q:%q}", cwd, "fixture")), 0600); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "conversations", "fixture.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec("CREATE TABLE gen_metadata(idx INTEGER PRIMARY KEY, data BLOB)"); err != nil {
		t.Fatal(err)
	}
	bytesField := func(field uint64, data []byte) []byte {
		prefix := binary.AppendUvarint(nil, field<<3|2)
		return append(binary.AppendUvarint(prefix, uint64(len(data))), data...)
	}
	now := time.Now()
	stamp := binary.AppendUvarint([]byte{0x08}, uint64(now.Unix()))
	// Observed nesting: chat(1), usage(4), timestamp wrapper(9)/time(4).
	chat := bytesField(4, []byte{0x10, 0x64, 0x18, 0x0f, 0x28, 0x14})
	chat = append(chat, bytesField(9, bytesField(4, stamp))...)
	blob := bytesField(1, chat)
	if _, err := db.Exec("INSERT INTO gen_metadata(idx,data) VALUES(0,?)", blob); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	snapshot, err := fetchLocalUsage(context.Background(), transcript.ProviderAntigravity, &antigravity.Source{Dir: dir}, nil)
	if err != nil || snapshot.Input != 100 || snapshot.Output != 15 || snapshot.Cache != 20 ||
		len(snapshot.Sessions) != 1 || snapshot.Daily[now.Format("2006-01-02")] != 135 {
		t.Fatalf("Antigravity dated Usage path failed: %v", err)
	}
	m := NewModel()
	m.usageProvider = antigravityProvider
	m.antigravityUsage.Snapshot = snapshot
	page := m.usagePage()
	overview := m.localOverview(antigravityProvider, now)
	if page.Label != "Antigravity CLI" || page.Sessions[0].Tokens != 135 ||
		overview.Today != 135 || overview.Week != 135 || overview.Lifetime != 135 {
		t.Fatal("dated usage was not reflected in Usage and Overview")
	}
}

func TestLocalUsageNavigationMessagesAndWidth(t *testing.T) {
	m := NewModel()
	m.activeTab = usageTab
	m.usageProvider = claudeProvider
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRight})
	m = updated.(model)
	if cmd == nil || m.usageProvider != grokProvider || !m.grokUsage.Loading {
		t.Fatal("new Usage provider did not start asynchronous refresh")
	}
	msg := localUsageResultMsg{provider: grokProvider, refreshed: time.Now(),
		snapshot: localUsageSnapshot{Sessions: []usageSession{{ID: "fixture", Title: "History"}}}}
	updated, _ = m.Update(msg)
	m = updated.(model)
	if m.grokUsage.Loading || len(m.usagePage().Sessions) != 1 || m.usageProviderName() != "grok" {
		t.Fatal("typed usage result not applied")
	}
	for _, kind := range []usageProvider{grokProvider, antigravityProvider} {
		m.usageProvider = kind
		for _, width := range []int{24, 80, 160} {
			m.width, m.height = width, 40
			if !strings.Contains(ansi.Strip(m.usageProviderSelectorLine()), "[ "+strings.ToUpper(m.usageProviderName())+" ]") {
				t.Fatal("active provider chip was clipped")
			}
			for _, line := range strings.Split(m.View(), "\n") {
				if ansi.StringWidth(line) > width {
					t.Fatal("new usage rendering exceeded terminal width")
				}
			}
		}
	}
	m.usageProvider = antigravityProvider
	m.cycleUsageProvider(1)
	if m.usageProvider != codexProvider {
		t.Fatal("provider navigation did not wrap")
	}
}
