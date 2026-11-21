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
	"time"

	"github.com/firekeeper-ai/firekeeper/internal/claudelimits"
	"github.com/firekeeper-ai/firekeeper/internal/window"
)

func stubWindow(t *testing.T) {
	t.Helper()
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir()) // never read real transcripts
	prev := windowLimits
	windowLimits = func(context.Context, window.Period) (*claudelimits.Window, error) { return nil, nil }
	t.Cleanup(func() { windowLimits = prev })
}

func TestWindowRejectsBadInput(t *testing.T) {
	stubWindow(t)
	for _, test := range []struct {
		args   []string
		stderr string
	}{
		{[]string{"--provider", "nope"}, `unknown provider "nope"`},
		{[]string{"--provider", ""}, "no provider given"},
		{[]string{"--json", "--html", "x.html"}, "not both"},
		{[]string{"extra"}, "unexpected argument"},
		{[]string{"--bogus"}, "not defined"},
	} {
		var out, errb bytes.Buffer
		if code := runWindow(test.args, &out, &errb); code != 2 || !strings.Contains(errb.String(), test.stderr) {
			t.Errorf("%v: code %d, stderr %q; want 2 and %q", test.args, code, errb.String(), test.stderr)
		}
	}
}

// --provider accepts a name in any case and with surrounding spaces, like
// every other command's --provider, and reports the canonical id.
func TestWindowProviderIgnoresCaseAndSpaces(t *testing.T) {
	stubWindow(t)
	for _, name := range []string{"Claude", " claude ", "CLAUDE"} {
		var out, errb bytes.Buffer
		if code := runWindow([]string{"--provider", name, "--json"}, &out, &errb); code != 0 {
			t.Fatalf("--provider %q: code %d: %s", name, code, errb.String())
		}
		var got struct {
			Provider string `json:"provider"`
		}
		if err := json.Unmarshal(out.Bytes(), &got); err != nil || got.Provider != "claude" {
			t.Fatalf("--provider %q: provider %q, %v; want claude", name, got.Provider, err)
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

func TestWindowWeekUsesWeeklyLimit(t *testing.T) {
	stubWindow(t)
	var asked window.Period
	windowLimits = func(_ context.Context, p window.Period) (*claudelimits.Window, error) {
		asked = p
		return &claudelimits.Window{UsedPercent: 91, ResetsAt: windowNow().Add(48 * time.Hour)}, nil
	}
	var out, errb bytes.Buffer
	if code := runWindow([]string{"--week", "--json"}, &out, &errb); code != 0 {
		t.Fatalf("code %d: %s", code, errb.String())
	}
	var got struct {
		Period      string   `json:"period"`
		Basis       string   `json:"basis"`
		UsedPercent *float64 `json:"used_percent"`
		Daily       []any    `json:"daily"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if asked != window.PeriodWeek || got.Period != "week" || got.Basis != "provider_reset" ||
		got.UsedPercent == nil || *got.UsedPercent != 91 || len(got.Daily) != 7 {
		t.Fatalf("asked %q, got %+v", asked, got)
	}
}
