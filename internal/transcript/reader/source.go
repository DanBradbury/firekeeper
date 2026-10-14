// Package reader holds the registry of provider transcript readers and the
// helpers that enumerate sessions with transcripts on disk. It depends on
// internal/session for session metadata; the event types it returns live in
// internal/transcript, which does not depend on session.
package reader

import (
	"fmt"
	"sort"
	"sync"

	"github.com/DanBradbury/firekeeper/internal/session"
	"github.com/DanBradbury/firekeeper/internal/transcript"
)

// Source reads one provider's transcripts into normalized events.
// Implementations read provider data only and never mutate it.
type Source interface {
	// Locate returns the transcript paths for a session. A session with no
	// transcript yet returns no paths and no error.
	Locate(meta session.Meta) ([]string, error)

	// Read returns events starting at fromOffset and the offset to resume
	// from. Offsets are bytes for JSONL sources and a row cursor for SQLite
	// sources; 0 always means the start. Reading at the end returns no
	// events and the same offset. Splitting a read at any returned offset
	// must yield the same Seq values as reading the whole source at once.
	Read(path string, fromOffset int64) (events []transcript.Event, newOffset int64, err error)
}

var (
	registryMu sync.RWMutex
	registry   = map[transcript.Provider]Source{}
)

// Register makes source available for provider. It panics if source is nil
// or provider already has a source, since both are programming errors.
func Register(provider transcript.Provider, source Source) {
	registryMu.Lock()
	defer registryMu.Unlock()
	if source == nil {
		panic(fmt.Sprintf("reader: Register source for %q is nil", provider))
	}
	if _, dup := registry[provider]; dup {
		panic(fmt.Sprintf("reader: Register called twice for %q", provider))
	}
	registry[provider] = source
}

// For returns the source registered for provider.
func For(provider transcript.Provider) (Source, bool) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	source, ok := registry[provider]
	return source, ok
}

// Registered returns the providers with a registered source, sorted.
func Registered() []transcript.Provider {
	registryMu.RLock()
	defer registryMu.RUnlock()
	providers := make([]transcript.Provider, 0, len(registry))
	for provider := range registry {
		providers = append(providers, provider)
	}
	sort.Slice(providers, func(i, j int) bool { return providers[i] < providers[j] })
	return providers
}
