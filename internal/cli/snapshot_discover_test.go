package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/DanBradbury/firekeeper/internal/session"
)

// snapshot --json runs the real discovery (with fixture process output)
// and lists only what the output rule allows: sessions, linked or not;
// never tools or empty servers.
func TestSnapshotJSONFollowsOutputRule(t *testing.T) {
	home := t.TempDir()
	previous := discoverSessions
	discoverSessions = func(ctx context.Context, _ session.Options) ([]session.Meta, error) {
		return session.Discover(ctx, session.Options{
			Home: home, CodexHome: filepath.Join(home, "codex"), KimiHome: filepath.Join(home, "kimi"),
			Run: func(_ context.Context, name string, _ ...string) ([]byte, error) {
				if name == "ps" {
					return []byte("10 1 ttys001 00:01 kimi\n11 1 ? 00:01 kimi web\n12 1 ttys002 00:01 kimi export\n13 1 ? 00:01 codex app-server --listen stdio://\n14 1 ttys003 00:01 claude mcp list\n"), nil
				}
				return nil, os.ErrNotExist
			},
		})
	}
	t.Cleanup(func() { discoverSessions = previous })

	var stdout, stderr bytes.Buffer
	if code := runSnapshot([]string{"--json"}, &stdout, &stderr); code != 0 {
		t.Fatalf("snapshot exit %d: %s", code, stderr.String())
	}
	var metas []struct {
		PID      int    `json:"pid"`
		Provider string `json:"provider"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &metas); err != nil {
		t.Fatalf("snapshot output isn't a JSON array: %v", err)
	}
	var pids []int
	for _, m := range metas {
		pids = append(pids, m.PID)
	}
	if !slices.Equal(pids, []int{10}) || metas[0].Provider != "kimi" {
		t.Fatalf("snapshot listed %+v, want only the Kimi session (pid 10)", metas)
	}
}
