package store

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"strings"
	"testing"
	"time"
)

// TestUsageSpeed seeds a production-sized database and times Usage. It only
// runs when FK_USAGE_BENCH names a database path to create, so it never
// slows the normal suite. Data is synthetic.
func TestUsageSpeed(t *testing.T) {
	path := os.Getenv("FK_USAGE_BENCH")
	if path == "" {
		t.Skip("set FK_USAGE_BENCH=/path/to/new.db to run")
	}
	ctx := context.Background()
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	const (
		acct     = "acct1"
		sessions = 500
		events   = 150000
		days     = 40
	)
	var n int
	s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM events`).Scan(&n)
	if n == 0 {
		seedUsage(t, s, acct, sessions, events, days)
	}

	now := time.Now().UTC().Truncate(24 * time.Hour)
	f := UsageFilter{From: now.AddDate(0, 0, -29), To: now.AddDate(0, 0, 1)}
	for _, g := range [][]string{{"day", "model"}, {"project"}} {
		f.GroupBy = g
		for i := 0; i < 3; i++ {
			start := time.Now()
			rows, sum, err := s.Usage(ctx, acct, f)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("group_by=%s run %d: %v rows=%d total=%d", strings.Join(g, ","), i+1, time.Since(start).Round(time.Millisecond), len(rows), total(sum.Tokens))
		}
	}
}

func seedUsage(t *testing.T, s *Store, acct string, sessions, events, days int) {
	t.Helper()
	ctx := context.Background()
	rng := rand.New(rand.NewSource(1))
	models := []string{"gpt-5", "gpt-5-mini", "claude-sonnet", "kimi-k2"}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO accounts(id, email, password_hash, created_at) VALUES (?, 'bench@example.test', '', '2026-01-01T00:00:00Z')`, acct); err != nil {
		t.Fatal(err)
	}
	for _, m := range []string{"m1", "m2"} {
		if _, err := tx.ExecContext(ctx, `INSERT INTO machines(account_id, id, name) VALUES (?, ?, ?)`, acct, m, m); err != nil {
			t.Fatal(err)
		}
	}
	start := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -days)
	for i := 0; i < sessions; i++ {
		if _, err := tx.ExecContext(ctx, `INSERT INTO sessions(account_id, machine_id, session_id, provider, project, model) VALUES (?,?,?,?,?,?)`,
			acct, fmt.Sprintf("m%d", i%2+1), fmt.Sprintf("s%d", i), []string{"codex", "copilot", "kimi"}[i%3],
			fmt.Sprintf("project-%d", i%30), models[i%len(models)]); err != nil {
			t.Fatal(err)
		}
	}
	text := strings.Repeat("lorem ipsum dolor sit amet ", 90) // ~2.4 KB
	raw := `{"x":"` + strings.Repeat("y", 2400) + `"}`
	seq := make([]int, sessions)
	for i := 0; i < events; i++ {
		si := rng.Intn(sessions)
		ts := start.Add(time.Duration(rng.Int63n(int64(days) * 24 * int64(time.Hour))))
		var in, out, cache int
		model := any(nil)
		if rng.Intn(4) == 0 {
			in, out, cache = rng.Intn(5000), rng.Intn(2000), rng.Intn(20000)
			model = models[rng.Intn(len(models))]
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO events(account_id, machine_id, session_id, seq, provider, ts, role, text, model, input_tokens, output_tokens, cache_tokens, raw)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`, acct, fmt.Sprintf("m%d", si%2+1), fmt.Sprintf("s%d", si), seq[si],
			[]string{"codex", "copilot", "kimi"}[si%3], fmtTime(&ts), "assistant", text, model, in, out, cache, raw); err != nil {
			t.Fatal(err)
		}
		seq[si]++
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// TestUsageUsesCoveringIndex guards the usage query against falling back to
// a scan of the events table, which holds every transcript payload.
func TestUsageUsesCoveringIndex(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, t.TempDir()+"/plan.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	rows, err := s.db.QueryContext(ctx, `EXPLAIN QUERY PLAN SELECT substr(e.ts, 1, 10), SUM(e.input_tokens), SUM(e.output_tokens), SUM(e.cache_tokens)
FROM events e WHERE e.account_id = ? AND e.ts >= ? AND e.ts < ? AND (e.input_tokens > 0 OR e.output_tokens > 0 OR e.cache_tokens > 0)
GROUP BY 1`, "a", "2026-01-01", "2026-02-01")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, detail)
	}
	if !strings.Contains(strings.Join(plan, "\n"), "COVERING INDEX events_usage") {
		t.Fatalf("usage query does not use events_usage: %v", plan)
	}
}
