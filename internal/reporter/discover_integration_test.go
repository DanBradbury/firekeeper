package reporter

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DanBradbury/firekeeper/internal/session"
)

// The reporter runs on real discovery (with fixture process output and
// thread metadata) against fakeapi: a Codex session resumed by id is
// reported; a Codex tool and an empty app-server, which discovery doesn't
// emit, aren't.
func TestReportsOnlyDiscoveredSessions(t *testing.T) {
	f := newFixture(t)
	f.append(t, 0, 3)
	codexHome := filepath.Join(f.home, ".codex")
	if err := os.WriteFile(filepath.Join(codexHome, "state_5.sqlite"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(f.home, ".firekeeper"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.home, ".firekeeper", "machine-id"), []byte(testMachine+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ts := newTestServer(t)
	cfg := f.config(ts.URL)
	cfg.Discover = func(ctx context.Context, opts session.Options) ([]session.Meta, error) {
		return session.Discover(ctx, session.Options{Home: opts.Home, CodexHome: codexHome, Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
			switch name {
			case "ps":
				return []byte("10 1 ttys001 00:01 codex resume " + testSession + "\n11 1 ttys002 00:01 codex login\n12 1 ?? 00:01 codex app-server --listen stdio://\n"), nil
			case "sqlite3":
				if strings.Contains(strings.Join(args, " "), "rollout_path") {
					return []byte(fmt.Sprintf(`[{"id":%q,"display_name":"Demo","cwd":%q,"updated_at_ms":10,"rollout_path":%q}]`, testSession, f.repo, f.rollout)), nil
				}
			case "lsof":
				return nil, nil
			}
			return nil, os.ErrNotExist
		}})
	}
	res := runOnce(t, cfg) // fails unless exactly one session was reported
	if res.Events != 3 {
		t.Fatalf("uploaded %d events, want 3", res.Events)
	}
	if _, ok := ts.store.Session(testMachine, testSession); !ok {
		t.Fatal("server has no session for the discovered Codex session")
	}
}
