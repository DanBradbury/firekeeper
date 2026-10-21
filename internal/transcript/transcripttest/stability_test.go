package transcripttest

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DanBradbury/firekeeper/internal/session"
	"github.com/DanBradbury/firekeeper/internal/transcript"
)

// lineSource is a minimal JSONL source: one meta event per complete line,
// seq is the line index, and the offset is the byte after the last complete
// line. restartSeq makes it number events per read instead, which is wrong.
type lineSource struct{ restartSeq bool }

func (lineSource) Locate(session.Meta) ([]string, error) { return nil, nil }

func (s lineSource) Read(path string, from int64) ([]transcript.Event, int64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, err
	}
	seq := int64(bytes.Count(data[:from], []byte("\n")))
	if s.restartSeq {
		seq = 0
	}
	var events []transcript.Event
	offset := from
	for {
		n := bytes.IndexByte(data[offset:], '\n')
		if n < 0 {
			return events, offset, nil
		}
		events = append(events, transcript.Event{
			Provider: transcript.ProviderCodex,
			Seq:      seq,
			Role:     transcript.RoleMeta,
			Text:     string(data[offset : offset+int64(n)]),
		})
		seq++
		offset += int64(n) + 1
	}
}

func writeFixture(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRunSeqStabilityPasses(t *testing.T) {
	full := writeFixture(t, "rollout.jsonl", "{\"a\":1}\n{\"b\":2}\n{\"c\":3}\n{\"d\":4}\n{\"e\":5}\n")
	partial := writeFixture(t, "rollout.jsonl", "{\"a\":1}\n")
	RunSeqStability(t, lineSource{}, []SeqCase{
		{Name: "default halves", Path: full},
		{Name: "explicit partial", Path: full, Partial: partial},
	})
}

func TestCheckSeqStabilityFailures(t *testing.T) {
	tests := []struct {
		name    string
		source  lineSource
		content string
		partial string
		want    string
	}{
		{name: "seq restarts per read", source: lineSource{restartSeq: true}, content: "1\n2\n3\n4\n", want: "split at offset"},
		{name: "too few events", content: "1\n", want: "need at least 2"},
		{name: "partial not a prefix", content: "1\n2\n", partial: "1\n2\n", want: "strict, non-empty prefix"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := SeqCase{Path: writeFixture(t, "s.jsonl", tt.content)}
			if tt.partial != "" {
				c.Partial = writeFixture(t, "s.jsonl", tt.partial)
			}
			err := checkSeqStability(tt.source, c, t.TempDir())
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestWriteLinePrefix(t *testing.T) {
	tests := map[string]string{
		"even":             "a\nb\nc\nd\n",
		"odd":              "a\nb\nc\n",
		"no trailing line": "a\nb\nc",
	}
	want := map[string]string{"even": "a\nb\n", "odd": "a\nb\n", "no trailing line": "a\nb\n"}
	for name, content := range tests {
		t.Run(name, func(t *testing.T) {
			out, err := writeLinePrefix(writeFixture(t, "f.jsonl", content), t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			got, _ := os.ReadFile(out)
			if string(got) != want[name] || filepath.Base(out) != "f.jsonl" {
				t.Fatalf("prefix %q at %s, want %q", got, filepath.Base(out), want[name])
			}
		})
	}
}
