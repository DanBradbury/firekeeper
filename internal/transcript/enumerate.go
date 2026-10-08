package transcript

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/DanBradbury/firekeeper/internal/session"
)

// HeadBytes is the most an enumerator reads from the start of a transcript
// to fill metadata its provider's metadata store lacks.
const HeadBytes = 64 << 10

// EnumerateOptions narrows an enumeration pass.
type EnumerateOptions struct {
	// ModifiedAfter drops transcripts whose file modification time is not
	// after it. The check uses stat only. Zero keeps every transcript.
	ModifiedAfter time.Time
	// Skip drops a session the caller excludes, such as one in an ignored
	// directory. When the provider's metadata store supplies the working
	// directory, Skip runs before the transcript is opened; otherwise it runs
	// after the transcript head has been read for it. It is called at most
	// once per session. Nil keeps every session.
	Skip func(session.Meta) bool
}

// Enumerator lists every session with a transcript on disk, including
// sessions whose runtime has ended. It is optional: a TranscriptSource that
// does not implement it cannot be backfilled.
type Enumerator interface {
	// Enumerate returns sessions newest first by last activity, falling back
	// to file modification time. Each Meta has State SessionStateEnded and
	// RolloutPath set to the transcript, so Locate returns it directly.
	// Event counts and token totals stay zero. Empty, unreadable, or corrupt
	// transcripts are left out and counted in a *session.Warning returned
	// with the usable results.
	Enumerate(ctx context.Context, opts EnumerateOptions) ([]session.Meta, error)
}

// EnumeratorFor returns the enumerator for provider's registered source, if
// it has one.
func EnumeratorFor(provider Provider) (Enumerator, bool) {
	source, ok := For(provider)
	if !ok {
		return nil, false
	}
	enumerator, ok := source.(Enumerator)
	return enumerator, ok
}

// ProjectName returns the basename of the Git root containing cwd, or of cwd
// itself when no Git root is found or cwd no longer exists, as Discover does.
// It only stats paths.
func ProjectName(cwd string) string {
	if cwd == "" {
		return ""
	}
	cwd = filepath.Clean(cwd)
	if info, err := os.Stat(cwd); err != nil || !info.IsDir() {
		return filepath.Base(cwd)
	}
	for dir := cwd; ; {
		if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
			return filepath.Base(dir)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return filepath.Base(cwd)
		}
		dir = parent
	}
}

// SortNewestFirst orders metas by LastActivityAt, using fallback (keyed by
// RolloutPath) when it is unset, newest first. Ties sort by session id so
// the order is stable.
func SortNewestFirst(metas []session.Meta, fallback map[string]time.Time) {
	activity := func(m session.Meta) time.Time {
		if m.LastActivityAt != nil {
			return *m.LastActivityAt
		}
		return fallback[m.RolloutPath]
	}
	sort.SliceStable(metas, func(i, j int) bool {
		a, b := activity(metas[i]), activity(metas[j])
		if !a.Equal(b) {
			return a.After(b)
		}
		return metas[i].ID < metas[j].ID
	})
}
