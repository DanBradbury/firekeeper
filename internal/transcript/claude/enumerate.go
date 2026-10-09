package claude

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/DanBradbury/firekeeper/internal/session"
	"github.com/DanBradbury/firekeeper/internal/transcript"
)

// Enumerate lists historical top-level session transcripts. Project directory
// names are lossy encodings, so cwd is read from a bounded transcript head,
// never guessed from the directory name. Separate subagent files are omitted.
func (s *Source) Enumerate(ctx context.Context, opts transcript.EnumerateOptions) ([]session.Meta, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dir, err := s.configDir()
	if err != nil {
		return nil, err
	}
	paths, err := filepath.Glob(filepath.Join(dir, "projects", "*", "*.jsonl"))
	if err != nil {
		return nil, errors.New("claude: search transcripts failed")
	}
	type candidate struct {
		path  string
		mtime time.Time
	}
	byID := map[string]candidate{}
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		id := strings.ToLower(sessionIDFromPath(path))
		if id == "" {
			continue
		}
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if !opts.ModifiedAfter.IsZero() && !info.ModTime().After(opts.ModifiedAfter) {
			continue
		}
		if previous, exists := byID[id]; exists && !info.ModTime().After(previous.mtime) {
			continue
		}
		byID[id] = candidate{path: path, mtime: info.ModTime()}
	}
	var metas []session.Meta
	bad := 0
	for _, c := range byID {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		meta, err := readSessionHead(ctx, c.path)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			bad++
			continue
		}
		meta.ID = sessionIDFromPath(c.path)
		meta.Provider = string(transcript.ProviderClaude)
		meta.State = session.SessionStateEnded
		meta.RolloutPath = c.path
		meta.LastActivityAt = &c.mtime
		meta.UpdatedAt = c.mtime
		meta.Project = transcript.ProjectName(meta.CWD)
		meta.GitBranch = meta.Branch
		if opts.Skip != nil && opts.Skip(meta) {
			continue
		}
		metas = append(metas, meta)
	}
	transcript.SortNewestFirst(metas, nil)
	if bad > 0 {
		return metas, &session.Warning{Message: fmt.Sprintf("claude: skipped %d empty, unreadable, or corrupt transcript(s)", bad)}
	}
	return metas, nil
}

// readSessionHead decodes metadata only, retaining no conversation text.
func readSessionHead(ctx context.Context, path string) (session.Meta, error) {
	f, err := os.Open(path)
	if err != nil {
		return session.Meta{}, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(io.LimitReader(f, transcript.HeadBytes))
	scanner.Buffer(make([]byte, 4096), transcript.HeadBytes)
	var meta session.Meta
	decoded := false
	clean := func(value string) string { return strings.TrimSpace(session.SanitizeProcessCommand(value)) }
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return meta, err
		}
		var r struct {
			CWD       string `json:"cwd"`
			Branch    string `json:"gitBranch"`
			Timestamp string `json:"timestamp"`
			Message   *struct {
				Model string `json:"model"`
			} `json:"message"`
		}
		if json.Unmarshal(scanner.Bytes(), &r) != nil {
			continue
		}
		decoded = true
		meta.CWD = session.FirstNonEmpty(meta.CWD, clean(r.CWD))
		meta.Branch = session.FirstNonEmpty(meta.Branch, clean(r.Branch))
		if r.Message != nil && r.Message.Model != syntheticModel {
			meta.Model = session.FirstNonEmpty(meta.Model, clean(r.Message.Model))
		}
		if meta.StartedAt == nil {
			if ts, err := time.Parse(time.RFC3339Nano, r.Timestamp); err == nil {
				meta.StartedAt = &ts
			}
		}
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, bufio.ErrTooLong) {
		return meta, err
	}
	if !decoded {
		return meta, errors.New("no decodable record")
	}
	return meta, nil
}
