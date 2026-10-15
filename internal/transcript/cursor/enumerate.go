package cursor

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

// Enumerate lists historical main transcripts. The working directory comes
// from the project directory's name, accepted only when an existing
// directory encodes to it, so Skip runs before any session data is read.
// Title, model, and start time then come from the chat database when sqlite3
// can read it. Transcripts are never opened. Subagent transcripts are
// omitted.
func (s *Source) Enumerate(ctx context.Context, opts reader.EnumerateOptions) ([]session.Meta, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dir, err := s.dir()
	if err != nil {
		return nil, err
	}
	projects, err := filepath.Glob(filepath.Join(dir, "projects", "*"))
	if err != nil {
		return nil, errors.New("cursor: search transcripts failed")
	}
	type candidate struct {
		path, project string
		mtime         time.Time
	}
	byID := map[string]candidate{}
	for _, project := range projects {
		for _, path := range session.CursorSessionFiles(project) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			id := strings.ToLower(session.CursorTranscriptID(path))
			info, err := os.Stat(path)
			if err != nil || id == "" {
				continue
			}
			if !opts.ModifiedAfter.IsZero() && !info.ModTime().After(opts.ModifiedAfter) {
				continue
			}
			if previous, exists := byID[id]; exists && !info.ModTime().After(previous.mtime) {
				continue
			}
			byID[id] = candidate{path: path, project: filepath.Base(project), mtime: info.ModTime()}
		}
	}

	workspaces := map[string]string{}
	var metas []session.Meta
	bad := 0
	for _, c := range byID {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		cwd, known := workspaces[c.project]
		if !known {
			cwd = session.CursorWorkspace(c.project)
			workspaces[c.project] = cwd
		}
		mtime := c.mtime
		meta := session.Meta{
			ID:             session.CursorTranscriptID(c.path),
			Provider:       string(transcript.ProviderCursor),
			State:          session.SessionStateEnded,
			CWD:            session.SanitizeProcessCommand(cwd),
			Project:        reader.ProjectName(cwd),
			Source:         session.CursorSource,
			RolloutPath:    c.path,
			LastActivityAt: &mtime,
			UpdatedAt:      mtime,
		}
		if opts.Skip != nil && opts.Skip(meta) {
			continue
		}
		if chatPath := session.FindCursorChat(dir, cwd, meta.ID); chatPath != "" {
			chat, err := session.ReadCursorChat(ctx, chatPath)
			switch {
			case ctx.Err() != nil:
				return nil, ctx.Err()
			case err != nil || !strings.EqualFold(chat.ID, meta.ID):
				bad++
			default:
				clean := func(value string) string { return strings.TrimSpace(session.SanitizeProcessCommand(value)) }
				meta.Title, meta.Name, meta.Model = clean(chat.Name), clean(chat.Name), clean(chat.Model)
				if !chat.CreatedAt.IsZero() {
					started := chat.CreatedAt
					meta.StartedAt = &started
				}
			}
		}
		metas = append(metas, meta)
	}
	reader.SortNewestFirst(metas, nil)
	if bad > 0 {
		return metas, &session.Warning{Message: fmt.Sprintf("cursor: read no metadata from %d chat database(s)", bad)}
	}
	return metas, nil
}
