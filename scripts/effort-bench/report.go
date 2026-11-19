package main

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
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

// Result is one Codex run.
type Result struct {
	Run             int      `json:"run"`
	Rep             int      `json:"rep"`
	Effort          string   `json:"effort"`
	Kind            string   `json:"kind"`
	ModelRequested  string   `json:"model_requested,omitempty"`
	Model           string   `json:"model"`           // from the rollout's turn_context
	EffortRecorded  string   `json:"effort_recorded"` // from the rollout's turn_context
	SessionID       string   `json:"session_id"`
	CLIVersion      string   `json:"codex_version"`
	Source          string   `json:"source"`
	WallSeconds     float64  `json:"wall_seconds"`
	Usage           Usage    `json:"usage"`
	FinalTotal      int64    `json:"final_total_tokens"`
	Requests        int      `json:"requests"`
	ToolCalls       int      `json:"tool_calls"`
	FirstInput      int64    `json:"first_request_input"`
	FirstCached     int64    `json:"first_request_cached"`
	UsedPctStart    *float64 `json:"window_used_percent_start"`
	UsedPctEnd      *float64 `json:"window_used_percent_end"`
	TestsPass       *bool    `json:"tests_pass"`
	FirekeeperTotal *int64   `json:"firekeeper_total,omitempty"`
	Error           string   `json:"error,omitempty"`
}

func (r *Result) fill(st RolloutStats) {
	r.SessionID = st.SessionID
	r.CLIVersion = st.CLIVersion
	r.Model = strings.Join(st.Models, "+")
	r.EffortRecorded = strings.Join(st.Efforts, "+")
	r.Usage = st.Usage
	r.FinalTotal = st.Final.Total
	r.Requests = st.Requests
	r.ToolCalls = st.ToolCalls
	r.FirstInput = st.First.Input
	r.FirstCached = st.First.Cached
	r.UsedPctStart = st.UsedPercentStart
	r.UsedPctEnd = st.UsedPercentEnd
}

// Fresh is ordinary input: neither read from nor written to the cache.
// OpenAI's input_tokens is ordinary + cached + cache-write tokens, so
// Fresh + Cached + CacheWrite + Output == Total.
func (r Result) Fresh() int64 { return max(r.Usage.Input-r.Usage.Cached-r.Usage.CacheWrite, 0) }

// CacheHitPct is cache reads over all input (input_tokens includes cached).
func (r Result) CacheHitPct() float64 { return pct(r.Usage.Cached, r.Usage.Input) }

// CacheState says whether the first request already found a cached prefix.
func (r Result) CacheState() string {
	if r.Requests == 0 {
		return ""
	}
	if r.FirstCached == 0 {
		return "cold"
	}
	return "warm"
}

func (r Result) ok() bool { return r.Error == "" && r.Source == "rollout" }

func pct(a, b int64) float64 {
	if b == 0 {
		return 0
	}
	return 100 * float64(a) / float64(b)
}

// Row is the mean of one effort's runs of one kind.
type Row struct {
	Effort      string   `json:"effort"`
	Kind        string   `json:"kind"`
	Runs        int      `json:"runs"`
	Total       float64  `json:"total"`
	TotalMin    int64    `json:"total_min"`
	TotalMax    int64    `json:"total_max"`
	Fresh       float64  `json:"fresh_input"`
	Cached      float64  `json:"cache_read"`
	CacheWrite  float64  `json:"cache_write"`
	Output      float64  `json:"output"`
	Reasoning   float64  `json:"reasoning_output"`
	CacheHitPct float64  `json:"cache_hit_percent"` // sum cached / sum input
	Requests    float64  `json:"requests"`
	ToolCalls   float64  `json:"tool_calls"`
	Wall        float64  `json:"wall_seconds"`
	Passed      int      `json:"tests_passed"`
	Cold        int      `json:"cold_starts"`
	WindowDelta *float64 `json:"window_used_percent_delta,omitempty"`
}

// Report is everything the benchmark writes.
type Report struct {
	GeneratedAt time.Time `json:"generated_at"`
	Model       string    `json:"model"`
	Efforts     []string  `json:"efforts"`
	Reps        int       `json:"reps"`
	Seed        uint64    `json:"seed"`
	Order       string    `json:"order"`
	Prompt      string    `json:"prompt"`
	Rows        []Row     `json:"means"`
	Runs        []Result  `json:"runs"`
	Notes       []string  `json:"notes"`
}

func newReport(o options, results []Result, prompt string) Report {
	rep := Report{
		GeneratedAt: time.Now().UTC(), Model: o.model, Efforts: o.efforts, Reps: o.reps,
		Seed: o.seed, Order: o.order, Prompt: strings.TrimSpace(prompt), Runs: results, Notes: []string{},
	}
	models := map[string]bool{}
	for _, r := range results {
		if r.ok() {
			models[r.Model] = true
			if r.EffortRecorded != "" && r.EffortRecorded != r.Effort {
				rep.Notes = append(rep.Notes, fmt.Sprintf("run %d asked for effort %s but the rollout records %s", r.Run, r.Effort, r.EffortRecorded))
			}
			if r.FinalTotal != 0 && r.FinalTotal != r.Usage.Total {
				rep.Notes = append(rep.Notes, fmt.Sprintf("run %d: summed per-request usage (%d) differs from Codex's running total (%d)", r.Run, r.Usage.Total, r.FinalTotal))
			}
		} else if r.Error != "" {
			rep.Notes = append(rep.Notes, fmt.Sprintf("run %d (%s) not counted: %s", r.Run, r.Effort, r.Error))
		}
	}
	if len(models) > 1 {
		names := make([]string, 0, len(models))
		for m := range models {
			names = append(names, m)
		}
		sort.Strings(names)
		rep.Notes = append(rep.Notes, "runs used different models ("+strings.Join(names, ", ")+"); pin one with --model")
	}
	if rep.Model == "" && len(models) == 1 {
		for m := range models {
			rep.Model = m
		}
	}
	for _, kind := range []string{"run", "warm-repeat"} {
		for _, e := range o.efforts {
			if row, ok := aggregate(results, e, kind); ok {
				rep.Rows = append(rep.Rows, row)
			}
		}
	}
	return rep
}

func aggregate(results []Result, effort, kind string) (Row, bool) {
	row := Row{Effort: effort, Kind: kind}
	var input, cached int64
	var delta float64
	haveDelta := true
	for _, r := range results {
		if r.Effort != effort || r.Kind != kind || !r.ok() {
			continue
		}
		row.Runs++
		t := r.Usage.Total
		if row.Runs == 1 || t < row.TotalMin {
			row.TotalMin = t
		}
		row.TotalMax = max(row.TotalMax, t)
		row.Total += float64(t)
		row.Fresh += float64(r.Fresh())
		row.Cached += float64(r.Usage.Cached)
		row.CacheWrite += float64(r.Usage.CacheWrite)
		row.Output += float64(r.Usage.Output)
		row.Reasoning += float64(r.Usage.Reasoning)
		row.Requests += float64(r.Requests)
		row.ToolCalls += float64(r.ToolCalls)
		row.Wall += r.WallSeconds
		input += r.Usage.Input
		cached += r.Usage.Cached
		if r.TestsPass != nil && *r.TestsPass {
			row.Passed++
		}
		if r.CacheState() == "cold" {
			row.Cold++
		}
		if r.UsedPctStart != nil && r.UsedPctEnd != nil {
			delta += *r.UsedPctEnd - *r.UsedPctStart
		} else {
			haveDelta = false
		}
	}
	if row.Runs == 0 {
		return row, false
	}
	n := float64(row.Runs)
	for _, f := range []*float64{&row.Total, &row.Fresh, &row.Cached, &row.CacheWrite, &row.Output, &row.Reasoning, &row.Requests, &row.ToolCalls, &row.Wall} {
		*f /= n
	}
	row.CacheHitPct = pct(cached, input)
	if haveDelta {
		d := delta / n
		row.WindowDelta = &d
	}
	return row, true
}

// crossCheck asks `firekeeper window --provider codex --json` for the same
// sessions and records Firekeeper's total next to the benchmark's own. Only
// the benchmark's sessions are read from its output; nothing else is kept.
func crossCheck(o options, rep *Report) {
	if o.firekeeper == "off" {
		return
	}
	bin := o.firekeeper
	if bin == "" {
		var err error
		if bin, err = exec.LookPath("firekeeper"); err != nil {
			rep.Notes = append(rep.Notes, "firekeeper not on PATH; cross-check skipped (totals come from the Codex rollouts, the same files Firekeeper reads)")
			return
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "window", "--provider", "codex", "--json")
	cmd.Env = append(os.Environ(), "CODEX_HOME="+o.codexHome, "FIREKEEPER_NO_UPDATE_CHECK=1", "FIREKEEPER_NO_PROMPT=1")
	out, err := cmd.Output()
	if err != nil {
		rep.Notes = append(rep.Notes, "firekeeper window failed; cross-check skipped")
		return
	}
	totals, err := firekeeperTotals(out)
	if err != nil {
		rep.Notes = append(rep.Notes, "could not read firekeeper window --json; cross-check skipped")
		return
	}
	mismatches, matched := 0, 0
	for i := range rep.Runs {
		r := &rep.Runs[i]
		t, ok := totals[r.SessionID]
		if !ok || r.SessionID == "" {
			continue
		}
		matched++
		r.FirekeeperTotal = &t
		if t != r.Usage.Total {
			mismatches++
		}
	}
	rep.Notes = append(rep.Notes, fmt.Sprintf("firekeeper window cross-check: %d runs found, %d totals differ (runs older than 5 hours are outside the window)", matched, mismatches))
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
	add(filepath.Join(o.workdir, "bench-repo"), "<bench-repo>")
	add(o.out, "<results>")
	add(o.workdir, "<workdir>")
	add(o.codexHome, "<codex-home>")
	if home, err := os.UserHomeDir(); err == nil {
		add(home, "~")
	}
	if u, err := user.Current(); err == nil {
		r.word(filepath.Base(u.Username), "<user>") // DOMAIN\\user on Windows
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
		s = re.ReplaceAllString(s, r.wordR[i])
	}
	return s
}

func (r *redactor) ID(id string) string {
	if r.off {
		return id
	}
	return shortID(id)
}

func (rep Report) writeAll(dir string, red *redactor) error {
	out := rep
	out.Runs = append([]Result(nil), rep.Runs...)
	for i := range out.Runs {
		out.Runs[i].SessionID = red.ID(out.Runs[i].SessionID)
		out.Runs[i].Error = red.String(out.Runs[i].Error)
	}
	for i := range out.Notes {
		out.Notes[i] = red.String(out.Notes[i])
	}
	var md, js, cs, html bytes.Buffer
	out.writeMarkdown(&md, red)
	enc := json.NewEncoder(&js)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		return err
	}
	if err := out.writeCSV(&cs); err != nil {
		return err
	}
	if err := out.writeHTML(&html); err != nil {
		return err
	}
	for name, b := range map[string][]byte{"summary.md": md.Bytes(), "results.json": js.Bytes(), "results.csv": cs.Bytes(), "chart.html": html.Bytes()} {
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o600); err != nil {
			return err
		}
	}
	return nil
}

func (rep Report) writeMarkdown(w io.Writer, red *redactor) {
	fmt.Fprintf(w, "# Codex reasoning effort vs tokens\n\n")
	fmt.Fprintf(w, "Model: %s. Same prompt, same repo state, fresh `codex exec` session per run, %d rep(s), order %s (seed %d).\n\n",
		emptyOr(rep.Model, "unknown"), rep.Reps, rep.Order, rep.Seed)
	fmt.Fprintln(w, "| effort | runs | total tokens (mean) | min–max | fresh input | cache read | cache write | output | reasoning | cache hit % | requests | tool calls | wall s | tests pass | 5h window Δ% |")
	fmt.Fprintln(w, "|---|--:|--:|--:|--:|--:|--:|--:|--:|--:|--:|--:|--:|--:|--:|")
	for _, r := range rep.Rows {
		name := r.Effort
		if r.Kind != "run" {
			name += " (warm repeat)"
		}
		fmt.Fprintf(w, "| %s | %d | %s | %s–%s | %s | %s | %s | %s | %s | %.1f | %.1f | %.1f | %.0f | %d/%d | %s |\n",
			name, r.Runs, comma(int64(r.Total)), comma(r.TotalMin), comma(r.TotalMax), comma(int64(r.Fresh)), comma(int64(r.Cached)),
			comma(int64(r.CacheWrite)), comma(int64(r.Output)), comma(int64(r.Reasoning)), r.CacheHitPct, r.Requests, r.ToolCalls, r.Wall,
			r.Passed, r.Runs, optPct(r.WindowDelta))
	}
	fmt.Fprint(w, "\n## Runs\n\n")
	fmt.Fprintln(w, "| # | rep | effort | kind | session | total | fresh | cache read | output | reasoning | hit % | requests | first request | wall s | tests |")
	fmt.Fprintln(w, "|--:|--:|---|---|---|--:|--:|--:|--:|--:|--:|--:|---|--:|---|")
	for _, r := range rep.Runs {
		first := ""
		if st := r.CacheState(); st != "" {
			first = fmt.Sprintf("%s (%s of %s cached)", st, comma(r.FirstCached), comma(r.FirstInput))
		}
		tests := "n/a"
		if r.TestsPass != nil {
			tests = map[bool]string{true: "pass", false: "fail"}[*r.TestsPass]
		}
		if r.Error != "" {
			tests = "error"
		}
		fmt.Fprintf(w, "| %d | %d | %s | %s | %s | %s | %s | %s | %s | %s | %.1f | %d | %s | %.0f | %s |\n",
			r.Run, r.Rep, r.Effort, r.Kind, red.ID(r.SessionID), comma(r.Usage.Total), comma(r.Fresh()), comma(r.Usage.Cached),
			comma(r.Usage.Output), comma(r.Usage.Reasoning), r.CacheHitPct(), r.Requests, first, r.WallSeconds, tests)
	}
	fmt.Fprintln(w, "\nCache hit % = cache read / input (Codex input_tokens already includes cached tokens). Fresh input = input − cache read − cache write. Total = input + output; reasoning is part of output. \"cold\" means the first request of the run found nothing cached.")
	for _, n := range rep.Notes {
		fmt.Fprintf(w, "\n- note: %s", red.String(n))
	}
	if len(rep.Notes) > 0 {
		fmt.Fprintln(w)
	}
}

func (rep Report) writeCSV(w io.Writer) error {
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"run", "rep", "effort", "kind", "model", "effort_recorded", "session", "total", "input", "fresh_input", "cache_read", "cache_write", "output", "reasoning_output", "cache_hit_pct", "requests", "tool_calls", "first_request_input", "first_request_cached", "cache_state", "wall_seconds", "tests_pass", "window_used_pct_start", "window_used_pct_end", "firekeeper_total", "error"})
	for _, r := range rep.Runs {
		tests := ""
		if r.TestsPass != nil {
			tests = strconv.FormatBool(*r.TestsPass)
		}
		fk := ""
		if r.FirekeeperTotal != nil {
			fk = strconv.FormatInt(*r.FirekeeperTotal, 10)
		}
		i := func(v int64) string { return strconv.FormatInt(v, 10) }
		_ = cw.Write([]string{
			strconv.Itoa(r.Run), strconv.Itoa(r.Rep), r.Effort, r.Kind, r.Model, r.EffortRecorded, r.SessionID,
			i(r.Usage.Total), i(r.Usage.Input), i(r.Fresh()), i(r.Usage.Cached), i(r.Usage.CacheWrite), i(r.Usage.Output), i(r.Usage.Reasoning),
			fmt.Sprintf("%.2f", r.CacheHitPct()), strconv.Itoa(r.Requests), strconv.Itoa(r.ToolCalls), i(r.FirstInput), i(r.FirstCached), r.CacheState(),
			fmt.Sprintf("%.1f", r.WallSeconds), tests, optNum(r.UsedPctStart), optNum(r.UsedPctEnd), fk, r.Error,
		})
	}
	cw.Flush()
	return cw.Error()
}

func optPct(f *float64) string {
	if f == nil {
		return "n/a"
	}
	return fmt.Sprintf("%+.1f", *f)
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

// writeHTML renders a self-contained page: inline CSS and SVG, no scripts,
// no external requests.
func (rep Report) writeHTML(w io.Writer) error {
	type bar struct {
		Label                         string
		Y                             float64
		FreshW, CachedW, WriteW, OutW float64
		Total, Fresh, Cached, Out     string
		Hit                           float64
		HitW, ReasonW                 float64
		Reasoning                     string
	}
	var maxTotal, maxReason float64
	runs := []Row{}
	for _, r := range rep.Rows {
		runs = append(runs, r)
		maxTotal = max(maxTotal, r.Total)
		maxReason = max(maxReason, r.Reasoning)
	}
	const width = 440.0
	scale := func(v, m float64) float64 {
		if m == 0 {
			return 0
		}
		return width * v / m
	}
	bars := make([]bar, len(runs))
	for i, r := range runs {
		label := r.Effort
		if r.Kind != "run" {
			label += " (warm)"
		}
		bars[i] = bar{
			Label: label, Y: float64(10 + i*34),
			FreshW: scale(r.Fresh, maxTotal), CachedW: scale(r.Cached, maxTotal), WriteW: scale(r.CacheWrite, maxTotal), OutW: scale(r.Output, maxTotal),
			Total: comma(int64(r.Total)), Fresh: comma(int64(r.Fresh)), Cached: comma(int64(r.Cached)), Out: comma(int64(r.Output)),
			Hit: r.CacheHitPct, HitW: width * r.CacheHitPct / 100, ReasonW: scale(r.Reasoning, maxReason), Reasoning: comma(int64(r.Reasoning)),
		}
	}
	return htmlPage.Execute(w, map[string]any{
		"R": rep, "Bars": bars, "H": 20 + len(bars)*34,
		"Generated": rep.GeneratedAt.Format("Jan 2 2006 15:04 MST"),
	})
}

var htmlPage = template.Must(template.New("bench").Funcs(template.FuncMap{
	"f1": func(f float64) string { return fmt.Sprintf("%.1f", f) },
	"n": func(v any) string {
		switch x := v.(type) {
		case float64:
			return comma(int64(x))
		case int64:
			return comma(x)
		}
		return fmt.Sprint(v)
	},
	"add": func(a ...float64) float64 {
		var s float64
		for _, v := range a {
			s += v
		}
		return s
	},
}).Parse(`<!doctype html><html><head><meta charset="utf-8"><title>Codex reasoning effort vs tokens</title>
<style>body{font:14px -apple-system,system-ui,sans-serif;margin:2rem auto;max-width:980px;color:#222;padding:0 1rem}
h1{font-size:20px}h2{font-size:16px;margin-top:2rem}svg text{font:12px -apple-system,system-ui,sans-serif}
.k span{display:inline-block;width:10px;height:10px;margin:0 4px 0 12px}table{border-collapse:collapse;font-size:12px}td,th{padding:3px 8px;border-bottom:1px solid #eee;text-align:right}td:nth-child(-n+4),th:nth-child(-n+4){text-align:left}.mute{color:#777}</style></head><body>
<h1>Codex reasoning effort vs tokens</h1>
<p>Model <b>{{if .R.Model}}{{.R.Model}}{{else}}unknown{{end}}</b>. Same prompt and repo state, fresh session per run, {{.R.Reps}} rep(s), means per effort.</p>
<h2>Mean tokens per run</h2>
<div class="k"><span style="background:#e4572e"></span>fresh input<span style="background:#4c9be8"></span>cache read<span style="background:#f2a541"></span>cache write<span style="background:#2bb673"></span>output (incl. reasoning)</div>
<svg width="900" height="{{.H}}" role="img">{{range .Bars}}
<text x="0" y="{{add .Y 17}}">{{.Label}}</text>
<rect x="130" y="{{.Y}}" width="{{.FreshW}}" height="24" fill="#e4572e"/>
<rect x="{{add 130 .FreshW}}" y="{{.Y}}" width="{{.CachedW}}" height="24" fill="#4c9be8"/>
<rect x="{{add 130 .FreshW .CachedW}}" y="{{.Y}}" width="{{.WriteW}}" height="24" fill="#f2a541"/>
<rect x="{{add 130 .FreshW .CachedW .WriteW}}" y="{{.Y}}" width="{{.OutW}}" height="24" fill="#2bb673"/>
<text x="{{add 136 .FreshW .CachedW .WriteW .OutW}}" y="{{add .Y 17}}">{{.Total}} ({{.Fresh}} fresh, {{.Cached}} cached, {{.Out}} out)</text>{{end}}
</svg>
<h2>Cache hit % (cache read ÷ input)</h2>
<svg width="900" height="{{.H}}" role="img">{{range .Bars}}
<text x="0" y="{{add .Y 17}}">{{.Label}}</text>
<rect x="130" y="{{.Y}}" width="440" height="24" fill="#eee"/><rect x="130" y="{{.Y}}" width="{{.HitW}}" height="24" fill="#4c9be8"/>
<text x="580" y="{{add .Y 17}}">{{f1 .Hit}}%</text>{{end}}
</svg>
<h2>Mean reasoning output tokens</h2>
<svg width="900" height="{{.H}}" role="img">{{range .Bars}}
<text x="0" y="{{add .Y 17}}">{{.Label}}</text>
<rect x="130" y="{{.Y}}" width="{{.ReasonW}}" height="24" fill="#9b5de5"/>
<text x="{{add 136 .ReasonW}}" y="{{add .Y 17}}">{{.Reasoning}}</text>{{end}}
</svg>
<h2>Means per effort</h2>
<table><tr><th>effort</th><th>runs</th><th>total</th><th>fresh input</th><th>cache read</th><th>cache write</th><th>output</th><th>reasoning</th><th>hit %</th><th>requests</th><th>wall s</th><th>tests</th></tr>
{{range .R.Rows}}<tr><td>{{.Effort}}{{if ne .Kind "run"}} (warm){{end}}</td><td>{{.Runs}}</td><td>{{n .Total}}</td><td>{{n .Fresh}}</td><td>{{n .Cached}}</td><td>{{n .CacheWrite}}</td><td>{{n .Output}}</td><td>{{n .Reasoning}}</td><td>{{f1 .CacheHitPct}}</td><td>{{f1 .Requests}}</td><td>{{printf "%.0f" .Wall}}</td><td>{{.Passed}}/{{.Runs}}</td></tr>
{{end}}</table>
<h2>Runs</h2>
<table><tr><th>#</th><th>rep</th><th>effort</th><th>kind</th><th>session</th><th>total</th><th>fresh</th><th>cache read</th><th>output</th><th>reasoning</th><th>hit %</th><th>requests</th><th>first request</th><th>wall s</th></tr>
{{range .R.Runs}}<tr><td>{{.Run}}</td><td>{{.Rep}}</td><td>{{.Effort}}</td><td>{{.Kind}}</td><td>{{.SessionID}}</td><td>{{n .Usage.Total}}</td><td>{{n .Fresh}}</td><td>{{n .Usage.Cached}}</td><td>{{n .Usage.Output}}</td><td>{{n .Usage.Reasoning}}</td><td>{{f1 .CacheHitPct}}</td><td>{{.Requests}}</td><td>{{.CacheState}}{{if .Error}} error{{end}}</td><td>{{printf "%.0f" .WallSeconds}}</td></tr>
{{end}}</table>
{{range .R.Notes}}<p class="mute">note: {{.}}</p>{{end}}
<p class="mute">Read from local Codex rollouts. Token counts are what Codex logged, not a billing statement. Generated {{.Generated}}.</p>
</body></html>`))
