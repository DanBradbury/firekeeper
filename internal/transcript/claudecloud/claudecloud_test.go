package claudecloud

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DanBradbury/firekeeper/internal/session"
	"github.com/DanBradbury/firekeeper/internal/transcript"
	"github.com/DanBradbury/firekeeper/internal/transcript/reader"
)

const sid = "session_01TESTAAAAAAAAAAAAAAAAAA"

func TestTokenFromKeychain(t *testing.T) {
	for name, tc := range map[string]struct{ raw, want string }{
		"valid":     {`{"claudeAiOauth":{"accessToken":"abc"}}` + "\n", "abc"},
		"no oauth":  {`{"other":1}`, ""},
		"malformed": {`not json`, ""},
		"empty":     {``, ""},
	} {
		t.Run(name, func(t *testing.T) {
			if got := tokenFromKeychain([]byte(tc.raw)); got != tc.want {
				t.Fatalf("token = %q, want %q", got, tc.want)
			}
		})
	}
}

func stubLogin(t *testing.T, platform string, keychain func() ([]byte, error)) {
	t.Helper()
	prevOS, prevKeychain := goos, readKeychain
	goos = platform
	readKeychain = func(context.Context, string) ([]byte, error) { return keychain() }
	t.Cleanup(func() { goos, readKeychain = prevOS, prevKeychain })
}

func homeWithConfig(t *testing.T, contents string) string {
	t.Helper()
	home := t.TempDir()
	if contents != "" {
		if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return home
}

func TestLoadCredentials(t *testing.T) {
	good := func() ([]byte, error) { return []byte(`{"claudeAiOauth":{"accessToken":"` + testToken + `"}}`), nil }
	validConfig := `{"oauthAccount":{"organizationUuid":"` + testOrg + `"}}`
	tests := []struct {
		name     string
		platform string
		keychain func() ([]byte, error)
		config   string
		want     error
	}{
		{"ok", "darwin", good, validConfig, nil},
		{"not macOS", "linux", good, validConfig, ErrUnsupportedPlatform},
		{"keychain error", "darwin", func() ([]byte, error) { return nil, errors.New("boom " + testToken) }, validConfig, ErrNoLogin},
		{"keychain without token", "darwin", func() ([]byte, error) { return []byte(`{}`), nil }, validConfig, ErrNoLogin},
		{"no config", "darwin", good, "", ErrNoOrganization},
		{"config without org", "darwin", good, `{"oauthAccount":{}}`, ErrNoOrganization},
		{"org not safe in a header", "darwin", good, `{"oauthAccount":{"organizationUuid":"a\r\nX: y"}}`, ErrNoOrganization},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stubLogin(t, tc.platform, tc.keychain)
			creds, err := LoadCredentials(context.Background(), homeWithConfig(t, tc.config))
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if err != nil {
				if strings.Contains(err.Error(), testToken) {
					t.Fatalf("error leaks the token: %v", err)
				}
				return
			}
			if creds.Token != testToken || creds.OrgUUID != testOrg {
				t.Fatalf("credentials = %+v", struct{ T, O string }{creds.Token, creds.OrgUUID})
			}
		})
	}
}

func TestCredentialsNeverPrint(t *testing.T) {
	creds := Credentials{Token: testToken, OrgUUID: testOrg}
	for _, out := range []string{fmt.Sprint(creds), fmt.Sprintf("%v", creds), fmt.Sprintf("%+v", creds), fmt.Sprintf("%#v", creds), fmt.Sprint(&creds)} {
		if strings.Contains(out, testToken) {
			t.Fatalf("credentials print the token: %q", out)
		}
	}
}

func TestSessionsPagesAndDropsUnsafeIDs(t *testing.T) {
	f, c := newFakeCloud(t)
	f.pageSize = 2
	f.sessions = []map[string]any{
		cloudSession("session_old", "2026-10-01T00:00:00Z"),
		cloudSession("../../etc/passwd", "2026-10-09T00:00:00Z"),
		cloudSession("session_new", "2026-10-05T00:00:00Z"),
	}
	got, err := c.Sessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != "session_new" || got[1].ID != "session_old" {
		t.Fatalf("sessions = %+v", got)
	}
	if f.sessionCalls != 2 {
		t.Fatalf("list requests = %d, want 2 pages", f.sessionCalls)
	}
	if f.badAuth != 0 {
		t.Fatalf("%d requests lacked the expected credentials", f.badAuth)
	}
	if got[0].Repo != "octo/demo" || got[0].CWD != "" || got[0].Model != "claude-sonnet-5-5" || got[0].Branch != "feature/x" || got[0].Title != "Fix the thing" {
		t.Fatalf("metadata = %+v", got[0])
	}
}

func TestEventsPageThroughAll(t *testing.T) {
	f, c := newFakeCloud(t)
	f.events[sid] = conversation(sid)
	f.pageSize = 3
	events, err := c.Events(context.Background(), sid)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 7 || events[0].UUID != "e1" || events[6].UUID != "e7" {
		t.Fatalf("got %d events", len(events))
	}
	if f.eventCalls[sid] != 3 {
		t.Fatalf("events requests = %d, want 3 pages", f.eventCalls[sid])
	}
	if _, err := c.Events(context.Background(), "../x"); err == nil {
		t.Fatal("unsafe session id accepted")
	}
}

func TestStatusErrorCarriesNoBodyOrToken(t *testing.T) {
	f, c := newFakeCloud(t)
	f.events[sid] = conversation(sid)
	f.failEvents[sid] = true
	_, err := c.Events(context.Background(), sid)
	var status *StatusError
	if !errors.As(err, &status) || status.Status != http.StatusInternalServerError {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "secret detail") || strings.Contains(err.Error(), testToken) {
		t.Fatalf("error leaks response or token: %v", err)
	}
}

func TestClientRejectsWrongCredentialsClearly(t *testing.T) {
	_, c := newFakeCloud(t)
	c.creds.Token = "other"
	_, err := c.Sessions(context.Background())
	var status *StatusError
	if !errors.As(err, &status) {
		t.Fatalf("err = %v", err)
	}
}

func TestClientDoesNotFollowRedirects(t *testing.T) {
	var leaked bool
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { leaked = true }))
	defer other.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusFound)
	}))
	defer srv.Close()
	c := NewClient(Credentials{Token: testToken, OrgUUID: testOrg})
	c.BaseURL = srv.URL
	if _, err := c.Sessions(context.Background()); err == nil {
		t.Fatal("redirect answered as success")
	}
	if leaked {
		t.Fatal("client followed a redirect to another host")
	}
}

func TestClientUnexpectedFormat(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`["not","an","object"]`)) }))
	defer srv.Close()
	c := NewClient(Credentials{Token: testToken, OrgUUID: testOrg})
	c.BaseURL = srv.URL
	if _, err := c.Sessions(context.Background()); err == nil || !strings.Contains(err.Error(), "may have changed") {
		t.Fatalf("err = %v", err)
	}
}

func syncOnce(t *testing.T, c *Client, dir string, after time.Time) SyncResult {
	t.Helper()
	res, err := Sync(context.Background(), c, SyncOptions{Dir: dir, After: after})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestSyncWritesTranscriptAndSource(t *testing.T) {
	f, c := newFakeCloud(t)
	f.sessions = []map[string]any{cloudSession(sid, "2026-10-01T11:00:00Z")}
	f.events[sid] = conversation(sid)
	dir := filepath.Join(t.TempDir(), "cache")

	res := syncOnce(t, c, dir, time.Time{})
	if res.Listed != 1 || res.Fetched != 1 || len(res.Failed) != 0 {
		t.Fatalf("result = %+v", res)
	}
	info, err := os.Stat(filepath.Join(dir, sid+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("cache file mode = %v, want 0600", info.Mode().Perm())
	}
	if !info.ModTime().Equal(time.Date(2026, 10, 1, 11, 0, 0, 0, time.UTC)) {
		t.Fatalf("cache file mtime = %v, want the session's update time", info.ModTime())
	}

	src := &Source{Dir: dir}
	metas, err := src.Enumerate(context.Background(), reader.EnumerateOptions{})
	if err != nil || len(metas) != 1 {
		t.Fatalf("enumerate = %v, %v", metas, err)
	}
	m := metas[0]
	if m.ID != sid || m.Provider != "claude" || m.CWD != "/claude-cloud/octo/demo" || m.Project != "demo" || m.Branch != "feature/x" ||
		m.Model != "claude-sonnet-5-5" || m.Title != "Fix the thing" || m.State != session.SessionStateEnded ||
		m.StartedAt == nil || m.LastActivityAt == nil {
		t.Fatalf("meta = %+v", m)
	}
	paths, err := src.Locate(m)
	if err != nil || len(paths) != 1 {
		t.Fatalf("locate = %v, %v", paths, err)
	}
	events, offset, err := src.Read(paths[0], 0)
	if err != nil {
		t.Fatal(err)
	}
	wantRoles := []transcript.Role{transcript.RoleUser, transcript.RoleToolCall, transcript.RoleToolResult, transcript.RoleAssistant, transcript.RoleMeta}
	if len(events) != len(wantRoles) {
		t.Fatalf("events = %d, want %d (plumbing events must be dropped)", len(events), len(wantRoles))
	}
	for i, e := range events {
		if e.Role != wantRoles[i] || e.Seq != int64(i) || e.SessionID != sid || e.Provider != transcript.ProviderClaude {
			t.Fatalf("event %d = role %s seq %d session %q provider %s", i, e.Role, e.Seq, e.SessionID, e.Provider)
		}
		if e.TS == nil {
			t.Fatalf("event %d has no timestamp; prompts arrive without one and need created_at", i)
		}
	}
	if events[2].ToolName == nil || *events[2].ToolName != "Bash" {
		t.Fatal("tool result lost its tool name")
	}
	if events[1].Tokens.Input != 10 || events[1].Tokens.Output != 5 || events[1].Tokens.Cache != 5 {
		t.Fatalf("tokens = %+v", events[1].Tokens)
	}
	// Reading from the returned offset yields nothing, and splitting a read
	// keeps the same Seq values.
	rest, again, err := src.Read(paths[0], offset)
	if err != nil || len(rest) != 0 || again != offset {
		t.Fatalf("read at end = %d events, offset %d, err %v", len(rest), again, err)
	}
}

func TestSyncIsIncrementalAndPrefixStable(t *testing.T) {
	f, c := newFakeCloud(t)
	f.sessions = []map[string]any{cloudSession(sid, "2026-10-01T11:00:00Z")}
	f.events[sid] = conversation(sid)
	dir := t.TempDir()
	syncOnce(t, c, dir, time.Time{})
	first, err := os.ReadFile(filepath.Join(dir, sid+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}

	calls := f.eventCalls[sid]
	if res := syncOnce(t, c, dir, time.Time{}); res.Current != 1 || res.Fetched != 0 || f.eventCalls[sid] != calls {
		t.Fatalf("unchanged session was fetched again: %+v", res)
	}

	// The session continues: a newer update time and one more event.
	f.sessions = []map[string]any{cloudSession(sid, "2026-10-01T12:00:00Z")}
	f.events[sid] = append(f.events[sid], map[string]any{"type": "assistant", "uuid": "e8", "session_id": sid,
		"created_at": "2026-10-01T11:30:00.000000Z", "timestamp": "2026-10-01T11:30:00.500Z",
		"message": map[string]any{"id": "m4", "role": "assistant", "model": "claude-sonnet-5-5", "content": []map[string]any{{"type": "text", "text": "more"}}}})
	// The new event is a sub-agent-free reply; the old lines must not change.
	if res := syncOnce(t, c, dir, time.Time{}); res.Fetched != 1 {
		t.Fatalf("updated session not fetched: %+v", res)
	}
	second, err := os.ReadFile(filepath.Join(dir, sid+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(second, first) || len(second) <= len(first) {
		t.Fatal("rewritten transcript does not keep the earlier bytes as its prefix; saved offsets would break")
	}
	// Resuming from the old end offset returns only the new event, with the
	// next Seq.
	src := &Source{Dir: dir}
	events, _, err := src.Read(filepath.Join(dir, sid+".jsonl"), int64(len(first)))
	if err != nil || len(events) != 1 || events[0].Seq != 5 {
		t.Fatalf("resume = %d events, err %v", len(events), err)
	}
}

func TestSyncAfterCutoffAndFailureIsolation(t *testing.T) {
	f, c := newFakeCloud(t)
	f.sessions = []map[string]any{
		cloudSession("session_old", "2026-09-01T00:00:00Z"),
		cloudSession("session_bad", "2026-10-02T00:00:00Z"),
		cloudSession("session_good", "2026-10-03T00:00:00Z"),
	}
	f.events["session_good"] = conversation("session_good")
	f.events["session_bad"] = conversation("session_bad")
	f.events["session_old"] = conversation("session_old")
	f.failEvents["session_bad"] = true
	dir := t.TempDir()
	res := syncOnce(t, c, dir, time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
	if res.Skipped != 1 || res.Fetched != 1 || len(res.Failed) != 1 || !strings.HasPrefix(res.Failed[0], "session_bad:") {
		t.Fatalf("result = %+v", res)
	}
	if f.eventCalls["session_old"] != 0 {
		t.Fatal("a session before the cutoff was downloaded")
	}
	if _, err := os.Stat(filepath.Join(dir, "session_bad.json")); err == nil {
		t.Fatal("a failed session left a metadata file that marks it complete")
	}
}

func TestEnumerateFilters(t *testing.T) {
	f, c := newFakeCloud(t)
	f.sessions = []map[string]any{
		cloudSession("session_a", "2026-10-01T00:00:00Z"),
		cloudSession("session_b", "2026-10-05T00:00:00Z"),
		cloudSession("session_empty", "2026-10-06T00:00:00Z"),
	}
	f.events["session_a"] = conversation("session_a")
	f.events["session_b"] = conversation("session_b")
	f.events["session_empty"] = []map[string]any{{"type": "control_request", "uuid": "c1", "created_at": "2026-10-06T00:00:00Z"}}
	dir := t.TempDir()
	syncOnce(t, c, dir, time.Time{})
	src := &Source{Dir: dir}

	metas, err := src.Enumerate(context.Background(), reader.EnumerateOptions{})
	if err != nil || len(metas) != 2 || metas[0].ID != "session_b" || metas[1].ID != "session_a" {
		t.Fatalf("enumerate = %v, %v (want b, a newest first, empty session left out)", ids(metas), err)
	}
	metas, _ = src.Enumerate(context.Background(), reader.EnumerateOptions{ModifiedAfter: time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)})
	if len(metas) != 1 || metas[0].ID != "session_b" {
		t.Fatalf("ModifiedAfter kept %v", ids(metas))
	}
	metas, _ = src.Enumerate(context.Background(), reader.EnumerateOptions{Skip: func(m session.Meta) bool { return m.ID == "session_b" }})
	if len(metas) != 1 || metas[0].ID != "session_a" {
		t.Fatalf("Skip kept %v", ids(metas))
	}
	if other, err := (&Source{Dir: filepath.Join(dir, "missing")}).Enumerate(context.Background(), reader.EnumerateOptions{}); err != nil || len(other) != 0 {
		t.Fatalf("missing cache = %v, %v", other, err)
	}
}

func TestSourceRejectsForeignAndUnsafeSessions(t *testing.T) {
	src := &Source{Dir: t.TempDir()}
	for _, meta := range []session.Meta{
		{ID: sid, Provider: "codex"},
		{ID: "../../x", Provider: "claude"},
		{ID: "session_not_cached", Provider: "claude"},
	} {
		if paths, err := src.Locate(meta); err != nil || len(paths) != 0 {
			t.Fatalf("Locate(%+v) = %v, %v", meta, paths, err)
		}
	}
	if _, _, err := src.Read(filepath.Join(src.Dir, "bad name.jsonl"), 0); err == nil {
		t.Fatal("unsafe transcript name accepted")
	}
}

func ids(metas []session.Meta) []string {
	out := make([]string, len(metas))
	for i, m := range metas {
		out[i] = m.ID
	}
	return out
}

func TestRepoParsing(t *testing.T) {
	for in, want := range map[string]string{
		"https://github.com/octo/demo":     "octo/demo",
		"https://github.com/octo/demo.git": "octo/demo",
		"https://github.com/octo/demo/":    "octo/demo",
		"octo/demo":                        "octo/demo",
		"/octo/demo":                       "octo/demo",
		"https://github.com/octo":          "",
		"https://github.com/a/b/c":         "",
		"https://github.com/../etc":        "",
		"https://github.com/octo/..":       "",
		"https://github.com/oc to/demo":    "",
		"https://github.com/octo/de\nmo":   "",
		"":                                 "",
	} {
		got := repoFromURL(in)
		if !strings.Contains(in, "://") {
			got = repoFromName(in)
		}
		if got != want {
			t.Errorf("repo(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRepoFallsBackToOutcomeAndBranchToOutcome(t *testing.T) {
	item := cloudSession("session_x", "2026-10-01T00:00:00Z")
	ctx := item["session_context"].(map[string]any)
	ctx["sources"] = []map[string]any{{"type": "git_repository", "url": "not a url"}}
	item["external_metadata"] = map[string]any{}
	f, c := newFakeCloud(t)
	f.sessions = []map[string]any{item}
	got, err := c.Sessions(context.Background())
	if err != nil || len(got) != 1 {
		t.Fatalf("sessions = %v, %v", got, err)
	}
	if got[0].Repo != "octo/demo" || got[0].Branch != "claude/other" {
		t.Fatalf("repo %q branch %q; want the outcome's", got[0].Repo, got[0].Branch)
	}
}

func TestCwdFor(t *testing.T) {
	for name, tc := range map[string]struct {
		info SessionInfo
		want string
	}{
		"reported cwd wins":  {SessionInfo{CWD: "/home/user/x", Repo: "octo/demo"}, "/home/user/x"},
		"repo":               {SessionInfo{Repo: "octo/demo"}, "/claude-cloud/octo/demo"},
		"no repo still kept": {SessionInfo{}, "/claude-cloud"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := cwdFor(tc.info); got != tc.want {
				t.Fatalf("cwd = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestOldCacheSchemaIsDownloadedAgain(t *testing.T) {
	f, c := newFakeCloud(t)
	f.sessions = []map[string]any{cloudSession(sid, "2026-10-01T11:00:00Z")}
	f.events[sid] = conversation(sid)
	dir := t.TempDir()
	syncOnce(t, c, dir, time.Time{})
	// Rewrite the metadata as a cache from before Repo existed.
	path := filepath.Join(dir, sid+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	old := strings.Replace(strings.Replace(string(data), `"schema":1`, `"schema":0`, 1), `"repo":"octo/demo"`, `"repo":""`, 1)
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	calls := f.eventCalls[sid]
	if res := syncOnce(t, c, dir, time.Time{}); res.Fetched != 1 || f.eventCalls[sid] == calls {
		t.Fatalf("old-schema cache entry was not refreshed: %+v", res)
	}
	if info, ok := readCached(dir, sid); !ok || info.Repo != "octo/demo" {
		t.Fatalf("refreshed entry = %+v, %v", info, ok)
	}
}

func TestEnumeratedSessionsAreCloud(t *testing.T) {
	f, c := newFakeCloud(t)
	f.sessions = []map[string]any{cloudSession(sid, "2026-10-01T11:00:00Z")}
	f.events[sid] = conversation(sid)
	dir := t.TempDir()
	syncOnce(t, c, dir, time.Time{})
	metas, err := (&Source{Dir: dir}).Enumerate(context.Background(), reader.EnumerateOptions{})
	if err != nil || len(metas) != 1 || metas[0].Origin != "cloud" {
		t.Fatalf("metas = %+v, %v; every imported session must carry origin cloud", metas, err)
	}
}
