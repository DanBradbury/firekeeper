package kimi

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
	"github.com/DanBradbury/firekeeper/internal/transcript/reader"
)

// Enumerate lists historical sessions that have a main agent wire log.
// Title, working directory, and times come from the session's state.json;
// a session whose state.json is missing or unreadable falls back to the
// bounded log head for its working directory. The model comes from the log
// head. Subagent logs are omitted.
func (s *Source) Enumerate(ctx context.Context, opts reader.EnumerateOptions) ([]session.Meta, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	home, err := s.home()
	if err != nil {
		return nil, err
	}
	paths, err := filepath.Glob(session.KimiWirePath(filepath.Join(home, "sessions", "*", "*")))
	if err != nil {
		return nil, errors.New("kimi: search transcripts failed")
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
		id := sessionIDFromPath(path)
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
	clean := func(value string) string { return strings.TrimSpace(session.SanitizeProcessCommand(value)) }
	var metas []session.Meta
	bad := 0
	for id, c := range byID {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		meta := session.Meta{
			ID:          id,
			Provider:    string(transcript.ProviderKimi),
			State:       session.SessionStateEnded,
			RolloutPath: c.path,
			Source:      "Kimi Code",
		}
		state, stateErr := readState(filepath.Join(filepath.Dir(filepath.Dir(filepath.Dir(c.path))), "state.json"))
		if stateErr == nil {
			meta.CWD = clean(state.WorkDir)
			meta.Title = clean(state.Title)
			if !state.CreatedAt.IsZero() {
				started := state.CreatedAt
				meta.StartedAt = &started
			}
		}
		skipped := false
		if meta.CWD != "" {
			meta.Project = reader.ProjectName(meta.CWD)
			if opts.Skip != nil && opts.Skip(meta) {
				continue
			}
			skipped = true // Skip already ran
		}
		head, err := readHead(ctx, c.path)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			bad++
			continue
		}
		meta.Model = clean(head.model)
		if meta.CWD == "" {
			meta.CWD = clean(head.cwd)
			meta.Project = reader.ProjectName(meta.CWD)
		}
		if meta.StartedAt == nil {
			meta.StartedAt = head.started
		}
		// state.json can lag the log, so activity is the later of the two.
		activity := c.mtime
		if stateErr == nil && state.UpdatedAt.After(activity) {
			activity = state.UpdatedAt
		}
		meta.LastActivityAt = &activity
		meta.UpdatedAt = activity
		meta.Name = meta.Title
		if !skipped && opts.Skip != nil && opts.Skip(meta) {
			continue
		}
		metas = append(metas, meta)
	}
	reader.SortNewestFirst(metas, nil)
	if bad > 0 {
		return metas, &session.Warning{Message: fmt.Sprintf("kimi: skipped %d empty, unreadable, or corrupt transcript(s)", bad)}
	}
	return metas, nil
}

func readState(path string) (session.KimiSessionState, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return session.KimiSessionState{}, err
	}
	var state session.KimiSessionState
	if err := json.Unmarshal(body, &state); err != nil {
		return session.KimiSessionState{}, err
	}
	return state, nil
}

type head struct {
	cwd     string
	model   string
	started *time.Time
}

// readHead decodes metadata only from the start of a wire log, retaining no
// conversation text: the log's creation time, the working directory from
// config.update, and the first requested model.
func readHead(ctx context.Context, path string) (head, error) {
	f, err := os.Open(path)
	if err != nil {
		return head{}, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(io.LimitReader(f, reader.HeadBytes))
	scanner.Buffer(make([]byte, 4096), reader.HeadBytes)
	var h head
	decoded := false
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return h, err
		}
		var r struct {
			Type        string          `json:"type"`
			CreatedAt   json.RawMessage `json:"created_at"`
			CWD         string          `json:"cwd"`
			Environment *struct {
				CWD string `json:"cwd"`
			} `json:"environmentDisclosure"`
			Model string `json:"model"`
		}
		if json.Unmarshal(scanner.Bytes(), &r) != nil || r.Type == "" {
			continue
		}
		decoded = true
		switch r.Type {
		case "metadata":
			if h.started == nil {
				h.started = timestamp(r.CreatedAt)
			}
		case "config.update":
			if r.Environment != nil {
				h.cwd = session.FirstNonEmpty(h.cwd, r.Environment.CWD)
			}
			h.cwd = session.FirstNonEmpty(h.cwd, r.CWD)
		case "llm.request":
			h.model = session.FirstNonEmpty(h.model, r.Model)
		}
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, bufio.ErrTooLong) {
		return h, err
	}
	if !decoded {
		return h, errors.New("no decodable record")
	}
	return h, nil
}
