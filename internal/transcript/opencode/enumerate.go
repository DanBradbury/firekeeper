package opencode

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/DanBradbury/firekeeper/internal/session"
	"github.com/DanBradbury/firekeeper/internal/transcript"
	"github.com/DanBradbury/firekeeper/internal/transcript/reader"
)

// Enumerate lists every top-level session in the database from its session
// row (session_v2, or session in the legacy layout), without reading messages
// beyond their last update time and first prompt.
// Subagent sessions, which have a parent, are left out.
func (s *Source) Enumerate(ctx context.Context, opts reader.EnumerateOptions) ([]session.Meta, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	db, err := s.database()
	if err != nil || db == "" {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	l, ok, err := s.open(ctx, db)
	if !ok || err != nil {
		return nil, err
	}
	var rows []struct {
		ID        string `json:"id"`
		Title     string `json:"title"`
		Directory string `json:"directory"`
		Model     string `json:"model"`
		Prompt    string `json:"first_prompt"`
		CreatedMS int64  `json:"time_created"`
		UpdatedMS int64  `json:"time_updated"`
	}
	query := fmt.Sprintf(`SELECT s.id, COALESCE(s.title, '') AS title, s.directory,
			COALESCE(s.model, '') AS model, s.time_created,
			COALESCE(%s, '') AS first_prompt,
			MAX(s.time_updated, COALESCE((SELECT MAX(m.time_updated) FROM %s m
				WHERE m.session_id = s.id), 0)) AS time_updated
		FROM %s s WHERE s.parent_id IS NULL`, session.OpenCodeFirstPromptSQL(l.legacy), l.messageTable(), l.sessionTable())
	if err := s.query(ctx, db, query, &rows); err != nil {
		return nil, err
	}
	clean := func(value string) string { return strings.TrimSpace(session.SanitizeProcessCommand(value)) }
	var metas []session.Meta
	bad := 0
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !session.ValidOpenCodeSessionID(row.ID) {
			bad++
			continue
		}
		updated := time.UnixMilli(row.UpdatedMS)
		if !opts.ModifiedAfter.IsZero() && !updated.After(opts.ModifiedAfter) {
			continue
		}
		meta := session.Meta{
			ID:             row.ID,
			Provider:       string(transcript.ProviderOpenCode),
			State:          session.SessionStateEnded,
			CWD:            clean(row.Directory),
			Model:          clean(session.OpenCodeModel([]byte(row.Model))),
			Title:          clean(session.OpenCodeTitle(row.Title, row.Prompt)),
			RolloutPath:    session.OpenCodeTranscriptPath(db, row.ID),
			UpdatedAt:      updated,
			LastActivityAt: &updated,
		}
		meta.Name = meta.Title
		if row.CreatedMS > 0 {
			started := time.UnixMilli(row.CreatedMS)
			meta.StartedAt = &started
		}
		meta.Project = reader.ProjectName(meta.CWD)
		if opts.Skip != nil && opts.Skip(meta) {
			continue
		}
		metas = append(metas, meta)
	}
	reader.SortNewestFirst(metas, nil)
	if bad > 0 {
		return metas, &session.Warning{Message: fmt.Sprintf("opencode: skipped %d session(s) with an unrecognized id", bad)}
	}
	return metas, nil
}
