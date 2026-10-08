package copilot

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/DanBradbury/firekeeper/internal/session"
	"github.com/DanBradbury/firekeeper/internal/transcript"
)

// Enumerate lists every session-state/<uuid>/events.jsonl. Metadata comes
// from the workspace.yaml beside it. Only when that lacks the working
// directory, branch, or start time, or (always, since workspace.yaml has no
// model) the model, is events.jsonl opened, and then only its first
// transcript.HeadBytes are read. session-store.db is not read.
func (s *Source) Enumerate(ctx context.Context, opts transcript.EnumerateOptions) ([]session.Meta, error) {
	home, err := s.copilotHome()
	if err != nil {
		return nil, err
	}
	paths, err := filepath.Glob(filepath.Join(home, "session-state", "*", eventsFile))
	if err != nil {
		return nil, fmt.Errorf("copilot: search sessions: %w", err)
	}
	sort.Strings(paths)

	var metas []session.Meta
	mtimes := make(map[string]time.Time, len(paths))
	bad := 0
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		id, ok := session.CopilotSessionIDFromStatePath(path)
		if !ok {
			continue
		}
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if !opts.ModifiedAfter.IsZero() && !info.ModTime().After(opts.ModifiedAfter) {
			continue
		}
		if info.Size() == 0 {
			bad++
			continue
		}
		meta := session.Meta{ID: id, Provider: string(transcript.ProviderCopilot), State: session.SessionStateEnded, RolloutPath: path}
		gitRoot := ""
		if ws, err := session.ReadCopilotWorkspace(filepath.Join(filepath.Dir(path), "workspace.yaml")); err == nil {
			meta.CWD = clean(session.FirstNonEmpty(ws.CWD, ws.GitRoot))
			gitRoot = clean(ws.GitRoot)
			meta.Branch = clean(ws.Branch)
			meta.Title = clean(ws.Name)
			meta.Repository = clean(ws.Repository)
			meta.StartedAt = optionalTime(ws.CreatedAt)
			meta.LastActivityAt = optionalTime(ws.UpdatedAt)
		}
		checked := false
		if meta.CWD != "" && opts.Skip != nil {
			checked = true
			if opts.Skip(finish(meta, gitRoot)) {
				continue
			}
		}
		// workspace.yaml never names the model, so the head is always read
		// for a session that is kept.
		h, err := s.readHead(path)
		if err != nil {
			bad++
			continue
		}
		meta.CWD = session.FirstNonEmpty(meta.CWD, clean(h.cwd))
		gitRoot = session.FirstNonEmpty(gitRoot, clean(h.gitRoot))
		meta.Branch = session.FirstNonEmpty(meta.Branch, clean(h.branch))
		meta.Model = clean(h.model)
		if meta.StartedAt == nil {
			meta.StartedAt = optionalTime(h.started)
		}
		meta = finish(meta, gitRoot)
		if !checked && opts.Skip != nil && opts.Skip(meta) {
			continue
		}
		mtimes[path] = info.ModTime()
		metas = append(metas, meta)
	}
	transcript.SortNewestFirst(metas, mtimes)
	if bad > 0 {
		return metas, &session.Warning{Message: fmt.Sprintf("copilot: skipped %d empty, unreadable, or corrupt event log(s)", bad)}
	}
	return metas, nil
}

// finish fills the fields derived from the others, matching Discover.
func finish(meta session.Meta, gitRoot string) session.Meta {
	if gitRoot != "" {
		meta.Project = filepath.Base(filepath.Clean(gitRoot))
	} else {
		meta.Project = transcript.ProjectName(meta.CWD)
	}
	meta.Name = meta.Title
	meta.GitBranch = meta.Branch
	if meta.LastActivityAt != nil {
		meta.UpdatedAt = *meta.LastActivityAt
	}
	return meta
}

func clean(value string) string {
	return strings.TrimSpace(session.SanitizeProcessCommand(value))
}

func optionalTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	t = t.UTC()
	return &t
}

type head struct {
	cwd, gitRoot, branch, model string
	started                     time.Time
}

// readHead decodes the first transcript.HeadBytes of an events file. It is
// corrupt when no complete line there is a JSON object. The model is the
// last one named in the head.
func (s *Source) readHead(path string) (head, error) {
	open := s.open
	if open == nil {
		open = func(name string) (io.ReadCloser, error) { return os.Open(name) }
	}
	file, err := open(path)
	if err != nil {
		return head{}, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(io.LimitReader(file, transcript.HeadBytes))
	scanner.Buffer(make([]byte, 0, 64<<10), transcript.HeadBytes)
	var h head
	decoded := false
	for scanner.Scan() {
		var r struct {
			Type      string `json:"type"`
			Timestamp string `json:"timestamp"`
			Data      struct {
				StartTime string `json:"startTime"`
				Model     string `json:"model"`
				NewModel  string `json:"newModel"`
				Context   *struct {
					CWD     string `json:"cwd"`
					GitRoot string `json:"gitRoot"`
					Branch  string `json:"branch"`
				} `json:"context"`
			} `json:"data"`
		}
		if json.Unmarshal(scanner.Bytes(), &r) != nil {
			continue
		}
		decoded = true
		switch r.Type {
		case "session.start", "session.resume":
			if c := r.Data.Context; c != nil {
				h.cwd = session.FirstNonEmpty(h.cwd, c.CWD)
				h.gitRoot = session.FirstNonEmpty(h.gitRoot, c.GitRoot)
				h.branch = session.FirstNonEmpty(h.branch, c.Branch)
			}
			if h.started.IsZero() {
				h.started = session.ParseCopilotTime(r.Data.StartTime)
			}
		case "session.model_change":
			h.model = session.FirstNonEmpty(r.Data.NewModel, h.model)
		case "assistant.message":
			h.model = session.FirstNonEmpty(r.Data.Model, h.model)
		}
		if h.started.IsZero() {
			h.started = session.ParseCopilotTime(r.Timestamp)
		}
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, bufio.ErrTooLong) {
		return head{}, err
	}
	if !decoded {
		return head{}, errors.New("no decodable record")
	}
	return h, nil
}
