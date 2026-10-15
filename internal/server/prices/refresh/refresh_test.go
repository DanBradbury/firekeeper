package refresh

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/DanBradbury/firekeeper/internal/server/prices"
)

func readFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func wantRows(t *testing.T, got map[string]row, want map[string]Rates, bad ...string) {
	t.Helper()
	for label, r := range want {
		g, ok := got[label]
		if !ok || g.err != nil || g.rates != r {
			t.Errorf("%q: got %+v (found %v), want %+v", label, g, ok, r)
		}
	}
	for _, label := range bad {
		if g, ok := got[label]; !ok || g.err == nil {
			t.Errorf("%q: want an unreadable row, got %+v (found %v)", label, g, ok)
		}
	}
}

func TestParseAnthropic(t *testing.T) {
	got, err := parseAnthropic(readFixture(t, "anthropic.md"))
	if err != nil {
		t.Fatal(err)
	}
	// Footnote markers are dropped; the batch table further down is not read.
	wantRows(t, got, map[string]Rates{
		"Claude Alpha 9": {Input: 10, Output: 50, Cache: 0.25},
		"Claude Beta 9 ([limited](https://example.com))": {Input: 2, Output: 10, Cache: 0.20},
		"Claude Gamma 9": {Input: 0.80, Output: 4, Cache: 0.08},
	}, "Claude Odd 9")
}

func TestParseOpenAI(t *testing.T) {
	got, err := parseOpenAI(readFixture(t, "openai.md"))
	if err != nil {
		t.Fatal(err)
	}
	// Standard tier only: gpt-x's Batch, Fast and untiered Cyber rows and
	// gpt-x-codex's Fast row are skipped, and the per-minute table ignored.
	wantRows(t, got, map[string]Rates{
		"gpt-x":                             {Input: 2, Output: 10, Cache: 0.20},
		"gpt-x-mini (<272K context length)": {Input: 0.25, Output: 2, Cache: 0.025},
		"gpt-x-codex":                       {Input: 1.75, Output: 14, Cache: 0.175},
	}, "gpt-x-pro", "moderation-latest")
	if _, ok := got["live-voice"]; ok {
		t.Error("read a per-minute table as token prices")
	}
	// Fine-tuning and image tables qualify their prices with another
	// column; gpt-x keeps its base price rather than a conflict.
	for _, label := range []string{"image-x", "gpt-x-2026-01-01"} {
		if _, ok := got[label]; ok {
			t.Errorf("read %q from a fine-tuning or image table", label)
		}
	}
}

func TestParseKimi(t *testing.T) {
	got, err := parseKimi(readFixture(t, "kimi.md"))
	if err != nil {
		t.Fatal(err)
	}
	// Both column namings: "Input Price"/"Cached Input Price" and
	// "Input Price (Cache Miss)"/"Input Price (Cache Hit)".
	wantRows(t, got, map[string]Rates{
		"kimi-k9":      {Input: 3, Output: 15, Cache: 0.30},
		"kimi-k8-code": {Input: 0.95, Output: 4, Cache: 0.19},
		"kimi-k8":      {Input: 0.95, Output: 4, Cache: 0.16},
	})
}

// TestParseLayoutChanges checks that a page whose layout moved fails
// instead of being read wrongly.
func TestParseLayoutChanges(t *testing.T) {
	anthropic, openai, kimi := readFixture(t, "anthropic.md"), readFixture(t, "openai.md"), readFixture(t, "kimi.md")
	tests := []struct {
		name  string
		parse pageParser
		page  string
	}{
		{"anthropic section renamed", parseAnthropic, strings.Replace(anthropic, "## Model pricing", "## Prices", 1)},
		{"anthropic column renamed", parseAnthropic, strings.Replace(anthropic, "Cache hits and refreshes", "Cache reads", 1)},
		{"anthropic table gone", parseAnthropic, "## Model pricing\n\nSee the console.\n\n## Other\n"},
		{"openai tiers renamed", parseOpenAI, strings.NewReplacer("Standard", "Default").Replace(openai)},
		{"kimi unit changed", parseKimi, strings.Replace(kimi, `"kimi-k9", "1M tokens"`, `"kimi-k9", "1K tokens"`, 1)},
		{"kimi tables gone", parseKimi, "# Pricing\n\n| Model | Input |\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got, err := tt.parse(tt.page); err == nil {
				t.Fatalf("parsed %+v, want an error", got)
			}
		})
	}

	// A model listed twice in the Standard tier with different prices is
	// ambiguous.
	dup := strings.Replace(openai, "| gpt-x-codex | $1.75 |", "| gpt-x-codex | $1.75 |", 1) +
		"\nPrices per 1M tokens.\n\nStandard\n\n| Model | Input | Cached input | Output |\n| --- | --- | --- | --- |\n| gpt-x | $3.00 | $0.30 | $12.00 |\n"
	got, err := parseOpenAI(dup)
	if err != nil {
		t.Fatal(err)
	}
	if got["gpt-x"].err == nil {
		t.Fatalf("conflicting duplicate read as %+v", got["gpt-x"])
	}
}

// pagesFetch serves the fixtures at the Markdown URLs, with optional edits.
func pagesFetch(t *testing.T, edit func(url, page string) string) Fetch {
	pages := map[string]string{
		"https://platform.claude.com/docs/en/about-claude/pricing.md":         readFixture(t, "anthropic.md"),
		"https://developers.openai.com/api/docs/pricing.md":                   readFixture(t, "openai.md"),
		"https://platform.kimi.ai/docs/pricing/chat.md":                       readFixture(t, "kimi.md"),
		"https://platform.claude.com/docs/en/about-claude/models/overview.md": readFixture(t, "anthropic-models.md"),
	}
	return func(_ context.Context, url string) ([]byte, error) {
		p, ok := pages[url]
		if !ok {
			return nil, errors.New("HTTP 404")
		}
		if edit != nil {
			p = edit(url, p)
		}
		return []byte(p), nil
	}
}

func day(s string) time.Time {
	d, err := time.Parse(dateLayout, s)
	if err != nil {
		panic(err)
	}
	return d
}

func TestRefreshUnchanged(t *testing.T) {
	table := []byte(readFixture(t, "table.toml"))
	out, rep, err := Refresh(context.Background(), table, pagesFetch(t, nil), day("2026-09-10"))
	if err != nil {
		t.Fatal(err)
	}
	if rep.Changed() || string(out) != string(table) || len(rep.Sources) != 3 {
		t.Fatalf("young unchanged table: changed %v sources %v", rep.Changed(), rep.Sources)
	}
}

func TestRefreshRestamp(t *testing.T) {
	table := readFixture(t, "table.toml")
	out, rep, err := Refresh(context.Background(), []byte(table), pagesFetch(t, nil), day("2026-10-01"))
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Restamped || len(rep.Changes) != 0 || len(rep.Review) != 0 {
		t.Fatalf("report %+v", rep)
	}
	// Only as_of lines change: "2.00" and "0.20" keep their spelling.
	if want := strings.ReplaceAll(table, `"2026-09-01"`, `"2026-10-01"`); string(out) != want {
		t.Fatalf("restamp rewrote more than dates:\n%s", out)
	}
}

func TestRefreshChanges(t *testing.T) {
	table := readFixture(t, "table.toml")
	edit := func(_, page string) string {
		return strings.NewReplacer(
			"| gpt-x | $2.00 | $0.20 |", "| gpt-x | $2.20 | $0.20 |", // +10%
			`["kimi-k9", "1M tokens", <>{"$"}3.00</>, <>{"$"}0.30</>, <>{"$"}3.00</>, <>{"$"}15.00</>`,
			`["kimi-k9", "1M tokens", <>{"$"}3.00</>, <>{"$"}0.30</>, <>{"$"}3.00</>, <>{"$"}5.00</>`, // -67%
		).Replace(page)
	}
	out, rep, err := Refresh(context.Background(), []byte(table), pagesFetch(t, edit), day("2026-09-10"))
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Changes) != 2 || len(rep.Review) != 1 || !strings.Contains(rep.Review[0], "kimi-k9 output 15 → 5") {
		t.Fatalf("report %+v", rep)
	}
	got, err := prices.Parse(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range got.Models {
		if e.AsOf != "2026-09-10" {
			t.Errorf("%s as_of %s", e.Name, e.AsOf)
		}
		switch e.Name {
		case "gpt-x":
			if e.Input != 2.2 || e.Cache != 0.2 {
				t.Errorf("gpt-x %+v", e)
			}
		case "kimi-k9":
			if e.Output != 5 {
				t.Errorf("kimi-k9 %+v", e)
			}
		}
	}
	// Comments, labels and aliases survive the rewrite.
	for _, keep := range []string{"# A synthetic table for refresh tests.", "# Kimi entries.", `aliases = ["claude-beta-9.0"]`, `label = "gpt-x-mini (<272K context length)"`} {
		if !strings.Contains(string(out), keep) {
			t.Errorf("rewrite lost %q", keep)
		}
	}
	md := rep.Markdown()
	for _, want := range []string{"| `gpt-x` | input | 2 | 2.2 | +10.0% |", "Needs review", "https://platform.kimi.ai/docs/pricing/chat.md"} {
		if !strings.Contains(md, want) {
			t.Errorf("report lacks %q:\n%s", want, md)
		}
	}
}

func TestGuard(t *testing.T) {
	for _, tt := range []struct {
		old, new float64
		review   bool
	}{
		{10, 15, false}, {10, 5, false}, {10, 15.01, true}, {10, 4.99, true},
		{0, 1, true}, {1, 0, true}, {500, 1001, true}, {1, 1.2, false},
	} {
		if got := guard(tt.old, tt.new) != ""; got != tt.review {
			t.Errorf("guard(%v, %v) review %v, want %v", tt.old, tt.new, got, tt.review)
		}
	}
}

// TestRefreshFailures checks that anything it cannot confirm fails the
// whole refresh and names the cause, so the table is never half-updated.
func TestRefreshFailures(t *testing.T) {
	table := readFixture(t, "table.toml")
	tests := []struct {
		name  string
		table string
		edit  func(url, page string) string
		want  string
	}{
		{"most of a page gone", table, func(u, p string) string {
			if strings.Contains(u, "kimi") {
				return strings.NewReplacer(`"kimi-k9"`, `"kimi-k10"`, `"kimi-k8-code"`, `"kimi-k10-code"`).Replace(p)
			}
			return p
		}, "names or layout probably changed"},
		{"layout changed", table, func(u, p string) string {
			if strings.Contains(u, "kimi") {
				return "# moved"
			}
			return p
		}, "Moonshot AI (Kimi)"},
		{"fetch failed", strings.ReplaceAll(table, "/docs/pricing/chat", "/docs/pricing/gone"), nil, "HTTP 404"},
		{"unknown source", strings.Replace(table, "https://developers.openai.com/api/docs/pricing", "https://example.com/pricing", 1), nil, "no page parser for example.com"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, _, err := Refresh(context.Background(), []byte(tt.table), pagesFetch(t, tt.edit), day("2026-10-01"))
			if err == nil || !strings.Contains(err.Error(), tt.want) || out != nil {
				t.Fatalf("err %v out %d bytes, want an error containing %q and no table", err, len(out), tt.want)
			}
		})
	}
}

func TestRewriteNeedsOneLinePerKey(t *testing.T) {
	table := strings.Replace(readFixture(t, "table.toml"), "input = 0.95\noutput = 4\n", "input = 0.95\noutput = 4\n# input = 1\n", 1)
	rates := map[string]Rates{}
	tbl, err := prices.Parse([]byte(table))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range tbl.Models {
		rates[e.Name] = Rates{e.Input, e.Output, e.Cache}
	}
	if _, err := rewrite([]byte(table), rates, nil, "2026-10-01"); err != nil {
		t.Fatalf("a commented-out key must be left alone: %v", err)
	}
	inline := strings.Replace(readFixture(t, "table.toml"), "input = 3\noutput = 15\n", "input = 3\noutput = 15 # list\n", 1)
	if _, err := rewrite([]byte(inline), rates, nil, "2026-10-01"); err != nil {
		t.Fatalf("trailing comment: %v", err)
	}
}

// TestRefreshRemoves checks that a model whose row is gone or unreadable is
// removed, with the comment that describes it, and flagged for review.
func TestRefreshRemoves(t *testing.T) {
	table := strings.Replace(readFixture(t, "table.toml"), "\n[[model]]\nname = \"gpt-x-codex\"", "\n# Codex note.\n[[model]]\nname = \"gpt-x-codex\"", 1)
	edit := func(_, p string) string {
		return strings.NewReplacer(
			"| gpt-x-codex |", "| gpt-y-codex |",
			"| Claude Alpha 9                                 | $10 / MTok ", "| Claude Alpha 9 | Ask us ",
		).Replace(p)
	}
	out, rep, err := Refresh(context.Background(), []byte(table), pagesFetch(t, edit), day("2026-09-10"))
	if err != nil {
		t.Fatal(err)
	}
	// The renamed codex row is removed under its old name and added under
	// the new one; the removal holds the pull request for review.
	if len(rep.Removed) != 2 || len(rep.Review) != 1 || len(rep.Changes) != 0 || !rep.Changed() ||
		len(rep.Added) != 1 || rep.Added[0].Name != "gpt-y-codex" {
		t.Fatalf("report %+v", rep)
	}
	got, err := prices.Parse(out)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if len(got.Models) != 8 {
		t.Fatalf("%d models left:\n%s", len(got.Models), out)
	}
	s := string(out)
	for _, gone := range []string{"name = \"gpt-x-codex\"", "# Codex note.", "claude-alpha-9"} {
		if strings.Contains(s, gone) {
			t.Errorf("%q survived the removal", gone)
		}
	}
	for _, keep := range []string{"# Anthropic entries.", "# OpenAI entries.", "# Kimi entries.", "name = \"gpt-x-mini\""} {
		if !strings.Contains(s, keep) {
			t.Errorf("removal took %q with it", keep)
		}
	}
	if strings.Contains(s, "\n\n\n") {
		t.Errorf("removal left a double blank line:\n%s", s)
	}
	if md := rep.Markdown(); !strings.Contains(md, "`gpt-x-codex`: no row") || !strings.Contains(md, "Needs review") {
		t.Errorf("report:\n%s", md)
	}
}

// TestRefreshDiscovers checks which new rows become entries: clean prices
// with an ID from a provider page only.
func TestRefreshDiscovers(t *testing.T) {
	table := readFixture(t, "table.toml")
	edit := func(u, p string) string {
		switch {
		case strings.HasSuffix(u, "about-claude/pricing.md"):
			return strings.Replace(p, "| Claude Odd 9 ", strings.Join([]string{
				"| Claude Delta 9 | $3 / MTok | - | - | $0.30 / MTok<sup>2</sup> | $15 / MTok |",
				"| Claude Tier 9 (for prompts up to 100,000 tokens) | $1 / MTok | - | - | $0.10 / MTok | $5 / MTok |",
				"| Claude Tier 9 (for prompts over 100,000 tokens) | $5 / MTok | - | - | $0.50 / MTok | $25 / MTok |",
				"| Claude Odd 9 ",
			}, "\n"), 1)
		case strings.HasSuffix(u, "api/docs/pricing.md"):
			return strings.Replace(p, "| gpt-x-pro |", strings.Join([]string{
				"| gpt-z | $1.00 | $0.10 | - | $8.00 | - | - | - | - |",
				"| gpt-z-big (<272K context length) | $3.00 | $0.30 | - | $20.00 | $6.00 | $0.60 | - | $30.00 |",
				"| gpt-x-old | $9.00 | $0.90 | - | $9.00 | - | - | - | - |",
				"| GPT Fancy Name | $1.00 | $0.10 | - | $2.00 | - | - | - | - |",
				"| gpt-x-pro |",
			}, "\n"), 1)
		case strings.HasSuffix(u, "pricing/chat.md"):
			return strings.Replace(p, `["kimi-k8", "1M tokens"`, `["kimi-k10", "1M tokens", <>{"$"}0.20</>, <>{"$"}1.00</>, <>{"$"}5.00</>, "262,144 tokens"],
["kimi-k8", "1M tokens"`, 1)
		}
		return p
	}
	out, rep, err := Refresh(context.Background(), []byte(table), pagesFetch(t, edit), day("2026-09-10"))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]prices.Entry{
		"claude-delta-9": {Name: "claude-delta-9", Label: "Claude Delta 9", Aliases: []string{"claude-delta-latest"}, Input: 3, Output: 15, Cache: 0.3},
		"gpt-z":          {Name: "gpt-z", Input: 1, Output: 8, Cache: 0.1},
		"gpt-z-big":      {Name: "gpt-z-big", Label: "gpt-z-big (<272K context length)", Input: 3, Output: 20, Cache: 0.3},
		"kimi-k10":       {Name: "kimi-k10", Input: 1, Output: 5, Cache: 0.2},
	}
	if len(rep.Added) != len(want) || len(rep.Review) != 0 {
		t.Fatalf("added %+v review %v", rep.Added, rep.Review)
	}
	got, err := prices.Parse(out)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	byName := map[string]prices.Entry{}
	for _, e := range got.Models {
		byName[e.Name] = e
		if e.AsOf != "2026-09-10" {
			t.Errorf("%s as_of %s", e.Name, e.AsOf)
		}
	}
	for name, w := range want {
		g, ok := byName[name]
		if !ok || g.Label != w.Label || strings.Join(g.Aliases, ",") != strings.Join(w.Aliases, ",") ||
			g.Input != w.Input || g.Output != w.Output || g.Cache != w.Cache || g.Source == "" {
			t.Errorf("%s: got %+v (found %v), want %+v", name, g, ok, w)
		}
	}
	// Never added: tiered prices, no model ID, an excluded model, and rows
	// without a readable price.
	for _, name := range []string{"claude-tier-9", "claude-odd-9", "gpt-x-old", "gpt-x-pro", "moderation-latest", "GPT Fancy Name"} {
		if _, ok := byName[name]; ok {
			t.Errorf("%s was added", name)
		}
	}
	md := rep.Markdown()
	for _, w := range []string{"`claude-tier-9`: listed in several rows", `"Claude Odd 9": no model ID`, `"gpt-x-pro": price not readable`, `"GPT Fancy Name": "GPT Fancy Name" is not a model ID`, "| `kimi-k10` | 1 | 5 | 0.2 | kimi-k10 |"} {
		if !strings.Contains(md, w) {
			t.Errorf("report lacks %q:\n%s", w, md)
		}
	}
	if strings.Contains(md, "gpt-x-old") {
		t.Error("report mentions an excluded model")
	}
	// New entries land in their provider's section.
	s := string(out)
	if !(strings.Index(s, `name = "claude-delta-9"`) < strings.Index(s, "# OpenAI entries.") &&
		strings.Index(s, `name = "gpt-z"`) < strings.Index(s, "# Kimi entries.") &&
		strings.Index(s, `name = "kimi-k10"`) > strings.Index(s, "# Kimi entries.")) {
		t.Errorf("new entries out of place:\n%s", s)
	}

	// A second refresh over the result finds nothing new.
	again, rep2, err := Refresh(context.Background(), out, pagesFetch(t, edit), day("2026-09-11"))
	if err != nil || rep2.Changed() || string(again) != s {
		t.Fatalf("second refresh: err %v report %+v", err, rep2)
	}
}

func TestRefreshAddingManyNeedsReview(t *testing.T) {
	var rows []string
	for i := range MaxAdded + 1 {
		rows = append(rows, fmt.Sprintf(`["kimi-n%d", "1M tokens", <>{"$"}0.10</>, <>{"$"}1.00</>, <>{"$"}2.00</>, "1 tokens"],`, i))
	}
	edit := func(u, p string) string {
		if strings.HasSuffix(u, "pricing/chat.md") {
			return strings.Replace(p, `["kimi-k8", "1M tokens"`, strings.Join(rows, "\n")+"\n"+`["kimi-k8", "1M tokens"`, 1)
		}
		return p
	}
	_, rep, err := Refresh(context.Background(), []byte(readFixture(t, "table.toml")), pagesFetch(t, edit), day("2026-09-10"))
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Added) != MaxAdded+1 || len(rep.Review) != 1 || !strings.Contains(rep.Review[0], "added at once") {
		t.Fatalf("added %d review %v", len(rep.Added), rep.Review)
	}
}

func TestParseAnthropicIDs(t *testing.T) {
	got, err := parseAnthropicIDs(readFixture(t, "anthropic-models.md"))
	if err != nil {
		t.Fatal(err)
	}
	if got["Claude Delta 9"] != (modelID{ID: "claude-delta-9", Alias: "claude-delta-latest"}) || got["Claude Alpha 9"] != (modelID{ID: "claude-alpha-9"}) {
		t.Fatalf("ids %+v", got)
	}
	if _, err := parseAnthropicIDs(strings.Replace(readFixture(t, "anthropic-models.md"), "Claude API ID", "API model", 1)); err == nil {
		t.Fatal("missing ID row should fail")
	}
}
