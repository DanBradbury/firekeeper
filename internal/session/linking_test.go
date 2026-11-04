package session

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/DanBradbury/firekeeper/pkg/event"
)

// setExplicitIDs does for groups built in a test what Discover does before
// Link: session groups get the id their root's command line names.
func setExplicitIDs(groups []ProcessGroup) {
	for i := range groups {
		g := &groups[i]
		if g.Role != RoleSession {
			continue
		}
		for _, a := range adapters {
			if a.Provider() == g.Provider {
				if id, ok := a.SessionID(g.Root.Invocation); ok {
					g.explicitID = id
				}
			}
		}
	}
}

func sessionIDs(g ProcessGroup) []string {
	var ids []string
	for _, s := range g.Sessions {
		ids = append(ids, s.ID)
	}
	return ids
}

func byRoot(groups []ProcessGroup) map[int]ProcessGroup {
	m := make(map[int]ProcessGroup)
	for _, g := range groups {
		m[g.Root.PID] = g
	}
	return m
}

// codexFixture serves lsof and sqlite3 for Codex linking tests.
type codexFixture struct {
	codexHome string
	threads   map[string]struct {
		cwd     string
		updated int
	}
	cwd      map[int]string   // pid -> working directory
	rollouts map[int][]string // pid -> thread ids it holds as rollout files
	locks    map[int][]string // pid -> thread ids it holds as writer locks
}

func newCodexFixture(t *testing.T) *codexFixture {
	t.Helper()
	home := t.TempDir()
	f := &codexFixture{codexHome: filepath.Join(home, ".codex"), cwd: map[int]string{}, rollouts: map[int][]string{}, locks: map[int][]string{}}
	f.threads = map[string]struct {
		cwd     string
		updated int
	}{}
	if err := os.MkdirAll(f.codexHome, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.codexHome, "state_5.sqlite"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *codexFixture) thread(id, cwd string, updated int) {
	f.threads[id] = struct {
		cwd     string
		updated int
	}{cwd, updated}
}

func (f *codexFixture) rolloutPath(id string) string {
	return filepath.Join(f.codexHome, "sessions", "rollout-2026-11-01T10-00-00-"+id+".jsonl")
}

func (f *codexFixture) run(_ context.Context, name string, args ...string) ([]byte, error) {
	joined := strings.Join(args, " ")
	switch {
	case name == "lsof" && strings.Contains(joined, "-d cwd"):
		var b strings.Builder
		for _, p := range strings.Split(args[len(args)-1], ",") {
			pid, _ := strconv.Atoi(p)
			if cwd := f.cwd[pid]; cwd != "" {
				fmt.Fprintf(&b, "p%d\nn%s\n", pid, cwd)
			}
		}
		return []byte(b.String()), nil
	case name == "lsof":
		if !openFilesListable {
			return nil, nil // Windows' emulated lsof lists no open files
		}
		var b strings.Builder
		for _, p := range strings.Split(args[len(args)-1], ",") {
			pid, _ := strconv.Atoi(p)
			fmt.Fprintf(&b, "p%d\n", pid)
			for _, id := range f.rollouts[pid] {
				fmt.Fprintf(&b, "n%s\n", f.rolloutPath(id))
			}
			for _, id := range f.locks[pid] {
				fmt.Fprintf(&b, "n%s\n", filepath.Join(f.codexHome, "thread-writer-locks", id+".lock"))
			}
		}
		return []byte(b.String()), nil
	case name == "sqlite3" && strings.Contains(joined, "SELECT id FROM threads WHERE"):
		var rows []string
		for id := range f.threads {
			rows = append(rows, `{"id":"`+id+`"}`)
		}
		slices.Sort(rows)
		return []byte("[" + strings.Join(rows, ",") + "]"), nil
	case name == "sqlite3":
		var rows []string
		for id, th := range f.threads {
			rows = append(rows, fmt.Sprintf(`{"id":%q,"display_name":%q,"cwd":%q,"updated_at_ms":%d,"rollout_path":%q}`, id, id[:4], th.cwd, th.updated, f.rolloutPath(id)))
		}
		slices.Sort(rows)
		return []byte("[" + strings.Join(rows, ",") + "]"), nil
	}
	return nil, os.ErrNotExist
}

func (f *codexFixture) link(t *testing.T, ps string) map[int]ProcessGroup {
	t.Helper()
	d := &discoverer{ctx: context.Background(), opts: Options{Home: filepath.Dir(f.codexHome), CodexHome: f.codexHome, Run: f.run}}
	groups := d.groupProcesses(d.parseProcesses(ps))
	setExplicitIDs(groups)
	if err := d.enrichCodexSessions(groups); err != nil {
		t.Fatal(err)
	}
	return byRoot(groups)
}

const (
	threadA = "01a141cf-34d4-7813-9969-ad12c634738d"
	threadD = "01a10dae-2417-7fd1-b90a-9798256f74a6"
	threadX = "01a1996c-e4af-7af1-851c-69404a420242"
	missing = "01a1996c-e645-7781-8069-3e95a35b075d"
)

func setOpenFilesListable(t *testing.T, v bool) {
	t.Helper()
	old := openFilesListable
	openFilesListable = v
	t.Cleanup(func() { openFilesListable = old })
}

func TestCodexLinkingExplicitIDs(t *testing.T) {
	setOpenFilesListable(t, true)
	t.Run("the resumed CLI gets D, the other gets A", func(t *testing.T) {
		f := newCodexFixture(t)
		f.thread(threadA, "/work", 20)
		f.thread(threadD, "/work", 10)
		f.cwd = map[int]string{100: "/work", 101: "/work", 200: "/work"}
		f.locks[200] = []string{threadA, threadD}
		g := f.link(t, "100 1 ttys001 01:00 codex resume "+threadD+"\n101 1 ttys002 01:00 codex\n200 1 ?? 02:00 codex app-server --listen unix://")
		if got := sessionIDs(g[100]); !slices.Equal(got, []string{threadD}) {
			t.Errorf("resumed CLI = %v, want D", got)
		}
		if got := sessionIDs(g[101]); !slices.Equal(got, []string{threadA}) {
			t.Errorf("other CLI = %v, want A", got)
		}
		if got := sessionIDs(g[200]); len(got) != 0 {
			t.Errorf("server = %v, want none: both threads were claimed", got)
		}
	})
	t.Run("an id naming no thread leaves the group unlinked, with no guess", func(t *testing.T) {
		f := newCodexFixture(t)
		f.thread(threadA, "/work", 20)
		f.cwd = map[int]string{100: "/work", 101: "/work"}
		f.locks[200] = []string{threadA}
		g := f.link(t, "100 1 ttys001 01:00 codex resume "+missing+"\n101 1 ttys002 01:00 codex\n200 1 ?? 02:00 codex app-server")
		if got := sessionIDs(g[100]); len(got) != 0 {
			t.Errorf("resumed CLI with a missing id = %v, want unlinked", got)
		}
		if got := sessionIDs(g[101]); !slices.Equal(got, []string{threadA}) {
			t.Errorf("other CLI = %v, want A", got)
		}
	})
	t.Run("two processes naming the same id both get it", func(t *testing.T) {
		f := newCodexFixture(t)
		f.thread(threadD, "/work", 10)
		f.cwd = map[int]string{100: "/work", 101: "/work"}
		g := f.link(t, "100 1 ttys001 01:00 codex resume "+threadD+"\n101 1 ttys002 01:00 codex resume "+threadD)
		if !slices.Equal(sessionIDs(g[100]), []string{threadD}) || !slices.Equal(sessionIDs(g[101]), []string{threadD}) {
			t.Errorf("sessions = %v and %v, want D on both", sessionIDs(g[100]), sessionIDs(g[101]))
		}
	})
	t.Run("a fork's id is never explicit", func(t *testing.T) {
		f := newCodexFixture(t)
		f.thread(threadD, "/work", 10)
		f.thread(threadA, "/work", 20)
		f.cwd = map[int]string{100: "/work"}
		f.locks[200] = []string{threadA}
		g := f.link(t, "100 1 ttys001 01:00 codex fork "+threadD+"\n200 1 ?? 02:00 codex app-server")
		if got := sessionIDs(g[100]); slices.Contains(got, threadD) {
			t.Errorf("fork = %v, must not be pinned to the source thread D", got)
		}
	})
}

func TestCodexLinkingServers(t *testing.T) {
	t.Run("a claim by a session group with no terminal takes the thread off the server", func(t *testing.T) {
		setOpenFilesListable(t, true)
		f := newCodexFixture(t)
		f.thread(threadD, "/work", 10)
		f.cwd = map[int]string{100: "/work", 200: "/work"}
		f.rollouts[200] = []string{threadD}
		g := f.link(t, "100 1 ?? 01:00 codex exec fix it\n200 1 ?? 02:00 codex app-server --listen unix://")
		if got := sessionIDs(g[100]); !slices.Equal(got, []string{threadD}) {
			t.Errorf("codex exec = %v, want D", got)
		}
		if got := sessionIDs(g[200]); len(got) != 0 {
			t.Errorf("server = %v, want none once a session group claimed D", got)
		}
	})
	t.Run("the usage probe is never given a Desktop chat", func(t *testing.T) {
		setOpenFilesListable(t, true)
		f := newCodexFixture(t)
		f.thread(threadX, "/work", 30)
		f.cwd = map[int]string{250: "/work", 300: "/work"}
		f.locks[300] = []string{threadX}
		g := f.link(t, "250 1 ?? 00:05 codex app-server --listen stdio://\n300 1 ?? 05:00 /Applications/Codex.app/Contents/Resources/codex app-server")
		if got := sessionIDs(g[250]); len(got) != 0 {
			t.Errorf("probe = %v, want none: it holds nothing", got)
		}
		if got := sessionIDs(g[300]); !slices.Equal(got, []string{threadX}) {
			t.Errorf("Desktop app-server = %v, want the chat it holds", got)
		}
	})
	t.Run("where open files can be listed a server never guesses", func(t *testing.T) {
		setOpenFilesListable(t, true)
		f := newCodexFixture(t)
		f.thread(threadA, "/work", 20)
		f.cwd = map[int]string{200: "/work"}
		f.locks[999] = []string{threadA} // held by a process outside the group
		g := f.link(t, "200 1 ?? 02:00 codex app-server\n999 1 ttys009 01:00 codex mcp list")
		if got := sessionIDs(g[200]); len(got) != 0 {
			t.Errorf("server = %v, want none: it holds nothing itself", got)
		}
	})
	t.Run("Windows exception: servers are matched by directory after sessions", func(t *testing.T) {
		setOpenFilesListable(t, false)
		f := newCodexFixture(t)
		f.thread(threadA, "/work", 20)
		f.thread(threadD, "/work", 10)
		f.cwd = map[int]string{100: "/work", 200: "/work"}
		g := f.link(t, "200 1 ? 02:00 codex app-server --listen stdio://\n100 1 ? 01:00 codex")
		if got := sessionIDs(g[100]); !slices.Equal(got, []string{threadA}) {
			t.Errorf("session = %v, want the newest, A: sessions go first", got)
		}
		if got := sessionIDs(g[200]); !slices.Equal(got, []string{threadD}) {
			t.Errorf("server = %v, want the remaining D", got)
		}
	})
}

func kimiSession(t *testing.T, home, name, cwd string, created, updated time.Time) {
	t.Helper()
	fixtureFile(t, filepath.Join(home, "sessions", "wd_project", name, "state.json"),
		fmt.Sprintf(`{"id":%q,"cwd":%q,"createdAt":%d,"updatedAt":%d,"title":%q}`, name, cwd, created.UnixMilli(), updated.UnixMilli(), name))
}

func kimiLink(t *testing.T, home, cwd, ps string) map[int]ProcessGroup {
	t.Helper()
	d := &discoverer{ctx: context.Background(), opts: Options{Home: home, KimiHome: home, Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "lsof" {
			return nil, os.ErrNotExist
		}
		var b strings.Builder
		for _, p := range strings.Split(args[len(args)-1], ",") {
			fmt.Fprintf(&b, "p%s\nn%s\n", p, cwd)
		}
		return []byte(b.String()), nil
	}}}
	groups := d.groupProcesses(d.parseProcesses(ps))
	setExplicitIDs(groups)
	if err := d.enrichKimiSessions(groups); err != nil {
		t.Fatal(err)
	}
	return byRoot(groups)
}

func TestKimiLinking(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	setKimiNow(t, now)
	cwd := "/work/project"
	t.Run("a session older than the process isn't linked", func(t *testing.T) {
		home := t.TempDir()
		kimiSession(t, home, "week_old", cwd, now.Add(-8*24*time.Hour), now.Add(-7*24*time.Hour))
		g := kimiLink(t, home, cwd, "30 1 ttys001 01:00 kimi")
		if got := sessionIDs(g[30]); len(got) != 0 {
			t.Errorf("kimi = %v, want unlinked", got)
		}
	})
	t.Run("two processes in one directory get different sessions, newest to newest", func(t *testing.T) {
		home := t.TempDir()
		kimiSession(t, home, "s_older", cwd, now.Add(-60*time.Second), now.Add(-10*time.Second))
		kimiSession(t, home, "s_newer", cwd, now.Add(-30*time.Second), now.Add(-5*time.Second))
		g := kimiLink(t, home, cwd, "30 1 ttys001 01:00 kimi\n31 1 ttys002 00:30 kimi")
		if !slices.Equal(sessionIDs(g[31]), []string{"s_newer"}) || !slices.Equal(sessionIDs(g[30]), []string{"s_older"}) {
			t.Errorf("sessions = 30:%v 31:%v, want 30:s_older 31:s_newer", sessionIDs(g[30]), sessionIDs(g[31]))
		}
	})
	t.Run("kimi -c links the newest session as of its start, even if updated before it", func(t *testing.T) {
		home := t.TempDir()
		kimiSession(t, home, "resumed", cwd, now.Add(-72*time.Hour), now.Add(-48*time.Hour))
		kimiSession(t, home, "created_later", cwd, now.Add(-2*time.Second), now.Add(-1*time.Second))
		g := kimiLink(t, home, cwd, "30 1 ttys001 00:10 kimi -c\n31 1 ttys002 00:06 kimi")
		if got := sessionIDs(g[30]); !slices.Equal(got, []string{"resumed"}) {
			t.Errorf("kimi -c = %v, want resumed", got)
		}
		if got := sessionIDs(g[31]); !slices.Equal(got, []string{"created_later"}) {
			t.Errorf("plain kimi = %v, want created_later", got)
		}
	})
	t.Run("kimi -S <id> uses the directory guess (Kimi guard)", func(t *testing.T) {
		home := t.TempDir()
		kimiSession(t, home, "fresh", cwd, now.Add(-20*time.Second), now.Add(-2*time.Second))
		g := kimiLink(t, home, cwd, "30 1 ttys001 00:30 kimi -S "+sessionUUID)
		if g[30].explicitID != "" || !slices.Equal(sessionIDs(g[30]), []string{"fresh"}) {
			t.Errorf("kimi -S = explicit %q sessions %v, want no explicit id and the guessed fresh session", g[30].explicitID, sessionIDs(g[30]))
		}
	})
	t.Run(`kimi -p "-c" is a plain session, not --continue`, func(t *testing.T) {
		home := t.TempDir()
		kimiSession(t, home, "week_old", cwd, now.Add(-8*24*time.Hour), now.Add(-7*24*time.Hour))
		g := kimiLink(t, home, cwd, "30 1 ttys001 00:30 kimi -p -c")
		if got := sessionIDs(g[30]); len(got) != 0 {
			t.Errorf(`kimi -p "-c" = %v, want unlinked (not the -c rule)`, got)
		}
	})
	t.Run("servers get nothing without evidence", func(t *testing.T) {
		home := t.TempDir()
		kimiSession(t, home, "fresh", cwd, now.Add(-20*time.Second), now.Add(-2*time.Second))
		g := kimiLink(t, home, cwd, "30 1 ? 00:30 kimi web\n31 1 ? 00:30 kimi acp")
		if len(g[30].Sessions) != 0 || len(g[31].Sessions) != 0 {
			t.Errorf("servers = %v %v, want none", sessionIDs(g[30]), sessionIDs(g[31]))
		}
	})
	t.Run("an unreadable start time leaves the process unlinked", func(t *testing.T) {
		home := t.TempDir()
		kimiSession(t, home, "fresh", cwd, now.Add(-20*time.Second), now.Add(-2*time.Second))
		g := kimiLink(t, home, cwd, "30 1 ttys001 bogus kimi")
		if got := sessionIDs(g[30]); len(got) != 0 {
			t.Errorf("kimi = %v, want unlinked", got)
		}
	})
}

func TestParseElapsed(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"00:01": time.Second, "01:30": 90 * time.Second, "02:03:04": 2*time.Hour + 3*time.Minute + 4*time.Second,
		"3-01:00:00": 73 * time.Hour,
	} {
		if got, ok := parseElapsed(in); !ok || got != want {
			t.Errorf("parseElapsed(%q) = %v, %v; want %v", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "5", "x:00", "00:61", "-1-00:00", "1:2:3:4", "aa-00:00"} {
		if got, ok := parseElapsed(in); ok {
			t.Errorf("parseElapsed(%q) = %v, want an error", in, got)
		}
	}
}

// recordingAdapter wraps a real adapter and records the groups Discover
// hands to Link.
type recordingAdapter struct {
	providerAdapter
	got *[]ProcessGroup
}

func (r recordingAdapter) Link(_ *discoverer, groups []*ProcessGroup) error {
	for _, g := range groups {
		*r.got = append(*r.got, *g)
	}
	return nil
}

// Shared adapter contract, checked through Discover for every adapter:
// Link gets only its own provider's session and server groups, never a
// tool's; explicit ids are set for session groups only; servers with
// nothing and tools are never emitted.
func TestDiscoverAdapterContract(t *testing.T) {
	ps := strings.Join([]string{
		"10 1 ttys001 00:01 codex resume " + sessionUUID,
		"11 1 ?? 00:01 codex app-server",
		"12 1 ttys002 00:01 codex login",
		"20 1 ttys003 00:01 kimi",
		"21 1 ? 00:01 kimi web",
		"22 1 ttys004 00:01 kimi export",
		"30 1 ttys005 00:01 claude --resume " + otherUUID,
		"31 1 ttys006 00:01 claude mcp list",
		"40 1 ttys007 00:01 opencode",
		"41 1 ? 00:01 opencode serve",
		"50 1 ttys008 00:01 gemini",
		"51 1 ttys009 00:01 gemini mcp list",
		"60 1 ttys010 00:01 cursor-agent",
		"61 1 ttys011 00:01 cursor-agent login",
		"70 1 ttys012 00:01 copilot --resume=" + sessionUUID,
	}, "\n")
	received := map[event.Provider]*[]ProcessGroup{}
	saved := adapters
	t.Cleanup(func() { adapters = saved })
	var wrapped []providerAdapter
	for _, a := range saved {
		got := &[]ProcessGroup{}
		received[a.Provider()] = got
		wrapped = append(wrapped, recordingAdapter{providerAdapter: a, got: got})
	}
	adapters = wrapped
	metas, err := Discover(context.Background(), Options{Home: t.TempDir(), Run: func(_ context.Context, name string, _ ...string) ([]byte, error) {
		if name == "ps" {
			return []byte(ps), nil
		}
		return nil, os.ErrNotExist
	}})
	var warning *Warning
	if err != nil && !errors.As(err, &warning) {
		t.Fatal(err)
	}
	wantExplicit := map[int]string{10: sessionUUID, 30: otherUUID, 70: sessionUUID}
	for provider, got := range received {
		for _, g := range *got {
			if g.Provider != provider {
				t.Errorf("%s's Link received a %s group (pid %d)", provider, g.Provider, g.Root.PID)
			}
			if g.Role != RoleSession && g.Role != RoleServer {
				t.Errorf("%s's Link received a %s group (pid %d)", provider, g.Role, g.Root.PID)
			}
			if g.explicitID != wantExplicit[g.Root.PID] {
				t.Errorf("pid %d explicit id = %q, want %q", g.Root.PID, g.explicitID, wantExplicit[g.Root.PID])
			}
			if g.Role == RoleServer && g.explicitID != "" {
				t.Errorf("server pid %d has an explicit id", g.Root.PID)
			}
		}
	}
	var emittedPIDs []int
	for _, m := range metas {
		emittedPIDs = append(emittedPIDs, m.PID)
	}
	slices.Sort(emittedPIDs)
	// Sessions only: no session was linked, so every server is empty, and
	// tools are never emitted.
	if want := []int{10, 20, 30, 40, 50, 60, 70}; !slices.Equal(emittedPIDs, want) {
		t.Errorf("emitted %v, want the sessions %v", emittedPIDs, want)
	}
}

// Output rule through Discover (invariant 6): an unlinked session is
// emitted; a server holding a session no session group claimed is emitted
// with it; an empty server and a tool are not.
func TestDiscoverOutputRule(t *testing.T) {
	setOpenFilesListable(t, true)
	f := newCodexFixture(t)
	f.thread(threadX, "/desktop", 30)
	f.cwd = map[int]string{100: "/work", 300: "/desktop", 400: "/work"}
	f.locks[300] = []string{threadX}
	ps := "100 1 ttys001 01:00 codex\n300 1 ?? 05:00 /Applications/Codex.app/Contents/Resources/codex app-server\n400 1 ?? 00:05 codex app-server --listen stdio://\n500 1 ttys002 00:01 codex doctor"
	metas, err := Discover(context.Background(), Options{Home: filepath.Dir(f.codexHome), CodexHome: f.codexHome, Run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "ps" {
			return []byte(ps), nil
		}
		return f.run(ctx, name, args...)
	}})
	var warning *Warning
	if err != nil && !errors.As(err, &warning) {
		t.Fatal(err)
	}
	got := map[int]string{}
	for _, m := range metas {
		got[m.PID] = m.ID
	}
	want := map[int]string{100: "", 300: threadX}
	if len(got) != len(want) || got[100] != "" || got[300] != threadX {
		t.Fatalf("emitted %v, want %v (unlinked session, and the server holding the unclaimed chat)", got, want)
	}
}

// A Claude server (claude mcp serve) beside a transcript in its directory
// gets nothing and isn't emitted; a Claude session there gets the
// transcript (invariant 8, through Discover).
func TestClaudeServerNeverGuesses(t *testing.T) {
	home := t.TempDir()
	cwd := filepath.Join(home, "project")
	claudeHome := filepath.Join(home, "claude")
	fixtureFile(t, filepath.Join(claudeHome, "projects", standalone().claudeProjectDirName(cwd), sessionUUID+".jsonl"), `{"type":"user","sessionId":"`+sessionUUID+`","cwd":"`+jsonPath(cwd)+`","message":{"role":"user","content":"hi"}}`+"\n")
	run := func(ps string) []Meta {
		metas, err := Discover(context.Background(), Options{Home: home, ClaudeHome: claudeHome, Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
			switch name {
			case "ps":
				return []byte(ps), nil
			case "lsof":
				var b strings.Builder
				for _, p := range strings.Split(args[len(args)-1], ",") {
					fmt.Fprintf(&b, "p%s\nn%s\n", p, cwd)
				}
				return []byte(b.String()), nil
			}
			return nil, os.ErrNotExist
		}})
		var warning *Warning
		if err != nil && !errors.As(err, &warning) {
			t.Fatal(err)
		}
		return metas
	}
	if metas := run("30 1 ? 00:01 claude mcp serve\n"); len(metas) != 0 {
		t.Fatalf("server alone emitted %+v, want nothing", metas)
	}
	metas := run("30 1 ? 00:01 claude mcp serve\n31 1 ttys001 00:01 claude\n")
	if len(metas) != 1 || metas[0].PID != 31 || metas[0].ID != sessionUUID {
		t.Fatalf("emitted %+v, want only the session 31 with the transcript", metas)
	}
}
