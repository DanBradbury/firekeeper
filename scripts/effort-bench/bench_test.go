package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const fixture = "testdata/rollout-2026-11-18T10-00-00-019a0000-0000-7000-8000-00000000beef.jsonl"

// TestMain lets the test binary stand in for the Codex CLI: with
// EFFORT_BENCH_FAKE_CODEX set it behaves like a tiny `codex exec --json`.
func TestMain(m *testing.M) {
	if os.Getenv("EFFORT_BENCH_FAKE_CODEX") == "1" {
		os.Exit(fakeCodex(os.Args[1:]))
	}
	os.Exit(m.Run())
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
	// Solve the task so the post-run test check passes.
	impl := `import re
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
	if err := os.WriteFile(filepath.Join(repo, "durations.py"), []byte(impl), 0o600); err != nil {
		return 1
	}
	reasoning := map[string]int{"low": 100, "medium": 400, "high": 1600, "xhigh": 6400}[effort]
	n := 0
	entries, _ := filepath.Glob(filepath.Join(os.Getenv("CODEX_HOME"), "sessions", "*", "*", "*", "*.jsonl"))
	n = len(entries) + 1
	id := fmt.Sprintf("019a0000-0000-7000-8000-%012d", n)
	dir := filepath.Join(os.Getenv("CODEX_HOME"), "sessions", "2026", "11", "18")
	_ = os.MkdirAll(dir, 0o700)
	var b bytes.Buffer
	line := func(v any) { j, _ := json.Marshal(v); b.Write(j); b.WriteByte('\n') }
	now := time.Now().UTC().Format(time.RFC3339Nano)
	line(map[string]any{"timestamp": now, "type": "session_meta", "payload": map[string]any{"id": id, "cwd": repo, "cli_version": "0.0.0-fake"}})
	line(map[string]any{"timestamp": now, "type": "turn_context", "payload": map[string]any{"model": model, "effort": effort, "cwd": repo}})
	for i, cached := range []int{0, 11000, 12000} {
		write := map[int]int{0: 4000}[i]
		u := map[string]int{"input_tokens": 12000 + 1000*i, "cached_input_tokens": cached, "cache_write_input_tokens": write, "output_tokens": 100 + reasoning/3, "reasoning_output_tokens": reasoning / 3}
		u["total_tokens"] = u["input_tokens"] + u["output_tokens"]
		line(map[string]any{"timestamp": now, "type": "event_msg", "payload": map[string]any{
			"type": "token_count", "info": map[string]any{"last_token_usage": u},
			"rate_limits": map[string]any{"primary": map[string]any{"used_percent": float64(n + i)}},
		}})
	}
	if err := os.WriteFile(filepath.Join(dir, "rollout-2026-11-18T10-00-00-"+id+".jsonl"), b.Bytes(), 0o600); err != nil {
		return 1
	}
	fmt.Printf(`{"type":"thread.started","thread_id":"%s"}`+"\n", id)
	fmt.Println(`{"type":"turn.completed","usage":{"input_tokens":1,"cached_input_tokens":0,"output_tokens":1,"reasoning_output_tokens":0}}`)
	return 0
}

func TestParseRolloutFixture(t *testing.T) {
	st, err := ParseRolloutFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	want := Usage{Input: 25000, Cached: 11904, Output: 1200, Reasoning: 1000, Total: 26200}
	if st.Usage != want {
		t.Fatalf("usage = %+v, want %+v", st.Usage, want)
	}
	if st.Final.Total != st.Usage.Total {
		t.Errorf("final total %d != summed %d", st.Final.Total, st.Usage.Total)
	}
	if st.Requests != 2 || st.ToolCalls != 1 {
		t.Errorf("requests=%d tool calls=%d, want 2 and 1", st.Requests, st.ToolCalls)
	}
	if st.SessionID != "019a0000-0000-7000-8000-00000000beef" || st.CLIVersion != "0.0.0-test" {
		t.Errorf("session %q version %q", st.SessionID, st.CLIVersion)
	}
	if strings.Join(st.Models, ",") != "gpt-test" || strings.Join(st.Efforts, ",") != "xhigh" {
		t.Errorf("models %v efforts %v", st.Models, st.Efforts)
	}
	if st.First.Cached != 0 || st.First.Input != 12000 {
		t.Errorf("first request %+v", st.First)
	}
	if st.UsedPercentStart == nil || *st.UsedPercentStart != 10 || *st.UsedPercentEnd != 11 {
		t.Errorf("window used %v..%v", st.UsedPercentStart, st.UsedPercentEnd)
	}
	r := Result{Effort: "xhigh", Kind: "run", Source: "rollout"}
	r.fill(st)
	if r.Fresh() != 13096 || r.CacheState() != "cold" {
		t.Errorf("fresh %d state %s", r.Fresh(), r.CacheState())
	}
	if got := fmt.Sprintf("%.2f", r.CacheHitPct()); got != "47.62" {
		t.Errorf("cache hit %s", got)
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
{"type":"turn.started"}
{"type":"turn.completed","usage":{"input_tokens":5,"cached_input_tokens":4,"output_tokens":1,"reasoning_output_tokens":0}}`))
	if id != "019a0000-0000-7000-8000-00000000beef" || usage == nil || usage.Cached != 4 {
		t.Fatalf("id %q usage %+v", id, usage)
	}
	got, err := FindRollout(home, id)
	if err != nil || got != want {
		t.Fatalf("FindRollout = %q, %v", got, err)
	}
	if _, err := FindRollout(home, "019a0000-0000-7000-8000-000000000000"); err == nil {
		t.Fatal("expected an error for an unknown session")
	}
}

func TestPlanAndMaxRuns(t *testing.T) {
	o := options{efforts: []string{"low", "medium", "high", "xhigh"}, reps: 2, seed: 1, order: "shuffle"}
	plan := buildPlan(o)
	if len(plan) != 8 {
		t.Fatalf("plan has %d runs", len(plan))
	}
	again := buildPlan(o)
	for i := range plan {
		if plan[i] != again[i] {
			t.Fatal("a seeded plan must be reproducible")
		}
	}
	for rep := 1; rep <= 2; rep++ {
		seen := map[string]bool{}
		for _, p := range plan {
			if p.Rep == rep {
				seen[p.Effort] = true
			}
		}
		if len(seen) != 4 {
			t.Fatalf("rep %d covers %v", rep, seen)
		}
	}
	var out, errb bytes.Buffer
	if code := run([]string{"--reps", "3", "--max-runs", "8", "--dry-run"}, &out, &errb); code != 2 || !strings.Contains(errb.String(), "over --max-runs") {
		t.Fatalf("code %d stderr %q", code, errb.String())
	}
}

func TestDryRunRedacts(t *testing.T) {
	work := filepath.Join(t.TempDir(), "secret-project")
	var out, errb bytes.Buffer
	code := run([]string{"--dry-run", "--model", "gpt-test", "--workdir", work, "--codex", "codex-not-installed"}, &out, &errb)
	if code != 0 {
		t.Fatalf("code %d: %s", code, errb.String())
	}
	s := out.String()
	for _, want := range []string{"8 Codex runs", "model_reasoning_effort=xhigh", "--ignore-user-config", "-C <bench-repo> -", "--sandbox workspace-write"} {
		if !strings.Contains(s, want) {
			t.Errorf("dry run output lacks %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "secret-project") {
		t.Errorf("dry run leaked the work dir:\n%s", s)
	}
}

func TestEndToEndWithFakeCodex(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Fatal("git is required")
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("EFFORT_BENCH_FAKE_CODEX", "1")
	home := t.TempDir()
	work := filepath.Join(t.TempDir(), "private-name")
	out := filepath.Join(work, "results")
	var stdout, stderr bytes.Buffer
	code := run([]string{"--codex", self, "--codex-home", home, "--workdir", work, "--out", out,
		"--model", "gpt-test", "--efforts", "low,xhigh", "--reps", "2", "--firekeeper", "off"}, &stdout, &stderr)
	t.Setenv("EFFORT_BENCH_FAKE_CODEX", "")
	if code != 0 {
		t.Fatalf("code %d\nstdout:\n%s\nstderr:\n%s", code, stdout.String(), stderr.String())
	}
	var rep Report
	b, err := os.ReadFile(filepath.Join(out, "results.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &rep); err != nil {
		t.Fatal(err)
	}
	if len(rep.Runs) != 4 || len(rep.Rows) != 2 {
		t.Fatalf("runs %d rows %d\n%s", len(rep.Runs), len(rep.Rows), stdout.String())
	}
	byEffort := map[string]Row{}
	for _, r := range rep.Rows {
		byEffort[r.Effort] = r
	}
	low, xhigh := byEffort["low"], byEffort["xhigh"]
	if low.Fresh+low.Cached+low.CacheWrite+low.Output != low.Total || low.CacheWrite != 4000 {
		t.Fatalf("token parts do not add up: %+v", low)
	}
	if low.Runs != 2 || xhigh.Runs != 2 || xhigh.Reasoning <= low.Reasoning || xhigh.Output <= low.Output {
		t.Fatalf("low %+v\nxhigh %+v", low, xhigh)
	}
	// Same input shape for both efforts in the fake, so the hit rate matches.
	if fmt.Sprintf("%.2f", low.CacheHitPct) != "58.97" || low.CacheHitPct != xhigh.CacheHitPct {
		t.Fatalf("cache hit low %.2f xhigh %.2f", low.CacheHitPct, xhigh.CacheHitPct)
	}
	for _, r := range rep.Runs {
		if r.Error != "" || r.Source != "rollout" || r.EffortRecorded != r.Effort || r.Model != "gpt-test" || len(r.SessionID) != 8 {
			t.Fatalf("run %+v", r)
		}
		if r.TestsPass != nil && !*r.TestsPass && runtime.GOOS != "windows" {
			t.Errorf("run %d: the fake solved the task but tests failed", r.Run)
		}
		if r.CacheState() != "cold" || r.Requests != 3 {
			t.Errorf("run %d state %s requests %d", r.Run, r.CacheState(), r.Requests)
		}
	}
	for _, name := range []string{"summary.md", "results.csv", "results.json", "chart.html"} {
		b, err := os.ReadFile(filepath.Join(out, name))
		if err != nil {
			t.Fatal(err)
		}
		s := string(b)
		if strings.Contains(s, "private-name") || strings.Contains(s, home) {
			t.Errorf("%s leaks a local path", name)
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
	got := r.String("cd /Users/dan/tmp/bench-repo && ssh dans-mbp; /Users/dan/x; dan@dans-mbp --dangerous")
	if want := "cd <bench-repo> && ssh <host>; ~/x; <user>@<host> --dangerous"; got != want {
		t.Fatalf("redacted %q, want %q", got, want)
	}
	off := &redactor{off: true, pairs: r.pairs}
	if off.String("/Users/dan") != "/Users/dan" {
		t.Fatal("--no-redact must keep the text")
	}
}
