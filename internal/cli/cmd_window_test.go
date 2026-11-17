package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DanBradbury/firekeeper/internal/claudelimits"
)

func stubWindow(t *testing.T) {
	t.Helper()
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir()) // never read real transcripts
	prev := windowLimits
	windowLimits = func(context.Context) (*claudelimits.Window, error) { return nil, nil }
	t.Cleanup(func() { windowLimits = prev })
}

func TestWindowRejectsBadInput(t *testing.T) {
	stubWindow(t)
	for _, args := range [][]string{
		{"--provider", "nope"},
		{"--json", "--html", "x.html"},
		{"extra"},
		{"--bogus"},
	} {
		var out, errb bytes.Buffer
		if code := runWindow(args, &out, &errb); code != 2 {
			t.Errorf("%v: code %d, want 2 (stderr %q)", args, code, errb.String())
		}
	}
}

func TestWindowJSONAndHTML(t *testing.T) {
	stubWindow(t)
	var out, errb bytes.Buffer
	if code := runWindow([]string{"--json"}, &out, &errb); code != 0 {
		t.Fatalf("code %d: %s", code, errb.String())
	}
	var got struct {
		Provider string            `json:"provider"`
		Sessions []json.RawMessage `json:"sessions"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil || got.Provider != "claude" || got.Sessions == nil {
		t.Fatalf("bad JSON %v: %s", err, out.String())
	}

	path := filepath.Join(t.TempDir(), "w.html")
	out.Reset()
	if code := runWindow([]string{"--html", path}, &out, &errb); code != 0 {
		t.Fatalf("code %d: %s", code, errb.String())
	}
	u, err := url.Parse(strings.TrimSpace(out.String()))
	if err != nil || u.Scheme != "file" {
		t.Fatalf("url = %q", out.String())
	}
	if page, err := os.ReadFile(path); err != nil || !strings.Contains(string(page), "5-hour window") {
		t.Fatalf("page: %v", err)
	}
}
