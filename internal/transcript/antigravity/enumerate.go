package antigravity

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/DanBradbury/firekeeper/internal/session"
	"github.com/DanBradbury/firekeeper/internal/transcript"
	"github.com/DanBradbury/firekeeper/internal/transcript/reader"
)

func (s *Source) Enumerate(ctx context.Context, opts reader.EnumerateOptions) ([]session.Meta, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dir, err := s.dir()
	if err != nil {
		return nil, err
	}
	cwds, cacheErr := session.AntigravityWorkDirs(dir)
	// An unreadable cache might hide an excluded cwd. Fail closed rather than
	// opening transcripts with known-but-unavailable workspace metadata.
	if cacheErr != nil {
		return nil, cacheErr
	}
	paths, err := filepath.Glob(filepath.Join(dir, "brain", "*", ".system_generated", "logs", "transcript.jsonl"))
	if err != nil {
		return nil, errors.New("antigravity: search transcripts")
	}
	var metas []session.Meta
	mtimes := map[string]time.Time{}
	bad := 0
	clean := func(s string) string { return strings.TrimSpace(session.SanitizeProcessCommand(s)) }
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return metas, err
		}
		id, ok := session.AntigravityTranscriptID(path)
		if !ok {
			continue
		}
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			bad++
			continue
		}
		if !opts.ModifiedAfter.IsZero() && !info.ModTime().After(opts.ModifiedAfter) {
			continue
		}
		meta := session.Meta{ID: id, Provider: string(transcript.ProviderAntigravity),
			State: session.SessionStateEnded, Source: session.AntigravitySource,
			RolloutPath: path, CWD: clean(cwds[id])}
		if opts.Skip != nil && opts.Skip(meta) {
			continue
		}
		t, err := session.ReadAntigravityTranscript(path)
		if err != nil {
			bad++
			continue
		}
		meta.Title, meta.Name, meta.Model = clean(t.Title), clean(t.Title), clean(t.Model)
		meta.Project = reader.ProjectName(meta.CWD)
		if !t.StartedAt.IsZero() {
			started := t.StartedAt
			meta.StartedAt = &started
		}
		activity := info.ModTime()
		if t.UpdatedAt.After(activity) {
			activity = t.UpdatedAt
		}
		meta.UpdatedAt, meta.LastActivityAt = activity, &activity
		metas = append(metas, meta)
		mtimes[path] = info.ModTime()
	}
	reader.SortNewestFirst(metas, mtimes)
	if bad > 0 {
		return metas, &session.Warning{Message: fmt.Sprintf("antigravity: skipped %d empty, unreadable, or corrupt transcript(s)", bad)}
	}
	return metas, nil
}
