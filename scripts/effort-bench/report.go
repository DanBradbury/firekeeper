package main

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"math"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Result is one run. It is the store's record format too.
type Result struct {
	Harness         string     `json:"harness"`
	Batch           string     `json:"batch"`
	PlanIndex       int        `json:"plan_index"`
	Rep             int        `json:"rep"`
	Effort          string     `json:"effort"`
	Kind            string     `json:"kind"`
	PromptID        string     `json:"prompt_id"`
	ModelRequested  string     `json:"model_requested,omitempty"`
	Model           string     `json:"model"`           // as the harness recorded it
	EffortRecorded  string     `json:"effort_recorded"` // as the harness recorded it
	SessionID       string     `json:"session_id"`
	Version         string     `json:"harness_version,omitempty"`
	StartedAt       time.Time  `json:"started_at"`
	EndedAt         time.Time  `json:"ended_at"`
	WallSeconds     float64    `json:"wall_seconds"`
	Usage           Usage      `json:"usage"`
	FinalTotal      *int64     `json:"final_total_tokens,omitempty"`
	Requests        []Request  `json:"requests"`
	ToolCalls       int        `json:"tool_calls"`
	UsedPctStart    *float64   `json:"window_used_percent_start"`
	UsedPctEnd      *float64   `json:"window_used_percent_end"`
	ResetsAt        *time.Time `json:"window_resets_at,omitempty"`
	TestsPass       *bool      `json:"tests_pass"`
	FirekeeperTotal *int64     `json:"firekeeper_total,omitempty"`
	NotLogged       []string   `json:"not_logged,omitempty"`
	Error           string     `json:"error,omitempty"`

	// Derived when a report is built; not stored.
	GapPrevSec       *float64 `json:"gap_since_previous_run_seconds,omitempty"`
	GapSameEffortSec *float64 `json:"gap_since_previous_same_effort_seconds,omitempty"`
}

func (r *Result) fill(d RunData) {
	r.SessionID = d.SessionID
	r.Version = d.Version
	r.Model = strings.Join(d.Models, "+")
	r.EffortRecorded = strings.Join(d.Efforts, "+")
	r.Requests = d.Requests
	r.Usage = d.Sum()
	if d.Final != nil {
		t := d.Final.Total
		r.FinalTotal = &t
	}
	r.ToolCalls = d.ToolCalls
	r.UsedPctStart, r.UsedPctEnd, r.ResetsAt = d.UsedPercentStart, d.UsedPercentEnd, d.ResetsAt
}

func (r Result) ok() bool { return r.Error == "" && r.SessionID != "" && len(r.Requests) > 0 }

func (r Result) logs(field string) bool {
	for _, f := range r.NotLogged {
		if f == field {
			return false
		}
	}
	return true
}

// Fresh is ordinary input: neither read from nor written to the cache.
// OpenAI's input_tokens is ordinary + cached + cache-write tokens, so
// Fresh + Cached + CacheWrite + Output == Total.
func (r Result) Fresh() int64 { return max(r.Usage.Input-r.Usage.Cached-r.Usage.CacheWrite, 0) }

// CacheHitPct is cache reads over all input.
func (r Result) CacheHitPct() float64 { return pct(r.Usage.Cached, r.Usage.Input) }

// CacheState says whether the first request already found a cached prefix.
func (r Result) CacheState() string {
	if len(r.Requests) == 0 {
		return ""
	}
	if r.Requests[0].Cached == 0 {
		return "cold"
	}
	return "warm"
}

// FirstCachedPct is the first request's cache-hit rate.
func (r Result) FirstCachedPct() float64 {
	if len(r.Requests) == 0 {
		return 0
	}
	return pct(r.Requests[0].Cached, r.Requests[0].Input)
}

// LaterMisses lists the 1-based request numbers after the first whose
// input was less than half cached.
func (r Result) LaterMisses() []int {
	var out []int
	for i, q := range r.Requests {
		if i > 0 && q.Input > 0 && 2*q.Cached < q.Input {
			out = append(out, i+1)
		}
	}
	return out
}

func (r Result) firstAt() time.Time {
	if len(r.Requests) > 0 && !r.Requests[0].At.IsZero() {
		return r.Requests[0].At
	}
	return r.StartedAt
}

func (r Result) lastAt() time.Time {
	if n := len(r.Requests); n > 0 && !r.Requests[n-1].At.IsZero() {
		return r.Requests[n-1].At
	}
	return r.EndedAt
}

func pct(a, b int64) float64 {
	if b == 0 {
		return 0
	}
	return 100 * float64(a) / float64(b)
}

// Row is one effort's statistics within a group.
type Row struct {
	Effort      string   `json:"effort"`
	N           int      `json:"n"`
	CacheHit    Summary  `json:"cache_hit_percent"`
	Total       Summary  `json:"total_tokens"`
	Output      Summary  `json:"output_tokens"`
	Reasoning   *Summary `json:"reasoning_tokens"` // nil when not logged
	Wall        Summary  `json:"wall_seconds"`
	Fresh       float64  `json:"fresh_input_mean"`
	Cached      float64  `json:"cache_read_mean"`
	CacheWrite  *float64 `json:"cache_write_mean"` // nil when not logged
	Requests    float64  `json:"requests_mean"`
	FirstCached float64  `json:"first_request_cached_percent_mean"`
	Passed      int      `json:"tests_passed"`
	Cold        int      `json:"cold_starts"`
	LaterMiss   int      `json:"runs_with_later_miss"`
	WindowDelta *float64 `json:"window_used_percent_delta_mean,omitempty"`
}

// Pair is one pairwise comparison between efforts.
type Pair struct {
	Metric     string  `json:"metric"`
	A          string  `json:"a"`
	B          string  `json:"b"`
	NA         int     `json:"n_a"`
	NB         int     `json:"n_b"`
	Diff       float64 `json:"mean_diff_b_minus_a"`
	DiffLo     float64 `json:"diff_ci95_lo"`
	DiffHi     float64 `json:"diff_ci95_hi"`
	U          float64 `json:"u"`
	P          float64 `json:"p"`
	PHolm      float64 `json:"p_holm"`
	Sufficient bool    `json:"sufficient_n"`
}

// ReqRow is the cache picture at one request number for one effort.
type ReqRow struct {
	Effort      string  `json:"effort"`
	Index       string  `json:"request"`
	Runs        int     `json:"runs"`
	InputMean   float64 `json:"input_mean"`
	CachedShare float64 `json:"cached_percent"`
	// UncachedShare is this request number's share of all the effort's
	// uncached input (fresh + cache write).
	UncachedShare float64 `json:"share_of_uncached_percent"`
}

// GapBin groups runs by the time since the previous run.
type GapBin struct {
	Label       string  `json:"gap"`
	N           int     `json:"n"`
	CacheHit    float64 `json:"cache_hit_percent_mean"`
	FirstCached float64 `json:"first_request_cached_percent_mean"`
	Cold        int     `json:"cold_starts"`
}

// Group is one harness, model and kind.
type Group struct {
	Harness    string   `json:"harness"`
	Model      string   `json:"model"`
	Kind       string   `json:"kind"`
	NotLogged  []string `json:"not_logged,omitempty"`
	Rows       []Row    `json:"efforts"`
	Pairs      []Pair   `json:"pairwise"`
	PerRequest []ReqRow `json:"per_request"`
	GapBins    []GapBin `json:"by_gap"`
	HourBins   []GapBin `json:"by_local_time"`
	Small      []string `json:"too_few_runs,omitempty"`
}

// Report is everything the benchmark writes.
type Report struct {
	GeneratedAt time.Time `json:"generated_at"`
	Batches     []string  `json:"batches"`
	Groups      []Group   `json:"groups"`
	Runs        []Result  `json:"runs"`
	Notes       []string  `json:"notes"`
}

const maxReqIndex = 10

var gapEdges = []struct {
	label string
	upTo  float64 // seconds
}{{"< 1 min", 60}, {"1–5 min", 300}, {"5–30 min", 1800}, {"30 min–2 h", 7200}, {"> 2 h", math.Inf(1)}}

func buildReport(runs []Result, seed uint64) Report {
	rep := Report{GeneratedAt: time.Now().UTC(), Runs: runs, Notes: []string{}}
	batches := map[string]bool{}
	prompts := map[string]bool{}
	for _, r := range runs {
		batches[r.Batch] = true
		if r.PromptID != "" {
			prompts[r.PromptID] = true
		}
	}
	rep.Batches = sortedKeys(batches)
	if len(prompts) > 1 {
		rep.Notes = append(rep.Notes, "the runs used different prompts ("+strings.Join(sortedKeys(prompts), ", ")+"); filter with --batch")
	}
	deriveGaps(rep.Runs)

	type gkey struct{ h, m, k string }
	groups := map[gkey][]Result{}
	var order []gkey
	for _, r := range rep.Runs {
		switch {
		case !r.ok():
			if r.Error != "" {
				rep.Notes = append(rep.Notes, fmt.Sprintf("%s run %s/%d (%s) not counted: %s", r.Harness, r.Batch, r.PlanIndex+1, r.Effort, r.Error))
			}
			continue
		case r.EffortRecorded != "" && r.EffortRecorded != r.Effort:
			rep.Notes = append(rep.Notes, fmt.Sprintf("%s run %s/%d asked for effort %s but recorded %s", r.Harness, r.Batch, r.PlanIndex+1, r.Effort, r.EffortRecorded))
		case r.FinalTotal != nil && *r.FinalTotal != r.Usage.Total:
			rep.Notes = append(rep.Notes, fmt.Sprintf("%s run %s/%d: summed per-request usage (%d) differs from the harness total (%d)", r.Harness, r.Batch, r.PlanIndex+1, r.Usage.Total, *r.FinalTotal))
		case r.FirekeeperTotal != nil && *r.FirekeeperTotal != r.Usage.Total:
			rep.Notes = append(rep.Notes, fmt.Sprintf("%s run %s/%d: firekeeper window total %d differs from %d", r.Harness, r.Batch, r.PlanIndex+1, *r.FirekeeperTotal, r.Usage.Total))
		}
		k := gkey{r.Harness, emptyOr(r.Model, r.ModelRequested), r.Kind}
		if _, seen := groups[k]; !seen {
			order = append(order, k)
		}
		groups[k] = append(groups[k], r)
	}
	matched, checked := 0, 0
	for _, r := range rep.Runs {
		if r.ok() && r.FirekeeperTotal != nil {
			checked++
			if *r.FirekeeperTotal == r.Usage.Total {
				matched++
			}
		}
	}
	if checked > 0 {
		rep.Notes = append(rep.Notes, fmt.Sprintf("firekeeper window cross-check: %d of %d checked runs match", matched, checked))
	}
	sort.SliceStable(order, func(i, j int) bool {
		a, b := order[i], order[j]
		if a.k != b.k {
			return a.k == "run"
		}
		if a.h != b.h {
			return a.h < b.h
		}
		return a.m < b.m
	})
	for gi, k := range order {
		rep.Groups = append(rep.Groups, buildGroup(k.h, k.m, k.k, groups[k], seed+uint64(gi)*1000))
	}
	return rep
}

// deriveGaps sets, for each run, the time from the previous run's last
// request (same harness and model, any effort: the same prompt prefix) and
// from the previous run of the same effort to this run's start.
func deriveGaps(runs []Result) {
	idx := make([]int, 0, len(runs))
	for i, r := range runs {
		if r.ok() {
			idx = append(idx, i)
		}
	}
	sort.SliceStable(idx, func(a, b int) bool { return runs[idx[a]].StartedAt.Before(runs[idx[b]].StartedAt) })
	last := map[string]time.Time{}
	lastEffort := map[string]time.Time{}
	for _, i := range idx {
		r := &runs[i]
		key := r.Harness + "\x00" + emptyOr(r.Model, r.ModelRequested)
		ekey := key + "\x00" + r.Effort
		if t, ok := last[key]; ok {
			g := max(0, r.StartedAt.Sub(t).Seconds())
			r.GapPrevSec = &g
		}
		if t, ok := lastEffort[ekey]; ok {
			g := max(0, r.StartedAt.Sub(t).Seconds())
			r.GapSameEffortSec = &g
		}
		last[key] = r.lastAt()
		lastEffort[ekey] = r.lastAt()
	}
}

func buildGroup(harness, model, kind string, runs []Result, seed uint64) Group {
	g := Group{Harness: harness, Model: model, Kind: kind, NotLogged: runs[0].NotLogged}
	byEffort := map[string][]Result{}
	var efforts []string
	for _, r := range runs {
		if _, ok := byEffort[r.Effort]; !ok {
			efforts = append(efforts, r.Effort)
		}
		byEffort[r.Effort] = append(byEffort[r.Effort], r)
	}
	sort.SliceStable(efforts, func(i, j int) bool { return effortRank(efforts[i]) < effortRank(efforts[j]) })
	metric := func(rs []Result, f func(Result) float64) []float64 {
		out := make([]float64, len(rs))
		for i, r := range rs {
			out[i] = f(r)
		}
		return out
	}
	hit := func(r Result) float64 { return r.CacheHitPct() }
	total := func(r Result) float64 { return float64(r.Usage.Total) }
	for ei, e := range efforts {
		rs := byEffort[e]
		s := seed + uint64(ei)*10
		row := Row{
			Effort: e, N: len(rs),
			CacheHit: summarize(metric(rs, hit), s+1),
			Total:    summarize(metric(rs, total), s+2),
			Output:   summarize(metric(rs, func(r Result) float64 { return float64(r.Usage.Output) }), s+3),
			Wall:     summarize(metric(rs, func(r Result) float64 { return r.WallSeconds }), s+5),
		}
		if rs[0].logs(fieldReasoning) {
			sum := summarize(metric(rs, func(r Result) float64 { return float64(r.Usage.Reasoning) }), s+4)
			row.Reasoning = &sum
		}
		row.Fresh = mean(metric(rs, func(r Result) float64 { return float64(r.Fresh()) }))
		row.Cached = mean(metric(rs, func(r Result) float64 { return float64(r.Usage.Cached) }))
		if rs[0].logs(fieldCacheWrite) {
			w := mean(metric(rs, func(r Result) float64 { return float64(r.Usage.CacheWrite) }))
			row.CacheWrite = &w
		}
		row.Requests = mean(metric(rs, func(r Result) float64 { return float64(len(r.Requests)) }))
		row.FirstCached = mean(metric(rs, Result.FirstCachedPct))
		var delta float64
		haveDelta := rs[0].logs(fieldWindow)
		for _, r := range rs {
			if r.TestsPass != nil && *r.TestsPass {
				row.Passed++
			}
			if r.CacheState() == "cold" {
				row.Cold++
			}
			if len(r.LaterMisses()) > 0 {
				row.LaterMiss++
			}
			if r.UsedPctStart != nil && r.UsedPctEnd != nil {
				delta += *r.UsedPctEnd - *r.UsedPctStart
			} else {
				haveDelta = false
			}
		}
		if haveDelta {
			d := delta / float64(len(rs))
			row.WindowDelta = &d
		}
		if row.N < minCI {
			g.Small = append(g.Small, fmt.Sprintf("%s (n=%d)", e, row.N))
		}
		g.Rows = append(g.Rows, row)
		g.PerRequest = append(g.PerRequest, perRequest(e, rs)...)
	}

	// Pairwise tests, Holm-corrected within each metric.
	for mi, m := range []struct {
		name string
		f    func(Result) float64
	}{{"cache_hit_percent", hit}, {"total_tokens", total}} {
		var pairs []Pair
		for i := 0; i < len(efforts); i++ {
			for j := i + 1; j < len(efforts); j++ {
				a, b := metric(byEffort[efforts[i]], m.f), metric(byEffort[efforts[j]], m.f)
				p := Pair{Metric: m.name, A: efforts[i], B: efforts[j], NA: len(a), NB: len(b), Diff: mean(b) - mean(a),
					Sufficient: len(a) >= minTest && len(b) >= minTest}
				u := mannWhitney(a, b)
				p.U, p.P = math.Min(u.U1, u.U2), u.P
				p.DiffLo, p.DiffHi = bootstrapDiffCI(a, b, bootstrapB, seed+uint64(100*mi+10*i+j))
				pairs = append(pairs, p)
			}
		}
		ps := make([]float64, len(pairs))
		for i, p := range pairs {
			ps[i] = p.P
		}
		for i, adj := range holm(ps) {
			pairs[i].PHolm = adj
		}
		g.Pairs = append(g.Pairs, pairs...)
	}

	// Cache-hit against the gap since the previous run, and by local time.
	g.GapBins = make([]GapBin, len(gapEdges)+1)
	g.GapBins[0].Label = "first run"
	for i, e := range gapEdges {
		g.GapBins[i+1].Label = e.label
	}
	hours := []string{"00–06", "06–12", "12–18", "18–24"}
	g.HourBins = make([]GapBin, len(hours))
	for i, h := range hours {
		g.HourBins[i].Label = h
	}
	addBin := func(b *GapBin, r Result) {
		b.N++
		b.CacheHit += r.CacheHitPct()
		b.FirstCached += r.FirstCachedPct()
		if r.CacheState() == "cold" {
			b.Cold++
		}
	}
	for _, r := range runs {
		bin := 0
		if r.GapPrevSec != nil {
			for i, e := range gapEdges {
				if *r.GapPrevSec < e.upTo {
					bin = i + 1
					break
				}
			}
		}
		addBin(&g.GapBins[bin], r)
		addBin(&g.HourBins[r.StartedAt.Local().Hour()/6], r)
	}
	for _, bins := range [][]GapBin{g.GapBins, g.HourBins} {
		for i := range bins {
			if bins[i].N > 0 {
				bins[i].CacheHit /= float64(bins[i].N)
				bins[i].FirstCached /= float64(bins[i].N)
			}
		}
	}
	return g
}

func perRequest(effort string, rs []Result) []ReqRow {
	type acc struct {
		runs                  int
		input, cached, uncach int64
	}
	accs := make([]acc, maxReqIndex+1)
	var allUncached int64
	for _, r := range rs {
		for i, q := range r.Requests {
			k := min(i, maxReqIndex)
			accs[k].runs++
			accs[k].input += q.Input
			accs[k].cached += q.Cached
			u := max(q.Input-q.Cached, 0)
			accs[k].uncach += u
			allUncached += u
		}
	}
	var out []ReqRow
	for k, a := range accs {
		if a.runs == 0 {
			continue
		}
		label := strconv.Itoa(k + 1)
		if k == maxReqIndex {
			label = strconv.Itoa(maxReqIndex+1) + "+"
		}
		out = append(out, ReqRow{
			Effort: effort, Index: label, Runs: a.runs, InputMean: float64(a.input) / float64(a.runs),
			CachedShare: pct(a.cached, a.input), UncachedShare: pct(a.uncach, allUncached),
		})
	}
	return out
}

// effortRank orders known efforts from least to most reasoning.
func effortRank(e string) int {
	order := []string{"none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra"}
	for i, o := range order {
		if o == e {
			return i
		}
	}
	return len(order)
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// crossCheck asks `firekeeper window --provider <p> --json` for the run's
// session and returns Firekeeper's total. Only that session is read from the
// output; nothing else is kept.
func crossCheck(o options, h harness, sessionID string) *int64 {
	if o.firekeeper == "off" || sessionID == "" {
		return nil
	}
	bin := o.firekeeper
	if bin == "" {
		var err error
		if bin, err = exec.LookPath("firekeeper"); err != nil {
			return nil
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "window", "--provider", h.Provider(), "--json")
	cmd.Env = append(append(os.Environ(), "FIREKEEPER_NO_UPDATE_CHECK=1", "FIREKEEPER_NO_PROMPT=1"), h.Env(o)...)
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	totals, err := firekeeperTotals(out)
	if err != nil {
		return nil
	}
	if t, ok := totals[sessionID]; ok {
		return &t
	}
	return nil
}

// firekeeperTotals maps session id to total tokens from `window --json`.
func firekeeperTotals(b []byte) (map[string]int64, error) {
	var w struct {
		Sessions []struct {
			SessionID string `json:"session_id"`
			Tokens    struct {
				Total int64 `json:"total"`
			} `json:"tokens"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(b, &w); err != nil {
		return nil, err
	}
	m := make(map[string]int64, len(w.Sessions))
	for _, s := range w.Sessions {
		m[s.SessionID] = s.Tokens.Total
	}
	return m, nil
}

// redactor scrubs paths, the user name and the host name from output.
type redactor struct {
	off   bool
	pairs []string         // path prefixes and their replacements
	words []*regexp.Regexp // user and host names, matched as whole words
	wordR []string
}

func newRedactor(o options) *redactor {
	r := &redactor{off: o.noRedact}
	add := func(from, to string) {
		if from != "" && from != "/" && from != "." {
			r.pairs = append(r.pairs, from, to)
		}
	}
	// Longest first, so the repo path wins over the workdir and home.
	if o.workdir != "" {
		add(filepath.Join(o.workdir, "bench-repo"), "<bench-repo>")
	}
	add(o.out, "<report>")
	add(o.store, "<store>")
	add(o.workdir, "<workdir>")
	add(o.codexHome, "<codex-home>")
	add(os.TempDir(), "<tmp>")
	if home, err := os.UserHomeDir(); err == nil {
		add(home, "~")
	}
	if u, err := user.Current(); err == nil {
		r.word(filepath.Base(u.Username), "<user>") // DOMAIN\user on Windows
	}
	if h, err := os.Hostname(); err == nil {
		r.word(h, "<host>")
		r.word(strings.SplitN(h, ".", 2)[0], "<host>")
	}
	return r
}

// word adds a name that is replaced only where it stands alone, so a user
// called "box" does not turn "--sandbox" into "--sand<user>".
func (r *redactor) word(name, to string) {
	if len(name) < 2 {
		return
	}
	r.words = append(r.words, regexp.MustCompile(`(^|[^A-Za-z0-9_-])`+regexp.QuoteMeta(name)+`($|[^A-Za-z0-9_-])`))
	r.wordR = append(r.wordR, "${1}"+to+"${2}")
}

func (r *redactor) String(s string) string {
	if r.off || s == "" {
		return s
	}
	// Apply in order (not strings.NewReplacer's single pass) so a user name
	// inside an already-shortened path is still caught.
	for i := 0; i+1 < len(r.pairs); i += 2 {
		if len(r.pairs[i]) >= 3 {
			s = strings.ReplaceAll(s, r.pairs[i], r.pairs[i+1])
		}
	}
	for i, re := range r.words {
		// Twice, because adjacent matches share a separator.
		s = re.ReplaceAllString(re.ReplaceAllString(s, r.wordR[i]), r.wordR[i])
	}
	return s
}

func (r *redactor) ID(id string) string {
	if r.off {
		return id
	}
	return shortID(id)
}

// redacted returns a copy safe to write: short session ids, scrubbed text.
func (rep Report) redacted(red *redactor) Report {
	out := rep
	out.Runs = append([]Result(nil), rep.Runs...)
	for i := range out.Runs {
		out.Runs[i].SessionID = red.ID(out.Runs[i].SessionID)
		out.Runs[i].Error = red.String(out.Runs[i].Error)
		out.Runs[i].Version = red.String(out.Runs[i].Version)
	}
	out.Notes = make([]string, len(rep.Notes))
	for i, n := range rep.Notes {
		out.Notes[i] = red.String(n)
	}
	return out
}

func (rep Report) writeAll(dir string, red *redactor) error {
	out := rep.redacted(red)
	var md, js, cs, rq, html bytes.Buffer
	out.writeMarkdown(&md, &redactor{off: true})
	enc := json.NewEncoder(&js)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		return err
	}
	if err := out.writeCSV(&cs); err != nil {
		return err
	}
	if err := out.writeRequestsCSV(&rq); err != nil {
		return err
	}
	if err := out.writeHTML(&html); err != nil {
		return err
	}
	for name, b := range map[string][]byte{"summary.md": md.Bytes(), "results.json": js.Bytes(), "results.csv": cs.Bytes(),
		"requests.csv": rq.Bytes(), "chart.html": html.Bytes()} {
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o600); err != nil {
			return err
		}
	}
	return nil
}

func fmtSummary(s Summary, digits int) string {
	f := "%." + strconv.Itoa(digits) + "f"
	v := func(x float64) string {
		if digits == 0 {
			return comma(int64(math.Round(x)))
		}
		return fmt.Sprintf(f, x)
	}
	if s.CILo == nil {
		return v(s.Mean) + " (CI: n<" + strconv.Itoa(minCI) + ")"
	}
	return fmt.Sprintf("%s [%s, %s]", v(s.Mean), v(*s.CILo), v(*s.CIHi))
}

func fmtP(p float64) string {
	if p < 0.001 {
		return "<0.001"
	}
	return fmt.Sprintf("%.3f", p)
}

func (rep Report) writeMarkdown(w io.Writer, red *redactor) {
	fmt.Fprintf(w, "# Reasoning effort vs tokens and cache\n\n")
	fmt.Fprintf(w, "%d stored runs from %d batch(es). Same prompt and repo state, a fresh session per run. Means with 95%% bootstrap intervals [lo, hi]; sd is the run-to-run spread.\n",
		len(rep.Runs), len(rep.Batches))
	for _, g := range rep.Groups {
		title := g.Harness + " · " + emptyOr(g.Model, "unknown model")
		if g.Kind != "run" {
			title += " · warm repeats"
		}
		fmt.Fprintf(w, "\n## %s\n\n", title)
		if len(g.NotLogged) > 0 {
			fmt.Fprintf(w, "Not logged by %s, shown as n/a: %s. Its system prompt and tools differ from other harnesses, so compare it as this harness's cost for the task, not as a model property.\n\n", g.Harness, strings.Join(g.NotLogged, ", "))
		}
		if len(g.Small) > 0 {
			fmt.Fprintf(w, "**Too few runs for intervals or tests:** %s. Collect at least %d per effort (more for real trends; see the README).\n\n", strings.Join(g.Small, ", "), minCI)
		}
		fmt.Fprintln(w, "| effort | n | cache hit % | median | sd | total tokens | sd | output | reasoning | wall s | fresh | cache read | cache write | requests | 1st req cached % | cold | runs w/ later miss | tests | 5h Δ% |")
		fmt.Fprintln(w, "|---|--:|--:|--:|--:|--:|--:|--:|--:|--:|--:|--:|--:|--:|--:|--:|--:|--:|--:|")
		for _, r := range g.Rows {
			reason, write := "n/a", "n/a"
			if r.Reasoning != nil {
				reason = fmtSummary(*r.Reasoning, 0)
			}
			if r.CacheWrite != nil {
				write = comma(int64(*r.CacheWrite))
			}
			delta := "n/a"
			if r.WindowDelta != nil {
				delta = fmt.Sprintf("%+.2f", *r.WindowDelta)
			}
			fmt.Fprintf(w, "| %s | %d | %s | %.1f | %.1f | %s | %s | %s | %s | %s | %s | %s | %s | %.1f | %.1f | %d | %d | %d/%d | %s |\n",
				r.Effort, r.N, fmtSummary(r.CacheHit, 1), r.CacheHit.Median, r.CacheHit.SD, fmtSummary(r.Total, 0), comma(int64(r.Total.SD)),
				fmtSummary(r.Output, 0), reason, fmtSummary(r.Wall, 0), comma(int64(r.Fresh)), comma(int64(r.Cached)), write,
				r.Requests, r.FirstCached, r.Cold, r.LaterMiss, r.Passed, r.N, delta)
		}
		if len(g.Pairs) > 0 {
			fmt.Fprint(w, "\n### Pairwise (Mann-Whitney U, two-sided, Holm-corrected per metric)\n\n")
			fmt.Fprintln(w, "| metric | a → b | n | mean diff (b−a) [95% CI] | U | p | p (Holm) | |")
			fmt.Fprintln(w, "|---|---|--:|--:|--:|--:|--:|---|")
			for _, p := range g.Pairs {
				verdict := ""
				switch {
				case !p.Sufficient:
					verdict = "n too small"
				case p.PHolm < 0.05:
					verdict = "**significant**"
				default:
					verdict = "not significant"
				}
				d := 1
				if p.Metric == "total_tokens" {
					d = 0
				}
				f := "%+." + strconv.Itoa(d) + "f [%+." + strconv.Itoa(d) + "f, %+." + strconv.Itoa(d) + "f]"
				fmt.Fprintf(w, "| %s | %s → %s | %d/%d | %s | %.1f | %s | %s | %s |\n", p.Metric, p.A, p.B, p.NA, p.NB,
					fmt.Sprintf(f, p.Diff, p.DiffLo, p.DiffHi), p.U, fmtP(p.P), fmtP(p.PHolm), verdict)
			}
		}
		fmt.Fprint(w, "\n### Where the cache misses are (by request number)\n\n")
		fmt.Fprint(w, "Cached % is cache read ÷ input at that request; \"share of uncached\" is how much of the effort's uncached input (fresh + cache write) lands there. A later miss is a request after the first that was under half cached.\n\n")
		fmt.Fprintln(w, "| effort | request | runs | mean input | cached % | share of uncached % |")
		fmt.Fprintln(w, "|---|--:|--:|--:|--:|--:|")
		for _, q := range g.PerRequest {
			fmt.Fprintf(w, "| %s | %s | %d | %s | %.1f | %.1f |\n", q.Effort, q.Index, q.Runs, comma(int64(q.InputMean)), q.CachedShare, q.UncachedShare)
		}
		fmt.Fprint(w, "\n### Cache hit vs time since the previous run (same harness and model)\n\n")
		fmt.Fprintln(w, "| gap | runs | cache hit % | 1st request cached % | cold starts |")
		fmt.Fprintln(w, "|---|--:|--:|--:|--:|")
		for _, b := range g.GapBins {
			if b.N > 0 {
				fmt.Fprintf(w, "| %s | %d | %.1f | %.1f | %d |\n", b.Label, b.N, b.CacheHit, b.FirstCached, b.Cold)
			}
		}
		fmt.Fprintln(w, "\n| local time | runs | cache hit % | 1st request cached % | cold starts |")
		fmt.Fprintln(w, "|---|--:|--:|--:|--:|")
		for _, b := range g.HourBins {
			if b.N > 0 {
				fmt.Fprintf(w, "| %s | %d | %.1f | %.1f | %d |\n", b.Label, b.N, b.CacheHit, b.FirstCached, b.Cold)
			}
		}
	}
	const maxRows = 60
	fmt.Fprintf(w, "\n## Runs")
	shown := rep.Runs
	if len(shown) > maxRows {
		shown = shown[len(shown)-maxRows:]
		fmt.Fprintf(w, " (latest %d of %d; all in results.csv)", maxRows, len(rep.Runs))
	}
	fmt.Fprintln(w, "\n\n| batch/# | harness | effort | kind | session | local start | gap | total | hit % | 1st req | later misses | requests | reasoning | wall s | tests |")
	fmt.Fprintln(w, "|---|---|---|---|---|---|--:|--:|--:|---|---|--:|--:|--:|---|")
	for _, r := range shown {
		tests := "n/a"
		if r.TestsPass != nil {
			tests = map[bool]string{true: "pass", false: "fail"}[*r.TestsPass]
		}
		if !r.ok() {
			tests = "error"
		}
		first := ""
		if st := r.CacheState(); st != "" {
			first = fmt.Sprintf("%s %.0f%%", st, r.FirstCachedPct())
		}
		reason := comma(r.Usage.Reasoning)
		if !r.logs(fieldReasoning) {
			reason = "n/a"
		}
		fmt.Fprintf(w, "| %s/%d | %s | %s | %s | %s | %s | %s | %s | %.1f | %s | %s | %d | %s | %.0f | %s |\n",
			r.Batch, r.PlanIndex+1, r.Harness, r.Effort, r.Kind, red.ID(r.SessionID), r.StartedAt.Local().Format("Jan 2 15:04"),
			fmtGap(r.GapPrevSec), comma(r.Usage.Total), r.CacheHitPct(), first, intsList(r.LaterMisses()), len(r.Requests), reason, r.WallSeconds, tests)
	}
	fmt.Fprintln(w, "\nCache hit % = cache read ÷ input (input includes cache reads and writes). Fresh = input − cache read − cache write. Total = input + output; reasoning is part of output. \"cold\" = the run's first request found nothing cached.")
	for _, n := range rep.Notes {
		fmt.Fprintf(w, "\n- note: %s", red.String(n))
	}
	if len(rep.Notes) > 0 {
		fmt.Fprintln(w)
	}
}

func fmtGap(s *float64) string {
	if s == nil {
		return "–"
	}
	switch {
	case *s < 120:
		return fmt.Sprintf("%.0fs", *s)
	case *s < 7200:
		return fmt.Sprintf("%.0fm", *s/60)
	default:
		return fmt.Sprintf("%.1fh", *s/3600)
	}
}

func intsList(xs []int) string {
	if len(xs) == 0 {
		return ""
	}
	s := make([]string, len(xs))
	for i, x := range xs {
		s[i] = "#" + strconv.Itoa(x)
	}
	return strings.Join(s, " ")
}

func (rep Report) writeCSV(w io.Writer) error {
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"batch", "plan_index", "rep", "harness", "effort", "kind", "model", "effort_recorded", "session", "started_local", "hour_local",
		"gap_prev_s", "gap_same_effort_s", "total", "input", "fresh_input", "cache_read", "cache_write", "output", "reasoning_output", "cache_hit_pct",
		"requests", "first_request_cached_pct", "cache_state", "later_misses", "tool_calls", "wall_seconds", "tests_pass",
		"window_used_pct_start", "window_used_pct_end", "firekeeper_total", "not_logged", "error"})
	i := func(v int64) string { return strconv.FormatInt(v, 10) }
	for _, r := range rep.Runs {
		tests := ""
		if r.TestsPass != nil {
			tests = strconv.FormatBool(*r.TestsPass)
		}
		fk := ""
		if r.FirekeeperTotal != nil {
			fk = i(*r.FirekeeperTotal)
		}
		write, reason := i(r.Usage.CacheWrite), i(r.Usage.Reasoning)
		if !r.logs(fieldCacheWrite) {
			write = "n/a"
		}
		if !r.logs(fieldReasoning) {
			reason = "n/a"
		}
		local := r.StartedAt.Local()
		_ = cw.Write([]string{
			r.Batch, strconv.Itoa(r.PlanIndex + 1), strconv.Itoa(r.Rep), r.Harness, r.Effort, r.Kind, r.Model, r.EffortRecorded, r.SessionID,
			local.Format("2006-01-02 15:04"), strconv.Itoa(local.Hour()), optNum(r.GapPrevSec), optNum(r.GapSameEffortSec),
			i(r.Usage.Total), i(r.Usage.Input), i(r.Fresh()), i(r.Usage.Cached), write, i(r.Usage.Output), reason,
			fmt.Sprintf("%.2f", r.CacheHitPct()), strconv.Itoa(len(r.Requests)), fmt.Sprintf("%.2f", r.FirstCachedPct()), r.CacheState(),
			intsList(r.LaterMisses()), strconv.Itoa(r.ToolCalls), fmt.Sprintf("%.1f", r.WallSeconds), tests,
			optNum(r.UsedPctStart), optNum(r.UsedPctEnd), fk, strings.Join(r.NotLogged, " "), r.Error,
		})
	}
	cw.Flush()
	return cw.Error()
}

func (rep Report) writeRequestsCSV(w io.Writer) error {
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"batch", "plan_index", "harness", "effort", "kind", "session", "request", "seconds_from_start", "input", "cache_read", "cache_write", "uncached", "cached_pct", "output", "reasoning_output"})
	i := func(v int64) string { return strconv.FormatInt(v, 10) }
	for _, r := range rep.Runs {
		for k, q := range r.Requests {
			off := ""
			if !q.At.IsZero() && !r.StartedAt.IsZero() {
				off = fmt.Sprintf("%.1f", q.At.Sub(r.StartedAt).Seconds())
			}
			_ = cw.Write([]string{r.Batch, strconv.Itoa(r.PlanIndex + 1), r.Harness, r.Effort, r.Kind, r.SessionID, strconv.Itoa(k + 1), off,
				i(q.Input), i(q.Cached), i(q.CacheWrite), i(max(q.Input-q.Cached, 0)), fmt.Sprintf("%.2f", pct(q.Cached, q.Input)), i(q.Output), i(q.Reasoning)})
		}
	}
	cw.Flush()
	return cw.Error()
}

func optNum(f *float64) string {
	if f == nil {
		return ""
	}
	return strconv.FormatFloat(*f, 'f', -1, 64)
}

func comma(n int64) string {
	neg := n < 0
	if neg {
		n = -n
	}
	s := strconv.FormatInt(n, 10)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	if neg {
		return "-" + s
	}
	return s
}

var palette = []string{"#4c9be8", "#2bb673", "#f2a541", "#e4572e", "#9b5de5", "#00a6a6", "#777777"}

func effortColor(efforts []string, e string) string {
	for i, x := range efforts {
		if x == e {
			return palette[i%len(palette)]
		}
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(e))
	return palette[int(h.Sum32())%len(palette)]
}
