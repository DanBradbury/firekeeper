// Package session discovers local agent runtimes without a terminal UI.
package session

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Options configures discovery. Zero values use the local environment.
// Run overrides command execution for fixture tests; it must honor ctx.
type Options struct {
	Home        string
	CodexHome   string
	CopilotHome string
	KimiHome    string
	// ClaudeHome overrides CLAUDE_CONFIG_DIR, which defaults to ~/.claude.
	ClaudeHome string
	Run        func(ctx context.Context, name string, args ...string) ([]byte, error)
}

// Meta describes one observable session. Runtimes without metadata have an
// empty ID and Unknown state. Project is the basename of the Git root or cwd.
// Runtime preserves grouping for dashboard clients. Unknown event counts and
// token breakdowns remain zero; aggregate tokens are retained in TokensUsed.
// Discovery does not scan full transcripts to infer unavailable totals.
type Meta struct {
	ID             string       `json:"session_id"`
	PID            int          `json:"pid"`
	TTY            string       `json:"tty"`
	MachineID      string       `json:"machine_id"`
	Project        string       `json:"project"`
	Provider       string       `json:"provider"`
	Name           string       `json:"-"`
	State          SessionState `json:"state"`
	CWD            string       `json:"cwd"`
	Model          string       `json:"model"`
	Source         string       `json:"source"`
	Repository     string       `json:"repository"`
	GitBranch      string       `json:"-"`
	RolloutPath    string       `json:"-"`
	UpdatedAt      time.Time    `json:"-"`
	TokensUsed     int64        `json:"-"`
	Runtime        ProcessGroup `json:"-"`
	Title          string       `json:"title"`
	Branch         string       `json:"branch"`
	StartedAt      *time.Time   `json:"started_at"`
	LastActivityAt *time.Time   `json:"last_activity_at"`
	EventCount     int64        `json:"event_count"`
	Input          int64        `json:"input"`
	Output         int64        `json:"output"`
	Cache          int64        `json:"cache"`
}

// Warning reports partial enrichment failures. Discover returns usable metadata
// alongside this error; fatal discovery failures return no metadata.
type Warning struct{ Message string }

func (w *Warning) Error() string { return w.Message }

type discoverer struct {
	ctx  context.Context
	opts Options
}

// command supports the existing Output call sites while keeping execution scoped
// to this discovery's context and runner.
type commandOutput struct {
	d    *discoverer
	name string
	args []string
}

func (d *discoverer) command(name string, args ...string) commandOutput {
	return commandOutput{d, name, args}
}
func (c commandOutput) Output() ([]byte, error) {
	if err := c.d.ctx.Err(); err != nil {
		return nil, err
	}
	if c.d.opts.Run != nil {
		return c.d.opts.Run(c.d.ctx, c.name, c.args...)
	}
	return exec.CommandContext(c.d.ctx, c.name, c.args...).Output()
}

// Discover performs one bounded scan. Callers choose their own polling interval.
// Each provider is best effort and cannot prevent other runtimes from appearing.
func Discover(ctx context.Context, opts Options) ([]Meta, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	d := &discoverer{ctx: ctx, opts: opts}
	output, err := d.discoverProcesses()
	if err != nil {
		return nil, fmt.Errorf("run ps: %w", err)
	}
	groups := d.groupProcesses(d.parseProcesses(string(output)))
	var warnings []string
	for _, p := range []struct {
		name   string
		enrich func([]ProcessGroup) error
	}{{"Codex", d.enrichCodexSessions}, {"Copilot", d.enrichCopilotSessions}, {"Kimi", d.enrichKimiSessions}, {"Claude", d.enrichClaudeSessions}} {
		if err := p.enrich(groups); err != nil {
			warnings = append(warnings, p.name+": "+d.sanitizeProcessCommand(err.Error()))
		}
	}
	machine, err := machineID(opts.Home)
	if err != nil {
		warnings = append(warnings, err.Error())
	}
	var metas []Meta
	projects := make(map[string]string)
	for _, g := range groups {
		sessions := g.Sessions
		if len(sessions) == 0 {
			sessions = []SessionInfo{{}}
		}
		for _, s := range sessions {
			cwd := s.CWD
			if cwd == "" {
				output, err := d.command("lsof", "-a", "-d", "cwd", "-Fn", "-p", fmt.Sprint(g.Root.PID)).Output()
				if err == nil {
					for _, line := range strings.Split(string(output), "\n") {
						if strings.HasPrefix(line, "n") {
							cwd = d.sanitizeProcessCommand(line[1:])
							break
						}
					}
				}
			}
			project, known := projects[cwd]
			if !known {
				project = cwd
				if cwd != "" {
					if root, err := d.command("git", "-C", cwd, "rev-parse", "--show-toplevel").Output(); err == nil {
						if resolved := strings.TrimSpace(string(root)); resolved != "" {
							project = d.sanitizeProcessCommand(resolved)
						}
					}
				}
				if project != "" {
					project = filepath.Base(project)
				}
				projects[cwd] = project
			}
			metas = append(metas, Meta{ID: s.ID, PID: g.Root.PID, TTY: g.Root.TTY, MachineID: machine, Project: project, Provider: strings.ToLower(g.Tool), Title: s.Name, Branch: s.GitBranch, StartedAt: optionalTime(s.StartedAt), LastActivityAt: optionalTime(s.UpdatedAt), Name: s.Name, State: s.State, CWD: cwd, Model: s.Model, Source: s.Source, Repository: s.Repository, GitBranch: s.GitBranch, RolloutPath: s.RolloutPath, UpdatedAt: s.UpdatedAt, TokensUsed: s.TokensUsed, Runtime: g})
		}
	}
	if err := ctx.Err(); err != nil {
		return metas, err
	}
	if len(warnings) > 0 {
		return metas, &Warning{strings.Join(warnings, "; ")}
	}
	return metas, nil
}

// MachineID returns the id stored in home/.firekeeper/machine-id, creating
// it on first use. An empty home means the user's home directory.
func MachineID(home string) (string, error) {
	return machineID(home)
}

var machineMu sync.Mutex

func machineID(home string) (string, error) {
	machineMu.Lock()
	defer machineMu.Unlock()
	if home == "" {
		var err error
		home, err = os.UserHomeDir()
		if err != nil {
			return "", errors.New("find Firekeeper home directory")
		}
	}
	dir := filepath.Join(home, ".firekeeper")
	path := filepath.Join(dir, "machine-id")
	if data, err := os.ReadFile(path); err == nil {
		id := strings.TrimSpace(string(data))
		if (&discoverer{}).validUUID(id) {
			return id, nil
		}
		return "", errors.New("invalid Firekeeper machine-id")
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", errors.New("read Firekeeper machine-id")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", errors.New("create Firekeeper identity directory")
	}
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", errors.New("generate Firekeeper machine-id")
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	id := fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
	// Publish a complete file atomically. Link prevents concurrent processes from
	// replacing an identity that another process already created.
	f, err := os.CreateTemp(dir, ".machine-id-")
	if err != nil {
		return "", errors.New("create Firekeeper machine-id")
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	_, writeErr := f.WriteString(id + "\n")
	closeErr := f.Close()
	if writeErr != nil || closeErr != nil {
		return "", errors.New("write Firekeeper machine-id")
	}
	if err := os.Link(tmp, path); err != nil {
		if errors.Is(err, os.ErrExist) {
			data, e := os.ReadFile(path)
			existing := strings.TrimSpace(string(data))
			if e == nil && (&discoverer{}).validUUID(existing) {
				return existing, nil
			}
		}
		return "", errors.New("publish Firekeeper machine-id")
	}
	return id, nil
}

func (d *discoverer) home() (string, error) {
	if d.opts.Home != "" {
		return d.opts.Home, nil
	}
	return os.UserHomeDir()
}

// Unavailable timestamps stay null instead of claiming a year-one activity date.
func optionalTime(t time.Time) *time.Time {
	if t.IsZero() || t.UnixMilli() == 0 {
		return nil
	}
	return &t
}
