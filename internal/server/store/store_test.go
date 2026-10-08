package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"

	"github.com/DanBradbury/firekeeper/internal/transcript"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "fk.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func batch(sid string, from, n int) SessionBatch {
	b := SessionBatch{Meta: SessionMeta{SessionID: sid, Provider: "codex", State: "ACTIVE"}}
	for i := from; i < from+n; i++ {
		b.Events = append(b.Events, transcript.Event{
			Provider: transcript.ProviderCodex, Seq: int64(i), Role: transcript.RoleUser,
			Text: "hello needle", Tokens: transcript.Tokens{Input: 2, Output: 3, Cache: 1},
			Raw: json.RawMessage(`{"a":1}`),
		})
	}
	return b
}

func TestIngestIdempotentAndFTS(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	m := Machine{ID: "m1"}
	a, d, err := s.Ingest(ctx, m, []SessionBatch{batch("s1", 0, 5)})
	if err != nil || a != 5 || d != 0 {
		t.Fatalf("first: %d %d %v", a, d, err)
	}
	a2, d2, err := s.Ingest(ctx, m, []SessionBatch{batch("s1", 0, 5)})
	if err != nil || a2 != 0 || d2 != a {
		t.Fatalf("second: %d %d %v", a2, d2, err)
	}
	st, _ := s.Stats(ctx, "m1", "s1")
	if st.EventCount != 5 || st.Input != 10 || st.Output != 15 || st.Cache != 5 {
		t.Fatalf("stats %+v", st)
	}
	hits, err := s.SearchText(ctx, "needle", 10)
	if err != nil || len(hits) != 5 {
		t.Fatalf("fts %v %v", hits, err)
	}
	v, _ := s.SchemaVersion(ctx)
	if v != 1 {
		t.Fatalf("version %d", v)
	}
}

func TestConcurrentIngest(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 5; i++ {
				if _, _, err := s.Ingest(ctx, Machine{ID: "m"}, []SessionBatch{batch("s", i*10, 20)}); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	wg.Wait()
	st, _ := s.Stats(ctx, "m", "s")
	if st.EventCount != 60 {
		t.Fatalf("count %d", st.EventCount)
	}
}

func TestNotifyAndHeartbeat(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	m := Machine{ID: "m"}
	if _, _, err := s.Ingest(ctx, m, []SessionBatch{batch("s", 0, 1)}); err != nil {
		t.Fatal(err)
	}
	if len(s.Changes()) < 2 {
		t.Fatal("expected notifications")
	}
	if err := s.Heartbeat(ctx, m, []Heartbeat{{SessionID: "s", State: "WAITING"}}); err != nil {
		t.Fatal(err)
	}
	if st, _ := s.Stats(ctx, "m", "s"); st.State != "WAITING" {
		t.Fatalf("state %s", st.State)
	}
}
