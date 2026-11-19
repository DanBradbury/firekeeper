package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

const fixture = "testdata/rollout-2026-11-18T10-00-00-019a0000-0000-7000-8000-00000000beef.jsonl"

// TestMain lets the test binary stand in for the agent CLIs: with
// EFFORT_BENCH_FAKE set it behaves like a tiny `codex exec --json` or
// `opencode run --format json`.
func TestMain(m *testing.M) {
	if os.Getenv("EFFORT_BENCH_FAKE") == "1" {
		if len(os.Args) > 1 && os.Args[1] == "run" {
			os.Exit(fakeOpenCode(os.Args[1:]))
		}
		os.Exit(fakeCodex(os.Args[1:]))
	}
	os.Exit(m.Run())
}

const solution = `import re
U = [("d", 86400), ("h", 3600), ("m", 60), ("s", 1)]
def parse_duration(t):
    m = re.fullmatch(r"(?:(\d+)d)?(?:(\d+)h)?(?:(\d+)m)?(?:(\d+)s)?", t or "")
    if not t or not m:
        raise ValueError(t)
    return sum(int(v) * u for v, (_, u) in zip(m.groups(), U) if v)
def format_duration(n):
    if n < 0:
        raise ValueError(n)
    out = ""
    for k, u in U:
        q, n = divmod(n, u)
        if q:
            out += f"{q}{k}"
    return out or "0s"
`

var fakeReasoning = map[string]int{"low": 100, "medium": 400, "high": 1600, "xhigh": 6400}

// fakeRequests is the shape every fake run shares: three requests, the
// first cold with a cache write, the third a "later miss" for xhigh.
func fakeRequests(effort string) []Usage {
	var out []Usage
	for i, cached := range []int64{0, 11000, 12000} {
		input := int64(12000 + 1000*i)
		if effort == "xhigh" && i == 2 {
			cached = 3000
		}
		r := int64(fakeReasoning[effort] / 3)
		u := Usage{Input: input, Cached: cached, Output: 100 + r, Reasoning: r}
		if i == 0 {
			u.CacheWrite = 4000
		}
		u.Total = u.Input + u.Output
		out = append(out, u)
	}
	return out
}

func fakeCodex(args []string) int {
	var repo, effort, model string
	for i := 0; i < len(args)-1; i++ {
		switch args[i] {
		case "-C":
			repo = args[i+1]
		case "-m":
			model = args[i+1]
		case "-c":
			if v, ok := strings.CutPrefix(args[i+1], "model_reasoning_effort="); ok {
				effort = v
			}
		}
	}
	if args[0] != "exec" || repo == "" || effort == "" || args[len(args)-1] != "-" {
		fmt.Fprintln(os.Stderr, "fake codex: bad args", args)
		return 2
	}
	if err := os.WriteFile(filepath.Join(repo, "durations.py"), []byte(solution), 0o600); err != nil {
		return 1
	}
	home := os.Getenv("CODEX_HOME")
	entries, _ := filepath.Glob(filepath.Join(home, "sessions", "*", "*", "*", "*.jsonl"))
	n := len(entries) + 1
	id := fmt.Sprintf("019a0000-0000-7000-8000-%012d", n)
	dir := filepath.Join(home, "sessions", "2026", "11", "18")
	_ = os.MkdirAll(dir, 0o700)
	used := float64(n)
	if v, err := strconv.ParseFloat(os.Getenv("FAKE_USED"), 64); err == nil {
		used = v
	}
	reset := time.Now().Add(3 * time.Hour).Unix()
	var b bytes.Buffer
	line := func(v any) { j, _ := json.Marshal(v); b.Write(j); b.WriteByte('\n') }
	now := time.Now().UTC()
	line(map[string]any{"timestamp": now.Format(time.RFC3339Nano), "type": "session_meta", "payload": map[string]any{"id": id, "cwd": repo, "cli_version": "0.0.0-fake"}})
	line(map[string]any{"timestamp": now.Format(time.RFC3339Nano), "type": "turn_context", "payload": map[string]any{"model": model, "effort": effort, "cwd": repo}})
	for i, u := range fakeRequests(effort) {
		if os.Getenv("FAKE_NOISE") == "1" && i > 0 && (n*7+i)%5 == 0 {
			u.Cached /= 3 // an occasional later miss, for eyeballing the report
		}
		line(map[string]any{"timestamp": now.Add(time.Duration(i+1) * 100 * time.Millisecond).Format(time.RFC3339Nano), "type": "event_msg", "payload": map[string]any{
			"type": "token_count", "info": map[string]any{"last_token_usage": u},
			"rate_limits": map[string]any{"primary": map[string]any{"used_percent": used + float64(i), "resets_at": reset}},
		}})
	}
	if err := os.WriteFile(filepath.Join(dir, "rollout-2026-11-18T10-00-00-"+id+".jsonl"), b.Bytes(), 0o600); err != nil {
		return 1
	}
	fmt.Printf(`{"type":"thread.started","thread_id":"%s"}`+"\n", id)
	return 0
}

const fakeOpenCodeSchema = `CREATE TABLE IF NOT EXISTS session_v2 (id text PRIMARY KEY, directory text, version text, model text);
CREATE TABLE IF NOT EXISTS session_message (id text PRIMARY KEY, session_id text, type text, seq integer, time_created integer, time_updated integer, data text);`

func fakeOpenCode(args []string) int {
	var model string
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "--model" {
			model = args[i+1]
		}
	}
	id, effort, ok := strings.Cut(model, "#")
	if !ok || !strings.HasPrefix(id, "openai/") || args[1] != "--format" || args[2] != "json" {
		fmt.Fprintln(os.Stderr, "fake opencode: bad args", args)
		return 2
	}
	repo, _ := os.Getwd()
	if err := os.WriteFile(filepath.Join(repo, "durations.py"), []byte(solution), 0o600); err != nil {
		return 1
	}
	db, err := sql.Open("sqlite", os.Getenv("OPENCODE_DB"))
	if err != nil {
		return 1
	}
	defer db.Close()
	if _, err := db.Exec(fakeOpenCodeSchema); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	var n int
	_ = db.QueryRow("SELECT COUNT(*) FROM session_v2").Scan(&n)
	sid := fmt.Sprintf("ses_fake%04d", n+1)
	modelJSON, _ := json.Marshal(map[string]string{"id": strings.TrimPrefix(id, "openai/"), "providerID": "openai", "variant": effort})
	if _, err := db.Exec("INSERT INTO session_v2 VALUES (?, ?, ?, ?)", sid, repo, "2.3.2", string(modelJSON)); err != nil {
		return 1
	}
	now := time.Now().UnixMilli()
	for i, u := range fakeRequests(effort) {
		// OpenCode counts input without cache and reasoning apart from output.
		data, _ := json.Marshal(map[string]any{
			"content": []map[string]string{{"type": "tool"}},
			"tokens": map[string]any{"input": u.Input - u.Cached - u.CacheWrite, "output": u.Output - u.Reasoning, "reasoning": u.Reasoning,
				"cache": map[string]int64{"read": u.Cached, "write": u.CacheWrite}},
		})
		if _, err := db.Exec("INSERT INTO session_message VALUES (?, ?, 'assistant', ?, ?, ?, ?)",
			fmt.Sprintf("msg_%s_%d", sid, i), sid, i+1, now+int64(i)*100, now+int64(i)*100, string(data)); err != nil {
			return 1
		}
	}
	fmt.Printf(`{"type":"step_start","timestamp":%d,"sessionID":"%s"}`+"\n", now, sid)
	return 0
}

func TestParseRolloutFixture(t *testing.T) {
	d, err := ParseRolloutFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	want := Usage{Input: 25000, Cached: 11904, Output: 1200, Reasoning: 1000, Total: 26200}
	if got := d.Sum(); got != want {
		t.Fatalf("usage = %+v, want %+v", got, want)
	}
	if d.Final == nil || d.Final.Total != 26200 {
		t.Errorf("final total %+v", d.Final)
	}
	if len(d.Requests) != 2 || d.ToolCalls != 1 {
		t.Errorf("requests=%d tool calls=%d, want 2 and 1", len(d.Requests), d.ToolCalls)
	}
	if d.SessionID != "019a0000-0000-7000-8000-00000000beef" || d.Version != "0.0.0-test" {
		t.Errorf("session %q version %q", d.SessionID, d.Version)
	}
	if strings.Join(d.Models, ",") != "gpt-test" || strings.Join(d.Efforts, ",") != "xhigh" {
		t.Errorf("models %v efforts %v", d.Models, d.Efforts)
	}
	if d.UsedPercentStart == nil || *d.UsedPercentStart != 10 || *d.UsedPercentEnd != 11 || d.ResetsAt == nil || d.ResetsAt.Unix() != 1795000000 {
		t.Errorf("window %v..%v resets %v", d.UsedPercentStart, d.UsedPercentEnd, d.ResetsAt)
	}
	r := Result{Effort: "xhigh", Kind: "run"}
	r.fill(d)
	if r.Fresh() != 13096 || r.CacheState() != "cold" || r.FirstCachedPct() != 0 {
		t.Errorf("fresh %d state %s", r.Fresh(), r.CacheState())
	}
	if got := fmt.Sprintf("%.2f", r.CacheHitPct()); got != "47.62" {
		t.Errorf("cache hit %s", got)
	}
	if !r.Requests[1].At.After(r.Requests[0].At) {
		t.Error("request times not parsed")
	}
}

func TestFindRolloutAndExecJSON(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "sessions", "2026", "11", "18")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(fixture)
	want := filepath.Join(dir, filepath.Base(fixture))
	if err := os.WriteFile(want, b, 0o600); err != nil {
		t.Fatal(err)
	}
	id, usage := ThreadIDFromExecJSON(strings.NewReader(`{"type":"thread.started","thread_id":"019a0000-0000-7000-8000-00000000beef"}
{"type":"turn.completed","usage":{"input_tokens":5,"cached_input_tokens":4,"output_tokens":1,"reasoning_output_tokens":0}}`))
	if id != "019a0000-0000-7000-8000-00000000beef" || usage == nil || usage.Cached != 4 {
		t.Fatalf("id %q usage %+v", id, usage)
	}
	if got, err := FindRollout(home, id); err != nil || got != want {
		t.Fatalf("FindRollout = %q, %v", got, err)
	}
	if _, err := FindRollout(home, "019a0000-0000-7000-8000-000000000000"); err == nil {
		t.Fatal("expected an error for an unknown session")
	}
}

// Mann-Whitney against Wikipedia's worked examples ("Mann–Whitney U test",
// Examples). Race 1, finishing order T H H H H H T T T T T H: U_T = 11,
// U_H = 25. Race 2 (19 of each), H×9 T×10 H×10 T×9: U = 100, which the
// article calls significant at p < 0.05 two-tailed. The p-values below are
// the normal approximation with continuity correction worked by hand:
// race 1 z = (11 − 18 + 0.5)/√39 = −1.041, p = 0.298; race 2
// z = (100 − 180.5 + 0.5)/√1173.25 = −2.336, p = 0.0195.
func TestMannWhitneyTextbook(t *testing.T) {
	race := func(order string) (tort, hare []float64) {
		for i, c := range strings.Fields(order) {
			if c == "T" {
				tort = append(tort, float64(i+1))
			} else {
				hare = append(hare, float64(i+1))
			}
		}
		return
	}
	tort, hare := race("T H H H H H T T T T T H")
	u := mannWhitney(tort, hare)
	if u.U1 != 25 || u.U2 != 11 {
		t.Fatalf("race 1: U1=%v U2=%v, want 25 and 11", u.U1, u.U2)
	}
	if math.Abs(u.P-0.298) > 0.001 {
		t.Errorf("race 1: p=%.4f, want 0.298", u.P)
	}
	tort, hare = race(strings.Repeat("H ", 9) + strings.Repeat("T ", 10) + strings.Repeat("H ", 10) + strings.Repeat("T ", 9))
	u = mannWhitney(hare, tort)
	if math.Min(u.U1, u.U2) != 100 || u.P >= 0.05 || math.Abs(u.P-0.0195) > 0.0005 {
		t.Fatalf("race 2: U=%v p=%.4f, want 100 and 0.0195", math.Min(u.U1, u.U2), u.P)
	}
	// Ties: a=[1,2,2,3,5], b=[2,3,4,4,6,7]. Counting pairs by hand gives
	// U1 = 6.5; the tie-corrected variance gives z = −1.4809, p = 0.1386.
	u = mannWhitney([]float64{1, 2, 2, 3, 5}, []float64{2, 3, 4, 4, 6, 7})
	if u.U1 != 6.5 || u.U2 != 23.5 || math.Abs(u.Z+1.4809) > 1e-4 || math.Abs(u.P-0.1386) > 1e-4 {
		t.Fatalf("ties: %+v", u)
	}
	if u := mannWhitney([]float64{1, 1}, []float64{1, 1}); u.P != 1 {
		t.Errorf("all tied: p=%v, want 1", u.P)
	}
}

func TestHolmAndSummary(t *testing.T) {
	got := holm([]float64{0.01, 0.04, 0.03, 0.005})
	want := []float64{0.03, 0.06, 0.06, 0.02}
	for i := range want {
		if math.Abs(got[i]-want[i]) > 1e-12 {
			t.Fatalf("holm = %v, want %v", got, want)
		}
	}
	s := summarize([]float64{1, 2, 3, 4, 100}, 7)
	if s.Mean != 22 || s.Median != 3 || math.Abs(s.SD-43.6176) > 1e-3 || s.CILo == nil {
		t.Fatalf("summary %+v", s)
	}
	if *s.CILo > s.Median || *s.CIHi < s.Mean {
		t.Errorf("interval [%v, %v] should span the bulk and the mean", *s.CILo, *s.CIHi)
	}
	if again := summarize([]float64{1, 2, 3, 4, 100}, 7); *again.CILo != *s.CILo {
		t.Error("bootstrap must be reproducible for a seed")
	}
	if small := summarize([]float64{1, 2}, 7); small.CILo != nil {
		t.Error("n < minCI must not get an interval")
	}
	lo, hi := bootstrapDiffCI([]float64{1, 2, 3, 4, 5}, []float64{11, 12, 13, 14, 15}, 2000, 3)
	if lo < 7 || hi > 13 {
		t.Errorf("diff CI [%v, %v] should be around 10", lo, hi)
	}
}

func TestPerRequestBreakdown(t *testing.T) {
	mk := func(effort string) Result {
		r := Result{Effort: effort, Kind: "run", SessionID: "s"}
		for _, u := range fakeRequests(effort) {
			r.Requests = append(r.Requests, Request{Usage: u})
			r.Usage.add(u)
		}
		return r
	}
	low, xhigh := mk("low"), mk("xhigh")
	if len(low.LaterMisses()) != 0 || fmt.Sprint(xhigh.LaterMisses()) != "[3]" {
		t.Fatalf("later misses low %v xhigh %v", low.LaterMisses(), xhigh.LaterMisses())
	}
	rows := perRequest("xhigh", []Result{xhigh, xhigh})
	if len(rows) != 3 || rows[0].Index != "1" || rows[0].Runs != 2 || rows[0].CachedShare != 0 {
		t.Fatalf("rows %+v", rows)
	}
	// Uncached input: 12000 + 2000 + 11000 per run.
	if math.Abs(rows[2].UncachedShare-100*11000.0/25000) > 1e-9 || math.Abs(rows[1].CachedShare-100*11000.0/13000) > 1e-9 {
		t.Fatalf("shares %+v", rows)
	}
}

func TestOpenCodeParse(t *testing.T) {
	meta := []byte(`[{"model":"{\"id\":\"gpt-test\",\"providerID\":\"openai\",\"variant\":\"high\"}","version":"2.3.2"}]`)
	rows := []byte(`[{"seq":5,"time_created":1791892802000,"data":"{\"content\":[{\"type\":\"reasoning\"},{\"type\":\"tool\"}],\"tokens\":{\"input\":10,\"output\":5,\"reasoning\":4,\"cache\":{\"read\":400,\"write\":100}}}"},
{"seq":6,"time_created":1791892804000,"data":"{\"tokens\":{\"input\":20,\"output\":9,\"reasoning\":0,\"cache\":{\"read\":500,\"write\":0}}}"},
{"seq":7,"time_created":1791892805000,"data":"not json"}]`)
	d, err := parseOpenCode("ses_x", meta, rows)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Requests) != 2 || d.ToolCalls != 1 || d.Models[0] != "gpt-test" || d.Efforts[0] != "high" || d.Version != "2.3.2" {
		t.Fatalf("%+v", d)
	}
	want := Usage{Input: 510, Cached: 400, CacheWrite: 100, Output: 9, Reasoning: 4, Total: 519}
	if d.Requests[0].Usage != want {
		t.Fatalf("request 1 %+v, want %+v", d.Requests[0].Usage, want)
	}
	// Firekeeper's OpenCode total adds all five counts; ours must agree.
	if d.Sum().Total != 10+5+4+400+100+20+9+0+500+0 {
		t.Fatalf("total %d", d.Sum().Total)
	}
	if _, err := parseOpenCode("ses_x", []byte(""), rows); err == nil {
		t.Error("a missing session must be an error")
	}
}

func TestPlanAndCaps(t *testing.T) {
	o := options{harnesses: []string{"codex"}, efforts: []string{"low", "medium", "high", "xhigh"}, reps: 2, seed: 1, order: "shuffle"}
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	p := buildPlan(o, "prompt", now)
	if len(p.Items) != 8 || !strings.HasPrefix(p.Batch, "20261118T120000Z-") || p.PromptID != promptID("prompt") {
		t.Fatalf("plan %+v", p)
	}
	again := buildPlan(o, "prompt", now)
	for i := range p.Items {
		if p.Items[i] != again.Items[i] || p.Items[i].Index != i {
			t.Fatal("a seeded plan must be reproducible and indexed")
		}
	}
	o.harnesses = []string{"codex", "opencode"}
	if n := len(buildPlan(o, "prompt", now).Items); n != 16 {
		t.Fatalf("two harnesses: %d runs", n)
	}
	store := t.TempDir()
	var out, errb bytes.Buffer
	if code := run([]string{"--store", store, "--reps", "60", "--dry-run"}, &out, &errb); code != 2 || !strings.Contains(errb.String(), "over --max-runs 200") {
		t.Fatalf("code %d stderr %q", code, errb.String())
	}
	out.Reset()
	if code := run([]string{"--store", store, "--reps", "60", "--max-runs", "0", "--dry-run"}, &out, &errb); code != 0 || !strings.Contains(out.String(), "batch of 240 runs") {
		t.Fatalf("--max-runs 0 must lift the cap: %d %s", code, out.String())
	}
	if _, err := harnessFor("cursor"); err == nil || !strings.Contains(err.Error(), "token usage") {
		t.Fatalf("cursor: %v", err)
	}
}

func TestDryRunRedacts(t *testing.T) {
	work := filepath.Join(t.TempDir(), "secret-project")
	store := filepath.Join(t.TempDir(), "secret-store")
	var out, errb bytes.Buffer
	code := run([]string{"--dry-run", "--model", "gpt-test", "--harness", "codex,opencode", "--workdir", work, "--store", store, "--codex", "codex-not-installed"}, &out, &errb)
	if code != 0 {
		t.Fatalf("code %d: %s", code, errb.String())
	}
	s := out.String()
	for _, want := range []string{"batch of 16 runs", "model_reasoning_effort=xhigh", "--ignore-user-config", "<bench-repo>",
		"--sandbox workspace-write", "opencode run --format json --model openai/gpt-test#", "--auto", "<store>"} {
		if !strings.Contains(s, want) {
			t.Errorf("dry run output lacks %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "secret-project") || strings.Contains(s, "secret-store") {
		t.Errorf("dry run leaked a path:\n%s", s)
	}
	if _, err := os.Stat(filepath.Join(store, planFile)); err == nil {
		t.Error("a dry run must not save a plan")
	}
}

type benchEnv struct {
	t                       *testing.T
	self, home, work, store string
}

func newBenchEnv(t *testing.T) *benchEnv {
	if _, err := os.Stat(gitPath(t)); err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("EFFORT_BENCH_FAKE", "1")
	t.Setenv("FAKE_USED", "")
	t.Setenv("OPENCODE_DB", filepath.Join(t.TempDir(), "opencode.db"))
	return &benchEnv{t: t, self: self, home: t.TempDir(), work: filepath.Join(t.TempDir(), "private-name"), store: filepath.Join(t.TempDir(), "private-store")}
}

func gitPath(t *testing.T) string {
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		for _, name := range []string{"git", "git.exe"} {
			if p := filepath.Join(dir, name); fileExists(p) {
				return p
			}
		}
	}
	t.Fatal("git is required")
	return ""
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

func (e *benchEnv) run(args ...string) (int, string, string) {
	base := []string{"--codex", e.self, "--opencode", e.self, "--codex-home", e.home, "--workdir", e.work, "--store", e.store, "--firekeeper", "off"}
	if len(args) > 0 && args[0] == "report" {
		base = []string{"report", "--store", e.store}
		args = args[1:]
	}
	var stdout, stderr bytes.Buffer
	code := run(append(base, args...), &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestBatchStopResumeReport(t *testing.T) {
	e := newBenchEnv(t)
	code, _, stderr := e.run("--model", "gpt-test", "--efforts", "low,xhigh", "--reps", "2", "--batch", "3", "--seed", "5")
	if code != 0 || !strings.Contains(stderr, "stopped: ran --batch 3") || !strings.Contains(stderr, "1 of 4 runs") || !strings.Contains(stderr, "Resume with: go run ./scripts/effort-bench --resume --store") {
		t.Fatalf("first batch: %d\n%s", code, stderr)
	}
	runs, _ := loadRuns(e.store)
	if len(runs) != 3 {
		t.Fatalf("stored %d runs", len(runs))
	}
	// An unfinished batch is not replaced by accident.
	if code, _, stderr := e.run("--model", "gpt-test"); code != 2 || !strings.Contains(stderr, "--resume") {
		t.Fatalf("new batch over unfinished: %d %s", code, stderr)
	}
	code, stdout, stderr := e.run("--resume")
	if code != 0 || !strings.Contains(stderr, "1 of 4 runs left") || !strings.Contains(stderr, "complete") {
		t.Fatalf("resume: %d\n%s", code, stderr)
	}
	runs, _ = loadRuns(e.store)
	if len(runs) != 4 {
		t.Fatalf("stored %d runs after resume", len(runs))
	}
	// Appending a stored session again is a no-op; a duplicate line on disk is dropped on load.
	if wrote, err := appendRun(e.store, runs[0]); err != nil || wrote {
		t.Fatalf("duplicate append: wrote=%v err=%v", wrote, err)
	}
	f, _ := os.OpenFile(filepath.Join(e.store, runsFile), os.O_APPEND|os.O_WRONLY, 0o600)
	b, _ := json.Marshal(runs[1])
	_, _ = f.Write(append(b, '\n'))
	f.Close()
	if again, _ := loadRuns(e.store); len(again) != 4 {
		t.Fatalf("dedupe on load: %d", len(again))
	}
	// A second batch accumulates into the same store.
	if code, _, stderr := e.run("--model", "gpt-test", "--efforts", "low,xhigh", "--reps", "1", "--seed", "6"); code != 0 {
		t.Fatalf("second batch: %d %s", code, stderr)
	}
	code, stdout, stderr = e.run("report")
	if code != 0 || !strings.Contains(stderr, "from 6 stored runs") {
		t.Fatalf("report: %d %s", code, stderr)
	}
	for _, want := range []string{"6 stored runs from 2 batch(es)", "## codex · gpt-test", "| low | 3 |", "| xhigh | 3 |",
		"Too few runs", "n too small", "Where the cache misses are", "| xhigh | 3 | 3 |", "first run"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("report lacks %q:\n%s", want, stdout)
		}
	}
	var rep Report
	b, _ = os.ReadFile(filepath.Join(e.store, "report", "results.json"))
	if err := json.Unmarshal(b, &rep); err != nil {
		t.Fatal(err)
	}
	if len(rep.Groups) != 1 || len(rep.Groups[0].Rows) != 2 || len(rep.Groups[0].Pairs) != 2 {
		t.Fatalf("groups %+v", rep.Groups)
	}
	low, xhigh := rep.Groups[0].Rows[0], rep.Groups[0].Rows[1]
	if low.Effort != "low" || xhigh.LaterMiss != 3 || low.LaterMiss != 0 || xhigh.Reasoning.Mean <= low.Reasoning.Mean || low.Cold != 3 {
		t.Fatalf("rows %+v %+v", low, xhigh)
	}
	gaps := 0
	for _, r := range rep.Runs {
		if r.GapPrevSec != nil {
			gaps++
		}
		if r.TestsPass != nil && !*r.TestsPass && runtime.GOOS != "windows" {
			t.Errorf("run %s/%d: tests failed", r.Batch, r.PlanIndex)
		}
	}
	if gaps != 5 {
		t.Errorf("%d runs have a gap, want 5 (all but the first)", gaps)
	}
	checkNoLeaks(t, e, filepath.Join(e.store, "report"))
}

func TestWindowStop(t *testing.T) {
	e := newBenchEnv(t)
	t.Setenv("FAKE_USED", "85")
	code, _, stderr := e.run("--model", "gpt-test", "--efforts", "low,high", "--reps", "1")
	if code != 0 || !strings.Contains(stderr, "5-hour window 87% used") || !strings.Contains(stderr, "resets") || !strings.Contains(stderr, "Resume with") {
		t.Fatalf("window stop: %d\n%s", code, stderr)
	}
	if runs, _ := loadRuns(e.store); len(runs) != 1 {
		t.Fatalf("stored %d runs, want 1", len(runs))
	}
	// Until the window resets, --resume does not start another run.
	code, _, stderr = e.run("--resume")
	if code != 0 || !strings.Contains(stderr, "not starting") {
		t.Fatalf("resume while blocked: %d %s", code, stderr)
	}
	// --stop-at-used-percent 0 disables the check.
	if code, _, stderr := e.run("--resume", "--stop-at-used-percent", "0"); code != 0 || !strings.Contains(stderr, "complete") {
		t.Fatalf("resume unblocked: %d %s", code, stderr)
	}
}

func TestOpenCodeHarnessEndToEnd(t *testing.T) {
	e := newBenchEnv(t)
	code, stdout, stderr := e.run("--model", "gpt-test", "--harness", "codex,opencode", "--efforts", "low,xhigh", "--reps", "1")
	if code != 0 {
		t.Fatalf("%d\n%s", code, stderr)
	}
	for _, want := range []string{"## codex · gpt-test", "## opencode · gpt-test", "Not logged by opencode, shown as n/a: window"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("report lacks %q:\n%s", want, stdout)
		}
	}
	runs, _ := loadRuns(e.store)
	var codex, oc Usage
	for _, r := range runs {
		if !r.ok() {
			t.Fatalf("run %+v", r)
		}
		if r.Effort == "xhigh" && r.Harness == "codex" {
			codex = r.Usage
		}
		if r.Effort == "xhigh" && r.Harness == "opencode" {
			oc = r.Usage
			if r.EffortRecorded != "xhigh" || r.Model != "gpt-test" || !strings.HasPrefix(r.SessionID, "ses_fake") {
				t.Errorf("opencode run %+v", r)
			}
		}
	}
	if codex != oc {
		t.Fatalf("the fakes log the same requests; codex %+v opencode %+v", codex, oc)
	}
	checkNoLeaks(t, e, filepath.Join(e.store, "report"))
}

func checkNoLeaks(t *testing.T, e *benchEnv, dir string) {
	t.Helper()
	for _, name := range []string{"summary.md", "results.csv", "requests.csv", "results.json", "chart.html"} {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		s := string(b)
		for _, leak := range []string{"private-name", "private-store", e.home, "019a0000-0000-7000"} {
			if strings.Contains(s, leak) {
				t.Errorf("%s leaks %q", name, leak)
			}
		}
		if host, err := os.Hostname(); err == nil && len(host) > 3 && strings.Contains(s, host) {
			t.Errorf("%s leaks the host name", name)
		}
		if name == "chart.html" && (strings.Contains(s, "<script") || strings.Contains(s, "http://") || strings.Contains(s, "https://")) {
			t.Errorf("chart.html must be self-contained")
		}
	}
}

func TestRedactor(t *testing.T) {
	r := &redactor{pairs: []string{"/Users/dan/tmp/bench-repo", "<bench-repo>", "/Users/dan", "~"}}
	r.word("dan", "<user>")
	r.word("dans-mbp", "<host>")
	got := r.String("cd /Users/dan/tmp/bench-repo && ssh dans-mbp; /Users/dan/x; dan@dans-mbp --dangerous dan dan")
	if want := "cd <bench-repo> && ssh <host>; ~/x; <user>@<host> --dangerous <user> <user>"; got != want {
		t.Fatalf("redacted %q, want %q", got, want)
	}
	off := &redactor{off: true, pairs: r.pairs}
	if off.String("/Users/dan") != "/Users/dan" || off.ID("019a0000-0000-7000-8000-00000000beef") != "019a0000-0000-7000-8000-00000000beef" {
		t.Fatal("--no-redact must keep the text")
	}
	if r.ID("019a0000-0000-7000-8000-00000000beef") != "0000beef" {
		t.Fatal("ids keep their last 8 characters")
	}
}
