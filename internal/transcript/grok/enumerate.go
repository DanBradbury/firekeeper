package grok

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/DanBradbury/firekeeper/internal/session"
	"github.com/DanBradbury/firekeeper/internal/transcript"
	"github.com/DanBradbury/firekeeper/internal/transcript/reader"
)

func (s *Source) Enumerate(ctx context.Context, opts reader.EnumerateOptions) ([]session.Meta, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	home, err := s.home()
	if err != nil {
		return nil, err
	}
	clean := func(s string) string { return strings.TrimSpace(session.SanitizeProcessCommand(s)) }
	byID := map[string]session.Meta{}
	bad := 0
	paths, err := session.GrokSessionFilesContext(ctx, home)
	if err != nil {
		return nil, err
	}
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		info, err := os.Stat(path)
		if err != nil {
			bad++
			continue
		}
		if !opts.ModifiedAfter.IsZero() && !info.ModTime().After(opts.ModifiedAfter) {
			continue
		}
		cwd := session.GrokWorkspace(filepath.Dir(filepath.Dir(path)))
		activity := info.ModTime()
		meta := session.Meta{
			ID: session.GrokTranscriptID(path), Provider: string(transcript.ProviderGrok),
			CWD: clean(cwd), Project: reader.ProjectName(cwd), State: session.SessionStateEnded,
			Source: session.GrokSource, RolloutPath: path, UpdatedAt: activity, LastActivityAt: &activity,
		}
		summary, summaryErr := session.ReadGrokSummary(path)
		if summaryErr == nil && summary.CWD != "" {
			meta.CWD, meta.Project = clean(summary.CWD), reader.ProjectName(summary.CWD)
		}
		if opts.Skip != nil && opts.Skip(meta) {
			continue
		}
		if summaryErr != nil && !errors.Is(summaryErr, os.ErrNotExist) {
			bad++
		}
		t, err := session.ReadGrokTranscript(path)
		if err != nil {
			bad++
			continue
		}
		meta.CWD, meta.Project = clean(t.CWD), reader.ProjectName(t.CWD)
		meta.Title, meta.Name, meta.Model, meta.Branch = clean(t.Title), clean(t.Title), clean(t.Model), clean(t.Branch)
		meta.Input, meta.Output, meta.Cache, meta.TokensUsed = t.Tokens.Input, t.Tokens.Output, t.Tokens.Cache(), t.Tokens.Total()
		if !t.StartedAt.IsZero() {
			start := t.StartedAt
			meta.StartedAt = &start
		}
		if t.UpdatedAt.After(activity) {
			activity = t.UpdatedAt
			meta.UpdatedAt = activity
		}
		key := strings.ToLower(meta.ID)
		if _, exists := byID[key]; !exists {
			byID[key] = meta
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var metas []session.Meta
	for _, meta := range byID {
		metas = append(metas, meta)
	}
	reader.SortNewestFirst(metas, nil)
	if bad > 0 {
		return metas, &session.Warning{Message: fmt.Sprintf("grok: %d empty, unreadable, or corrupt metadata/transcript file(s)", bad)}
	}
	return metas, nil
}
