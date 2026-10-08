package codex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/DanBradbury/firekeeper/internal/session"
	"github.com/DanBradbury/firekeeper/internal/transcript"
)

// storeTimeout bounds the state database queries.
const storeTimeout = 10 * time.Second

// threadRow is the metadata Enumerate takes from state_5.sqlite. Columns
// that hold conversation text (preview, first_user_message) are not read.
type threadRow struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	CWD       string `json:"cwd"`
	Model     string `json:"model"`
	Branch    string `json:"git_branch"`
	CreatedMS int64  `json:"created_ms"`
	UpdatedMS int64  `json:"updated_ms"`
}

// Enumerate lists every rollout under sessions/*/*/*/ and archived_sessions/.
// Metadata comes from the threads table of state_5.sqlite, read with
// sqlite3 -readonly. Only when a row is missing the working directory,
// branch, model, or start time is the rollout opened, and then only its first
// transcript.HeadBytes are read. A thread with rollouts in both places is
// listed once, from the most recently modified file.
func (s *Source) Enumerate(ctx context.Context, opts transcript.EnumerateOptions) ([]session.Meta, error) {
	home, err := s.codexHome()
	if err != nil {
		return nil, err
	}
	type candidate struct {
		id    string
		path  string
		size  int64
		mtime time.Time
	}
	byID := map[string]candidate{}
	for _, pattern := range []string{
		filepath.Join(home, "sessions", "*", "*", "*", "rollout-*.jsonl"),
		filepath.Join(home, "archived_sessions", "rollout-*.jsonl"),
	} {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			return nil, fmt.Errorf("codex: search rollouts: %w", err)
		}
		for _, path := range matches {
			id, ok := session.ThreadIDFromRolloutPath(path)
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
			key := strings.ToLower(id)
			if prev, dup := byID[key]; dup && !info.ModTime().After(prev.mtime) {
				continue
			}
			byID[key] = candidate{id: id, path: path, size: info.Size(), mtime: info.ModTime()}
		}
	}
	candidates := make([]candidate, 0, len(byID))
	for _, c := range byID {
		candidates = append(candidates, c)
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].path < candidates[j].path })

	var warnings []string
	var rows map[string]threadRow
	if len(candidates) > 0 {
		if rows, err = s.readThreads(ctx, home); err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			warnings = append(warnings, err.Error())
		}
	}

	var metas []session.Meta
	mtimes := make(map[string]time.Time, len(candidates))
	bad := 0
	for _, c := range candidates {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if c.size == 0 {
			bad++
			continue
		}
		meta := session.Meta{ID: c.id, Provider: string(transcript.ProviderCodex), State: session.SessionStateEnded, RolloutPath: c.path}
		if row, ok := rows[strings.ToLower(c.id)]; ok {
			meta.Title = clean(row.Title)
			meta.CWD = clean(row.CWD)
			meta.Model = clean(row.Model)
			meta.Branch = clean(row.Branch)
			meta.StartedAt = millis(row.CreatedMS)
			meta.LastActivityAt = millis(row.UpdatedMS)
		}
		checked := false
		if meta.CWD != "" && opts.Skip != nil {
			checked = true
			if opts.Skip(finish(meta)) {
				continue
			}
		}
		if meta.CWD == "" || meta.Branch == "" || meta.Model == "" || meta.StartedAt == nil {
			h, err := s.readHead(c.path)
			if err != nil {
				bad++
				continue
			}
			meta.CWD = session.FirstNonEmpty(meta.CWD, clean(h.cwd))
			meta.Branch = session.FirstNonEmpty(meta.Branch, clean(h.branch))
			meta.Model = session.FirstNonEmpty(meta.Model, clean(h.model))
			if meta.StartedAt == nil && !h.started.IsZero() {
				started := h.started
				meta.StartedAt = &started
			}
		}
		meta = finish(meta)
		if !checked && opts.Skip != nil && opts.Skip(meta) {
			continue
		}
		mtimes[c.path] = c.mtime
		metas = append(metas, meta)
	}
	transcript.SortNewestFirst(metas, mtimes)
	if bad > 0 {
		warnings = append(warnings, fmt.Sprintf("codex: skipped %d empty, unreadable, or corrupt rollout(s)", bad))
	}
	if len(warnings) > 0 {
		return metas, &session.Warning{Message: strings.Join(warnings, "; ")}
	}
	return metas, nil
}

// finish fills the fields derived from the others, matching Discover.
func finish(meta session.Meta) session.Meta {
	meta.Project = transcript.ProjectName(meta.CWD)
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

func millis(ms int64) *time.Time {
	if ms <= 0 {
		return nil
	}
	t := time.UnixMilli(ms).UTC()
	return &t
}

// readThreads returns threads rows keyed by lower-case id. A missing
// database is not an error; Enumerate falls back to rollout heads.
func (s *Source) readThreads(ctx context.Context, home string) (map[string]threadRow, error) {
	path := filepath.Join(home, "state_5.sqlite")
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, errors.New("codex: state database unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, storeTimeout)
	defer cancel()
	output, err := s.sqlite(ctx, path, "PRAGMA table_info(threads)")
	if err != nil {
		return nil, errors.New("codex: inspect state database failed")
	}
	var schema []struct {
		Name string `json:"name"`
	}
	if len(bytes.TrimSpace(output)) > 0 {
		if err := json.Unmarshal(output, &schema); err != nil {
			return nil, errors.New("codex: decode state database schema failed")
		}
	}
	columns := make(map[string]bool, len(schema))
	for _, column := range schema {
		columns[column.Name] = true
	}
	if !columns["id"] {
		return nil, errors.New("codex: state database has no threads table")
	}
	query := fmt.Sprintf(`SELECT id, %s AS title, %s AS cwd, %s AS model, %s AS git_branch, %s AS created_ms, %s AS updated_ms FROM threads`,
		textColumn(columns, "name", "title"), textColumn(columns, "cwd"), textColumn(columns, "model"),
		textColumn(columns, "git_branch"), timeColumn(columns, "created"), timeColumn(columns, "updated"))
	output, err = s.sqlite(ctx, path, query)
	if err != nil {
		return nil, errors.New("codex: read state database failed")
	}
	var rows []threadRow
	if len(bytes.TrimSpace(output)) > 0 {
		if err := json.Unmarshal(output, &rows); err != nil {
			return nil, errors.New("codex: decode state database failed")
		}
	}
	result := make(map[string]threadRow, len(rows))
	for _, row := range rows {
		result[strings.ToLower(row.ID)] = row
	}
	return result, nil
}

func textColumn(columns map[string]bool, candidates ...string) string {
	parts := []string{}
	for _, c := range candidates {
		if columns[c] {
			parts = append(parts, "NULLIF("+c+", '')")
		}
	}
	return "COALESCE(" + strings.Join(append(parts, "''"), ", ") + ")"
}

// timeColumn reads <prefix>_at_ms, falling back to <prefix>_at in seconds.
func timeColumn(columns map[string]bool, prefix string) string {
	parts := []string{}
	if columns[prefix+"_at_ms"] {
		parts = append(parts, "NULLIF("+prefix+"_at_ms, 0)")
	}
	if columns[prefix+"_at"] {
		parts = append(parts, "NULLIF("+prefix+"_at, 0) * 1000")
	}
	return "COALESCE(" + strings.Join(append(parts, "0"), ", ") + ")"
}

func (s *Source) sqlite(ctx context.Context, path, query string) ([]byte, error) {
	args := []string{"-readonly", "-json", path, query}
	if s.run != nil {
		return s.run(ctx, "sqlite3", args...)
	}
	return exec.CommandContext(ctx, "sqlite3", args...).Output()
}

type head struct {
	cwd, branch, model string
	started            time.Time
}

// readHead decodes the first transcript.HeadBytes of a rollout. It is
// corrupt when no complete line there is a JSON object.
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
			Timestamp string `json:"timestamp"`
			Type      string `json:"type"`
			Payload   struct {
				Timestamp string `json:"timestamp"`
				CWD       string `json:"cwd"`
				Model     string `json:"model"`
				Git       *struct {
					Branch string `json:"branch"`
				} `json:"git"`
			} `json:"payload"`
		}
		if json.Unmarshal(scanner.Bytes(), &r) != nil {
			continue
		}
		decoded = true
		switch r.Type {
		case "session_meta":
			h.cwd = session.FirstNonEmpty(h.cwd, r.Payload.CWD)
			if r.Payload.Git != nil {
				h.branch = session.FirstNonEmpty(h.branch, r.Payload.Git.Branch)
			}
			if t := parseTime(r.Payload.Timestamp); !t.IsZero() {
				h.started = t
			}
		case "turn_context":
			h.cwd = session.FirstNonEmpty(h.cwd, r.Payload.CWD)
			if r.Payload.Model != "" {
				h.model = r.Payload.Model
			}
		}
		if h.started.IsZero() {
			h.started = parseTime(r.Timestamp)
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

func parseTime(value string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}
	}
	return t
}
