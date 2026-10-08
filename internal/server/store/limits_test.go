package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/DanBradbury/firekeeper/internal/redact"
	"github.com/DanBradbury/firekeeper/internal/transcript"
)

// newAccount creates an account with a throwaway hash.
func newAccount(t *testing.T, s *Store, email string) Account {
	t.Helper()
	a, err := s.CreateAccount(context.Background(), email, "hash", "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// sized is a batch of n events whose text is size bytes each. Its raw
// payload is {} (2 bytes), so each event counts size+2 toward the limit.
func sized(sid string, from, n, size int) SessionBatch {
	b := batch(sid, from, n)
	for i := range b.Events {
		b.Events[i].Text = strings.Repeat("x", size)
		b.Events[i].Raw = json.RawMessage(`{}`)
	}
	return b
}

// codexBatch is the codex files fixture as one session batch, redacted as
// the reporter would, without ingesting it. Its tool calls change files, so
// ingesting it fills session_files.
func codexBatch(t *testing.T) SessionBatch {
	t.Helper()
	src, ok := transcript.For(transcript.ProviderCodex)
	if !ok {
		t.Fatal("no codex source")
	}
	events, _, err := src.Read("testdata/files/codex.jsonl", 0)
	if err != nil {
		t.Fatal(err)
	}
	opts := redact.Options{HomeDir: fixtureHome}
	cwd, _ := redact.String(fixtureHome+"/src/demo", opts)
	b := SessionBatch{Meta: SessionMeta{SessionID: "s-codex", Provider: "codex", CWD: cwd}}
	for _, e := range events {
		e.MachineID, e.SessionID = "m1", b.Meta.SessionID
		e, _ = redact.Event(e, opts)
		b.Events = append(b.Events, e)
	}
	return b
}

// footprint counts every row the account owns, in every table that has an
// account_id column, plus its FTS index entries. It finds tables from the
// schema so a table added later is covered without editing the test.
func footprint(t *testing.T, s *Store, accountID string) map[string]int {
	t.Helper()
	rows, err := s.db.Query(`SELECT m.name FROM sqlite_master m, pragma_table_info(m.name) p
WHERE m.type = 'table' AND p.name = 'account_id' ORDER BY m.name`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var n string
		rows.Scan(&n)
		tables = append(tables, n)
	}
	rows.Close()
	if len(tables) < 5 {
		t.Fatalf("found only tables %v with an account_id column", tables)
	}
	out := map[string]int{}
	for _, tbl := range tables {
		var n int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM `+tbl+` WHERE account_id = ?`, accountID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		out[tbl] = n
	}
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM accounts WHERE id = ?`, accountID).Scan(&n)
	out["accounts"] = n
	s.db.QueryRow(`SELECT COUNT(*) FROM invites WHERE created_by = ? OR used_by = ?`, accountID, accountID).Scan(&n)
	out["invites"] = n
	return out
}

func rowTotal(m map[string]int) (n int) {
	for _, v := range m {
		n += v
	}
	return n
}

func TestStorageLimitRejectsWholeBatch(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	a := newAccount(t, s, "a@example.com")
	m := Machine{ID: "m1"}
	// 3 events of 100+2 bytes = 306.
	if _, _, err := s.IngestLimited(ctx, a.ID, m, []SessionBatch{sized("s1", 0, 3, 100)}, Limits{MaxBytes: 400}); err != nil {
		t.Fatal(err)
	}
	if st, _ := s.AccountStats(ctx, a.ID); st.StoredBytes != 306 {
		t.Fatalf("stored bytes = %d, want 306", st.StoredBytes)
	}
	before := footprint(t, s, a.ID)

	// 94 bytes remain. The second session's batch alone fits, but the
	// first session's new event does not: nothing from the request lands.
	_, _, err := s.IngestLimited(ctx, a.ID, Machine{ID: "m2", Name: "other"}, []SessionBatch{
		sized("s2", 0, 1, 10),
		sized("s1", 3, 1, 100),
	}, Limits{MaxBytes: 400})
	if !errors.Is(err, ErrStorageLimit) {
		t.Fatalf("over limit = %v, want ErrStorageLimit", err)
	}
	after := footprint(t, s, a.ID)
	for tbl, n := range before {
		if after[tbl] != n {
			t.Errorf("table %s: %d rows before the rejected batch, %d after", tbl, n, after[tbl])
		}
	}
	if st, _ := s.AccountStats(ctx, a.ID); st.StoredBytes != 306 || st.Events != 3 {
		t.Fatalf("stats after rejection = %+v", st)
	}
	if hits, _ := s.SearchText(ctx, a.ID, strings.Repeat("x", 100), 10); len(hits) != 3 {
		t.Fatalf("search index after rejection has %d hits, want 3", len(hits))
	}
	if ms, _ := s.ListMachines(ctx, a.ID); len(ms) != 1 {
		t.Fatalf("rejected batch left machines %+v", ms)
	}

	// Exactly at the limit is allowed; one byte over is not.
	if _, _, err := s.IngestLimited(ctx, a.ID, m, []SessionBatch{sized("s1", 3, 1, 92)}, Limits{MaxBytes: 400}); err != nil {
		t.Fatalf("filling to exactly the limit: %v", err)
	}
	if _, _, err := s.IngestLimited(ctx, a.ID, m, []SessionBatch{sized("s1", 4, 1, 0)}, Limits{MaxBytes: 400}); !errors.Is(err, ErrStorageLimit) {
		t.Fatalf("one event past the limit = %v", err)
	}

	// Re-sending what is stored adds nothing, so it passes even when full.
	acc, dup, err := s.IngestLimited(ctx, a.ID, m, []SessionBatch{sized("s1", 0, 3, 100)}, Limits{MaxBytes: 400})
	if err != nil || acc != 0 || dup != 3 {
		t.Fatalf("re-send at the limit = %d, %d, %v", acc, dup, err)
	}
	// And so does a metadata-only update, even with the limit now lowered
	// below what is stored.
	meta := SessionBatch{Meta: SessionMeta{SessionID: "s1", Provider: "codex", Title: "renamed"}}
	if _, _, err := s.IngestLimited(ctx, a.ID, m, []SessionBatch{meta}, Limits{MaxBytes: 10}); err != nil {
		t.Fatalf("metadata update with a lowered limit: %v", err)
	}
	// Another account is counted separately.
	b := newAccount(t, s, "b@example.com")
	if _, _, err := s.IngestLimited(ctx, b.ID, m, []SessionBatch{sized("s1", 0, 3, 100)}, Limits{MaxBytes: 400}); err != nil {
		t.Fatalf("second account shares the first one's usage: %v", err)
	}
	// Zero means no limit.
	if _, _, err := s.IngestLimited(ctx, b.ID, m, []SessionBatch{sized("s1", 3, 50, 1000)}, Limits{}); err != nil {
		t.Fatalf("no limit: %v", err)
	}
}

func TestSessionLimitRejectsWholeBatch(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	a := newAccount(t, s, "a@example.com")
	m := Machine{ID: "m1"}
	lim := Limits{MaxSessions: 2}
	if _, _, err := s.IngestLimited(ctx, a.ID, m, []SessionBatch{sized("s1", 0, 1, 5), sized("s2", 0, 1, 5)}, lim); err != nil {
		t.Fatal(err)
	}
	before := footprint(t, s, a.ID)

	// One existing session gains an event and a third session appears: the
	// new session is over the limit, so the new event must not land either.
	_, _, err := s.IngestLimited(ctx, a.ID, m, []SessionBatch{sized("s1", 1, 1, 5), sized("s3", 0, 1, 5)}, lim)
	if !errors.Is(err, ErrSessionLimit) {
		t.Fatalf("third session = %v, want ErrSessionLimit", err)
	}
	after := footprint(t, s, a.ID)
	for tbl, n := range before {
		if after[tbl] != n {
			t.Errorf("table %s: %d rows before, %d after the rejected batch", tbl, n, after[tbl])
		}
	}
	if st, _ := s.AccountStats(ctx, a.ID); st.Events != 2 || st.Sessions != 2 {
		t.Fatalf("stats after rejection = %+v", st)
	}
	// Existing sessions keep working at the limit.
	if _, _, err := s.IngestLimited(ctx, a.ID, m, []SessionBatch{sized("s1", 1, 1, 5)}, lim); err != nil {
		t.Fatalf("append to an existing session at the limit: %v", err)
	}
}

func TestDeleteAccountLeavesNothing(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	victim := newAccount(t, s, "victim@example.com")
	keeper := newAccount(t, s, "keeper@example.com")
	now := time.Now()

	for _, a := range []Account{victim, keeper} {
		// The same machine and session ids in both accounts.
		b := batch("s1", 0, 4)
		b.Events[0].Text = "shared word " + a.Email
		b.Events[1].Role = transcript.RoleToolCall
		b.Events[1].Raw = json.RawMessage(`{"name":"apply_patch","arguments":{"path":"main.go"}}`)
		if _, _, err := s.Ingest(ctx, a.ID, Machine{ID: "m1", Name: "laptop"}, []SessionBatch{b, batch("s2", 0, 2), codexBatch(t)}); err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.CreateToken(ctx, a.ID, "tok", ScopeIngest, "m1"); err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.CreateWebSession(ctx, a.ID, time.Hour, now); err != nil {
			t.Fatal(err)
		}
	}
	// The victim made an invite, and used one the keeper made.
	if _, err := s.CreateInvite(ctx, victim.ID, time.Hour, now); err != nil {
		t.Fatal(err)
	}
	code, err := s.CreateInvite(ctx, keeper.ID, time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE invites SET used_by = ? WHERE code_hash = ?`, victim.ID, hashSecret(code)); err != nil {
		t.Fatal(err)
	}

	before := footprint(t, s, victim.ID)
	for _, tbl := range []string{"machines", "sessions", "events", "session_files", "tokens", "web_sessions", "accounts", "invites"} {
		if before[tbl] == 0 {
			t.Fatalf("test setup left %s empty: %v", tbl, before)
		}
	}
	keeperBefore := footprint(t, s, keeper.ID)
	if hits, _ := s.SearchText(ctx, victim.ID, "shared", 10); len(hits) != 1 {
		t.Fatalf("victim search before = %v", hits)
	}

	st, err := s.DeleteAccount(ctx, victim.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st.Sessions != 3 || st.Events != int64(6+len(codexBatch(t).Events)) || st.Machines != 1 || st.Tokens != 1 {
		t.Fatalf("deleted stats = %+v", st)
	}

	if left := footprint(t, s, victim.ID); rowTotal(left) != 0 {
		t.Fatalf("rows left for a deleted account: %v", left)
	}
	if hits, _ := s.SearchText(ctx, victim.ID, "shared", 10); len(hits) != 0 {
		t.Fatalf("victim still searchable: %v", hits)
	}
	// The index itself holds no entry for the deleted rows: a search over
	// every account finds only the keeper's, and the index is consistent.
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM events_fts WHERE events_fts MATCH 'shared'`).Scan(&n)
	if n != 1 {
		t.Fatalf("%d index entries match, want only the keeper's", n)
	}
	var docs, events int
	s.db.QueryRow(`SELECT COUNT(*) FROM events_fts_docsize`).Scan(&docs)
	s.db.QueryRow(`SELECT COUNT(*) FROM events`).Scan(&events)
	if docs != events {
		t.Fatalf("index has %d documents for %d events", docs, events)
	}
	if _, err := s.db.Exec(`INSERT INTO events_fts(events_fts, rank) VALUES('integrity-check', 1)`); err != nil {
		t.Fatalf("index inconsistent after delete: %v", err)
	}

	// Nothing of the keeper changed, except that the invite it issued and
	// the victim used is gone with the victim. It stays unusable.
	keeperBefore["invites"]--
	if got := footprint(t, s, keeper.ID); fmt.Sprint(got) != fmt.Sprint(keeperBefore) {
		t.Fatalf("keeper rows changed: %v -> %v", keeperBefore, got)
	}
	if _, err := s.CreateAccount(ctx, "third@example.com", "hash", code, now); !errors.Is(err, ErrInviteInvalid) {
		t.Fatalf("invite used by a deleted account = %v, want ErrInviteInvalid", err)
	}
	if _, err := s.GetAccount(ctx, victim.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted account lookup = %v", err)
	}
	if _, err := s.DeleteAccount(ctx, victim.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleting twice = %v", err)
	}
	// Its email is free again.
	if _, err := s.CreateAccount(ctx, "victim@example.com", "hash", "", now); err != nil {
		t.Fatalf("re-register after delete: %v", err)
	}
	if _, err := s.DeleteAccount(ctx, DefaultAccountID); !errors.Is(err, ErrInvalid) {
		t.Fatalf("deleting the default account = %v", err)
	}
}

func TestSetPassword(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	a := newAccount(t, s, "a@example.com")
	b := newAccount(t, s, "b@example.com")
	now := time.Now()
	cookieA, _, _ := s.CreateWebSession(ctx, a.ID, time.Hour, now)
	cookieB, _, _ := s.CreateWebSession(ctx, b.ID, time.Hour, now)

	if err := s.SetPassword(ctx, a.ID, "new-hash"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetAccount(ctx, a.ID); got.PasswordHash != "new-hash" {
		t.Fatalf("hash = %q", got.PasswordHash)
	}
	if _, err := s.LookupWebSession(ctx, cookieA, now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("old browser session survived a reset: %v", err)
	}
	if _, err := s.LookupWebSession(ctx, cookieB, now); err != nil {
		t.Fatalf("another account's session ended: %v", err)
	}
	if err := s.SetPassword(ctx, "nobody", "h"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown account = %v", err)
	}
	if err := s.SetPassword(ctx, DefaultAccountID, "h"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("default account = %v", err)
	}
	if err := s.SetPassword(ctx, a.ID, ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty hash = %v", err)
	}
}

func TestExportAccountMatchesIngest(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	a := newAccount(t, s, "a@example.com")
	b := newAccount(t, s, "b@example.com")
	ts := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	tool := "apply_patch"

	var want []transcript.Event
	var batches []SessionBatch
	for _, sid := range []string{"s-b", "s-a"} {
		sb := batch(sid, 0, 3)
		sb.Meta.Title = "title " + sid
		sb.Meta.CWD = "/repo"
		for i := range sb.Events {
			sb.Events[i].MachineID, sb.Events[i].SessionID = "m1", sid
			sb.Events[i].TS = &ts
		}
		sb.Events[1].ToolName = &tool
		sb.Events[1].Role = transcript.RoleToolCall
		sb.Events[1].Raw = json.RawMessage(`{"name":"apply_patch","arguments":{"path":"/repo/main.go"}}`)
		batches = append(batches, sb)
	}
	if _, _, err := s.Ingest(ctx, a.ID, Machine{ID: "m1", Name: "laptop"}, batches); err != nil {
		t.Fatal(err)
	}
	// A session whose tool calls changed files, so there are file records.
	cb := codexBatch(t)
	if _, _, err := s.Ingest(ctx, a.ID, Machine{ID: "m1"}, []SessionBatch{cb}); err != nil {
		t.Fatal(err)
	}
	wantFiles, err := s.ListFiles(ctx, a.ID, "m1", "s-codex")
	if err != nil || len(wantFiles) == 0 {
		t.Fatalf("fixture changed no files: %v %v", wantFiles, err)
	}
	// Another account's data must not appear.
	if _, _, err := s.Ingest(ctx, b.ID, Machine{ID: "m1", Name: "theirs"}, []SessionBatch{batch("s-other", 0, 2)}); err != nil {
		t.Fatal(err)
	}
	tokenA, secret, err := s.CreateToken(ctx, a.ID, "laptop", ScopeIngest, "m1")
	if err != nil {
		t.Fatal(err)
	}
	// Sessions come back in (machine, session) order.
	for _, sid := range []string{"s-a", "s-b", "s-codex"} {
		if sid == "s-codex" {
			want = append(want, cb.Events...)
			continue
		}
		for _, bt := range batches {
			if bt.Meta.SessionID == sid {
				want = append(want, bt.Events...)
			}
		}
	}

	counts := map[string]int{}
	var files []SessionFile
	var got []transcript.Event
	var sessions []Session
	err = s.ExportAccount(ctx, a.ID, func(typ string, v any) error {
		counts[typ]++
		raw, err := json.Marshal(v)
		if err != nil || raw[0] != '{' {
			t.Fatalf("%s record %q is not a JSON object: %v", typ, raw, err)
		}
		if strings.Contains(string(raw), secret) || strings.Contains(string(raw), tokenA.Hash) {
			t.Fatalf("%s record leaks a token secret or hash: %s", typ, raw)
		}
		switch typ {
		case "event":
			got = append(got, v.(transcript.Event))
		case "session":
			sessions = append(sessions, v.(Session))
		case "file":
			raw, _ := json.Marshal(v)
			var f SessionFile
			json.Unmarshal(raw, &f)
			files = append(files, f)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if counts["account"] != 1 || counts["machine"] != 1 || counts["token"] != 1 || counts["session"] != 3 || counts["event"] != 6+len(cb.Events) || counts["file"] != len(wantFiles) {
		t.Fatalf("record counts = %v", counts)
	}
	if !reflect.DeepEqual(files, wantFiles) {
		t.Fatalf("exported files = %+v, want %+v", files, wantFiles)
	}
	if len(sessions) != 3 || sessions[0].SessionID != "s-a" || sessions[0].Title != "title s-a" || sessions[0].EventCount != 3 {
		t.Fatalf("sessions = %+v", sessions)
	}
	gj, _ := json.Marshal(got)
	wj, _ := json.Marshal(want)
	if string(gj) != string(wj) {
		t.Fatalf("exported events differ from what was ingested:\n got %s\nwant %s", gj, wj)
	}

	// A failing consumer stops the export and surfaces the error.
	stop := errors.New("client went away")
	if err := s.ExportAccount(ctx, a.ID, func(string, any) error { return stop }); !errors.Is(err, stop) {
		t.Fatalf("emit error = %v", err)
	}
	if err := s.ExportAccount(ctx, "nobody", func(string, any) error { return nil }); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown account = %v", err)
	}
}

func TestExportPagesLargeAccounts(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	a := newAccount(t, s, "a@example.com")
	// More events than one export page, in a session sorted last, and more
	// sessions than one session page.
	events := exportPage*2 + 7
	if _, _, err := s.Ingest(ctx, a.ID, Machine{ID: "m1"}, []SessionBatch{sized("zzz", 0, events, 1)}); err != nil {
		t.Fatal(err)
	}
	var batches []SessionBatch
	for i := 0; i < exportPage+3; i++ {
		batches = append(batches, sized(fmt.Sprintf("s%04d", i), 0, 1, 1))
	}
	for lo := 0; lo < len(batches); lo += 100 {
		hi := min(lo+100, len(batches))
		if _, _, err := s.Ingest(ctx, a.ID, Machine{ID: "m1"}, batches[lo:hi]); err != nil {
			t.Fatal(err)
		}
	}
	counts := map[string]int{}
	seen := map[string]bool{}
	s.ExportAccount(ctx, a.ID, func(typ string, v any) error {
		counts[typ]++
		if typ == "session" {
			id := v.(Session).SessionID
			if seen[id] {
				t.Fatalf("session %s exported twice", id)
			}
			seen[id] = true
		}
		return nil
	})
	if counts["session"] != exportPage+4 || counts["event"] != events+exportPage+3 {
		t.Fatalf("counts = %v", counts)
	}
}
