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

	"github.com/firekeeper-ai/firekeeper/internal/parallel"
	"github.com/firekeeper-ai/firekeeper/internal/sqlitejson"
	"github.com/firekeeper-ai/firekeeper/pkg/event"
)

// Options configures discovery. Zero values use the local environment.
// Run overrides command execution for fixture tests; it must honor ctx.
// Discover links each provider on its own goroutine and reads files and runs
// commands for several sessions at once, so Run is called from several
// goroutines at once and must be safe for that.
type Options struct {
	Home        string
	CodexHome   string
	CopilotHome string
	KimiHome    string
	// ClaudeHome overrides CLAUDE_CONFIG_DIR, which defaults to ~/.claude.
	ClaudeHome string
	// OpenCodeDB overrides the OpenCode database path, which defaults to
	// OPENCODE_DB or $XDG_DATA_HOME/opencode/opencode.db.
	OpenCodeDB string
	// GeminiDir overrides Gemini CLI's runtime directory, which defaults to
	// $GEMINI_CLI_HOME/.gemini or ~/.gemini.
	GeminiDir string
	// CursorDir overrides the Cursor CLI data directory, which defaults to
	// CURSOR_CONFIG_DIR or ~/.cursor.
	CursorDir string
	Run       func(ctx context.Context, name string, args ...string) ([]byte, error)
	// Args overrides reading a process's argument list from the operating
	// system, for tests. When it is nil and Run is set, arguments are split
	// from the ps command line, so fixture runs never read live processes.
	Args func(pid int) ([]string, error)
}

// Meta describes one observable session. Runtimes without metadata have an
// empty ID and Unknown state. Project is the basename of the Git root or cwd.
// Runtime preserves grouping for dashboard clients. Unknown event counts and
// token breakdowns remain zero; aggregate tokens are retained in TokensUsed.
// Discovery does not scan full transcripts to infer unavailable totals.
type Meta struct {
	ID        string         `json:"session_id"`
	PID       int            `json:"pid"`
	TTY       string         `json:"tty"`
	MachineID string         `json:"machine_id"`
	Project   string         `json:"project"`
	Provider  event.Provider `json:"provider"`
	// Origin is "cloud" for a session that ran on a hosted service and was
	// imported, and empty for one that ran on this machine.
	Origin string `json:"-"`
	// ParentID is the session that spawned this one (a Codex subagent
	// thread's parent), AgentName and AgentRole the agent's name and role.
	// They are empty for a top-level session and when the provider does not
	// record them, and are uploaded as parent_session_id, agent_name and
	// agent_role.
	ParentID  string `json:"-"`
	AgentName string `json:"-"`
	AgentRole string `json:"-"`
	// ForkedFromID is the session a forked session was forked from (a Codex
	// user fork, or a subagent spawned with its parent's context), uploaded
	// as forked_from_session_id. Its tokens include the copied history.
	ForkedFromID   string       `json:"-"`
	Name           string       `json:"-"`
	State          State        `json:"state"`
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
	// UndercountWarning is set for a Claude Code session with subagent
	// transcripts. It is omitted otherwise and is not part of the token totals.
	// It is local snapshot metadata; upload does not send it.
	UndercountWarning string `json:"undercount_warning,omitempty"`
}

// Warning reports partial enrichment failures. Discover returns usable metadata
// alongside this error; fatal discovery failures return no metadata.
type Warning struct{ Message string }

func (w *Warning) Error() string { return w.Message }

type discoverer struct {
	ctx  context.Context
	opts Options
}

// standalone returns a discoverer with default options for the exported
// helpers that callers use outside a Discover call.
func standalone() *discoverer { return &discoverer{ctx: context.Background()} }

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
	if out, handled, err := nativeCommand(c.name, c.args); handled {
		return out, err
	}
	if c.name == "sqlite3" {
		// Windows has no sqlite3 command line tool; sqlitejson reads the
		// database in-process there and anywhere else it is missing.
		return sqlitejson.Run(c.d.ctx, c.args...)
	}
	return exec.CommandContext(c.d.ctx, c.name, c.args...).Output()
}

// linkPanic is a panic from one provider's Link, carried from its goroutine
// to Discover's caller. It names the provider and keeps the stack of the
// goroutine that panicked, which the second panic would otherwise lose.
type linkPanic struct {
	provider string
	cause    *parallel.Panic
}

func (p *linkPanic) Error() string {
	return fmt.Sprintf("panic linking %s sessions: %v\n\n%s", p.provider, p.cause.Value, p.cause.Stack)
}

// Unwrap returns the original panic value when it was an error.
func (p *linkPanic) Unwrap() error {
	return p.cause.Unwrap()
}

// linkProviders calls link for each adapter index in active, one goroutine
// per provider. A panic in a Link reaches the caller as a *linkPanic naming
// that provider, after every Link has returned; with several, the first in
// registry order.
func linkProviders(active []int, link func(index int)) {
	defer func() {
		if value := recover(); value != nil {
			if p, ok := value.(*parallel.Panic); ok {
				panic(&linkPanic{provider: ToolName(adapters[active[p.Index]].Provider()), cause: p})
			}
			panic(value)
		}
	}()
	parallel.Each(len(active), func(i int) { link(active[i]) })
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
	// Partition the groups by provider first, and set explicit ids, before any
	// Link runs: a running Link rewrites its own groups, so nothing else may
	// read them from here until every Link has returned.
	owned := make([][]*ProcessGroup, len(adapters))
	for index, a := range adapters {
		for i := range groups {
			g := &groups[i]
			if g.Provider != a.Provider() {
				continue // another provider's group (step 4)
			}
			link, explicit := linkRole(g.Role)
			if !link {
				continue
			}
			if explicit {
				// Explicit ids first, the same way for every provider: Link
				// gives the group exactly that session (step 7).
				if id, ok := a.SessionID(g.Root.Invocation); ok {
					g.explicitID = id
				}
			}
			owned[index] = append(owned[index], g)
		}
	}
	// Providers are linked concurrently, one goroutine per provider that has
	// groups, so a slow provider does not hold up the others. Each Link gets
	// only its own provider's groups, which no other goroutine touches. Each
	// provider's warning is kept at its adapter's index, so the joined
	// message does not depend on which Link finished first. A panic in a
	// Link reaches the caller on its goroutine (see linkProviders).
	var active []int
	for index := range adapters {
		if len(owned[index]) > 0 {
			active = append(active, index)
		}
	}
	linkWarnings := make([]string, len(adapters))
	linkProviders(active, func(index int) {
		a := adapters[index]
		if err := a.Link(d, owned[index]); err != nil {
			linkWarnings[index] = ToolName(a.Provider()) + ": " + d.sanitizeProcessCommand(err.Error())
		}
	})
	var warnings []string
	for _, warning := range linkWarnings {
		if warning != "" {
			warnings = append(warnings, warning)
		}
	}
	machine, err := machineID(opts.Home)
	if err != nil {
		warnings = append(warnings, err.Error())
	}
	// Resolve each emitted group's fallback cwd (one lsof per group), then
	// each distinct cwd's project (one git per directory), concurrently.
	var emit []*ProcessGroup
	for i := range groups {
		if emitted(groups[i]) {
			emit = append(emit, &groups[i])
		}
	}
	rootCWDs := make([]string, len(emit))
	parallel.Bounded(len(emit), func(i int) {
		g := emit[i]
		needed := len(g.Sessions) == 0
		for _, s := range g.Sessions {
			needed = needed || s.CWD == ""
		}
		if needed {
			rootCWDs[i] = d.processCWD(g.Root.PID)
		}
	})
	var cwds []string
	projects := make(map[string]string)
	for i, g := range emit {
		for _, s := range g.Sessions {
			if _, seen := projects[s.CWD]; !seen && s.CWD != "" {
				projects[s.CWD] = ""
				cwds = append(cwds, s.CWD)
			}
		}
		if _, seen := projects[rootCWDs[i]]; !seen && rootCWDs[i] != "" {
			projects[rootCWDs[i]] = ""
			cwds = append(cwds, rootCWDs[i])
		}
	}
	resolved := make([]string, len(cwds))
	parallel.Bounded(len(cwds), func(i int) { resolved[i] = d.projectName(cwds[i]) })
	for i, cwd := range cwds {
		projects[cwd] = resolved[i]
	}
	var metas []Meta
	for i, g := range emit {
		sessions := g.Sessions
		if len(sessions) == 0 {
			sessions = []Info{{}}
		}
		for _, s := range sessions {
			cwd := s.CWD
			if cwd == "" {
				cwd = rootCWDs[i]
			}
			project := projects[cwd]
			metas = append(metas, Meta{ID: s.ID, PID: g.Root.PID, TTY: g.Root.TTY, MachineID: machine, Project: project, Provider: g.Provider, Title: s.Name, Branch: s.GitBranch, StartedAt: optionalTime(s.StartedAt), LastActivityAt: optionalTime(s.UpdatedAt), Name: s.Name, State: s.State, CWD: cwd, Model: s.Model, Source: s.Source, Repository: s.Repository, GitBranch: s.GitBranch, RolloutPath: s.RolloutPath, UpdatedAt: s.UpdatedAt, TokensUsed: s.TokensUsed, UndercountWarning: s.UndercountWarning, ParentID: s.ParentID, AgentName: s.AgentName, AgentRole: s.AgentRole, ForkedFromID: s.ForkedFrom, Runtime: *g})
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

// projectName returns the basename of cwd's Git root, else of cwd itself.
func (d *discoverer) projectName(cwd string) string {
	if cwd == "" {
		return ""
	}
	project := cwd
	if root, err := d.command("git", "-C", cwd, "rev-parse", "--show-toplevel").Output(); err == nil {
		if resolved := strings.TrimSpace(string(root)); resolved != "" {
			project = d.sanitizeProcessCommand(resolved)
		}
	}
	return filepath.Base(project)
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

// emitted applies the output rule (invariant 6) to a linked group: session
// groups always, linked or not; server groups only while they hold a session
// no session group claimed (claimed ones were removed during linking), so
// there are no empty server rows; tool groups and groups whose role was
// never set, never.
func emitted(g ProcessGroup) bool {
	switch g.Role {
	case RoleSession:
		return true
	case RoleServer:
		return len(g.Sessions) > 0
	case RoleTool, RoleUnknown:
		return false
	default:
		return false
	}
}

// linkRole says whether Discover passes a group with role r to its adapter's
// Link, and whether it reads the group's explicit session id first: session
// groups both; servers are linked from evidence only (invariant 8); tools and
// unset roles never.
func linkRole(r Role) (link, explicit bool) {
	switch r {
	case RoleSession:
		return true, true
	case RoleServer:
		return true, false
	case RoleTool, RoleUnknown:
		return false, false
	default:
		return false, false
	}
}
