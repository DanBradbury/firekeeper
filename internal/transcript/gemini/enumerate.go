package gemini

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

// Enumerate lists historical sessions with a main session log. The session id,
// start time, and kind come from the log's first record; the title (Gemini
// CLI's generated summary), model, and last activity from its bounded tail.
// The working directory is the project root its tmp/<project> directory
// belongs to. Subagent logs are omitted.
func (s *Source) Enumerate(ctx context.Context, opts reader.EnumerateOptions) ([]session.Meta, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dirs, err := s.dirs()
	if err != nil {
		return nil, err
	}
	type candidate struct {
		path       string
		runtimeDir string
		mtime      time.Time
	}
	var candidates []candidate
	for _, runtimeDir := range dirs {
		projects, err := filepath.Glob(filepath.Join(runtimeDir, "tmp", "*"))
		if err != nil {
			return nil, errors.New("gemini: search transcripts failed")
		}
		for _, projectDir := range projects {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			for _, path := range session.GeminiSessionFiles(projectDir) {
				info, err := os.Stat(path)
				if err != nil || !info.Mode().IsRegular() {
					continue
				}
				if !opts.ModifiedAfter.IsZero() && !info.ModTime().After(opts.ModifiedAfter) {
					continue
				}
				candidates = append(candidates, candidate{path: path, runtimeDir: runtimeDir, mtime: info.ModTime()})
			}
		}
	}

	clean := func(value string) string { return strings.TrimSpace(session.SanitizeProcessCommand(value)) }
	byID := map[string]session.Meta{}
	mtimes := map[string]time.Time{}
	bad := 0
	for _, c := range candidates {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		t, err := session.ReadGeminiTranscript(c.path)
		if err != nil {
			bad++
			continue
		}
		if t.Kind == "subagent" {
			continue
		}
		// A session resumed after the JSONL switch can leave a .json and a
		// .jsonl copy in different project directories; the .jsonl wins.
		if previous, exists := byID[t.SessionID]; exists {
			if strings.HasSuffix(previous.RolloutPath, ".jsonl") && !strings.HasSuffix(c.path, ".jsonl") {
				continue
			}
			if strings.HasSuffix(previous.RolloutPath, ".jsonl") == strings.HasSuffix(c.path, ".jsonl") && !c.mtime.After(mtimes[t.SessionID]) {
				continue
			}
		}
		meta := session.Meta{
			ID:          t.SessionID,
			Provider:    string(transcript.ProviderGemini),
			State:       session.SessionStateEnded,
			RolloutPath: c.path,
			Source:      session.GeminiSource,
			Model:       clean(t.Model),
			Title:       clean(t.Summary),
		}
		meta.Name = meta.Title
		meta.CWD = clean(session.GeminiProjectRoot(c.runtimeDir, filepath.Dir(filepath.Dir(c.path)), t.ProjectHash))
		meta.Project = reader.ProjectName(meta.CWD)
		if !t.StartedAt.IsZero() {
			started := t.StartedAt
			meta.StartedAt = &started
		}
		// The recorded lastUpdated can lag the log, so activity is the later
		// of the two.
		activity := c.mtime
		if t.UpdatedAt.After(activity) {
			activity = t.UpdatedAt
		}
		meta.LastActivityAt = &activity
		meta.UpdatedAt = activity
		byID[t.SessionID] = meta
		mtimes[t.SessionID] = c.mtime
	}

	var metas []session.Meta
	for _, meta := range byID {
		if opts.Skip != nil && opts.Skip(meta) {
			continue
		}
		metas = append(metas, meta)
	}
	reader.SortNewestFirst(metas, nil)
	if bad > 0 {
		return metas, &session.Warning{Message: fmt.Sprintf("gemini: skipped %d empty, unreadable, or corrupt transcript(s)", bad)}
	}
	return metas, nil
}
