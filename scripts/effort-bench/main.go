// Command effort-bench runs one fixed coding task through `codex exec` at
// several reasoning efforts and compares the token use of each run: fresh
// input, cache reads, cache writes, output, reasoning output and cache-hit
// rate. Everything is read from the local Codex rollouts (the files
// Firekeeper reads) and written to local files; the benchmark itself sends
// nothing anywhere, though each Codex run of course talks to its model.
//
//	go run ./scripts/effort-bench --model <model>
//
// See README.md next to this file.
package main

import (
	"bytes"
	"context"
	"embed"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

//go:embed seed prompt.txt
var assets embed.FS

type options struct {
	efforts       []string
	reps          int
	model         string
	codex         string
	codexHome     string
	promptFile    string
	workdir       string
	out           string
	seed          uint64
	order         string
	warmRepeat    bool
	maxRuns       int
	maxTokens     int64
	stopAtUsedPct float64
	timeout       time.Duration
	dryRun        bool
	noRedact      bool
	userConfig    bool
	codexArgs     []string
	firekeeper    string
}

type listFlag []string

func (l *listFlag) String() string     { return strings.Join(*l, " ") }
func (l *listFlag) Set(v string) error { *l = append(*l, v); return nil }

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	opts, err := parseFlags(args, stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		fmt.Fprintln(stderr, "effort-bench:", err)
		return 2
	}
	red := newRedactor(opts)
	plan := buildPlan(opts)
	if len(plan) > opts.maxRuns {
		fmt.Fprintf(stderr, "effort-bench: the matrix is %d runs, over --max-runs %d; lower --reps or --efforts, or raise --max-runs\n", len(plan), opts.maxRuns)
		return 2
	}
	prompt, err := loadPrompt(opts.promptFile)
	if err != nil {
		fmt.Fprintln(stderr, "effort-bench:", err)
		return 2
	}
	repo := filepath.Join(opts.workdir, "bench-repo")

	if opts.dryRun {
		fmt.Fprintf(stdout, "dry run: %d Codex runs (seed %d, order %s), nothing executed\n", len(plan), opts.seed, opts.order)
		fmt.Fprintf(stdout, "model: %s\nprompt (%d bytes): %s\n\n", emptyOr(opts.model, "(Codex default; pass --model to pin it)"), len(prompt), strings.TrimSpace(prompt))
		for i, p := range plan {
			fmt.Fprintf(stdout, "%2d. rep %d  %-7s %-11s %s\n", i+1, p.Rep, p.Effort, p.Kind, red.String(strings.Join(codexArgv(opts, p.Effort, repo), " ")))
		}
		fmt.Fprintf(stdout, "\nbudget guard: stop after a run once tokens > %d or the 5-hour window is >= %.0f%% used\n", opts.maxTokens, opts.stopAtUsedPct)
		fmt.Fprintf(stdout, "results would go to %s\n", red.String(opts.out))
		return 0
	}

	if _, err := exec.LookPath(opts.codex); err != nil {
		fmt.Fprintf(stderr, "effort-bench: Codex CLI %q not found: %v\n", opts.codex, err)
		return 1
	}
	if _, err := exec.LookPath("git"); err != nil {
		fmt.Fprintln(stderr, "effort-bench: git is required to reset the throwaway repo")
		return 1
	}
	logs := filepath.Join(opts.workdir, "logs")
	for _, d := range []string{opts.out, logs} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			fmt.Fprintln(stderr, "effort-bench:", err)
			return 1
		}
	}

	var results []Result
	var tokens int64
	stopped := ""
	for i, p := range plan {
		fmt.Fprintf(stderr, "[%d/%d] rep %d %s %s ... ", i+1, len(plan), p.Rep, p.Effort, p.Kind)
		r := runOne(opts, p, i+1, repo, logs, prompt)
		r.Error = red.String(r.Error)
		results = append(results, r)
		tokens += r.Usage.Total
		if r.Error != "" {
			fmt.Fprintf(stderr, "error: %s\n", r.Error)
		} else {
			fmt.Fprintf(stderr, "%s tokens, %.1f%% cache hit, %.0fs\n", comma(r.Usage.Total), r.CacheHitPct(), r.WallSeconds)
		}
		if tokens > opts.maxTokens {
			stopped = fmt.Sprintf("stopped after run %d: %s tokens used, over --max-tokens %s", i+1, comma(tokens), comma(opts.maxTokens))
		}
		if r.UsedPctEnd != nil && *r.UsedPctEnd >= opts.stopAtUsedPct {
			stopped = fmt.Sprintf("stopped after run %d: the 5-hour window is %.0f%% used (--stop-at-used-percent %.0f)", i+1, *r.UsedPctEnd, opts.stopAtUsedPct)
		}
		if stopped != "" {
			fmt.Fprintln(stderr, stopped)
			break
		}
	}

	rep := newReport(opts, results, prompt)
	if stopped != "" {
		rep.Notes = append(rep.Notes, stopped)
	}
	crossCheck(opts, &rep)
	if err := rep.writeAll(opts.out, red); err != nil {
		fmt.Fprintln(stderr, "effort-bench:", err)
		return 1
	}
	rep.writeMarkdown(stdout, red)
	// The real path goes to stderr only, so a screenshot of stdout stays redacted.
	fmt.Fprintf(stderr, "\nwrote %s (summary.md, results.csv, results.json, chart.html)\n", opts.out)
	return 0
}

func parseFlags(args []string, stderr io.Writer) (options, error) {
	var o options
	fs := flag.NewFlagSet("effort-bench", flag.ContinueOnError)
	fs.SetOutput(stderr)
	efforts := fs.String("efforts", "low,medium,high,xhigh", "comma-separated model_reasoning_effort values")
	fs.IntVar(&o.reps, "reps", 2, "repetitions per effort")
	fs.StringVar(&o.model, "model", "", "model passed to codex exec -m (pin it; the same for every run)")
	fs.StringVar(&o.codex, "codex", "codex", "Codex CLI binary")
	fs.StringVar(&o.codexHome, "codex-home", "", "Codex home (default $CODEX_HOME or ~/.codex)")
	fs.StringVar(&o.promptFile, "prompt", "", "prompt file (default: the built-in prompt.txt)")
	fs.StringVar(&o.workdir, "workdir", "", "scratch directory for the throwaway repo and logs (default: a temp dir)")
	fs.StringVar(&o.out, "out", "", "results directory (default: <workdir>/results-<time>)")
	fs.Uint64Var(&o.seed, "seed", 1, "seed for the per-rep run order")
	fs.StringVar(&o.order, "order", "shuffle", "run order inside each rep: shuffle or fixed")
	fs.BoolVar(&o.warmRepeat, "warm-repeat", false, "repeat each run immediately once more and report it separately (doubles the cost)")
	fs.IntVar(&o.maxRuns, "max-runs", 10, "refuse to start a matrix with more runs than this")
	fs.Int64Var(&o.maxTokens, "max-tokens", 4_000_000, "stop once the runs so far used more tokens than this")
	fs.Float64Var(&o.stopAtUsedPct, "stop-at-used-percent", 80, "stop once Codex reports the 5-hour window at least this % used")
	fs.DurationVar(&o.timeout, "timeout", 20*time.Minute, "time limit per Codex run")
	fs.BoolVar(&o.dryRun, "dry-run", false, "print the plan and Codex commands without running anything")
	fs.BoolVar(&o.noRedact, "no-redact", false, "keep paths, user name, host name and full session ids in the output")
	fs.BoolVar(&o.userConfig, "use-user-config", false, "load ~/.codex/config.toml (default: --ignore-user-config, so MCP servers and profiles do not skew runs)")
	var extra listFlag
	fs.Var(&extra, "codex-arg", "extra argument for codex exec (repeatable)")
	fs.StringVar(&o.firekeeper, "firekeeper", "", "firekeeper binary for the cross-check (default: firekeeper on PATH; \"off\" to skip)")
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	if fs.NArg() > 0 {
		return o, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	o.codexArgs = extra
	for _, e := range strings.Split(*efforts, ",") {
		if e = strings.TrimSpace(e); e != "" {
			o.efforts = append(o.efforts, e)
		}
	}
	if len(o.efforts) == 0 || o.reps < 1 {
		return o, errors.New("need at least one effort and --reps >= 1")
	}
	if o.order != "shuffle" && o.order != "fixed" {
		return o, fmt.Errorf("--order must be shuffle or fixed, not %q", o.order)
	}
	if o.codexHome == "" {
		o.codexHome = os.Getenv("CODEX_HOME")
	}
	if o.codexHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return o, err
		}
		o.codexHome = filepath.Join(home, ".codex")
	}
	if o.workdir == "" {
		o.workdir = filepath.Join(os.TempDir(), "firekeeper-effort-bench")
	}
	if o.out == "" {
		o.out = filepath.Join(o.workdir, "results-"+time.Now().Format("20060102-150405"))
	}
	return o, nil
}

// Planned is one Codex run in the matrix.
type Planned struct {
	Rep    int
	Effort string
	Kind   string // "run", or "warm-repeat" for the immediate repeat
}

// buildPlan lists the runs: every effort once per rep, shuffled inside each
// rep (seeded, so the order is reproducible) to spread drift in time across
// efforts.
func buildPlan(o options) []Planned {
	rng := rand.New(rand.NewPCG(o.seed, 0x9e3779b97f4a7c15))
	var plan []Planned
	for rep := 1; rep <= o.reps; rep++ {
		order := append([]string(nil), o.efforts...)
		if o.order == "shuffle" {
			rng.Shuffle(len(order), func(i, j int) { order[i], order[j] = order[j], order[i] })
		}
		for _, e := range order {
			plan = append(plan, Planned{Rep: rep, Effort: e, Kind: "run"})
			if o.warmRepeat {
				plan = append(plan, Planned{Rep: rep, Effort: e, Kind: "warm-repeat"})
			}
		}
	}
	return plan
}

func loadPrompt(path string) (string, error) {
	if path == "" {
		b, err := assets.ReadFile("prompt.txt")
		return string(b), err
	}
	b, err := os.ReadFile(path)
	return string(b), err
}

// codexArgv is the codex exec command line for one run; the prompt goes on
// stdin ("-").
func codexArgv(o options, effort, repo string) []string {
	argv := []string{o.codex, "exec", "--json"}
	if o.model != "" {
		argv = append(argv, "-m", o.model)
	}
	argv = append(argv, "-c", "model_reasoning_effort="+effort, "--sandbox", "workspace-write")
	if !o.userConfig {
		argv = append(argv, "--ignore-user-config")
	}
	argv = append(argv, o.codexArgs...)
	return append(argv, "-C", repo, "-")
}

// resetRepo recreates the throwaway repo at the same path with the same
// files and the same commit, so every run sees an identical starting state
// (and an identical cwd in Codex's environment context).
func resetRepo(repo string) error {
	// Git object files are read-only, which stops RemoveAll on Windows.
	_ = filepath.WalkDir(repo, func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			_ = os.Chmod(path, 0o600)
		}
		return nil
	})
	if err := os.RemoveAll(repo); err != nil {
		return err
	}
	if err := os.MkdirAll(repo, 0o700); err != nil {
		return err
	}
	err := fs.WalkDir(assets, "seed", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := assets.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(repo, strings.TrimPrefix(path, "seed/")), b, 0o600)
	})
	if err != nil {
		return err
	}
	env := append(os.Environ(),
		"GIT_AUTHOR_DATE=2026-01-01T00:00:00Z", "GIT_COMMITTER_DATE=2026-01-01T00:00:00Z",
		"GIT_CONFIG_NOSYSTEM=1")
	for _, args := range [][]string{
		{"init", "-q"},
		{"add", "-A"},
		{"-c", "user.name=effort-bench", "-c", "user.email=bench@example.invalid", "-c", "commit.gpgsign=false",
			"commit", "-q", "--no-verify", "-m", "seed"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = env
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("git %s: %v: %s", args[0], err, bytes.TrimSpace(out))
		}
	}
	return nil
}

func runOne(o options, p Planned, n int, repo, logs, prompt string) Result {
	r := Result{Run: n, Rep: p.Rep, Effort: p.Effort, Kind: p.Kind, ModelRequested: o.model}
	if err := resetRepo(repo); err != nil {
		r.Error = "reset repo: " + err.Error()
		return r
	}
	argv := codexArgv(o, p.Effort, repo)
	ctx, cancel := context.WithTimeout(context.Background(), o.timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = repo
	cmd.Stdin = strings.NewReader(prompt)
	cmd.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1", "CODEX_HOME="+o.codexHome)
	var stdout bytes.Buffer
	stderrFile, err := os.Create(filepath.Join(logs, fmt.Sprintf("run-%02d.stderr", n)))
	if err == nil {
		defer stderrFile.Close()
		cmd.Stderr = stderrFile
	}
	cmd.Stdout = &stdout
	start := time.Now()
	runErr := cmd.Run()
	r.WallSeconds = time.Since(start).Seconds()
	_ = os.WriteFile(filepath.Join(logs, fmt.Sprintf("run-%02d.jsonl", n)), stdout.Bytes(), 0o600)
	if runErr != nil {
		r.Error = "codex exec: " + runErr.Error()
		if ctx.Err() != nil {
			r.Error = "codex exec timed out after " + o.timeout.String()
		}
	}

	id, execUsage := ThreadIDFromExecJSON(bytes.NewReader(stdout.Bytes()))
	path, err := FindRollout(o.codexHome, id)
	if err != nil {
		path, err = newestRollout(o.codexHome, start)
	}
	if err != nil {
		r.Error = joinErr(r.Error, "find rollout: "+err.Error())
		if execUsage != nil {
			r.Usage = *execUsage // codex exec's own turn usage, no rollout detail
			r.Source = "codex-exec-json"
		}
		return r
	}
	st, err := ParseRolloutFile(path)
	if err != nil {
		r.Error = joinErr(r.Error, "read rollout: "+err.Error())
		return r
	}
	r.fill(st)
	r.Source = "rollout"
	r.TestsPass = runTests(repo)
	return r
}

// newestRollout is the fallback when the thread id is unknown: the newest
// rollout written since the run started.
func newestRollout(codexHome string, since time.Time) (string, error) {
	matches, _ := filepath.Glob(filepath.Join(codexHome, "sessions", "*", "*", "*", "rollout-*.jsonl"))
	best, bestAt := "", since.Add(-time.Second)
	for _, m := range matches {
		if info, err := os.Stat(m); err == nil && info.ModTime().After(bestAt) {
			best, bestAt = m, info.ModTime()
		}
	}
	if best == "" {
		return "", errors.New("no rollout written during the run")
	}
	return best, nil
}

// runTests reports whether the task's tests pass after the run, or nil when
// python3 is not available.
func runTests(repo string) *bool {
	py, err := exec.LookPath("python3")
	if err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, py, "-m", "unittest", "-q")
	cmd.Dir = repo
	cmd.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1")
	ok := cmd.Run() == nil
	return &ok
}

func joinErr(a, b string) string {
	if a == "" {
		return b
	}
	return a + "; " + b
}

func emptyOr(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}
