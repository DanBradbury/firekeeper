package gemini

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DanBradbury/firekeeper/internal/session"
	"github.com/DanBradbury/firekeeper/internal/transcript/reader"
)

func TestEnumerate(t *testing.T) {
	metas, err := (&Source{Dir: fixtureDir}).Enumerate(context.Background(), reader.EnumerateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(metas) != 2 {
		t.Fatalf("got %d sessions, want the main and legacy sessions without the subagent", len(metas))
	}
	tests := []struct {
		id, cwd, project, title, model, path string
		started                              time.Time
	}{
		{fixtureID, "/work/demo", "demo", "Fix README typo", model, fixturePath, time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)},
		// The sha256-named directory resolves through projects.json.
		{legacyID, "/work/legacy", "legacy", "Create hello file", "gemini-2.5-pro", legacyPath, time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)},
	}
	for _, tt := range tests {
		var m session.Meta
		for _, candidate := range metas {
			if candidate.ID == tt.id {
				m = candidate
			}
		}
		if m.ID == "" {
			t.Fatalf("session %s missing", tt.id)
		}
		if m.Provider != "gemini" || m.State != session.SessionStateEnded || m.Source != session.GeminiSource {
			t.Errorf("%s: provider %q state %s source %q", tt.id, m.Provider, m.State, m.Source)
		}
		if m.CWD != tt.cwd || m.Project != tt.project || m.Title != tt.title || m.Name != tt.title || m.Model != tt.model {
			t.Errorf("%s: cwd %q project %q title %q model %q", tt.id, m.CWD, m.Project, m.Title, m.Model)
		}
		if filepath.Clean(m.RolloutPath) != filepath.Clean(tt.path) {
			t.Errorf("%s: path %q", tt.id, m.RolloutPath)
		}
		if m.StartedAt == nil || !m.StartedAt.Equal(tt.started) || m.LastActivityAt == nil {
			t.Errorf("%s: started %v activity %v", tt.id, m.StartedAt, m.LastActivityAt)
		}
		if paths, err := (&Source{Dir: fixtureDir}).Locate(m); err != nil || len(paths) != 1 || paths[0] != m.RolloutPath {
			t.Errorf("%s: Locate(enumerated meta) = %q, %v", tt.id, paths, err)
		}
	}
}

func TestEnumerateOptions(t *testing.T) {
	source := &Source{Dir: fixtureDir}
	var skipped []string
	metas, err := source.Enumerate(context.Background(), reader.EnumerateOptions{Skip: func(m session.Meta) bool {
		skipped = append(skipped, m.ID)
		return m.CWD == "/work/legacy"
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(metas) != 1 || metas[0].ID != fixtureID || len(skipped) != 2 {
		t.Fatalf("Skip: got %d sessions, Skip called %d times", len(metas), len(skipped))
	}
	metas, err = source.Enumerate(context.Background(), reader.EnumerateOptions{ModifiedAfter: time.Now().Add(time.Hour)})
	if err != nil || len(metas) != 0 {
		t.Fatalf("ModifiedAfter: got %d sessions, %v", len(metas), err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := source.Enumerate(ctx, reader.EnumerateOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled: %v", err)
	}
}

func TestEnumerateCorruptAndMissing(t *testing.T) {
	if metas, err := (&Source{Dir: filepath.Join(t.TempDir(), "missing")}).Enumerate(context.Background(), reader.EnumerateOptions{}); err != nil || len(metas) != 0 {
		t.Fatalf("missing directory: %d sessions, %v", len(metas), err)
	}
	dir := t.TempDir()
	chats := filepath.Join(dir, "tmp", "proj", "chats")
	if err := os.MkdirAll(chats, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"session-a-00000001.jsonl": "",
		"session-b-00000002.jsonl": "not json\n",
		"session-c-00000003.json":  "{",
		"notes.jsonl":              `{"sessionId":"x","projectHash":"h"}` + "\n",
	} {
		if err := os.WriteFile(filepath.Join(chats, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	metas, err := (&Source{Dir: dir}).Enumerate(context.Background(), reader.EnumerateOptions{})
	var warning *session.Warning
	if len(metas) != 0 || !errors.As(err, &warning) || !strings.Contains(warning.Message, "skipped 3") {
		t.Fatalf("got %d sessions, err %v", len(metas), err)
	}
	if strings.Contains(warning.Message, dir) {
		t.Error("warning must not name paths")
	}
}
