//go:build linux || darwin

package session

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/DanBradbury/firekeeper/internal/testenv"
)

// realRunner runs the real ps and lsof, but keeps only ps lines for the
// test's own stand-in processes (those under dir), so nothing else on the
// machine is classified or linked. sqlite3 answers from fake, when set.
func realRunner(dir string, fake func(args []string) ([]byte, error)) func(context.Context, string, ...string) ([]byte, error) {
	return func(ctx context.Context, name string, args ...string) ([]byte, error) {
		switch name {
		case "ps":
			out, err := exec.CommandContext(ctx, name, args...).Output()
			if err != nil {
				return nil, err
			}
			var kept []string
			for _, line := range strings.Split(string(out), "\n") {
				if strings.Contains(line, dir) {
					kept = append(kept, line)
				}
			}
			return []byte(strings.Join(kept, "\n")), nil
		case "lsof":
			return exec.CommandContext(ctx, name, args...).Output()
		case "sqlite3":
			if fake != nil {
				return fake(args)
			}
		}
		return nil, os.ErrNotExist
	}
}

// discoverUntil runs Discover until ok accepts its result or the deadline
// passes; stand-ins take a moment to show up in ps.
func discoverUntil(t *testing.T, opts Options, ok func([]Meta) bool) []Meta {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		metas, err := Discover(context.Background(), opts)
		var warning *Warning
		if err != nil && !errors.As(err, &warning) {
			t.Fatal(err)
		}
		if ok(metas) {
			return metas
		}
		if time.Now().After(deadline) {
			t.Fatalf("discovery never settled: %+v", metas)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func metaPIDs(metas []Meta) []int {
	var pids []int
	for _, m := range metas {
		pids = append(pids, m.PID)
	}
	slices.Sort(pids)
	return pids
}

// End to end with real processes, real ps and real argument lists: a Codex
// session with its own app-server child is one group; a Kimi session is
// emitted; a Kimi tool and an empty Kimi server (npm form, run by a "node"
// stand-in) are not. The install path has a space in it.
func TestDiscoverEndToEndStandIns(t *testing.T) {
	testenv.NeedTool(t, "lsof")
	home := t.TempDir()
	dir := filepath.Join(home, "install dir", "bin")
	codexProgram := helperProgram(t, dir, "codex")
	codex := startHelperWith(t, dir, "codex", helperOptions{env: []string{
		helperEnv + "=spawn", helperChildEnv + "=" + codexProgram,
		helperChildArgsEnv + "=app-server\x1f--listen\x1fstdio://",
	}}, "--search")
	kimi := startHelper(t, dir, "kimi")
	kimiTool := startHelper(t, dir, "kimi", "export")
	script := filepath.Join(home, "install dir", "lib", "node_modules", "@moonshot-ai", "kimi-code", "dist", "main.mjs")
	kimiServer := startHelper(t, dir, "node", script, "web")

	opts := Options{Home: home, CodexHome: filepath.Join(home, "codex"), KimiHome: filepath.Join(home, "kimi"),
		ClaudeHome: filepath.Join(home, "claude"), Args: readProcessArgs, Run: realRunner(dir, nil)}
	metas := discoverUntil(t, opts, func(metas []Meta) bool {
		for _, m := range metas {
			if m.PID == codex.Process.Pid && len(m.Runtime.Processes) == 2 {
				return true
			}
		}
		return false
	})
	if got, want := metaPIDs(metas), []int{codex.Process.Pid, kimi.Process.Pid}; !slices.Equal(got, func() []int { slices.Sort(want); return want }()) {
		t.Fatalf("emitted %v, want the Codex and Kimi sessions %v (not tool %d or empty server %d)", got, want, kimiTool.Process.Pid, kimiServer.Process.Pid)
	}
	for _, m := range metas {
		if m.PID == codex.Process.Pid {
			if m.Provider != "codex" || m.Runtime.Role != RoleSession {
				t.Errorf("codex meta = %s %s", m.Provider, m.Runtime.Role)
			}
			var roles []Role
			for _, p := range m.Runtime.Processes {
				roles = append(roles, p.Role)
			}
			if !slices.Contains(roles, RoleServer) {
				t.Errorf("the app-server child isn't in the session's group: %v", roles)
			}
		}
	}
}

// Real lsof evidence: a stand-in app-server holds a rollout file open. Alone
// it keeps that thread (a server keeps what it holds); next to a Codex
// session in the same directory the session claims it and the server row
// goes; a tool holding the same file is never linked or emitted.
func TestDiscoverRealLsofEvidence(t *testing.T) {
	testenv.NeedTool(t, "lsof")
	home := t.TempDir()
	work, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, "install dir", "bin")
	codexHome := filepath.Join(home, "codex")
	rollout := filepath.Join(codexHome, "sessions", "rollout-2026-11-03T10-00-00-"+threadX+".jsonl")
	fixtureFile(t, rollout, "")
	fixtureFile(t, filepath.Join(codexHome, "state_5.sqlite"), "")
	metadata := func([]string) ([]byte, error) {
		return []byte(fmt.Sprintf(`[{"id":%q,"display_name":"chat","cwd":%q,"updated_at_ms":10,"rollout_path":%q}]`, threadX, work, rollout)), nil
	}
	opts := Options{Home: home, CodexHome: codexHome, Args: readProcessArgs, Run: realRunner(dir, metadata)}
	hold := helperOptions{dir: work, env: []string{helperEnv + "=hold", helperHoldEnv + "=" + rollout}}

	server := startHelperWith(t, dir, "codex", hold, "app-server", "--listen", "stdio://")
	tool := startHelperWith(t, dir, "codex", hold, "login")
	metas := discoverUntil(t, opts, func(metas []Meta) bool {
		return len(metas) == 1 && metas[0].PID == server.Process.Pid && metas[0].ID == threadX
	})
	_ = metas

	session := startHelperWith(t, dir, "codex", helperOptions{dir: work, env: []string{helperEnv + "=wait"}})
	metas = discoverUntil(t, opts, func(metas []Meta) bool {
		return len(metas) == 1 && metas[0].PID == session.Process.Pid && metas[0].ID == threadX
	})
	for _, m := range metas {
		if m.PID == tool.Process.Pid || m.PID == server.Process.Pid {
			t.Fatalf("emitted %+v; the tool and the claimed server must not be", m)
		}
	}
}
