package api

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DanBradbury/firekeeper/internal/server/store"
	"github.com/DanBradbury/firekeeper/internal/transcript"
)

type usagePage struct {
	From    time.Time        `json:"from"`
	To      time.Time        `json:"to"`
	GroupBy []string         `json:"group_by"`
	Priced  bool             `json:"priced"`
	Rows    []store.UsageRow `json:"rows"`
	Totals  struct {
		Tokens         transcript.Tokens `json:"tokens"`
		Cost           *float64          `json:"cost"`
		UnpricedTokens int64             `json:"unpriced_tokens"`
	} `json:"totals"`
}

func ts(s string) *time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return &t
}

func strp(s string) *string { return &s }

// usageFixture stores two sessions whose sums are worked out by hand in
// TestUsageMatchesHandComputedFixture.
func usageFixture(t *testing.T, s *store.Store) {
	t.Helper()
	ev := func(seq int64, at *time.Time, model *string, in, out, cache int64) transcript.Event {
		return transcript.Event{Seq: seq, TS: at, Role: transcript.RoleAssistant, Model: model,
			Tokens: transcript.Tokens{Input: in, Output: out, Cache: cache}, Raw: json.RawMessage(`{}`)}
	}
	alpha := store.SessionBatch{
		Meta: store.SessionMeta{SessionID: "s1", Provider: "codex", Project: "alpha", Model: "gpt-5"},
		Events: []transcript.Event{
			ev(0, ts("2026-10-01T10:00:00Z"), nil, 0, 0, 0),
			ev(1, ts("2026-10-01T10:01:00Z"), strp("gpt-5"), 100, 10, 50),
			ev(2, ts("2026-10-02T23:59:59Z"), nil, 200, 20, 0), // session model
			ev(3, ts("2026-10-02T00:00:00Z"), strp("o3"), 1000, 100, 0),
			ev(4, nil, strp("gpt-5"), 5, 5, 5),                        // no timestamp: excluded
			ev(5, ts("2026-10-05T00:00:00Z"), strp("gpt-5"), 7, 0, 0), // after "to"
			ev(6, ts("2026-09-30T23:59:59Z"), strp("gpt-5"), 9, 0, 0), // before "from"
		},
	}
	for i := range alpha.Events {
		alpha.Events[i].Provider = transcript.ProviderCodex
	}
	beta := store.SessionBatch{
		Meta: store.SessionMeta{SessionID: "s2", Provider: "claude", Project: "beta"},
		Events: []transcript.Event{
			ev(0, ts("2026-10-03T12:00:00Z"), strp("claude-opus"), 300, 30, 30),
		},
	}
	beta.Events[0].Provider = transcript.ProviderClaude
	ctx := context.Background()
	if _, _, err := s.Ingest(ctx, store.DefaultAccountID, store.Machine{ID: "m1"}, []store.SessionBatch{alpha}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Ingest(ctx, store.DefaultAccountID, store.Machine{ID: "m2"}, []store.SessionBatch{beta}); err != nil {
		t.Fatal(err)
	}
}

var fixturePrices = map[string]store.Price{
	"gpt-5":       {Input: 1, Output: 10, Cache: 0.1},
	"claude-opus": {Input: 15, Output: 75, Cache: 1.5},
}

func near(a *float64, b float64) bool { return a != nil && math.Abs(*a-b) < 1e-12 }

func TestUsageMatchesHandComputedFixture(t *testing.T) {
	s := newStore(t)
	usageFixture(t, s)
	h := Handler(s, WithPrices(fixturePrices))

	var page usagePage
	if code := get(t, h, "/v1/usage?from=2026-10-01&to=2026-10-04&group_by=project", &page); code != 200 {
		t.Fatalf("status %d", code)
	}
	if !page.Priced || !page.From.Equal(*ts("2026-10-01T00:00:00Z")) || !page.To.Equal(*ts("2026-10-05T00:00:00Z")) {
		t.Fatalf("range %v..%v priced %v", page.From, page.To, page.Priced)
	}
	// alpha: gpt-5 {300,30,50} + o3 {1000,100,0}; cost 300*1 + 30*10 + 50*0.1
	// = 605 per million, with o3's 1,100 tokens unpriced.
	// beta: claude-opus {300,30,30}; cost 300*15 + 30*75 + 30*1.5 = 6795.
	if len(page.Rows) != 2 {
		t.Fatalf("rows %+v", page.Rows)
	}
	a, b := page.Rows[0], page.Rows[1]
	if a.Group["project"] != "alpha" || a.Tokens != (transcript.Tokens{Input: 1300, Output: 130, Cache: 50}) ||
		!near(a.Cost, 605e-6) || a.UnpricedTokens != 1100 {
		t.Errorf("alpha row %+v cost %v", a, a.Cost)
	}
	if b.Group["project"] != "beta" || b.Tokens != (transcript.Tokens{Input: 300, Output: 30, Cache: 30}) ||
		!near(b.Cost, 6795e-6) || b.UnpricedTokens != 0 {
		t.Errorf("beta row %+v cost %v", b, b.Cost)
	}
	if page.Totals.Tokens != (transcript.Tokens{Input: 1600, Output: 160, Cache: 80}) ||
		!near(page.Totals.Cost, 7400e-6) || page.Totals.UnpricedTokens != 1100 {
		t.Errorf("totals %+v cost %v", page.Totals, page.Totals.Cost)
	}

	page = usagePage{}
	get(t, h, "/v1/usage?from=2026-10-01&to=2026-10-04&group_by=day,model", &page)
	type key struct{ day, model string }
	want := []struct {
		key
		tok transcript.Tokens
	}{
		{key{"2026-10-01", "gpt-5"}, transcript.Tokens{Input: 100, Output: 10, Cache: 50}},
		{key{"2026-10-02", "o3"}, transcript.Tokens{Input: 1000, Output: 100}},
		{key{"2026-10-02", "gpt-5"}, transcript.Tokens{Input: 200, Output: 20}},
		{key{"2026-10-03", "claude-opus"}, transcript.Tokens{Input: 300, Output: 30, Cache: 30}},
	}
	if len(page.Rows) != len(want) {
		t.Fatalf("day,model rows %+v", page.Rows)
	}
	for i, w := range want {
		r := page.Rows[i]
		if (key{r.Group["day"], r.Group["model"]}) != w.key || r.Tokens != w.tok {
			t.Errorf("row %d = %v %+v, want %v %+v", i, r.Group, r.Tokens, w.key, w.tok)
		}
	}

	for _, tc := range []struct {
		group string
		want  map[string]int64 // group value -> total tokens
	}{
		{"provider", map[string]int64{"codex": 1480, "claude": 360}},
		{"machine", map[string]int64{"m1": 1480, "m2": 360}},
		{"model", map[string]int64{"gpt-5": 380, "o3": 1100, "claude-opus": 360}},
	} {
		page = usagePage{}
		get(t, h, "/v1/usage?from=2026-10-01&to=2026-10-04&group_by="+tc.group, &page)
		got := map[string]int64{}
		for _, r := range page.Rows {
			got[r.Group[tc.group]] = r.Tokens.Input + r.Tokens.Output + r.Tokens.Cache
		}
		if len(got) != len(tc.want) {
			t.Errorf("%s: got %v, want %v", tc.group, got, tc.want)
		}
		for k, v := range tc.want {
			if got[k] != v {
				t.Errorf("%s %s: %d, want %d", tc.group, k, got[k], v)
			}
		}
	}
}

func TestUsageWithoutPricesHasNoCost(t *testing.T) {
	s := newStore(t)
	usageFixture(t, s)
	h := Handler(s)
	rr := serveGet(h, "/v1/usage?from=2026-10-01&to=2026-10-04&group_by=project")
	var raw map[string]any
	json.Unmarshal(rr.Body.Bytes(), &raw)
	if raw["priced"] != false {
		t.Fatalf("priced = %v", raw["priced"])
	}
	for _, r := range raw["rows"].([]any) {
		if _, ok := r.(map[string]any)["cost"]; ok {
			t.Fatalf("row has cost without prices: %v", r)
		}
	}
	if _, ok := raw["totals"].(map[string]any)["cost"]; ok {
		t.Fatal("totals have cost without prices")
	}
}

func TestUsageEmptyRange(t *testing.T) {
	s := newStore(t)
	usageFixture(t, s)
	h := Handler(s, WithPrices(fixturePrices))
	var page usagePage
	if code := get(t, h, "/v1/usage?from=2025-01-01&to=2025-01-31&group_by=day,model", &page); code != 200 {
		t.Fatalf("status %d", code)
	}
	if page.Rows == nil || len(page.Rows) != 0 || page.Totals.Tokens != (transcript.Tokens{}) || !near(page.Totals.Cost, 0) {
		t.Fatalf("empty range: %+v", page)
	}
	rr := serveGet(h, "/v1/usage?from=2025-01-01&to=2025-01-31")
	if want := `"rows":[]`; !json.Valid(rr.Body.Bytes()) || !strings.Contains(rr.Body.String(), want) {
		t.Fatalf("body %s lacks %s", rr.Body, want)
	}
}

func TestUsageDefaultsAndErrors(t *testing.T) {
	h := Handler(newStore(t))
	var page usagePage
	if code := get(t, h, "/v1/usage", &page); code != 200 {
		t.Fatalf("default: %d", code)
	}
	if d := page.To.Sub(page.From); d != DefaultUsageDays*24*time.Hour {
		t.Errorf("default range %v", d)
	}
	if !page.To.After(time.Now()) || len(page.GroupBy) != 0 {
		t.Errorf("default to %v group_by %v", page.To, page.GroupBy)
	}
	for _, q := range []string{
		"from=yesterday",
		"to=2026-13-01",
		"from=2026-10-05&to=2026-10-01",
		"from=2024-01-01&to=2026-01-01",
		"group_by=week",
		"group_by=day,,model",
		"group_by=day,day",
	} {
		if code := get(t, h, "/v1/usage?"+q, nil); code != 400 {
			t.Errorf("%s: status %d, want 400", q, code)
		}
	}
	var rfc usagePage
	if code := get(t, h, "/v1/usage?from=2026-10-01T06:00:00%2B02:00&to=2026-10-01T12:00:00Z", &rfc); code != 200 ||
		!rfc.From.Equal(*ts("2026-10-01T04:00:00Z")) {
		t.Errorf("rfc3339 bounds: %d %v", code, rfc.From)
	}
}

// TestUsageNinetyDays checks the page's largest default range stays a
// bounded, correct aggregation.
func TestUsageNinetyDays(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	start := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	models := []string{"gpt-5", "o3", "claude-opus", "kimi-k2"}
	const perDay = 40
	var wantTotal int64
	for d := 0; d < 90; d += 10 {
		b := store.SessionBatch{Meta: store.SessionMeta{SessionID: start.AddDate(0, 0, d).Format("s-2006-01-02"), Provider: "codex", Project: "p"}}
		for dd := d; dd < d+10; dd++ {
			for i := 0; i < perDay; i++ {
				at := start.AddDate(0, 0, dd).Add(time.Duration(i) * 30 * time.Minute)
				b.Events = append(b.Events, transcript.Event{
					Provider: transcript.ProviderCodex, Seq: int64(len(b.Events)), TS: &at, Role: transcript.RoleAssistant,
					Model: strp(models[i%len(models)]), Tokens: transcript.Tokens{Input: 10, Output: 2, Cache: 1}, Raw: json.RawMessage(`{}`),
				})
				wantTotal += 13
			}
		}
		for i := 0; i < len(b.Events); i += MaxIngestEvents {
			part := b
			part.Events = b.Events[i:min(i+MaxIngestEvents, len(b.Events))]
			if _, _, err := s.Ingest(ctx, store.DefaultAccountID, store.Machine{ID: "m"}, []store.SessionBatch{part}); err != nil {
				t.Fatal(err)
			}
		}
	}
	h := Handler(s)
	var page usagePage
	if code := get(t, h, "/v1/usage?from=2026-07-01&to=2026-09-28&group_by=day,model", &page); code != 200 {
		t.Fatalf("status %d", code)
	}
	if len(page.Rows) != 90*len(models) {
		t.Fatalf("rows %d, want %d", len(page.Rows), 90*len(models))
	}
	tot := page.Totals.Tokens
	if got := tot.Input + tot.Output + tot.Cache; got != wantTotal {
		t.Fatalf("total %d, want %d", got, wantTotal)
	}
	if page.Rows[0].Group["day"] != "2026-07-01" || page.Rows[len(page.Rows)-1].Group["day"] != "2026-09-28" {
		t.Fatalf("day order %v .. %v", page.Rows[0].Group, page.Rows[len(page.Rows)-1].Group)
	}
}

func serveGet(h http.Handler, path string) *httptest.ResponseRecorder {
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", path, nil))
	return rr
}
