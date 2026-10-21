package reporter

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/DanBradbury/firekeeper/internal/session"
	"github.com/DanBradbury/firekeeper/internal/transcript"
	"github.com/DanBradbury/firekeeper/internal/transcript/reader"
)

// rowSource is a reader.Stater whose transcript is a list of rows inside a
// shared database, like OpenCode's, so its path is not a file on disk.
// Offsets are a seq cursor.
type rowSource struct {
	mu      sync.Mutex
	seqs    []int64
	updated time.Time
}

const rowPath = "/nonexistent/shared.db#ses_reporter"

func (s *rowSource) set(seqs ...int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seqs = seqs
	s.updated = s.updated.Add(time.Second)
}

func (s *rowSource) Locate(session.Meta) ([]string, error) { return []string{rowPath}, nil }

func (s *rowSource) Read(_ string, from int64) ([]transcript.Event, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var events []transcript.Event
	offset := from
	for _, seq := range s.seqs {
		if seq >= from {
			events = append(events, transcript.Event{Provider: transcript.ProviderOpenCode, Seq: seq, Role: transcript.RoleUser, Text: "row", Raw: []byte(`{}`)})
			offset = seq + 1
		}
	}
	return events, offset, nil
}

func (s *rowSource) Stat(string) (reader.Info, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	info := reader.Info{ModTime: s.updated}
	if n := len(s.seqs); n > 0 {
		info.Size = s.seqs[n-1] + 1
	}
	return info, nil
}

func (s *rowSource) Pending(_ context.Context, _ string, from int64) (int64, int64, error) {
	events, _, _ := s.Read("", from)
	return int64(len(events)), int64(len(events)) * 10, nil
}

func rowConfig(f *fixture, source *rowSource, server string) Config {
	cfg := f.config(server)
	cfg.Providers = []transcript.Provider{transcript.ProviderOpenCode}
	f.meta.Provider = string(transcript.ProviderOpenCode)
	f.meta.RolloutPath = rowPath
	cfg.Sources = func(p transcript.Provider) (reader.Source, bool) {
		return source, p == transcript.ProviderOpenCode
	}
	return cfg
}

// TestStaterSourceIsReadByCursor checks that a transcript inside a shared
// file is uploaded, resumed, and reset by its Stat, not by stat on its path.
func TestStaterSourceIsReadByCursor(t *testing.T) {
	f := newFixture(t)
	ts := newTestServer(t)
	source := &rowSource{updated: time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)}
	source.set(0, 2, 3)
	cfg := rowConfig(f, source, ts.URL)

	if got := runOnce(t, cfg); got.Err != nil || got.Events != 3 {
		t.Fatalf("first run = %+v", got)
	}
	state := func() FileState {
		t.Helper()
		s, err := LoadState(filepath.Join(f.home, ".firekeeper", "state.json"))
		if err != nil {
			t.Fatal(err)
		}
		return s.Files[rowPath]
	}
	if pos := state(); pos.Offset != 4 || pos.Size != 4 || pos.LastSeq != 3 {
		t.Fatalf("state = %+v, want offset and size 4, last seq 3", pos)
	}
	if got := runOnce(t, cfg); got.Events != 0 || got.Batches != 0 {
		t.Fatalf("second run = %+v", got)
	}

	source.set(0, 2, 3, 6)
	if got := runOnce(t, cfg); got.Events != 1 || got.Duplicates != 0 {
		t.Fatalf("run after a new row = %+v, want 1 new event", got)
	}

	// A revert deletes the newest rows, so the transcript shrinks: it is
	// read again from the start and the server drops what it has.
	source.set(0, 2)
	if got := runOnce(t, cfg); got.Err != nil || got.Events != 2 || got.Duplicates != 2 {
		t.Fatalf("run after shrinking = %+v", got)
	}
	if pos := state(); pos.Offset != 3 || pos.Size != 3 {
		t.Fatalf("state after shrinking = %+v", pos)
	}
	if _, err := os.Stat(rowPath); err == nil {
		t.Fatal("test path unexpectedly exists")
	}
}

func TestPendingAsksStaterSources(t *testing.T) {
	source := &rowSource{}
	source.set(0, 2, 3)
	events, bytes, err := pending(context.Background(), source, rowPath, 2, 4)
	if err != nil || events != 2 || bytes != 20 {
		t.Fatalf("pending = %d events, %d bytes, %v; want 2 and 20", events, bytes, err)
	}
}
