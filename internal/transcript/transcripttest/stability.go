// Package transcripttest holds conformance helpers that every
// transcript.TranscriptSource runs against its own fixtures.
package transcripttest

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/DanBradbury/firekeeper/internal/transcript"
)

// SeqCase is one fixture for RunSeqStability.
type SeqCase struct {
	Name string
	// Path is the complete fixture.
	Path string
	// Partial is an earlier state of the same source, such as a SQLite
	// fixture holding only the first rows. When empty, Path is treated as
	// line-oriented and its first half of lines is copied into a temporary
	// file with the same base name.
	Partial string
}

// RunSeqStability checks that reading each fixture in two halves, the way a
// tailing reporter sees a growing file, yields the same events as reading it
// whole. The first half is read from Partial at offset 0; the second half is
// read from Path at the offset that read returned. Seq, Role, Text, and
// ToolName must match the whole read in order. It also checks that re-reads
// are deterministic, seqs strictly increase, and reading at the end is a no-op.
//
// Failure messages name seqs and offsets only, never transcript text.
func RunSeqStability(t *testing.T, source transcript.TranscriptSource, cases []SeqCase) {
	t.Helper()
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			if err := checkSeqStability(source, c, t.TempDir()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

type eventKey struct {
	Seq      int64
	Role     transcript.Role
	Text     string
	ToolName string
}

func keys(events []transcript.Event) []eventKey {
	out := make([]eventKey, len(events))
	for i, e := range events {
		out[i] = eventKey{Seq: e.Seq, Role: e.Role, Text: e.Text}
		if e.ToolName != nil {
			out[i].ToolName = *e.ToolName
		}
	}
	return out
}

func seqs(events []transcript.Event) []int64 {
	out := make([]int64, len(events))
	for i, e := range events {
		out[i] = e.Seq
	}
	return out
}

func checkSeqStability(source transcript.TranscriptSource, c SeqCase, tempDir string) error {
	whole, end, err := source.Read(c.Path, 0)
	if err != nil {
		return fmt.Errorf("whole read: %w", err)
	}
	if len(whole) < 2 {
		return fmt.Errorf("fixture yields %d events; need at least 2 to split", len(whole))
	}
	for i, e := range whole {
		if e.Seq < 0 || (i > 0 && e.Seq <= whole[i-1].Seq) {
			return fmt.Errorf("seqs not strictly increasing from zero or above: %v", seqs(whole))
		}
	}

	again, againEnd, err := source.Read(c.Path, 0)
	if err != nil {
		return fmt.Errorf("second whole read: %w", err)
	}
	if againEnd != end || !equalKeys(keys(again), keys(whole)) {
		return fmt.Errorf("re-read not deterministic: seqs %v end %d, then seqs %v end %d", seqs(whole), end, seqs(again), againEnd)
	}

	partial := c.Partial
	if partial == "" {
		if partial, err = writeLinePrefix(c.Path, tempDir); err != nil {
			return err
		}
	}
	first, mid, err := source.Read(partial, 0)
	if err != nil {
		return fmt.Errorf("first half read: %w", err)
	}
	if len(first) == 0 || len(first) >= len(whole) {
		return fmt.Errorf("partial fixture yields %d of %d events; it must hold a strict, non-empty prefix", len(first), len(whole))
	}
	second, secondEnd, err := source.Read(c.Path, mid)
	if err != nil {
		return fmt.Errorf("second half read from offset %d: %w", mid, err)
	}
	split := append(append([]transcript.Event{}, first...), second...)
	if !equalKeys(keys(split), keys(whole)) {
		return fmt.Errorf("split at offset %d gives seqs %v + %v, whole gives %v (or roles, text, or tool names differ)", mid, seqs(first), seqs(second), seqs(whole))
	}
	if secondEnd != end {
		return fmt.Errorf("split read ends at offset %d, whole read at %d", secondEnd, end)
	}

	tail, tailEnd, err := source.Read(c.Path, end)
	if err != nil {
		return fmt.Errorf("read at end offset %d: %w", end, err)
	}
	if len(tail) != 0 || tailEnd != end {
		return fmt.Errorf("read at end offset %d returned %d events and offset %d; want none and the same offset", end, len(tail), tailEnd)
	}
	return nil
}

func equalKeys(a, b []eventKey) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// writeLinePrefix copies the first half of path's newline-terminated lines into
// dir under the same base name and returns the copy's path.
func writeLinePrefix(path, dir string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read fixture: %w", err)
	}
	lines := bytes.Count(data, []byte("\n"))
	if !bytes.HasSuffix(data, []byte("\n")) && len(data) > 0 {
		lines++
	}
	if lines < 2 {
		return "", errors.New("fixture has fewer than 2 lines; set SeqCase.Partial")
	}
	keep := (lines + 1) / 2
	cut := 0
	for i := 0; i < keep; i++ {
		cut += bytes.IndexByte(data[cut:], '\n') + 1
	}
	out := filepath.Join(dir, filepath.Base(path))
	if err := os.WriteFile(out, data[:cut], 0o600); err != nil {
		return "", fmt.Errorf("write partial fixture: %w", err)
	}
	return out, nil
}
