// Package reporter uploads local session metadata and redacted transcript
// events to a Firekeeper dashboard server in one pass.
//
// Upload is opt-in per provider. With no provider allowlisted, or with
// DryRun set, RunOnce reads and redacts as usual and prints what it would
// send, but never opens a network connection or moves a read offset.
package reporter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/DanBradbury/firekeeper/internal/config"
	"github.com/DanBradbury/firekeeper/internal/redact"
	"github.com/DanBradbury/firekeeper/internal/session"
	"github.com/DanBradbury/firekeeper/internal/transcript"

	// Register the transcript sources the reporter can read.
	_ "github.com/DanBradbury/firekeeper/internal/transcript/codex"
	_ "github.com/DanBradbury/firekeeper/internal/transcript/copilot"
)

// DefaultServer is the dashboard server's default listen address.
const DefaultServer = "http://127.0.0.1:7777"

// Ingest limits from the v1 contract. Batches stay under maxBatchBytes so the
// envelope and session metadata fit inside the 5 MiB request limit.
const (
	maxBatchEvents = 500
	maxBatchBytes  = 4 << 20
)

// Config configures one reporting pass. The zero value with Providers set
// reports to DefaultServer using the real home directory.
type Config struct {
	// Server is the dashboard base URL. Empty means DefaultServer.
	Server string
	// Providers is the upload allowlist. Empty uploads nothing.
	Providers []transcript.Provider
	// DryRun reads and redacts but uploads nothing and saves no offsets.
	DryRun bool
	// Exclude lists directory globs and Git remote patterns; matching
	// sessions are skipped without opening their transcripts.
	Exclude []string
	// Since, when positive, skips transcript files not modified within it.
	Since time.Duration
	// Home overrides the user's home directory for state, discovery, and
	// home-path redaction.
	Home string
	// StatePath overrides Home/.firekeeper/state.json.
	StatePath string
	// Version is sent as the machine's Firekeeper version.
	Version string
	// Out receives one summary line per session. Nil discards them.
	Out io.Writer

	// Discover, Sources, Client, and Now replace the real implementations
	// in tests. Nil means session.Discover, transcript.For,
	// a client with a 30 second timeout, and time.Now.
	Discover func(context.Context, session.Options) ([]session.Meta, error)
	Sources  func(transcript.Provider) (transcript.TranscriptSource, bool)
	Client   *http.Client
	Now      func() time.Time
}

// Uploading reports whether cfg can upload anything.
func (cfg Config) Uploading() bool {
	return !cfg.DryRun && len(cfg.Providers) > 0
}

// SessionResult summarizes one session's pass.
type SessionResult struct {
	Provider   string
	SessionID  string
	Project    string
	Skipped    string // reason the session was not read, if any
	Events     int    // new events read (and uploaded, unless dry run)
	Batches    int
	Duplicates int
	Redactions int
	Err        error
}

// Summary is the outcome of RunOnce.
type Summary struct {
	Sessions []SessionResult
	// Warning holds partial discovery failures that did not stop the pass.
	Warning error
}

// Failed reports whether any session hit an error.
func (s Summary) Failed() bool {
	for _, r := range s.Sessions {
		if r.Err != nil {
			return true
		}
	}
	return false
}

// RunOnce discovers sessions, reads events past the stored offsets, redacts
// them, and uploads them in batches. Offsets advance only after the server
// accepts a batch, so an interrupted pass loses nothing and the next pass
// re-sends nothing the server has confirmed. One session failing does not
// stop the others; their errors are in the summary. RunOnce returns an
// error only when the pass cannot start.
func RunOnce(ctx context.Context, cfg Config) (Summary, error) {
	r, err := newRun(cfg)
	if err != nil {
		return Summary{}, err
	}
	metas, err := r.discover(ctx, session.Options{Home: cfg.Home})
	var warning *session.Warning
	if err != nil && !errors.As(err, &warning) {
		return Summary{}, fmt.Errorf("discover sessions: %w", err)
	}
	summary := Summary{}
	if warning != nil {
		summary.Warning = warning
	}
	seen := map[string]bool{}
	for _, meta := range metas {
		if meta.ID == "" {
			continue // a runtime with no session metadata has no transcript
		}
		key := meta.Provider + "\x00" + meta.ID
		if seen[key] {
			continue
		}
		seen[key] = true
		if err := ctx.Err(); err != nil {
			return summary, err
		}
		result := r.session(ctx, meta)
		summary.Sessions = append(summary.Sessions, result)
		r.print(result)
	}
	return summary, nil
}

type run struct {
	cfg       Config
	upload    bool
	allowed   map[transcript.Provider]bool
	home      string
	statePath string
	state     *State
	server    string
	client    *http.Client
	discover  func(context.Context, session.Options) ([]session.Meta, error)
	sources   func(transcript.Provider) (transcript.TranscriptSource, bool)
	now       func() time.Time
	machine   machine
	exclude   *config.Excluder
}

func newRun(cfg Config) (*run, error) {
	r := &run{
		cfg:       cfg,
		upload:    cfg.Uploading(),
		allowed:   map[transcript.Provider]bool{},
		home:      cfg.Home,
		statePath: cfg.StatePath,
		discover:  cfg.Discover,
		sources:   cfg.Sources,
		now:       cfg.Now,
	}
	for _, p := range cfg.Providers {
		if !p.Valid() {
			return nil, fmt.Errorf("unknown provider %q", p)
		}
		r.allowed[p] = true
	}
	if r.home == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, errors.New("find home directory")
		}
		r.home = home
	}
	r.exclude = config.NewExcluder(cfg.Exclude, r.home)
	if r.statePath == "" {
		r.statePath = filepath.Join(r.home, ".firekeeper", "state.json")
	}
	if r.discover == nil {
		r.discover = session.Discover
	}
	if r.sources == nil {
		r.sources = transcript.For
	}
	if r.now == nil {
		r.now = time.Now
	}
	state, err := LoadState(r.statePath)
	if err != nil {
		return nil, err
	}
	r.state = state
	if !r.upload {
		return r, nil
	}
	server := cfg.Server
	if server == "" {
		server = DefaultServer
	}
	u, err := url.Parse(server)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("invalid server URL %q", server)
	}
	r.server = strings.TrimRight(server, "/")
	r.client = cfg.Client
	if r.client == nil {
		r.client = &http.Client{Timeout: 30 * time.Second}
	}
	hostname, _ := os.Hostname()
	r.machine = machine{Name: hostname, Hostname: hostname, OS: runtime.GOOS, Version: cfg.Version}
	return r, nil
}

func (r *run) session(ctx context.Context, meta session.Meta) SessionResult {
	result := SessionResult{Provider: meta.Provider, SessionID: meta.ID, Project: meta.Project}
	provider := transcript.Provider(meta.Provider)
	source, ok := r.sources(provider)
	switch {
	case !ok:
		result.Skipped = "no transcript reader for provider"
		return result
	case len(r.allowed) > 0 && !r.allowed[provider]:
		result.Skipped = "provider not allowlisted"
		return result
	case meta.CWD == "":
		// Without a directory the ignore rules cannot be checked.
		result.Skipped = "working directory unknown"
		return result
	case excluded(r.exclude, meta.CWD):
		result.Skipped = "excluded by config"
		return result
	case ignored(meta.CWD):
		result.Skipped = ".firekeeper-ignore"
		return result
	case r.upload && meta.MachineID == "":
		result.Err = errors.New("machine id unavailable")
		return result
	}
	paths, err := source.Locate(meta)
	if err != nil {
		result.Err = err
		return result
	}
	sessionMeta := r.sessionMeta(meta, &result)
	for _, path := range paths {
		if err := r.file(ctx, source, meta, sessionMeta, path, &result); err != nil {
			result.Err = err
			return result
		}
	}
	return result
}

// file reads one transcript file from its stored offset and uploads what is
// new.
func (r *run) file(ctx context.Context, source transcript.TranscriptSource, meta session.Meta, sessionMeta ingestMeta, path string, result *SessionResult) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return errors.New("resolve transcript path")
	}
	info, err := os.Stat(abs)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("stat transcript: %w", cause(err))
	}
	if r.cfg.Since > 0 && info.ModTime().Before(r.now().Add(-r.cfg.Since)) {
		return nil
	}
	pos, known := r.state.Files[abs]
	if !known || info.Size() < pos.Size {
		// New, truncated, or rotated: start over and let the server's
		// idempotency drop anything it already has.
		pos = newFileState()
	}
	events, newOffset, err := source.Read(abs, pos.Offset)
	if err != nil {
		return err
	}

	var pending []transcript.Event
	for _, e := range events {
		if e.Seq <= pos.LastSeq {
			continue // confirmed by an earlier, interrupted pass
		}
		e.MachineID = meta.MachineID
		e.SessionID = meta.ID
		redacted, counts := redact.Event(e, redact.Options{HomeDir: r.home})
		result.Redactions += counts.Total()
		pending = append(pending, redacted)
	}
	result.Events += len(pending)
	if !r.upload {
		return nil
	}

	batches, err := split(pending)
	if err != nil {
		return err
	}
	for _, batch := range batches {
		dups, err := r.post(ctx, meta.MachineID, sessionMeta, batch)
		if err != nil {
			return err
		}
		result.Batches++
		result.Duplicates += dups
		pos.LastSeq = max(pos.LastSeq, batch[len(batch)-1].Seq)
		r.state.Files[abs] = pos
		if err := r.state.Save(r.statePath); err != nil {
			return err
		}
	}
	if len(batches) == 0 && known && pos == r.state.Files[abs] && newOffset == pos.Offset {
		return nil // nothing changed; skip the write
	}
	pos.Offset = newOffset
	pos.Size = max(info.Size(), newOffset)
	pos.Mtime = info.ModTime().UTC()
	r.state.Files[abs] = pos
	return r.state.Save(r.statePath)
}

// split groups events into batches within the ingest limits.
func split(events []transcript.Event) ([][]transcript.Event, error) {
	var batches [][]transcript.Event
	var batch []transcript.Event
	size := 0
	for _, e := range events {
		data, err := json.Marshal(e)
		if err != nil {
			return nil, fmt.Errorf("encode event %d: %w", e.Seq, err)
		}
		if len(data) > maxBatchBytes {
			return nil, fmt.Errorf("event %d is %d bytes, over the upload limit", e.Seq, len(data))
		}
		if len(batch) == maxBatchEvents || size+len(data) > maxBatchBytes {
			batches = append(batches, batch)
			batch, size = nil, 0
		}
		batch = append(batch, e)
		size += len(data) + 1
	}
	if len(batch) > 0 {
		batches = append(batches, batch)
	}
	return batches, nil
}

// Wire types for POST /v1/ingest.
type machine struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Hostname string `json:"hostname"`
	OS       string `json:"os"`
	Version  string `json:"version"`
}

type ingestMeta struct {
	SessionID      string     `json:"session_id"`
	Provider       string     `json:"provider"`
	CWD            string     `json:"cwd"`
	Project        string     `json:"project"`
	Branch         string     `json:"branch"`
	Model          string     `json:"model"`
	State          string     `json:"state"`
	Title          string     `json:"title"`
	StartedAt      *time.Time `json:"started_at"`
	LastActivityAt *time.Time `json:"last_activity_at"`
}

type ingestSession struct {
	Meta   ingestMeta         `json:"meta"`
	Events []transcript.Event `json:"events"`
}

type ingestRequest struct {
	Machine  machine         `json:"machine"`
	Sessions []ingestSession `json:"sessions"`
}

type ingestResponse struct {
	Accepted   int `json:"accepted"`
	Duplicates int `json:"duplicates"`
}

type errorResponse struct {
	Error string `json:"error"`
	Code  string `json:"code"`
}

// sessionMeta builds the redacted metadata sent with every batch. Free-text
// fields can carry paths and pasted secrets, so they are redacted too.
func (r *run) sessionMeta(meta session.Meta, result *SessionResult) ingestMeta {
	opts := redact.Options{HomeDir: r.home}
	clean := func(s string) string {
		out, counts := redact.String(s, opts)
		result.Redactions += counts.Total()
		return out
	}
	state, _ := json.Marshal(meta.State)
	return ingestMeta{
		SessionID:      meta.ID,
		Provider:       meta.Provider,
		CWD:            clean(meta.CWD),
		Project:        meta.Project,
		Branch:         clean(meta.Branch),
		Model:          meta.Model,
		State:          strings.Trim(string(state), `"`),
		Title:          clean(meta.Title),
		StartedAt:      meta.StartedAt,
		LastActivityAt: meta.LastActivityAt,
	}
}

func (r *run) post(ctx context.Context, machineID string, meta ingestMeta, events []transcript.Event) (duplicates int, err error) {
	m := r.machine
	m.ID = machineID
	body, err := json.Marshal(ingestRequest{Machine: m, Sessions: []ingestSession{{Meta: meta, Events: events}}})
	if err != nil {
		return 0, fmt.Errorf("encode upload: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.server+"/v1/ingest", bytes.NewReader(body))
	if err != nil {
		return 0, fmt.Errorf("build upload request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := r.client.Do(req)
	if err != nil {
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		return 0, fmt.Errorf("upload: %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusOK {
		var e errorResponse
		if json.Unmarshal(data, &e) == nil && e.Code != "" {
			return 0, fmt.Errorf("upload: server returned %d (%s)", resp.StatusCode, e.Code)
		}
		return 0, fmt.Errorf("upload: server returned %d", resp.StatusCode)
	}
	var ok ingestResponse
	if err := json.Unmarshal(data, &ok); err != nil {
		return 0, errors.New("upload: server response is not valid JSON")
	}
	return ok.Duplicates, nil
}

func (r *run) print(res SessionResult) {
	if r.cfg.Out == nil {
		return
	}
	project := res.Project
	if project == "" {
		project = "-"
	}
	var status string
	switch {
	case res.Err != nil:
		status = "error: " + res.Err.Error()
	case res.Skipped != "":
		status = "skipped (" + res.Skipped + ")"
	case !r.upload:
		status = fmt.Sprintf("would upload %d events (%d redactions)", res.Events, res.Redactions)
	default:
		status = fmt.Sprintf("uploaded %d events in %d %s (%d duplicates, %d redactions)",
			res.Events, res.Batches, plural(res.Batches, "batch", "batches"), res.Duplicates, res.Redactions)
	}
	fmt.Fprintf(r.cfg.Out, "%s %s %s: %s\n", res.Provider, res.SessionID, project, status)
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// ignored reports whether dir is inside a repository, or below a
// directory, that contains .firekeeper-ignore. The search stops at the Git
// root so a marker outside the repository does not apply to it.
func ignored(dir string) bool {
	dir = filepath.Clean(dir)
	for {
		if _, err := os.Lstat(filepath.Join(dir, ".firekeeper-ignore")); err == nil {
			return true
		}
		if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
			return false
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false
		}
		dir = parent
	}
}

func excluded(e *config.Excluder, cwd string) bool {
	_, ok := e.Excluded(cwd)
	return ok
}
