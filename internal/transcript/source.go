package transcript

import (
	"fmt"
	"sort"
	"sync"

	"github.com/DanBradbury/firekeeper/internal/session"
)

// TranscriptSource reads one provider's transcripts into normalized events.
// Implementations read provider data only and never mutate it.
type TranscriptSource interface {
	// Locate returns the transcript paths for a session. A session with no
	// transcript yet returns no paths and no error.
	Locate(meta session.Meta) ([]string, error)

	// Read returns events starting at fromOffset and the offset to resume
	// from. Offsets are bytes for JSONL sources and a row cursor for SQLite
	// sources; 0 always means the start. Reading at the end returns no
	// events and the same offset. Splitting a read at any returned offset
	// must yield the same Seq values as reading the whole source at once.
	Read(path string, fromOffset int64) (events []Event, newOffset int64, err error)
}

var (
	registryMu sync.RWMutex
	registry   = map[Provider]TranscriptSource{}
)

// Register makes source available for provider. It panics if source is nil
// or provider already has a source, since both are programming errors.
func Register(provider Provider, source TranscriptSource) {
	registryMu.Lock()
	defer registryMu.Unlock()
	if source == nil {
		panic(fmt.Sprintf("transcript: Register source for %q is nil", provider))
	}
	if _, dup := registry[provider]; dup {
		panic(fmt.Sprintf("transcript: Register called twice for %q", provider))
	}
	registry[provider] = source
}

// For returns the source registered for provider.
func For(provider Provider) (TranscriptSource, bool) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	source, ok := registry[provider]
	return source, ok
}

// Registered returns the providers with a registered source, sorted.
func Registered() []Provider {
	registryMu.RLock()
	defer registryMu.RUnlock()
	providers := make([]Provider, 0, len(registry))
	for provider := range registry {
		providers = append(providers, provider)
	}
	sort.Slice(providers, func(i, j int) bool { return providers[i] < providers[j] })
	return providers
}
