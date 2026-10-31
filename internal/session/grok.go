package session

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/DanBradbury/firekeeper/internal/fsutil"
)

const GrokSource = "Grok Build"

const grokMetadataBytes int64 = 1 << 20
const grokTailBytes int64 = 4 << 20

func grokArgs(command string) []string {
	fields := commandFields(command)
	if len(fields) == 0 {
		return nil
	}
	switch strings.ToLower(baseName(fields[0])) {
	case "grok", "grok.exe":
		return append([]string{}, fields[1:]...)
	default:
		return nil
	}
}

func (d *discoverer) isGrokCommand(command string) bool {
	args := grokArgs(command)
	if args == nil {
		return false
	}
	if len(args) == 0 {
		return true
	}
	switch args[0] {
	case "inspect", "doctor", "leader", "login", "logout", "mcp", "plugin", "memory",
		"models", "sessions", "usage", "setup", "share", "wrap", "export", "trace",
		"update", "version", "v", "completions", "worktree", "du", "disk-usage",
		"workspace", "help", "--help", "-h", "--version", "-V":
		return false
	}
	return true
}

func grokCommandID(command string) string {
	args := grokArgs(command)
	for i, arg := range args {
		for _, flag := range []string{"--resume", "-r", "--session-id", "--load"} {
			var id string
			switch {
			case strings.HasPrefix(arg, flag+"="):
				id = strings.TrimPrefix(arg, flag+"=")
			case arg == flag && i+1 < len(args):
				id = args[i+1]
			}
			if ValidUUID(id) {
				return id
			}
		}
	}
	return ""
}

// GrokHome resolves the official Grok Build store without creating it.
func GrokHome() (string, error) { return standalone().grokHome() }

func (d *discoverer) grokHome() (string, error) {
	if home := os.Getenv("GROK_HOME"); home != "" {
		return home, nil
	}
	home, err := d.home()
	if err != nil {
		return "", errors.New("grok: find home directory")
	}
	return filepath.Join(home, ".grok"), nil
}

// GrokTranscriptID accepts only the append-only primary log, never the
// chat_history.jsonl cache that Grok rewrites on compaction.
func GrokTranscriptID(path string) string {
	if filepath.Base(path) != "updates.jsonl" {
		return ""
	}
	id := filepath.Base(filepath.Dir(path))
	if !ValidUUID(id) {
		return ""
	}
	return id
}

func grokAbsolute(path string) bool {
	path = slashPath(path)
	return strings.HasPrefix(path, "/") || (len(path) >= 3 && path[1] == ':' && path[2] == '/')
}

// GrokWorkspace decodes sessions/<URL-encoded-cwd>, or the .cwd sidecar
// required by Grok's long-path slug-and-hash encoding.
func GrokWorkspace(projectDir string) string {
	if cwd, err := url.PathUnescape(filepath.Base(projectDir)); err == nil && grokAbsolute(cwd) {
		return cwd
	}
	data, err := grokReadBounded(filepath.Join(projectDir, ".cwd"), grokMetadataBytes)
	if err == nil {
		if cwd := strings.TrimSpace(string(data)); grokAbsolute(cwd) {
			return cwd
		}
	}
	return ""
}

func GrokSessionFiles(home string) []string {
	paths, _ := GrokSessionFilesContext(context.Background(), home)
	return paths
}

// GrokSessionFilesContext inventories primary logs, newest first.
func GrokSessionFilesContext(ctx context.Context, home string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	projects, err := os.ReadDir(filepath.Join(home, "sessions"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, errors.New("grok: list session workspaces")
	}
	var paths []string
	for _, project := range projects {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !project.IsDir() {
			continue
		}
		dir := filepath.Join(home, "sessions", project.Name())
		sessions, err := os.ReadDir(dir)
		if err != nil {
			return paths, errors.New("grok: list workspace sessions")
		}
		for _, s := range sessions {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if !s.IsDir() || !ValidUUID(s.Name()) {
				continue
			}
			path := filepath.Join(dir, s.Name(), "updates.jsonl")
			if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
				paths = append(paths, path)
			}
		}
	}
	sort.Slice(paths, func(i, j int) bool {
		a, ae := os.Stat(paths[i])
		b, be := os.Stat(paths[j])
		if ae != nil || be != nil || a.ModTime().Equal(b.ModTime()) {
			return paths[i] < paths[j]
		}
		return a.ModTime().After(b.ModTime())
	})
	return paths, ctx.Err()
}

func FindGrokTranscript(home, id string) string {
	if !ValidUUID(id) {
		return ""
	}
	for _, path := range GrokSessionFiles(home) {
		if strings.EqualFold(GrokTranscriptID(path), id) {
			return path
		}
	}
	return ""
}

// GrokUpdate is the subset of the persisted ACP/xAI notification used by
// discovery and the transcript reader. Unknown fields remain in Event.Raw.
type GrokUpdate struct {
	SessionID string
	Method    string
	Timestamp time.Time
	Update    json.RawMessage
}

func DecodeGrokUpdate(line []byte) (GrokUpdate, error) {
	var envelope struct {
		Timestamp *int64          `json:"timestamp"`
		Method    string          `json:"method"`
		Params    json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(line, &envelope); err != nil {
		return GrokUpdate{}, errors.New("grok: invalid update")
	}
	payload := line
	if envelope.Method != "" {
		if envelope.Method != "session/update" && envelope.Method != "_x.ai/session/update" {
			return GrokUpdate{}, errors.New("grok: unsupported update")
		}
		payload = envelope.Params
	}
	var notification struct {
		SessionID string          `json:"sessionId"`
		Update    json.RawMessage `json:"update"`
	}
	if json.Unmarshal(payload, &notification) != nil || !ValidUUID(notification.SessionID) || len(notification.Update) == 0 {
		return GrokUpdate{}, errors.New("grok: invalid notification")
	}
	var kind struct {
		Kind string `json:"sessionUpdate"`
	}
	if json.Unmarshal(notification.Update, &kind) != nil || kind.Kind == "" {
		return GrokUpdate{}, errors.New("grok: invalid notification")
	}
	update := GrokUpdate{SessionID: notification.SessionID, Method: envelope.Method, Update: notification.Update}
	if envelope.Timestamp != nil && *envelope.Timestamp >= 0 {
		update.Timestamp = time.Unix(*envelope.Timestamp, 0).UTC()
	}
	return update, nil
}

type GrokTranscript struct {
	SessionID, CWD, Title, Model, Branch string
	StartedAt, UpdatedAt                 time.Time
	State                                State
	Tokens                               TokenBreakdown
}

func grokReadBounded(path string, limit int64) ([]byte, error) {
	f, err := fsutil.Open(path)
	if err != nil {
		var pe *os.PathError
		if errors.As(err, &pe) {
			err = pe.Err
		}
		return nil, fmt.Errorf("grok: open metadata: %w", err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, errors.New("grok: unreadable or oversized metadata")
	}
	return data, nil
}

// ReadGrokSummary reads identity-checked metadata without opening the log,
// allowing callers to apply exclusions to relocated/hashed workspaces first.
func ReadGrokSummary(path string) (GrokTranscript, error) {
	t := GrokTranscript{SessionID: GrokTranscriptID(path)}
	if t.SessionID == "" {
		return t, errors.New("grok: invalid transcript path")
	}
	var summary struct {
		Info struct {
			ID  string `json:"id"`
			CWD string `json:"cwd"`
		} `json:"info"`
		Summary string    `json:"session_summary"`
		Title   string    `json:"generated_title"`
		Model   string    `json:"current_model_id"`
		Branch  string    `json:"head_branch"`
		Created time.Time `json:"created_at"`
		Updated time.Time `json:"updated_at"`
	}
	data, err := grokReadBounded(filepath.Join(filepath.Dir(path), "summary.json"), grokMetadataBytes)
	if err != nil {
		return t, err
	}
	if json.Unmarshal(data, &summary) != nil || !strings.EqualFold(summary.Info.ID, t.SessionID) {
		return t, errors.New("grok: invalid summary")
	}
	t.Title = FirstNonEmpty(summary.Title, summary.Summary)
	t.Model, t.Branch = summary.Model, summary.Branch
	if grokAbsolute(summary.Info.CWD) {
		t.CWD = summary.Info.CWD
	}
	t.StartedAt, t.UpdatedAt = summary.Created, summary.Updated
	return t, nil
}

// ReadGrokTranscript reads bounded head/tail metadata plus atomic sidecars.
// signals.json is cumulative behavioral telemetry, not billed token usage;
// usage.json is Grok's authoritative session billing sidecar.
func ReadGrokTranscript(path string) (GrokTranscript, error) {
	t := GrokTranscript{SessionID: GrokTranscriptID(path), State: SessionStateUnknown}
	if t.SessionID == "" {
		return t, errors.New("grok: invalid transcript path")
	}
	t.CWD = GrokWorkspace(filepath.Dir(filepath.Dir(path)))
	f, err := fsutil.Open(path)
	if err != nil {
		return t, errors.New("grok: open transcript")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return t, errors.New("grok: stat transcript")
	}
	t.UpdatedAt = info.ModTime()
	head, err := io.ReadAll(io.LimitReader(f, grokMetadataBytes))
	if err != nil {
		return t, errors.New("grok: read transcript")
	}
	valid := false
	scan := func(data []byte, state bool) {
		for _, line := range bytes.SplitAfter(data, []byte("\n")) {
			if len(line) == 0 || line[len(line)-1] != '\n' {
				continue
			}
			u, err := DecodeGrokUpdate(line)
			if err != nil || !strings.EqualFold(u.SessionID, t.SessionID) {
				continue
			}
			valid = true
			if t.StartedAt.IsZero() && !u.Timestamp.IsZero() {
				t.StartedAt = u.Timestamp
			}
			if u.Timestamp.After(t.UpdatedAt) {
				t.UpdatedAt = u.Timestamp
			}
			var v struct {
				Kind       string `json:"sessionUpdate"`
				Model      string `json:"model"`
				ModelID    string `json:"model_id"`
				NewModelID string `json:"new_model_id"`
				StopReason string `json:"stop_reason"`
			}
			if json.Unmarshal(u.Update, &v) != nil {
				continue
			}
			if model := FirstNonEmpty(v.ModelID, v.NewModelID, v.Model); model != "" {
				t.Model = model
			}
			if !state {
				continue
			}
			switch v.Kind {
			case "user_message_chunk", "agent_thought_chunk", "tool_call", "tool_call_update", "response_started", "plan_executing":
				t.State = SessionStateActive
			case "agent_message_chunk":
				t.State = SessionStateWaiting
			case "turn_completed", "auto_continue_completed":
				t.State = SessionStateWaiting
			case "response_completed":
				if v.StopReason == "end_turn" || v.StopReason == "stop_sequence" {
					t.State = SessionStateWaiting
				}
			}
		}
	}
	scan(head, info.Size() <= grokMetadataBytes)
	start := max(int64(0), info.Size()-grokTailBytes)
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return t, errors.New("grok: seek transcript")
	}
	tail, err := io.ReadAll(io.LimitReader(f, grokTailBytes))
	if err != nil {
		return t, errors.New("grok: read transcript")
	}
	if start > 0 {
		if i := bytes.IndexByte(tail, '\n'); i >= 0 {
			tail = tail[i+1:]
		} else {
			tail = nil
		}
	}
	scan(tail, true)
	if summary, err := ReadGrokSummary(path); err == nil {
		t.Title = summary.Title
		t.Model = FirstNonEmpty(t.Model, summary.Model)
		t.Branch = summary.Branch
		if summary.CWD != "" {
			t.CWD = summary.CWD
		}
		if !summary.StartedAt.IsZero() {
			t.StartedAt = summary.StartedAt
		}
		if summary.UpdatedAt.After(t.UpdatedAt) {
			t.UpdatedAt = summary.UpdatedAt
		}
		valid = true
	}
	if usage, err := ReadGrokUsage(path); err == nil {
		t.Tokens = usage.Tokens
	}
	if !valid {
		return t, errors.New("grok: empty or corrupt transcript")
	}
	return t, nil
}

type GrokUsageTurn struct {
	EndedAt time.Time
	Tokens  TokenBreakdown
}

type GrokUsage struct {
	Tokens     TokenBreakdown
	Turns      []GrokUsageTurn
	Incomplete bool
}

// ReadGrokUsage reads the authoritative billing sidecar. Dated turn deltas
// are separate from the cumulative session total and must not be added to it.
func ReadGrokUsage(path string) (GrokUsage, error) {
	type counts struct {
		Input      int64 `json:"inputTokens"`
		Output     int64 `json:"outputTokens"`
		Read       int64 `json:"cachedReadTokens"`
		Write      int64 `json:"cacheCreationTokens"`
		Incomplete bool  `json:"usageIsIncomplete"`
	}
	var file struct {
		ID      string `json:"sessionId"`
		Session counts `json:"session"`
		Turns   []struct {
			counts
			EndedAt string `json:"endedAt"`
		} `json:"turns"`
	}
	data, err := grokReadBounded(filepath.Join(filepath.Dir(path), "usage.json"), grokMetadataBytes)
	if err != nil {
		return GrokUsage{}, err
	}
	if json.Unmarshal(data, &file) != nil || GrokTranscriptID(path) == "" ||
		!strings.EqualFold(file.ID, GrokTranscriptID(path)) {
		return GrokUsage{}, errors.New("grok: invalid usage sidecar")
	}
	convert := func(c counts) (TokenBreakdown, error) {
		if c.Input < 0 || c.Output < 0 || c.Read < 0 || c.Write < 0 ||
			c.Read > c.Input || c.Write > c.Input-c.Read || c.Input > math.MaxInt64-c.Output {
			return TokenBreakdown{}, errors.New("grok: invalid token counts")
		}
		return TokenBreakdown{Input: c.Input - c.Read - c.Write, Output: c.Output, CacheRead: c.Read, CacheWrite: c.Write}, nil
	}
	total, err := convert(file.Session)
	if err != nil {
		return GrokUsage{}, err
	}
	result := GrokUsage{Tokens: total, Incomplete: file.Session.Incomplete}
	for _, turn := range file.Turns {
		tokens, err := convert(turn.counts)
		if err != nil {
			return GrokUsage{}, err
		}
		ended, _ := time.Parse(time.RFC3339Nano, turn.EndedAt)
		result.Turns = append(result.Turns, GrokUsageTurn{EndedAt: ended, Tokens: tokens})
	}
	return result, nil
}

func (d *discoverer) enrichGrokSessions(groups []ProcessGroup) error {
	var indexes []int
	for i := range groups {
		if groups[i].Tool == "Grok" {
			indexes = append(indexes, i)
		}
	}
	if len(indexes) == 0 {
		return nil
	}
	home, err := d.grokHome()
	if err != nil {
		return err
	}
	files, err := GrokSessionFilesContext(d.ctx, home)
	if err != nil {
		return err
	}
	cwds := d.claudeWorkDirs(groups, indexes)
	claimed := map[string]bool{}
	paths := map[int]string{}
	// IDs and held primary logs are stronger evidence than cwd recency.
	for _, i := range indexes {
		for _, process := range append([]ProcessInfo{groups[i].Root}, groups[i].Processes...) {
			id := grokCommandID(process.Command)
			for _, path := range files {
				if id != "" && strings.EqualFold(GrokTranscriptID(path), id) && !claimed[path] {
					paths[i], claimed[path] = path, true
					break
				}
			}
			if paths[i] != "" {
				break
			}
		}
		if paths[i] != "" || !openFilesListable {
			continue
		}
		for _, process := range append([]ProcessInfo{groups[i].Root}, groups[i].Processes...) {
			out, err := d.command("lsof", "-Fn", "-p", strconv.Itoa(process.PID)).Output()
			if err != nil {
				continue
			}
			for _, line := range strings.Split(string(out), "\n") {
				if !strings.HasPrefix(line, "n") {
					continue
				}
				path := strings.TrimPrefix(line, "n")
				for _, candidate := range files {
					if sameDirectory(path, candidate) && !claimed[candidate] {
						paths[i], claimed[candidate] = candidate, true
						break
					}
				}
				if paths[i] != "" {
					break
				}
			}
			if paths[i] != "" {
				break
			}
		}
	}
	sort.Slice(indexes, func(a, b int) bool { return groups[indexes[a]].Root.PID > groups[indexes[b]].Root.PID })
	for _, i := range indexes {
		if paths[i] != "" {
			continue
		}
		cwd := cwds[groups[i].Root.PID]
		if cwd == "" {
			continue
		}
		for _, path := range files {
			if !claimed[path] && sameDirectory(cwd, GrokWorkspace(filepath.Dir(filepath.Dir(path)))) {
				if summary, err := ReadGrokSummary(path); err == nil && summary.CWD != "" && !sameDirectory(cwd, summary.CWD) {
					continue
				}
				paths[i], claimed[path] = path, true
				break
			}
		}
	}
	missing := 0
	for _, i := range indexes {
		t, err := ReadGrokTranscript(paths[i])
		if err != nil {
			missing++
			continue
		}
		clean := d.sanitizeProcessCommand
		groups[i].Sessions = []Info{{
			ID: t.SessionID, Name: d.emptyFallback(clean(t.Title), GrokSource), CWD: clean(t.CWD),
			State: t.State, Model: clean(t.Model), Source: GrokSource, GitBranch: clean(t.Branch),
			StartedAt: t.StartedAt, UpdatedAt: t.UpdatedAt, RolloutPath: paths[i],
			Tokens: t.Tokens, TokensUsed: t.Tokens.Total(),
		}}
	}
	if missing > 0 {
		return fmt.Errorf("metadata unavailable for %d runtime(s)", missing)
	}
	return nil
}
