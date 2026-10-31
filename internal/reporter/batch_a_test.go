package reporter

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DanBradbury/firekeeper/internal/session"
	"github.com/DanBradbury/firekeeper/internal/transcript"
	"github.com/DanBradbury/firekeeper/internal/transcript/antigravity"
	"github.com/DanBradbury/firekeeper/internal/transcript/grok"
	"github.com/DanBradbury/firekeeper/internal/transcript/reader"
)

func TestBatchAProvidersBackfillAndResume(t *testing.T) {
	for _, provider := range []transcript.Provider{transcript.ProviderGrok, transcript.ProviderAntigravity} {
		t.Run(string(provider), func(t *testing.T) {
			f := newFixture(t)
			ts := newTestServer(t)
			var source reader.Source
			var record string
			switch provider {
			case transcript.ProviderGrok:
				home := filepath.Join(f.home, ".grok")
				f.rollout = filepath.Join(home, "sessions", url.PathEscape(f.repo), testSession, "updates.jsonl")
				source = &grok.Source{Home: home}
				record = fmt.Sprintf(`{"timestamp":1793429000,"method":"session/update","params":{"sessionId":%q,"update":{"sessionUpdate":"user_message_chunk","content":{"type":"text","text":"Synthetic fixture"}}}}`+"\n", testSession)
			case transcript.ProviderAntigravity:
				dir := filepath.Join(f.home, ".gemini", "antigravity-cli")
				f.rollout = filepath.Join(dir, "brain", testSession, ".system_generated", "logs", "transcript.jsonl")
				source = &antigravity.Source{Dir: dir}
				record = `{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","status":"DONE","created_at":"2026-10-30T12:00:00Z","content":"Synthetic fixture"}` + "\n"
				cache := filepath.Join(dir, "cache")
				if err := os.MkdirAll(cache, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(cache, "last_conversations.json"), []byte(fmt.Sprintf("{%q:%q}", f.repo, testSession)), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.MkdirAll(filepath.Dir(f.rollout), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(f.rollout, []byte(record), 0600); err != nil {
				t.Fatal(err)
			}
			f.meta.Provider, f.meta.RolloutPath = string(provider), f.rollout
			cfg := f.config(ts.URL)
			cfg.Providers = []transcript.Provider{provider}
			cfg.Sources = func(p transcript.Provider) (reader.Source, bool) { return source, p == provider }
			enumerator := source.(reader.Enumerator)
			bcfg := BackfillConfig{Config: cfg, Enumerators: func(p transcript.Provider) (reader.Enumerator, bool) { return enumerator, p == provider }}
			result := backfill(t, bcfg)
			if len(result.Sessions) != 1 || result.Sessions[0].Err != nil || result.Sessions[0].Events != 1 {
				t.Fatal("new provider did not backfill through the API")
			}
			// A foreground pass resumes the same offset rather than duplicating history.
			next := runOnce(t, cfg)
			if next.Err != nil || next.Events != 0 {
				t.Fatal("foreground pass did not reuse backfill offset")
			}
			file, err := os.OpenFile(f.rollout, os.O_APPEND|os.O_WRONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			_, err = file.WriteString(record)
			closeErr := file.Close()
			if err != nil || closeErr != nil {
				t.Fatal("append fixture")
			}
			next = runOnce(t, cfg)
			if next.Err != nil || next.Events != 1 {
				t.Fatal("appended source record not uploaded")
			}
			assertSeqs(t, ts.storedFor(t, testSession), 2)
			before := f.state(t)
			large := strings.Replace(record, "Synthetic fixture", strings.Repeat("x", 4<<20), 1)
			file, err = os.OpenFile(f.rollout, os.O_APPEND|os.O_WRONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			_, err = file.WriteString(large)
			closeErr = file.Close()
			if err != nil || closeErr != nil {
				t.Fatal("append oversized fixture")
			}
			next = runOnce(t, cfg)
			if next.Err == nil || next.Batches != 0 || f.state(t).Offset != before.Offset {
				t.Fatal("oversized event was silently dropped or advanced the upload cursor")
			}
			cfg.Exclude = []string{f.repo}
			cfg.Discover = func(context.Context, session.Options) ([]session.Meta, error) { return []session.Meta{f.meta}, nil }
			if result := runOnce(t, cfg); result.Events != 0 || result.Skipped == "" {
				t.Fatal("excluded session was read")
			}
		})
	}
}
