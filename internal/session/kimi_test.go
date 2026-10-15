package session

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func TestKimiSessionStateLayouts(t *testing.T) {
	created := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	updated := created.Add(90 * time.Second)
	for name, tc := range map[string]struct {
		body             string
		created, updated time.Time
		workDir          string
	}{
		// Current Kimi Code: epoch milliseconds and cwd.
		"current":  {fmt.Sprintf(`{"id":"session_1","version":2,"cwd":"/w","createdAt":%d,"updatedAt":%d,"title":"t","archived":false}`, created.UnixMilli(), updated.UnixMilli()), created, updated, "/w"},
		"legacy":   {`{"workDir":"/w","createdAt":"2026-10-07T12:00:00Z","updatedAt":"2026-10-07T12:01:30Z","title":"t"}`, created, updated, "/w"},
		"cwd wins": {`{"cwd":"/new","workDir":"/old","title":"t"}`, time.Time{}, time.Time{}, "/new"},
		"bad time": {`{"cwd":"/w","createdAt":-5,"updatedAt":"yesterday","title":"t"}`, time.Time{}, time.Time{}, "/w"},
		"huge":     {`{"cwd":"/w","createdAt":1e300,"title":"t"}`, time.Time{}, time.Time{}, "/w"},
	} {
		t.Run(name, func(t *testing.T) {
			var state KimiSessionState
			if err := json.Unmarshal([]byte(tc.body), &state); err != nil {
				t.Fatal(err)
			}
			if !state.CreatedAt.Equal(tc.created) || !state.UpdatedAt.Equal(tc.updated) || state.WorkDir != tc.workDir || state.Title != "t" {
				t.Fatalf("got %+v", state)
			}
		})
	}
	var state KimiSessionState
	if json.Unmarshal([]byte(`{"cwd":`), &state) == nil {
		t.Fatal("malformed state.json decoded")
	}
}

// Current Kimi Code writes epoch-millisecond timestamps and cwd, which the
// earlier time.Time/workDir decoding rejected, so no running session matched.
func TestEnrichKimiSessionsCurrentStateLayout(t *testing.T) {
	home := t.TempDir()
	cwd := filepath.Join(home, "project")
	sessionDir := filepath.Join(home, "kimi", "sessions", "wd_project_0123456789ab", "session_abc")
	fixtureFile(t, filepath.Join(sessionDir, "state.json"), fmt.Sprintf(`{"id":"session_abc","cwd":%q,"createdAt":1791460800000,"updatedAt":1791460860000,"title":"Kimi now"}`, cwd))
	fixtureFile(t, KimiWirePath(sessionDir), `{"type":"metadata","protocol_version":"1.5","created_at":1791460800000}`+"\n")
	older := filepath.Join(home, "kimi", "sessions", "wd_project_0123456789ab", "session_old")
	fixtureFile(t, filepath.Join(older, "state.json"), fmt.Sprintf(`{"cwd":%q,"createdAt":1791374400000,"updatedAt":1791374400000,"title":"older"}`, cwd))

	d := &discoverer{ctx: context.Background(), opts: Options{Home: home, KimiHome: filepath.Join(home, "kimi"), Run: func(_ context.Context, name string, _ ...string) ([]byte, error) {
		if name != "lsof" {
			t.Fatalf("unexpected command %s", name)
		}
		return []byte("p30\nn" + cwd + "\n"), nil
	}}}
	groups := []ProcessGroup{{Tool: "Kimi", Root: ProcessInfo{PID: 30}}}
	if err := d.enrichKimiSessions(groups); err != nil {
		t.Fatal(err)
	}
	if len(groups[0].Sessions) != 1 {
		t.Fatalf("got %d sessions, want 1", len(groups[0].Sessions))
	}
	got := groups[0].Sessions[0]
	if got.ID != "session_abc" || got.Name != "Kimi now" || got.CWD != cwd || got.RolloutPath != KimiWirePath(sessionDir) ||
		!got.StartedAt.Equal(time.UnixMilli(1791460800000)) || !got.UpdatedAt.Equal(time.UnixMilli(1791460860000)) {
		t.Fatalf("unexpected session %+v", got)
	}
}
