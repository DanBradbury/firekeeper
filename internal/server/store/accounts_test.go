package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestMigrationAssignsExistingRowsToDefaultAccount upgrades a database
// created by schema 3 and checks nothing is lost and everything becomes the
// default account's, including full-text search.
func TestMigrationAssignsExistingRowsToDefaultAccount(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "old.db")
	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"001_init.sql", "002_tokens.sql", "003_usage.sql", "004_files.sql"} {
		body, err := migrationFS.ReadFile("migrations/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := raw.Exec(string(body)); err != nil {
			t.Fatal(err)
		}
	}
	for _, q := range []string{
		`CREATE TABLE schema_version (version INTEGER NOT NULL)`,
		`INSERT INTO schema_version VALUES (1),(2),(3),(4)`,
		`INSERT INTO machines(id, name) VALUES ('m1', 'laptop')`,
		`INSERT INTO sessions(machine_id, session_id, provider, project, "commit", event_count) VALUES ('m1', 's1', 'codex', 'proj', 'abc123', 2)`,
		`INSERT INTO events(machine_id, session_id, seq, provider, role, text, input_tokens) VALUES ('m1', 's1', 0, 'codex', 'user', 'legacy needle', 7)`,
		`INSERT INTO events(machine_id, session_id, seq, provider, role, text) VALUES ('m1', 's1', 1, 'codex', 'assistant', 'plain')`,
		`INSERT INTO session_files(machine_id, session_id, path, first_seq, last_seq, changes) VALUES ('m1', 's1', 'main.go', 0, 1, 2)`,
		`INSERT INTO tokens(id, name, scope, machine_id, hash, created_at) VALUES ('t1', 'old', 'ingest', 'm1', 'h1', '2026-01-01T00:00:00Z')`,
	} {
		if _, err := raw.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	raw.Close()

	s, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	defer s.Close()

	se, err := s.GetSession(ctx, DefaultAccountID, "m1", "s1")
	if err != nil || se.Project != "proj" || se.EventCount != 2 || se.Commit != "abc123" {
		t.Fatalf("session = %+v, %v", se, err)
	}
	if _, err := s.GetSession(ctx, "someone-else", "m1", "s1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other account sees migrated session: %v", err)
	}
	files, err := s.ListFiles(ctx, DefaultAccountID, "m1", "s1")
	if err != nil || len(files) != 1 || files[0].Path != "main.go" || files[0].Changes != 2 {
		t.Fatalf("files = %+v, %v", files, err)
	}
	if other, _ := s.ListFiles(ctx, "someone-else", "m1", "s1"); len(other) != 0 {
		t.Fatalf("other account sees migrated files: %+v", other)
	}
	evs, _, err := s.ListEvents(ctx, DefaultAccountID, "m1", "s1", -1, 10)
	if err != nil || len(evs) != 2 || evs[0].Tokens.Input != 7 {
		t.Fatalf("events = %+v, %v", evs, err)
	}
	hits, err := s.SearchText(ctx, DefaultAccountID, "needle", 10)
	if err != nil || len(hits) != 1 {
		t.Fatalf("fts after migration = %v, %v", hits, err)
	}
	if hits, _ := s.SearchText(ctx, "someone-else", "needle", 10); len(hits) != 0 {
		t.Fatalf("other account found migrated text: %v", hits)
	}
	ms, _ := s.ListMachines(ctx, DefaultAccountID)
	if len(ms) != 1 || ms[0].Name != "laptop" || ms[0].SessionCount != 1 {
		t.Fatalf("machines = %+v", ms)
	}
	toks, _ := s.ListTokens(ctx, true)
	if len(toks) != 1 || toks[0].AccountID != DefaultAccountID || toks[0].Hash != "h1" {
		t.Fatalf("tokens = %+v", toks)
	}
	if has, _ := s.HasAccounts(ctx); has {
		t.Fatal("migration made the default account count as a real account")
	}
	if _, err := s.GetAccount(ctx, DefaultAccountID); err != nil {
		t.Fatalf("default account: %v", err)
	}
	// Triggers still feed FTS for new rows.
	if _, _, err := s.Ingest(ctx, DefaultAccountID, Machine{ID: "m1"}, []SessionBatch{batch("s2", 0, 1)}); err != nil {
		t.Fatalf("ingest after migration: %v", err)
	}
	if v, _ := s.SchemaVersion(ctx); v != 7 {
		t.Fatalf("schema version %d", v)
	}
	// The upgrade backfills storage accounting from the existing events:
	// "legacy needle" and "plain" plus the default raw payload "null" each.
	// The ingest above added "hello needle" and {"a":1}.
	if st, err := s.AccountStats(ctx, DefaultAccountID); err != nil || st.StoredBytes != int64(13+4+5+4+12+7) {
		t.Fatalf("stored bytes after upgrade = %+v, %v", st, err)
	}
	var violations int
	rows, err := s.db.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		violations++
	}
	rows.Close()
	if violations != 0 {
		t.Fatalf("%d foreign key violations after migration", violations)
	}
}

func TestSameIDsInTwoAccounts(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "d.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	a, _ := s.CreateAccount(ctx, "a@example.com", "h", "", time.Now())
	b, _ := s.CreateAccount(ctx, "b@example.com", "h", "", time.Now())
	m := Machine{ID: "m1"}
	if _, _, err := s.Ingest(ctx, a.ID, m, []SessionBatch{batch("s", 0, 3)}); err != nil {
		t.Fatal(err)
	}
	acc, dup, err := s.Ingest(ctx, b.ID, m, []SessionBatch{batch("s", 0, 5)})
	if err != nil || acc != 5 || dup != 0 {
		t.Fatalf("second account ingest = %d, %d, %v (events must not collide)", acc, dup, err)
	}
	if st, _ := s.Stats(ctx, a.ID, "m1", "s"); st.EventCount != 3 {
		t.Fatalf("a = %+v", st)
	}
	if st, _ := s.Stats(ctx, b.ID, "m1", "s"); st.EventCount != 5 {
		t.Fatalf("b = %+v", st)
	}
	if _, _, err := s.Ingest(ctx, "", m, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("ingest without account = %v", err)
	}
	if _, _, err := s.Ingest(ctx, "no-such-account", m, []SessionBatch{batch("s", 0, 1)}); err == nil {
		t.Fatal("ingest into an account that does not exist succeeded")
	}
	if _, _, err := s.ListSessions(ctx, "", SessionFilter{Limit: 5}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("list without account = %v", err)
	}
}

func TestAccountsAndSessions(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "d.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	if _, err := s.CreateAccount(ctx, "bad", "h", "", now); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad email = %v", err)
	}
	if _, err := s.CreateAccount(ctx, "a@example.com", "", "", now); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty hash = %v", err)
	}
	a, err := s.CreateAccount(ctx, " A@Example.COM ", "hash", "", now)
	if err != nil || a.Email != "a@example.com" {
		t.Fatalf("create = %+v, %v", a, err)
	}
	if _, err := s.CreateAccount(ctx, "a@example.com", "hash", "", now); !errors.Is(err, ErrEmailTaken) {
		t.Fatalf("duplicate = %v", err)
	}
	if got, err := s.AccountByEmail(ctx, "A@EXAMPLE.com"); err != nil || got.ID != a.ID {
		t.Fatalf("lookup = %+v, %v", got, err)
	}
	if _, err := s.AccountByEmail(ctx, "nobody@example.com"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing = %v", err)
	}

	cookie, ws, err := s.CreateWebSession(ctx, a.ID, time.Hour, now)
	if err != nil || !strings.HasPrefix(cookie, SessionPrefix) {
		t.Fatalf("session = %q, %v", cookie, err)
	}
	var stored string
	s.db.QueryRow(`SELECT group_concat(id_hash) FROM web_sessions`).Scan(&stored)
	if strings.Contains(stored, cookie) {
		t.Fatal("cookie value stored in the clear")
	}
	if got, err := s.LookupWebSession(ctx, cookie, now.Add(59*time.Minute)); err != nil || got.AccountID != a.ID || got.CSRFToken != ws.CSRFToken {
		t.Fatalf("lookup = %+v, %v", got, err)
	}
	if _, err := s.LookupWebSession(ctx, cookie, now.Add(time.Hour)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired = %v", err)
	}
	if err := s.DeleteWebSession(ctx, ws.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LookupWebSession(ctx, cookie, now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted = %v", err)
	}
	// Creating a session sweeps expired ones.
	s.CreateWebSession(ctx, a.ID, time.Minute, now)
	s.CreateWebSession(ctx, a.ID, time.Hour, now.Add(2*time.Minute))
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM web_sessions`).Scan(&n)
	if n != 1 {
		t.Fatalf("%d sessions left after sweep, want 1", n)
	}

	if _, err := s.CreateInvite(ctx, DefaultAccountID, 0, now); !errors.Is(err, ErrInvalid) {
		t.Fatalf("zero ttl = %v", err)
	}
}
