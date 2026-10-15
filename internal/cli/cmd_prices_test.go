package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DanBradbury/firekeeper/internal/server"
	"github.com/DanBradbury/firekeeper/internal/server/prices"
	"github.com/DanBradbury/firekeeper/internal/server/store"
	"github.com/DanBradbury/firekeeper/internal/transcript"
)

// serveConfig runs `firekeeper serve` with the given config file body ("" for
// no file) and returns the server config it would start with.
func serveConfig(t *testing.T, body string) server.Config {
	t.Helper()
	if body != "" {
		withConfigFile(t, body)
	}
	var got server.Config
	previous := serveRun
	serveRun = func(_ context.Context, cfg server.Config) error { got = cfg; return nil }
	t.Cleanup(func() { serveRun = previous })
	var stdout, stderr bytes.Buffer
	if code := runServe(nil, &stdout, &stderr); code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr.String())
	}
	return got
}

func TestServeDefaultPrices(t *testing.T) {
	d, err := prices.Defaults()
	if err != nil {
		t.Fatal(err)
	}

	// No config file: the built-in table, dated.
	got := serveConfig(t, "")
	if p, ok := got.Prices["gpt-5"]; !ok || p.Source != store.PriceSourceDefault || got.PricesAsOf != d.AsOf {
		t.Fatalf("no config: gpt-5 %+v (found %v) as_of %q", p, ok, got.PricesAsOf)
	}
	if len(got.Prices) < len(d.Models) {
		t.Fatalf("no config: %d prices for %d default models", len(got.Prices), len(d.Models))
	}

	// A custom price wins for its model only.
	got = serveConfig(t, "[prices.\"gpt-5\"]\ninput = 9\noutput = 9\ncache = 9\n")
	if p := got.Prices["gpt-5"]; p.Input != 9 || p.Source != store.PriceSourceCustom {
		t.Fatalf("custom gpt-5: %+v", p)
	}
	if p := got.Prices["o3"]; p.Source != store.PriceSourceDefault || got.PricesAsOf != d.AsOf {
		t.Fatalf("o3 should keep its default: %+v as_of %q", p, got.PricesAsOf)
	}

	// defaults = false: the user's table only, with no date.
	got = serveConfig(t, "[prices]\ndefaults = false\n\n[prices.\"gpt-5\"]\ninput = 9\n")
	if len(got.Prices) != 1 || got.Prices["gpt-5"].Input != 9 || got.PricesAsOf != "" {
		t.Fatalf("defaults off: %+v as_of %q", got.Prices, got.PricesAsOf)
	}

	// defaults = false and no prices: tokens only, exactly as before.
	got = serveConfig(t, "[prices]\ndefaults = false\n")
	if got.Prices != nil || got.PricesAsOf != "" {
		t.Fatalf("no prices at all: %+v as_of %q", got.Prices, got.PricesAsOf)
	}
}

func TestConfigShowDefaultsOff(t *testing.T) {
	withConfigFile(t, "[prices]\ndefaults = false\n")
	var stdout, stderr bytes.Buffer
	if code := runConfig([]string{"show"}, &stdout, &stderr); code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "[prices]\ndefaults = false\n") {
		t.Fatalf("output lacks the defaults switch:\n%s", stdout.String())
	}
}

func TestPricesMissing(t *testing.T) {
	const secret = "synthetic transcript text that must never print"
	db := filepath.Join(t.TempDir(), "dashboard.db")
	ctx := context.Background()
	s, err := store.Open(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	ev := func(seq int64, model string, in, out, cache int64) transcript.Event {
		at := time.Date(2026, 10, 1, 0, 0, int(seq), 0, time.UTC)
		e := transcript.Event{Seq: seq, TS: &at, Provider: transcript.ProviderCodex, Role: transcript.RoleAssistant,
			Text: secret, Tokens: transcript.Tokens{Input: in, Output: out, Cache: cache}, Raw: json.RawMessage(`{}`)}
		if model != "" {
			e.Model = &model
		}
		return e
	}
	batch := []store.SessionBatch{
		{Meta: store.SessionMeta{SessionID: "s1", Provider: "codex", Model: "gpt-5", Title: secret}, Events: []transcript.Event{
			ev(0, "gpt-5", 100, 10, 0),             // default price
			ev(1, "gpt-5-2026-01-01", 1000, 0, 0),  // dated name: no prefix match
			ev(2, "my-local-model", 5, 5, 5),       // custom price
			ev(3, "gpt-5-2026-01-01", 0, 200, 300), // summed
			ev(4, "weird\tname", 1, 0, 0),          // unprintable: quoted
			ev(5, "claude-sonnet-4.6", 7, 0, 0),    // alias
		}},
		{Meta: store.SessionMeta{SessionID: "s2", Provider: "codex"}, Events: []transcript.Event{
			ev(0, "", 2, 2, 2), // no model anywhere
		}},
	}
	if _, _, err := s.Ingest(ctx, store.DefaultAccountID, store.Machine{ID: "m1"}, batch); err != nil {
		t.Fatal(err)
	}
	// A second account's models count too: this is an operator report.
	other, err := s.CreateAccount(ctx, "other@example.com", "synthetic-hash", "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Ingest(ctx, other.ID, store.Machine{ID: "m1"}, []store.SessionBatch{{
		Meta: store.SessionMeta{SessionID: "s1", Provider: "codex"}, Events: []transcript.Event{ev(0, "other-model", 3, 0, 0)},
	}}); err != nil {
		t.Fatal(err)
	}
	s.Close()

	withConfigFile(t, "[prices.\"my-local-model\"]\ninput = 1\n")
	var stdout, stderr bytes.Buffer
	if code := runServe([]string{"prices", "missing", "--db", db}, &stdout, &stderr); code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr.String())
	}
	out := stdout.String()
	lines := strings.Split(strings.TrimSpace(out), "\n")
	want := [][]string{
		{"MODEL", "INPUT", "OUTPUT", "CACHE", "TOTAL"},
		{"gpt-5-2026-01-01", "1000", "200", "300", "1500"},
		{"(no", "model)", "2", "2", "2", "6"},
		{"other-model", "3", "0", "0", "3"},
		{`"weird\tname"`, "1", "0", "0", "1"},
	}
	if len(lines) != len(want) {
		t.Fatalf("output:\n%s", out)
	}
	for i, w := range want {
		if got := strings.Fields(lines[i]); strings.Join(got, " ") != strings.Join(w, " ") {
			t.Errorf("line %d = %q, want %q", i, got, w)
		}
	}
	if strings.Contains(out, secret) || strings.Contains(stderr.String(), secret) {
		t.Fatal("output includes transcript text")
	}
	for _, priced := range []string{"my-local-model", "claude-sonnet-4.6", "gpt-5 "} {
		if strings.Contains(out, priced) {
			t.Errorf("priced model %q listed as missing", priced)
		}
	}

	// Defaults off: the default-priced models are missing too.
	withConfigFile(t, "[prices]\ndefaults = false\n")
	stdout.Reset()
	if code := runServe([]string{"prices", "missing", "--db", db}, &stdout, &stderr); code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "claude-sonnet-4.6") || !strings.Contains(stdout.String(), "my-local-model") {
		t.Fatalf("defaults off:\n%s", stdout.String())
	}

	for _, args := range [][]string{{"prices"}, {"prices", "list"}, {"prices", "missing", "extra"}} {
		if code := runServe(args, &stdout, &stderr); code != 2 {
			t.Errorf("%q: code %d, want 2", args, code)
		}
	}
}
