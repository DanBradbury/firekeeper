package copilot

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DanBradbury/firekeeper/internal/session"
	"github.com/DanBradbury/firekeeper/internal/transcript"
)

const (
	idWorkspace = "11111111-1111-4111-8111-111111111111"
	idHeadOnly  = "22222222-2222-4222-8222-222222222222"
	idExcluded  = "33333333-3333-4333-8333-333333333333"
	idEmpty     = "44444444-4444-4444-8444-444444444444"
	idCorrupt   = "55555555-5555-4555-8555-555555555555"
	idLarge     = "66666666-6666-4666-8666-666666666666"
)

func writeFile(t *testing.T, path, contents string, mtime time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

func day(d int) time.Time { return time.Date(2026, 1, d, 12, 0, 0, 0, time.UTC) }

func start(cwd string) string {
	return `{"type":"session.start","data":{"startTime":"2026-01-02T03:04:05.000Z","context":{"cwd":"` + cwd + `","gitRoot":"` + cwd + `","branch":"head-branch"}},"timestamp":"2026-01-02T03:04:05.000Z"}` + "\n"
}

func modelChange(model string) string {
	return `{"type":"session.model_change","data":{"newModel":"` + model + `"},"timestamp":"2026-01-02T03:04:06.000Z"}` + "\n"
}

// fixture builds a COPILOT_HOME with sessions backed by workspace.yaml, one
// without it, an excluded one, an empty and a corrupt events file, and one
// far larger than the head limit.
func fixture(t *testing.T) (home string, paths map[string]string) {
	home = t.TempDir()
	paths = map[string]string{}
	for _, id := range []string{idWorkspace, idHeadOnly, idExcluded, idEmpty, idCorrupt, idLarge} {
		paths[id] = filepath.Join(home, "session-state", id, "events.jsonl")
	}
	writeFile(t, filepath.Join(home, "session-state", idWorkspace, "workspace.yaml"),
		"id: "+idWorkspace+"\ncwd: /work/repo/sub\ngit_root: /work/repo\nbranch: main\nname: Named session\ncreated_at: 2026-01-02T00:00:00.000Z\nupdated_at: 2026-01-05T00:00:00.000Z\n", day(5))
	writeFile(t, paths[idWorkspace], start("/wrong")+modelChange("first-model")+modelChange("workspace-model"), day(5))
	writeFile(t, paths[idHeadOnly], start("/work/head")+modelChange("head-model"), day(6))
	writeFile(t, filepath.Join(home, "session-state", idExcluded, "workspace.yaml"), "id: "+idExcluded+"\ncwd: /excluded/repo\n", day(7))
	writeFile(t, paths[idExcluded], start("/excluded/repo"), day(7))
	writeFile(t, paths[idEmpty], "", day(8))
	writeFile(t, paths[idCorrupt], "not json\n[[[\n", day(8))

	var large strings.Builder
	large.WriteString(start("/work/large"))
	large.WriteString(modelChange("head-model"))
	filler := `{"type":"assistant.message_delta","data":{"deltaContent":"` + strings.Repeat("x", 1000) + `"}}` + "\n"
	for large.Len() < 4*transcript.HeadBytes {
		large.WriteString(filler)
	}
	large.WriteString(modelChange("tail-model"))
	writeFile(t, paths[idLarge], large.String(), day(4))

	writeFile(t, filepath.Join(home, "session-state", "not-a-uuid", "events.jsonl"), start("/x"), day(9))
	return home, paths
}

// opens records each events file opened and how many bytes were read.
type opens struct {
	mu    sync.Mutex
	bytes map[string]int64
}

func (o *opens) open(name string) (io.ReadCloser, error) {
	file, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.bytes == nil {
		o.bytes = map[string]int64{}
	}
	o.bytes[name] += 0
	return &countingReader{file, o, name}, nil
}

type countingReader struct {
	*os.File
	o    *opens
	name string
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.File.Read(p)
	c.o.mu.Lock()
	c.o.bytes[c.name] += int64(n)
	c.o.mu.Unlock()
	return n, err
}

func (o *opens) opened(path string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	_, ok := o.bytes[path]
	return ok
}

func TestEnumerate(t *testing.T) {
	home, paths := fixture(t)
	var o opens
	source := &Source{CopilotHome: home, open: o.open}
	skipCalls := map[string]int{}
	metas, err := source.Enumerate(context.Background(), transcript.EnumerateOptions{Skip: func(m session.Meta) bool {
		skipCalls[m.ID]++
		return strings.HasPrefix(m.CWD, "/excluded")
	}})
	var warning *session.Warning
	if !errors.As(err, &warning) || !strings.Contains(warning.Message, "skipped 2 ") {
		t.Fatalf("err = %v, want warning counting the empty and corrupt files", err)
	}
	var ids []string
	for _, m := range metas {
		ids = append(ids, m.ID)
	}
	if want := []string{idHeadOnly, idWorkspace, idLarge}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("ids = %v, want %v", ids, want)
	}
	for id, calls := range skipCalls {
		if calls != 1 {
			t.Errorf("Skip called %d times for %s", calls, id)
		}
	}

	// workspace.yaml supplied the excluded session's directory, so its
	// events file is never opened.
	if skipCalls[idExcluded] != 1 || o.opened(paths[idExcluded]) {
		t.Errorf("excluded session: Skip calls %d, opened %v", skipCalls[idExcluded], o.opened(paths[idExcluded]))
	}
	if o.opened(paths[idEmpty]) {
		t.Error("empty events file was opened")
	}
	if n := o.bytes[paths[idLarge]]; n == 0 || n > transcript.HeadBytes {
		t.Errorf("read %d bytes of the large events file, want 1..%d", n, transcript.HeadBytes)
	}

	byID := map[string]session.Meta{}
	for _, m := range metas {
		byID[m.ID] = m
		if m.State != session.SessionStateEnded || m.Provider != "copilot" || m.EventCount != 0 || m.TokensUsed != 0 || m.RolloutPath != paths[m.ID] {
			t.Errorf("%s: %+v", m.ID, m)
		}
	}
	ws := byID[idWorkspace]
	if ws.CWD != "/work/repo/sub" || ws.Project != "repo" || ws.Branch != "main" || ws.Title != "Named session" ||
		ws.Model != "workspace-model" || !ws.StartedAt.Equal(day(2).Add(-12*time.Hour)) || !ws.LastActivityAt.Equal(day(5).Add(-12*time.Hour)) {
		t.Errorf("workspace-backed meta = %+v", ws)
	}
	head := byID[idHeadOnly]
	if head.CWD != "/work/head" || head.Project != "head" || head.Branch != "head-branch" || head.Model != "head-model" ||
		head.StartedAt == nil || head.LastActivityAt != nil {
		t.Errorf("head-backed meta = %+v", head)
	}
	if large := byID[idLarge]; large.Model != "head-model" {
		t.Errorf("large file model = %q, want the head's model", large.Model)
	}

	for _, m := range metas {
		for _, query := range []session.Meta{m, {ID: m.ID, Provider: m.Provider}} {
			got, err := source.Locate(query)
			if err != nil || !reflect.DeepEqual(got, []string{m.RolloutPath}) {
				t.Errorf("Locate(%s) = %v, %v", m.ID, got, err)
			}
		}
	}
}

func TestEnumerateModifiedAfterAndEnv(t *testing.T) {
	home, _ := fixture(t)
	t.Setenv("COPILOT_HOME", home)
	metas, err := (&Source{}).Enumerate(context.Background(), transcript.EnumerateOptions{ModifiedAfter: day(6)})
	var warning *session.Warning
	if err != nil && !errors.As(err, &warning) {
		t.Fatal(err)
	}
	if len(metas) != 1 || metas[0].ID != idExcluded {
		t.Fatalf("metas = %+v, want only the session modified after day 6", metas)
	}
}

func TestEnumerateCanceled(t *testing.T) {
	home, _ := fixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (&Source{CopilotHome: home}).Enumerate(ctx, transcript.EnumerateOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestEnumeratorRegistered(t *testing.T) {
	if _, ok := transcript.EnumeratorFor(transcript.ProviderCopilot); !ok {
		t.Fatal("copilot source does not enumerate")
	}
	if _, ok := transcript.EnumeratorFor(transcript.ProviderKimi); ok {
		t.Fatal("kimi has no transcript source and cannot enumerate")
	}
}
