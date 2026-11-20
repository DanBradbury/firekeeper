package otelrecv

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// supplementFixture writes a Claude config dir with one transcript of lines
// assistant messages for each of sessions session ids, and a telemetry file
// with two requests for each of them, one for a session without a transcript,
// and one for a session id that is not a UUID. CLAUDE_CONFIG_DIR points at
// the config dir for the rest of the test.
func supplementFixture(tb testing.TB, sessions, lines int) (telemetry, configDir string, ids []string) {
	tb.Helper()
	root := tb.TempDir()
	configDir = filepath.Join(root, "claude")
	project := filepath.Join(configDir, "projects", "-work")
	if err := os.MkdirAll(project, 0o700); err != nil {
		tb.Fatal(err)
	}
	tb.Setenv("CLAUDE_CONFIG_DIR", configDir)
	var file bytes.Buffer
	for s := range sessions {
		id := fmt.Sprintf("%08x-1111-4111-8111-111111111111", s)
		ids = append(ids, id)
		var transcript strings.Builder
		for l := range lines {
			fmt.Fprintf(&transcript, `{"type":"assistant","message":{"id":"msg_%d","model":"claude-haiku","content":"SECRET_TRANSCRIPT","usage":{"input_tokens":%d,"output_tokens":2,"cache_read_input_tokens":3,"cache_creation_input_tokens":0}}}`+"\n", l, s+1)
		}
		if err := os.WriteFile(filepath.Join(project, id+".jsonl"), []byte(transcript.String()), 0o600); err != nil {
			tb.Fatal(err)
		}
		for r, source := range []string{"repl_main_thread", "agent_summary"} {
			rec := Request{SessionID: id, Model: "claude-haiku", QuerySource: source, InputTokens: 10, OutputTokens: 1, RequestID: fmt.Sprintf("req_%d_%d", s, r)}
			if err := encodeRequest(&file, rec); err != nil {
				tb.Fatal(err)
			}
		}
	}
	for _, id := range []string{"22222222-2222-4222-8222-222222222222", "not-a-uuid"} {
		ids = append(ids, id)
		if err := encodeRequest(&file, Request{SessionID: id, QuerySource: "compact", InputTokens: 7, RequestID: "req_" + id}); err != nil {
			tb.Fatal(err)
		}
	}
	telemetry = filepath.Join(root, "telemetry.jsonl")
	if err := os.WriteFile(telemetry, file.Bytes(), 0o600); err != nil {
		tb.Fatal(err)
	}
	return telemetry, configDir, ids
}

// Every session in the telemetry gets the same Supplement as merging it with
// its transcript one at a time, including sessions whose transcript is
// missing or whose id is not looked up.
func TestLoadSupplementsMergesEverySession(t *testing.T) {
	telemetry, configDir, ids := supplementFixture(t, 12, 5)
	got, err := LoadSupplements(telemetry)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(ids) {
		t.Fatalf("got %d sessions, want %d", len(got), len(ids))
	}
	recs, _, err := ReadFile(telemetry)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		var records []Request
		for _, rec := range recs {
			if rec.SessionID == id {
				records = append(records, rec)
			}
		}
		usage, err := LoadTranscript(id, configDir)
		if err != nil {
			usage = SessionUsage{}
		}
		if want := Merge(usage, records); !reflect.DeepEqual(got[id], want) {
			t.Errorf("session %s = %+v, want %+v", id, got[id], want)
		}
	}
	// The fixture's first session has a transcript, its last two do not.
	if got[ids[0]].Transcript == 0 {
		t.Error("session with a transcript has no transcript tokens")
	}
	for _, id := range ids[len(ids)-2:] {
		if got[id].Transcript != 0 || !got[id].Covered {
			t.Errorf("session %s = %+v, want telemetry only", id, got[id])
		}
	}
}

func TestLoadSupplementsMissingFileIsEmpty(t *testing.T) {
	got, err := LoadSupplements(filepath.Join(t.TempDir(), "missing.jsonl"))
	if err != nil || len(got) != 0 {
		t.Fatalf("got %v, %v; want an empty result and no error", got, err)
	}
}

func TestLoadSupplementsUnreadableFileFails(t *testing.T) {
	if _, err := LoadSupplements(t.TempDir()); err == nil {
		t.Fatal("reading a directory as the telemetry file succeeded")
	}
}

// BenchmarkLoadSupplements measures an uncached load: the telemetry file
// changed, so every session's transcript is read again.
func BenchmarkLoadSupplements(b *testing.B) {
	for _, sessions := range []int{4, 16} {
		b.Run(fmt.Sprintf("sessions=%d", sessions), func(b *testing.B) {
			telemetry, _, _ := supplementFixture(b, sessions, 2000)
			for b.Loop() {
				supplementCache.mu.Lock()
				supplementCache.val = nil
				supplementCache.mu.Unlock()
				if _, err := LoadSupplements(telemetry); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
