package reporter

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/DanBradbury/firekeeper/internal/session"
	"github.com/DanBradbury/firekeeper/internal/transcript"
	"github.com/DanBradbury/firekeeper/internal/transcript/claude"
	"github.com/DanBradbury/firekeeper/internal/transcript/reader"
)

// BenchmarkScanIdlePass times a pass that finds nothing to upload over 2,000
// synthetic Claude Code transcripts, all already uploaded. "outside window"
// is the daemon's steady state, where every transcript is older than the
// window and the scan only stats files; "inside window" is the first pass
// after a start, where each transcript head is read for its working
// directory and each file is compared with its saved position.
func BenchmarkScanIdlePass(b *testing.B) {
	const files = 2000
	home := b.TempDir()
	repo := filepath.Join(home, "src", "demo")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		b.Fatal(err)
	}
	configDir := filepath.Join(home, ".claude")
	state := &State{Files: map[string]FileState{}}
	old := time.Now().Add(-time.Hour)
	for i := 0; i < files; i++ {
		dir := filepath.Join(configDir, "projects", fmt.Sprintf("-src-demo-%d", i%40))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			b.Fatal(err)
		}
		id := fmt.Sprintf("0199c3d4-e5f6-7a8b-9c0d-%012x", i)
		path := filepath.Join(dir, id+".jsonl")
		var data []byte
		for j := 0; j < 20; j++ {
			data = append(data, fmt.Sprintf(`{"type":"user","uuid":"00000000-0000-4000-8000-%012x","timestamp":"2026-01-02T03:04:05.000Z","cwd":%q,"sessionId":%q,"message":{"role":"user","content":"synthetic %d"}}`+"\n", j, repo, id, j)...)
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			b.Fatal(err)
		}
		if err := os.Chtimes(path, old, old); err != nil {
			b.Fatal(err)
		}
		state.Files[path] = FileState{Offset: int64(len(data)), Size: int64(len(data)), Mtime: old, LastSeq: 19}
	}
	statePath := filepath.Join(home, ".firekeeper", "state.json")
	if err := os.MkdirAll(filepath.Dir(statePath), 0o700); err != nil {
		b.Fatal(err)
	}
	if err := state.Save(statePath); err != nil {
		b.Fatal(err)
	}
	source := &claude.Source{ConfigDir: configDir}
	cfg := Config{
		Providers: []transcript.Provider{transcript.ProviderClaude},
		DryRun:    true,
		Home:      home,
		ScanEnded: true,
		Discover:  func(context.Context, session.Options) ([]session.Meta, error) { return nil, nil },
		Sources: func(p transcript.Provider) (reader.Source, bool) {
			return source, p == transcript.ProviderClaude
		},
		ScanEnumerators: func(p transcript.Provider) (reader.Enumerator, bool) {
			return source, p == transcript.ProviderClaude
		},
	}
	for _, bc := range []struct {
		name  string
		after time.Time
	}{
		{"outside window", time.Now().Add(-30 * time.Second)},
		{"inside window", time.Now().Add(-24 * time.Hour)},
	} {
		b.Run(bc.name, func(b *testing.B) {
			cfg := cfg
			cfg.ScanAfter = bc.after
			for b.Loop() {
				summary, err := RunOnce(context.Background(), cfg)
				if err != nil {
					b.Fatal(err)
				}
				if summary.Scan.Found != 0 || len(summary.Sessions) != 0 {
					b.Fatalf("idle pass found work: %+v", summary.Scan)
				}
			}
		})
	}
}
