package opencode

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/DanBradbury/firekeeper/internal/session"
	"github.com/DanBradbury/firekeeper/internal/testenv"
	"github.com/DanBradbury/firekeeper/internal/transcript"
	"github.com/DanBradbury/firekeeper/internal/transcript/reader"
)

const fixtureID = "ses_fixtureA"

// base is the fixture's time origin, 2026-10-13T12:00:00Z.
var base = time.UnixMilli(1791892800000)

// fixtureDB builds the synthetic database from testdata/opencode.sql.
func fixtureDB(t *testing.T) string {
	t.Helper()
	testenv.NeedTool(t, "sqlite3")
	schema, err := filepath.Abs(filepath.Join("testdata", "opencode.sql"))
	if err != nil {
		t.Fatal(err)
	}
	db := filepath.Join(t.TempDir(), "opencode.db")
	if out, err := exec.Command("sqlite3", db, ".read "+schema).CombinedOutput(); err != nil {
		t.Fatalf("build fixture database: %v: %s", err, out)
	}
	return db
}

// at returns a Source over db whose clock reads base+offset.
func at(db string, offset time.Duration) *Source {
	return &Source{Database: db, now: func() time.Time { return base.Add(offset) }}
}

type want struct {
	seq    int64
	role   transcript.Role
	text   string
	tool   string
	model  string
	tokens transcript.Tokens
}

func TestRead(t *testing.T) {
	db := fixtureDB(t)
	path := session.OpenCodeTranscriptPath(db, fixtureID)
	const model = "model-test"
	expected := []want{
		{seq: 1, role: transcript.RoleMeta},
		{seq: 2, role: transcript.RoleMeta, model: model},
		{seq: 3, role: transcript.RoleUser, text: "Fix the parser."},
		// Undecodable data is kept as meta.
		{seq: 4, role: transcript.RoleMeta},
		// Only tool calls, so the calls and their output are the text.
		{seq: 5, role: transcript.RoleToolCall, tool: "edit", model: model,
			text: `{"path":"parser.go","oldString":"a","newString":"b"}` + "\nEdited parser.go\n" +
				`{"patchText":"*** Begin Patch\n*** Add File: parser_test.go\n+package parser\n*** End Patch"}` + "\nerror: denied",
			tokens: transcript.Tokens{Input: 10, Output: 9, Cache: 500}},
		// A reply with a tool call reads as the reply.
		{seq: 6, role: transcript.RoleAssistant, text: "Fixed the parser.", tool: "read", model: model,
			tokens: transcript.Tokens{Input: 20, Output: 9, Cache: 500}},
		{seq: 7, role: transcript.RoleToolResult, text: "$ go test ./...\nok", tool: "shell"},
		{seq: 9, role: transcript.RoleMeta},
		{seq: 10, role: transcript.RoleUser, text: "Now add tests."},
		{seq: 11, role: transcript.RoleSystem, text: "Parser fixed; tests next.", model: model,
			tokens: transcript.Tokens{Input: 1, Output: 2, Cache: 3}},
	}

	// Message 12 is still being written, so the read stops before it, and
	// before message 13 after it.
	events, end, err := at(db, 20*time.Second).Read(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	if end != 12 {
		t.Errorf("end offset = %d, want 12", end)
	}
	check(t, events, expected)
	again, againEnd, err := at(db, 20*time.Second).Read(path, end)
	if err != nil || len(again) != 0 || againEnd != end {
		t.Errorf("read at unfinished message = %d events, offset %d, %v; want none at %d", len(again), againEnd, err, end)
	}

	// Left unchanged for settleAfter, it is read as final.
	events, end, err = at(db, 2*time.Hour).Read(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	if end != 14 {
		t.Errorf("stale end offset = %d, want 14", end)
	}
	check(t, events, append(expected,
		want{seq: 12, role: transcript.RoleAssistant, text: "Writing tes", model: model},
		want{seq: 13, role: transcript.RoleUser, text: "Use table tests."},
	))
	for _, e := range events {
		if err := e.Validate(); err != nil {
			t.Errorf("seq %d: %v", e.Seq, err)
		}
		if e.SessionID != fixtureID || e.Provider != transcript.ProviderOpenCode {
			t.Errorf("seq %d: session %q provider %q", e.Seq, e.SessionID, e.Provider)
		}
		if e.TS == nil {
			t.Errorf("seq %d: no timestamp", e.Seq)
		}
		var raw struct {
			ID   string          `json:"id"`
			Seq  int64           `json:"seq"`
			Data json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(e.Raw, &raw); err != nil || raw.Seq != e.Seq || raw.ID == "" || len(raw.Data) == 0 {
			t.Errorf("seq %d: raw does not hold the row: %v", e.Seq, err)
		}
	}
	if got := events[3].Raw; !strings.Contains(string(got), `"data":"not json {"`) {
		t.Errorf("malformed row raw does not keep its data as a string")
	}
	if ts := events[2].TS; ts == nil || !ts.Equal(base.Add(time.Second)) {
		t.Errorf("seq 3 ts = %v, want %v", ts, base.Add(time.Second))
	}

	// Reading at the end is a no-op.
	tail, tailEnd, err := at(db, 2*time.Hour).Read(path, end)
	if err != nil || len(tail) != 0 || tailEnd != end {
		t.Errorf("read at end = %d events, offset %d, %v; want none at %d", len(tail), tailEnd, err, end)
	}
}

// TestReadSplit checks that a read starting at any offset a read can return
// yields the same events as the tail of a whole read, the way a tailing
// reporter sees a growing session.
func TestReadSplit(t *testing.T) {
	db := fixtureDB(t)
	path := session.OpenCodeTranscriptPath(db, fixtureID)
	source := at(db, 2*time.Hour)
	whole, _, err := source.Read(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	// Seqs have gaps, so both an event's own seq and the offset one past the
	// event before it must resume at that event.
	for i, e := range whole {
		froms := []int64{e.Seq}
		if i > 0 {
			froms = append(froms, whole[i-1].Seq+1)
		}
		for _, from := range froms {
			part, _, err := source.Read(path, from)
			if err != nil {
				t.Fatal(err)
			}
			if got, want := keys(part), keys(whole[i:]); !reflect.DeepEqual(got, want) {
				t.Errorf("read from %d: seqs %v, want %v", from, seqs(part), seqs(whole[i:]))
			}
		}
	}
}

type key struct {
	seq        int64
	role       transcript.Role
	text, tool string
}

func keys(events []transcript.Event) []key {
	out := []key{}
	for _, e := range events {
		k := key{seq: e.Seq, role: e.Role, text: e.Text}
		if e.ToolName != nil {
			k.tool = *e.ToolName
		}
		out = append(out, k)
	}
	return out
}

func seqs(events []transcript.Event) []int64 {
	out := []int64{}
	for _, e := range events {
		out = append(out, e.Seq)
	}
	return out
}

func check(t *testing.T, events []transcript.Event, expected []want) {
	t.Helper()
	if len(events) != len(expected) {
		t.Fatalf("got %d events %v, want %d", len(events), seqs(events), len(expected))
	}
	for i, w := range expected {
		e := events[i]
		if e.Seq != w.seq || e.Role != w.role || e.Text != w.text || e.Tokens != w.tokens {
			// Names seqs and roles only; fixture text is synthetic but the
			// message stays in the same shape as for real data.
			t.Errorf("event %d: seq %d role %s tokens %+v text match %v; want seq %d role %s tokens %+v",
				i, e.Seq, e.Role, e.Tokens, e.Text == w.text, w.seq, w.role, w.tokens)
		}
		if got := deref(e.ToolName); got != w.tool {
			t.Errorf("seq %d: tool %q, want %q", e.Seq, got, w.tool)
		}
		if got := deref(e.Model); got != w.model {
			t.Errorf("seq %d: model %q, want %q", e.Seq, got, w.model)
		}
	}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func TestTouchedFiles(t *testing.T) {
	db := fixtureDB(t)
	events, _, err := at(db, 20*time.Second).Read(session.OpenCodeTranscriptPath(db, fixtureID), 0)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range events {
		got = append(got, transcript.TouchedFiles(e)...)
	}
	// The read tool does not change files.
	if want := []string{"parser.go", "parser_test.go"}; !reflect.DeepEqual(got, want) {
		t.Errorf("touched files = %v, want %v", got, want)
	}
}

func TestLocate(t *testing.T) {
	db := fixtureDB(t)
	source := &Source{Database: db}
	want := []string{session.OpenCodeTranscriptPath(db, fixtureID)}
	for name, c := range map[string]struct {
		meta session.Meta
		want []string
	}{
		"by id":          {session.Meta{ID: fixtureID, Provider: "opencode"}, want},
		"no provider":    {session.Meta{ID: fixtureID}, want},
		"rollout path":   {session.Meta{ID: fixtureID, RolloutPath: want[0]}, want},
		"unknown id":     {session.Meta{ID: "ses_missing"}, nil},
		"invalid id":     {session.Meta{ID: "ses_x' OR '1'='1"}, nil},
		"other provider": {session.Meta{ID: fixtureID, Provider: "codex"}, nil},
	} {
		got, err := source.Locate(c.meta)
		if err != nil || !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: Locate = %v, %v; want %v", name, got, err, c.want)
		}
	}

	// A rollout path into another database is used as given.
	other := filepath.Join(t.TempDir(), "other.db")
	if err := os.Link(db, other); err != nil {
		t.Fatal(err)
	}
	path := session.OpenCodeTranscriptPath(other, fixtureID)
	if got, err := (&Source{Database: db}).Locate(session.Meta{ID: fixtureID, RolloutPath: path}); err != nil || !reflect.DeepEqual(got, []string{path}) {
		t.Errorf("Locate with rollout path = %v, %v; want %v", got, err, path)
	}

	// No database yet means no transcript, not an error.
	missing := &Source{Database: filepath.Join(t.TempDir(), "opencode.db")}
	if got, err := missing.Locate(session.Meta{ID: fixtureID}); err != nil || got != nil {
		t.Errorf("missing database: Locate = %v, %v", got, err)
	}
}

func TestUnsupportedSchema(t *testing.T) {
	testenv.NeedTool(t, "sqlite3")
	db := filepath.Join(t.TempDir(), "opencode.db")
	// An OpenCode release from before session_v2 kept sessions in session.
	if out, err := exec.Command("sqlite3", db, "CREATE TABLE session (id text PRIMARY KEY);").CombinedOutput(); err != nil {
		t.Fatalf("build database: %v: %s", err, out)
	}
	source := &Source{Database: db}
	if _, err := source.Locate(session.Meta{ID: fixtureID}); !errors.Is(err, ErrUnsupportedSchema) {
		t.Errorf("Locate error = %v, want ErrUnsupportedSchema", err)
	}
	if _, err := source.Enumerate(context.Background(), reader.EnumerateOptions{}); !errors.Is(err, ErrUnsupportedSchema) {
		t.Errorf("Enumerate error = %v, want ErrUnsupportedSchema", err)
	}
}

func TestStatAndPending(t *testing.T) {
	db := fixtureDB(t)
	source := &Source{Database: db}
	path := session.OpenCodeTranscriptPath(db, fixtureID)
	info, err := reader.Stat(source, path)
	if err != nil {
		t.Fatal(err)
	}
	if want := (reader.Info{Size: 14, ModTime: base.Add(12 * time.Second)}); info.Size != want.Size || !info.ModTime.Equal(want.ModTime) {
		t.Errorf("Stat = %+v, want %+v", info, want)
	}
	empty, err := source.Stat(session.OpenCodeTranscriptPath(db, "ses_fixtureEmpty"))
	if err != nil || empty.Size != 0 {
		t.Errorf("empty session Stat = %+v, %v; want size 0", empty, err)
	}
	for name, p := range map[string]string{
		"session":  session.OpenCodeTranscriptPath(db, "ses_missing"),
		"database": session.OpenCodeTranscriptPath(filepath.Join(t.TempDir(), "gone.db"), fixtureID),
	} {
		if _, err := source.Stat(p); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("missing %s: Stat error = %v, want os.ErrNotExist", name, err)
		}
	}

	events, size, err := source.Pending(context.Background(), path, 0)
	if err != nil || events != 12 || size <= 0 {
		t.Errorf("Pending(0) = %d events, %d bytes, %v; want 12 events", events, size, err)
	}
	events, _, err = source.Pending(context.Background(), path, 10)
	if err != nil || events != 4 {
		t.Errorf("Pending(10) = %d events, %v; want 4", events, err)
	}
}

func TestEnumerate(t *testing.T) {
	db := fixtureDB(t)
	source := &Source{Database: db}
	metas, err := source.Enumerate(context.Background(), reader.EnumerateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, m := range metas {
		ids = append(ids, m.ID)
	}
	// Newest first; the subagent session is left out.
	if want := []string{fixtureID, "ses_fixtureEmpty", "ses_fixtureB"}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("ids = %v, want %v", ids, want)
	}
	m := metas[0]
	if m.Provider != "opencode" || m.State != session.SessionStateEnded || m.CWD != "/work/repo" ||
		m.Project != "repo" || m.Title != "Fix the parser" || m.Model != "model-test" ||
		m.RolloutPath != session.OpenCodeTranscriptPath(db, fixtureID) {
		t.Errorf("meta = %+v", m)
	}
	if m.StartedAt == nil || !m.StartedAt.Equal(base) || m.LastActivityAt == nil || !m.LastActivityAt.Equal(base.Add(12*time.Second)) {
		t.Errorf("times = %v, %v", m.StartedAt, m.LastActivityAt)
	}
	if got, err := source.Locate(m); err != nil || len(got) != 1 || got[0] != m.RolloutPath {
		t.Errorf("Locate(enumerated) = %v, %v", got, err)
	}

	recent, err := source.Enumerate(context.Background(), reader.EnumerateOptions{ModifiedAfter: base})
	if err != nil || len(recent) != 1 || recent[0].ID != fixtureID {
		t.Errorf("ModifiedAfter: %d sessions, %v; want only %s", len(recent), err, fixtureID)
	}
	skipped, err := source.Enumerate(context.Background(), reader.EnumerateOptions{
		Skip: func(m session.Meta) bool { return m.CWD == "/work/repo" },
	})
	if err != nil || len(skipped) != 1 || skipped[0].ID != "ses_fixtureB" {
		t.Errorf("Skip: %d sessions, %v; want only ses_fixtureB", len(skipped), err)
	}

	none, err := (&Source{Database: filepath.Join(t.TempDir(), "opencode.db")}).Enumerate(context.Background(), reader.EnumerateOptions{})
	if err != nil || len(none) != 0 {
		t.Errorf("missing database: %d sessions, %v", len(none), err)
	}
}

func TestQueryErrorsStaySanitized(t *testing.T) {
	db := filepath.Join(t.TempDir(), "private-name.db")
	if err := os.WriteFile(db, nil, 0600); err != nil {
		t.Fatal(err)
	}
	source := &Source{Database: db, run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, "false").Output()
	}}
	_, _, err := source.Read(session.OpenCodeTranscriptPath(db, fixtureID), 0)
	if err == nil {
		t.Fatal("Read succeeded")
	}
	if msg := err.Error(); strings.Contains(msg, "private-name") || strings.Contains(msg, "SELECT") {
		t.Errorf("error exposes its path or query: %q", msg)
	}
	if _, _, err := source.Read("/not/a/transcript.jsonl", 0); err == nil {
		t.Error("Read of a non-OpenCode path succeeded")
	}
	if _, _, err := source.Read(session.OpenCodeTranscriptPath(db, fixtureID), -1); err == nil {
		t.Error("Read at a negative offset succeeded")
	}
}

func TestSettled(t *testing.T) {
	now := base.Add(time.Minute)
	completed := 1.0
	fresh := now.Add(-time.Second).UnixMilli()
	stale := now.Add(-settleAfter).UnixMilli()
	finished := message{}
	finished.Time.Completed = &completed
	for name, c := range map[string]struct {
		r        row
		msg      message
		decoded  bool
		lastIdle int64
		want     bool
	}{
		"assistant writing":     {row{Type: "assistant", Seq: 5, TimeUpdated: fresh}, message{}, true, -1, false},
		"assistant completed":   {row{Type: "assistant", Seq: 5, TimeUpdated: fresh}, finished, true, -1, true},
		"assistant before idle": {row{Type: "assistant", Seq: 5, TimeUpdated: fresh}, message{}, true, 6, true},
		"assistant stale":       {row{Type: "assistant", Seq: 5, TimeUpdated: stale}, message{}, true, -1, true},
		"shell running":         {row{Type: "shell", Seq: 5, TimeUpdated: fresh}, message{Status: "running"}, true, -1, false},
		"shell exited":          {row{Type: "shell", Seq: 5, TimeUpdated: fresh}, message{Status: "exited"}, true, -1, true},
		"compaction running":    {row{Type: "compaction", Seq: 5, TimeUpdated: fresh}, message{Status: "running"}, true, -1, false},
		"compaction failed":     {row{Type: "compaction", Seq: 5, TimeUpdated: fresh}, message{Status: "failed"}, true, -1, true},
		"user":                  {row{Type: "user", Seq: 5, TimeUpdated: fresh}, message{}, true, -1, true},
		"undecodable":           {row{Type: "assistant", Seq: 5, TimeUpdated: fresh}, message{}, false, -1, true},
		"unknown type":          {row{Type: "future", Seq: 5, TimeUpdated: fresh}, message{}, true, -1, true},
	} {
		if got := settled(c.r, c.msg, c.decoded, c.lastIdle, now); got != c.want {
			t.Errorf("%s: settled = %v, want %v", name, got, c.want)
		}
	}
}

func TestTruncate(t *testing.T) {
	long := strings.Repeat("é", maxToolText)
	got := truncate(long)
	if !strings.HasSuffix(got, " bytes]") || len(got) > maxToolText+64 || !json.Valid([]byte(`"`+strings.Split(got, "\n")[0]+`"`)) {
		t.Errorf("truncate kept %d bytes", len(got))
	}
	if truncate("short") != "short" {
		t.Error("short text changed")
	}
}

func legacyDB(t *testing.T) string {
	t.Helper()
	testenv.NeedTool(t, "sqlite3")
	schema, err := filepath.Abs(filepath.Join("testdata", "opencode_legacy.sql"))
	if err != nil {
		t.Fatal(err)
	}
	db := filepath.Join(t.TempDir(), "opencode.db")
	if out, err := exec.Command("sqlite3", db, ".read "+schema).CombinedOutput(); err != nil {
		t.Fatalf("build legacy database: %v: %s", err, out)
	}
	return db
}

// The layout before session_v2 keeps messages in message and their content in
// part. It reads like the current one, with rowid as seq.
func TestLegacyLayout(t *testing.T) {
	db := legacyDB(t)
	const id = "ses_legacyA"
	path := session.OpenCodeTranscriptPath(db, id)
	source := at(db, 2*time.Hour)

	events, offset, err := source.Read(path, 0)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(events) != 3 || offset != 4 {
		t.Fatalf("got %d events, offset %d; want 3 events, offset 4", len(events), offset)
	}
	check(t, events, []want{
		{seq: 1, role: transcript.RoleUser, text: "fix the bug"},
		{seq: 2, role: transcript.RoleAssistant, text: "done", tool: "edit", model: "model-test",
			tokens: transcript.Tokens{Input: 10, Output: 5, Cache: 105}},
		{seq: 3, role: transcript.RoleToolCall, text: "{\"command\":\"false\"}\nerror: exit 1", tool: "bash", model: "model-test"},
	})
	if strings.Contains(string(events[0].Raw), "summary") {
		t.Errorf("user message summary should be left out of raw")
	}
	if got := transcript.TouchedFiles(events[1]); !reflect.DeepEqual(got, []string{"/work/demo/main.go"}) {
		t.Errorf("TouchedFiles = %v", got)
	}

	// Offsets resume after the last message read.
	rest, next, err := source.Read(path, 3)
	if err != nil || len(rest) != 1 || rest[0].Seq != 3 || next != 4 {
		t.Errorf("Read from 3 = %d events, offset %d, %v", len(rest), next, err)
	}

	// An unfinished last message waits until it is old enough to count as
	// final.
	waiting, offset, err := at(db, 0).Read(path, 0)
	if err != nil || len(waiting) != 2 || offset != 3 {
		t.Errorf("fresh unfinished message: %d events, offset %d, %v; want 2 events, offset 3", len(waiting), offset, err)
	}

	info, err := reader.Stat(source, path)
	if err != nil || info.Size != 4 {
		t.Errorf("Stat = %+v, %v; want size 4", info, err)
	}
	pending, size, err := source.Pending(context.Background(), path, 2)
	if err != nil || pending != 2 || size <= 0 {
		t.Errorf("Pending = %d, %d, %v", pending, size, err)
	}

	located, err := source.Locate(session.Meta{ID: id})
	if err != nil || !reflect.DeepEqual(located, []string{path}) {
		t.Errorf("Locate = %v, %v", located, err)
	}
	metas, err := source.Enumerate(context.Background(), reader.EnumerateOptions{})
	titles := map[string]string{}
	for _, m := range metas {
		titles[m.ID] = m.Title
	}
	// Subagent sessions are left out, and a placeholder title becomes the
	// first prompt.
	wantTitles := map[string]string{id: "Legacy demo", "ses_legacyNew": "add a --verbose flag to the CLI"}
	if err != nil || !reflect.DeepEqual(titles, wantTitles) {
		t.Errorf("Enumerate titles = %v, %v; want %v", titles, err, wantTitles)
	}
}
