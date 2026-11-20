package otelrecv

import (
	"os"
	"sync"
	"time"

	"github.com/firekeeper-ai/firekeeper/internal/parallel"
)

// LoadSupplements reads the telemetry JSONL and merges each session with
// its transcript. A missing file is an empty result. The result is cached
// until the file's size or modification time changes.
func LoadSupplements(path string) (map[string]Supplement, error) {
	if path == "" {
		var err error
		path, err = DefaultPath()
		if err != nil {
			return nil, err
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]Supplement{}, nil
		}
		return nil, err
	}
	supplementCache.mu.Lock()
	if supplementCache.path == path && supplementCache.size == info.Size() && supplementCache.mod.Equal(info.ModTime()) && supplementCache.val != nil {
		val := supplementCache.val
		supplementCache.mu.Unlock()
		return val, nil
	}
	supplementCache.mu.Unlock()

	recs, _, err := ReadFile(path)
	if err != nil {
		return nil, err
	}
	grouped := map[string][]Request{}
	for _, rec := range recs {
		grouped[rec.SessionID] = append(grouped[rec.SessionID], rec)
	}
	// Each session's transcript is read on its own worker; results are kept
	// by index and put in the map on this goroutine.
	ids := make([]string, 0, len(grouped))
	for id := range grouped {
		ids = append(ids, id)
	}
	merged := make([]Supplement, len(ids))
	parallel.Bounded(len(ids), func(i int) {
		usage, err := LoadTranscript(ids[i], "")
		if err != nil {
			usage = SessionUsage{}
		}
		merged[i] = Merge(usage, grouped[ids[i]])
	})
	out := make(map[string]Supplement, len(ids))
	for i, id := range ids {
		out[id] = merged[i]
	}
	supplementCache.mu.Lock()
	supplementCache.path = path
	supplementCache.size = info.Size()
	supplementCache.mod = info.ModTime()
	supplementCache.val = out
	supplementCache.mu.Unlock()
	return out, nil
}

// LastEvent reports the newest record timestamp in path. ok is false when
// the file is missing or no record has a timestamp.
func LastEvent(path string) (time.Time, bool, error) {
	if path == "" {
		var err error
		path, err = DefaultPath()
		if err != nil {
			return time.Time{}, false, err
		}
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return time.Time{}, false, nil
		}
		return time.Time{}, false, err
	}
	recs, _, err := ReadFile(path)
	if err != nil {
		return time.Time{}, false, err
	}
	var newest time.Time
	var found bool
	for _, rec := range recs {
		if rec.TS == "" {
			continue
		}
		ts, err := time.Parse(time.RFC3339Nano, rec.TS)
		if err != nil {
			continue
		}
		if !found || ts.After(newest) {
			newest = ts
			found = true
		}
	}
	return newest, found, nil
}

var supplementCache struct {
	mu   sync.Mutex
	path string
	size int64
	mod  time.Time
	val  map[string]Supplement
}
