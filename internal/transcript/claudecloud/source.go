package claudecloud

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/DanBradbury/firekeeper/internal/session"
	"github.com/DanBradbury/firekeeper/internal/transcript"
	"github.com/DanBradbury/firekeeper/internal/transcript/claude"
	"github.com/DanBradbury/firekeeper/internal/transcript/reader"
)

// Source reads the cache Sync writes. It implements reader.Source and
// reader.Enumerator for provider claude, but is not registered: only
// `backfill --claude-cloud` passes it to the reporter, in place of the local
// Claude Code source. Cloud sessions keep their own ids, which are not UUIDs.
type Source struct {
	// Dir is the cache directory Sync wrote.
	Dir string
}

// Origin is the ingest `origin` of every session this package imports.
const Origin = "cloud"

var (
	_ reader.Source     = (*Source)(nil)
	_ reader.Enumerator = (*Source)(nil)
)

// Locate returns the cached transcript for meta, if there is one.
func (s *Source) Locate(meta session.Meta) ([]string, error) {
	if meta.Provider != "" && meta.Provider != string(transcript.ProviderClaude) {
		return nil, nil
	}
	if !safeID(meta.ID) {
		return nil, nil
	}
	path := transcriptPath(s.Dir, meta.ID)
	if _, err := os.Stat(path); err != nil {
		return nil, nil
	}
	return []string{path}, nil
}

// Read parses the cached transcript with the Claude Code parser, under the
// cloud session id taken from the file name.
func (s *Source) Read(path string, fromOffset int64) ([]transcript.Event, int64, error) {
	id := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	if !safeID(id) {
		return nil, fromOffset, errors.New("claudecloud: invalid transcript name")
	}
	return claude.ReadAs(path, fromOffset, id)
}

// Enumerate lists the cached sessions, newest update first. Sessions with no
// conversation events are left out.
func (s *Source) Enumerate(ctx context.Context, opts reader.EnumerateOptions) ([]session.Meta, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	paths, err := filepath.Glob(filepath.Join(s.Dir, "*.json"))
	if err != nil {
		return nil, errors.New("claudecloud: search cache")
	}
	var metas []session.Meta
	bad := 0
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		id := strings.TrimSuffix(filepath.Base(path), ".json")
		info, ok := readCached(s.Dir, id)
		if !ok {
			bad++
			continue
		}
		if info.Events == 0 {
			continue
		}
		if !opts.ModifiedAfter.IsZero() && !info.UpdatedAt.After(opts.ModifiedAfter) {
			continue
		}
		meta := metaFor(s.Dir, info)
		if opts.Skip != nil && opts.Skip(meta) {
			continue
		}
		metas = append(metas, meta)
	}
	reader.SortNewestFirst(metas, nil)
	if bad > 0 {
		return metas, &session.Warning{Message: "claudecloud: skipped unreadable cache entries"}
	}
	return metas, nil
}

// cloudRoot is the virtual working directory of a cloud session. The cloud
// container's real directory is not reported, and the reporter excludes
// sessions without one. The path is absolute so no search for a Git root or
// ignore file can climb into the directory firekeeper runs from, and it names
// the repository so the project shows as the repository.
const cloudRoot = "/claude-cloud"

// cwdFor returns the working directory shown for a cloud session: the
// container's, when the API reports one, else cloudRoot/owner/name.
func cwdFor(info SessionInfo) string {
	if info.CWD != "" {
		return info.CWD
	}
	if info.Repo != "" {
		return cloudRoot + "/" + info.Repo
	}
	return cloudRoot
}

func metaFor(dir string, info SessionInfo) session.Meta {
	created, updated := info.CreatedAt, info.UpdatedAt
	cwd := cwdFor(info)
	meta := session.Meta{
		ID:          info.ID,
		Provider:    string(transcript.ProviderClaude),
		Origin:      Origin,
		State:       session.SessionStateEnded,
		CWD:         cwd,
		Project:     reader.ProjectName(cwd),
		Model:       info.Model,
		Title:       info.Title,
		Branch:      info.Branch,
		GitBranch:   info.Branch,
		RolloutPath: transcriptPath(dir, info.ID),
		UpdatedAt:   updated,
	}
	if !created.IsZero() {
		meta.StartedAt = &created
	}
	if !updated.IsZero() {
		meta.LastActivityAt = &updated
	}
	return meta
}
