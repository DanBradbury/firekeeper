// Command effort-bench runs one fixed coding task through an agent CLI
// (`codex exec`, optionally `opencode run`) at several reasoning efforts and
// compares the token use of each run: fresh input, cache reads, cache
// writes, output, reasoning output and cache-hit rate, per run and per
// request. Runs accumulate in a local store across batches, 5-hour windows
// and days; `report` aggregates them with summary statistics and pairwise
// tests. Everything is read from local records and written to local files;
// the benchmark itself sends nothing anywhere, though each agent run of
// course talks to its model.
//
//	go run ./scripts/effort-bench --model <model> --reps <n>
//	go run ./scripts/effort-bench --resume
//	go run ./scripts/effort-bench report
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

// Recommended scale: 50 runs per effort detects a 5-point cache-hit
// difference at sd ~9 (see README); four efforts make 200 runs of ~100K
// tokens. The caps default to that matrix with headroom.
const (
	defaultMaxRuns   = 200
	defaultMaxTokens = 25_000_000
)

type options struct {
	harnesses     []string
	efforts       []string
	reps          int
	model         string
	codex         string
	codexHome     string
	opencode      string
	promptFile    string
	workdir       string
	store         string
	out           string
	seed          uint64
	order         string
	warmRepeat    bool
	maxRuns       int
	batch         int
	maxTokens     int64
	stopAtUsedPct float64
	timeout       time.Duration
	dryRun        bool
	resume        bool
	newBatch      bool
	noRedact      bool
	userConfig    bool
	codexArgs     []string
	opencodeArgs  []string
	firekeeper    string
	// report filters
	filterModel, filterHarness, filterBatch string
}

type listFlag []string

func (l *listFlag) String() string     { return strings.Join(*l, " ") }
func (l *listFlag) Set(v string) error { *l = append(*l, v); return nil }

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "report" {
		return runReport(args[1:], stdout, stderr)
	}
	opts, err := parseFlags(args, stderr, false)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		fmt.Fprintln(stderr, "effort-bench:", err)
		return 2
	}
	red := newRedactor(opts)
	stored, err := loadRuns(opts.store)
	if err != nil {
		fmt.Fprintln(stderr, "effort-bench: read store:", err)
		return 1
	}

	plan, err := loadPlan(opts.store)
	if err != nil {
		fmt.Fprintln(stderr, "effort-bench:", err)
		return 1
	}
	switch {
	case opts.resume:
		if plan == nil {
			fmt.Fprintln(stderr, "effort-bench: nothing to resume: no plan in the store")
			return 2
		}
		// The batch keeps its own settings.
		opts.model, opts.efforts, opts.harnesses, opts.reps = plan.Model, plan.Efforts, plan.Harnesses, plan.Reps
	default:
		if plan != nil && len(remaining(plan, stored)) > 0 && !opts.newBatch && !opts.dryRun {
			fmt.Fprintf(stderr, "effort-bench: batch %s still has %d of %d runs left; continue it with --resume, or start another with --new\n",
				plan.Batch, len(remaining(plan, stored)), len(plan.Items))
			return 2
		}
		prompt, err := loadPrompt(opts.promptFile)
		if err != nil {
			fmt.Fprintln(stderr, "effort-bench:", err)
			return 2
		}
		plan = buildPlan(opts, prompt, time.Now())
		if opts.maxRuns > 0 && len(plan.Items) > opts.maxRuns {
			fmt.Fprintf(stderr, "effort-bench: the matrix is %d runs, over --max-runs %d; lower --reps or --efforts, or raise --max-runs (0 = no limit)\n", len(plan.Items), opts.maxRuns)
			return 2
		}
	}
	hs := map[string]harness{}
	for _, name := range plan.Harnesses {
		h, err := harnessFor(name)
		if err != nil {
			fmt.Fprintln(stderr, "effort-bench:", err)
			return 2
		}
		hs[name] = h
	}
	todo := remaining(plan, stored)
	repo := filepath.Join(opts.workdir, "bench-repo")

	if opts.dryRun {
		fmt.Fprintf(stdout, "dry run: batch of %d runs (%d left), harness %s, seed %d, order %s; nothing executed\n",
			len(plan.Items), len(todo), strings.Join(plan.Harnesses, ","), plan.Seed, plan.Order)
		fmt.Fprintf(stdout, "model: %s\nprompt %s (%d bytes): %s\n\n", emptyOr(plan.Model, "(harness default; pass --model to pin it)"), plan.PromptID, len(plan.Prompt), plan.Prompt)
		for i, p := range todo {
			if i == 20 && len(todo) > 24 {
				fmt.Fprintf(stdout, "... %d more\n", len(todo)-20)
				break
			}
			argv, _ := hs[p.Harness].Command(opts, p.Effort, repo, plan.Prompt)
			fmt.Fprintf(stdout, "%3d. rep %d  %-8s %-7s %-11s %s\n", p.Index+1, p.Rep, p.Harness, p.Effort, p.Kind, red.String(shellQuote(argv)))
		}
		fmt.Fprintf(stdout, "\nstops: %s\n", stopsLine(opts))
		fmt.Fprintf(stdout, "estimate at ~100K tokens per run: %s tokens for the runs left\n", comma(int64(len(todo))*100_000))
		fmt.Fprintf(stdout, "store: %s (%d runs stored)\n", red.String(opts.store), len(stored))
		return 0
	}

	if len(todo) == 0 {
		fmt.Fprintf(stderr, "batch %s is complete (%d runs)\n", plan.Batch, len(plan.Items))
		return writeStoreReport(opts, stdout, stderr, red)
	}
	if reason := windowBlocked(opts, stored, time.Now()); reason != "" {
		fmt.Fprintln(stderr, reason)
		fmt.Fprintln(stderr, resumeHint(opts))
		return 0
	}
	for name := range hs {
		bin := map[string]string{"codex": opts.codex, "opencode": opts.opencode}[name]
		if _, err := exec.LookPath(bin); err != nil {
			fmt.Fprintf(stderr, "effort-bench: %s CLI %q not found: %v\n", name, bin, err)
			return 1
		}
	}
	if _, err := exec.LookPath("git"); err != nil {
		fmt.Fprintln(stderr, "effort-bench: git is required to reset the throwaway repo")
		return 1
	}
	if !opts.resume {
		if err := savePlan(opts.store, plan); err != nil {
			fmt.Fprintln(stderr, "effort-bench: save plan:", err)
			return 1
		}
	}
	logs := filepath.Join(opts.workdir, "logs")
	if err := os.MkdirAll(logs, 0o700); err != nil {
		fmt.Fprintln(stderr, "effort-bench:", err)
		return 1
	}

	fmt.Fprintf(stderr, "batch %s: %d of %d runs left\n", plan.Batch, len(todo), len(plan.Items))
	var tokens int64
	ran, failures := 0, 0
	stopped := ""
	for _, p := range todo {
		fmt.Fprintf(stderr, "[%d/%d] rep %d %s %s %s ... ", p.Index+1, len(plan.Items), p.Rep, p.Harness, p.Effort, p.Kind)
		r := runOne(opts, hs[p.Harness], plan, p, repo, logs)
		r.Error = red.String(r.Error)
		if _, err := appendRun(opts.store, r); err != nil {
			fmt.Fprintln(stderr, "\neffort-bench: save run:", err)
			return 1
		}
		ran++
		tokens += r.Usage.Total
		if r.Error != "" {
			failures++
			fmt.Fprintf(stderr, "error: %s\n", r.Error)
		} else {
			failures = 0
			fmt.Fprintf(stderr, "%s tokens, %.1f%% cache hit, %.0fs\n", comma(r.Usage.Total), r.CacheHitPct(), r.WallSeconds)
		}
		switch {
		case opts.maxTokens > 0 && tokens > opts.maxTokens:
			stopped = fmt.Sprintf("stopped: %s tokens used in this session, over --max-tokens %s", comma(tokens), comma(opts.maxTokens))
		case opts.stopAtUsedPct > 0 && r.UsedPctEnd != nil && *r.UsedPctEnd >= opts.stopAtUsedPct:
			stopped = fmt.Sprintf("stopped: Codex reports the 5-hour window %.0f%% used (--stop-at-used-percent %.0f)%s",
				*r.UsedPctEnd, opts.stopAtUsedPct, resetsText(r.ResetsAt))
		case failures >= 3:
			stopped = "stopped: 3 runs in a row failed; check the logs in " + red.String(logs)
		case opts.batch > 0 && ran >= opts.batch:
			stopped = fmt.Sprintf("stopped: ran --batch %d", opts.batch)
		}
		if stopped != "" {
			break
		}
	}
	left := len(todo) - ran
	if stopped != "" && left > 0 {
		fmt.Fprintf(stderr, "\n%s. %d of %d runs in batch %s are left.\n%s\n", stopped, left, len(plan.Items), plan.Batch, resumeHint(opts))
	} else {
		fmt.Fprintf(stderr, "\nbatch %s complete\n", plan.Batch)
	}
	return writeStoreReport(opts, stdout, stderr, red)
}

func resetsText(t *time.Time) string {
	if t == nil {
		return ""
	}
	return ", resets " + t.Local().Format("Jan 2 15:04 MST")
}

func resumeHint(o options) string {
	cmd := "go run ./scripts/effort-bench --resume"
	if o.storeSet() {
		cmd += " --store " + shellQuote([]string{o.store})
	}
	return "Resume with: " + cmd
}

func (o options) storeSet() bool { return o.store != defaultStore() }

// windowBlocked refuses to start while the last stored Codex run reported
// the 5-hour window over the threshold and that window has not reset yet.
func windowBlocked(o options, runs []Result, now time.Time) string {
	if o.stopAtUsedPct <= 0 {
		return ""
	}
	for i := len(runs) - 1; i >= 0; i-- {
		r := runs[i]
		if r.UsedPctEnd == nil {
			continue
		}
		if *r.UsedPctEnd >= o.stopAtUsedPct && r.ResetsAt != nil && r.ResetsAt.After(now) {
			return fmt.Sprintf("not starting: the last run saw the 5-hour window %.0f%% used (--stop-at-used-percent %.0f)%s",
				*r.UsedPctEnd, o.stopAtUsedPct, resetsText(r.ResetsAt))
		}
		return ""
	}
	return ""
}

func stopsLine(o options) string {
	var parts []string
	if o.maxTokens > 0 {
		parts = append(parts, "after "+comma(o.maxTokens)+" tokens")
	}
	if o.stopAtUsedPct > 0 {
		parts = append(parts, fmt.Sprintf("when the 5-hour window reaches %.0f%%", o.stopAtUsedPct))
	}
	if o.batch > 0 {
		parts = append(parts, fmt.Sprintf("after %d runs", o.batch))
	}
	if len(parts) == 0 {
		return "none (runs the whole batch)"
	}
	return strings.Join(parts, ", ") + "; --resume continues"
}

func defaultStore() string {
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".firekeeper-effort-bench")
	}
	return filepath.Join(os.TempDir(), "firekeeper-effort-bench-store")
}

func parseFlags(args []string, stderr io.Writer, report bool) (options, error) {
	var o options
	name := "effort-bench"
	if report {
		name += " report"
	}
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&o.store, "store", defaultStore(), "directory that accumulates runs (runs.jsonl) and the current batch (plan.json)")
	fs.StringVar(&o.out, "out", "", "report directory (default: <store>/report)")
	fs.BoolVar(&o.noRedact, "no-redact", false, "keep paths, user name, host name and full session ids in the output")
	fs.StringVar(&o.firekeeper, "firekeeper", "", "firekeeper binary for the cross-check (default: firekeeper on PATH; \"off\" to skip)")
	var harnesses, efforts *string
	var extra, ocExtra listFlag
	if report {
		fs.StringVar(&o.filterModel, "model", "", "only runs of this model")
		fs.StringVar(&o.filterHarness, "harness", "", "only runs of this harness")
		fs.StringVar(&o.filterBatch, "batch", "", "only runs of this batch")
		fs.Uint64Var(&o.seed, "seed", 1, "seed for the bootstrap intervals")
	} else {
		harnesses = fs.String("harness", "codex", "comma-separated harnesses: codex, opencode")
		efforts = fs.String("efforts", "low,medium,high,xhigh", "comma-separated reasoning efforts")
		fs.IntVar(&o.reps, "reps", 2, "repetitions per effort (and harness)")
		fs.StringVar(&o.model, "model", "", "model for every run (Codex -m; OpenCode openai/<model>); pin it")
		fs.StringVar(&o.codex, "codex", "codex", "Codex CLI binary")
		fs.StringVar(&o.codexHome, "codex-home", "", "Codex home (default $CODEX_HOME or ~/.codex)")
		fs.StringVar(&o.opencode, "opencode", "opencode", "OpenCode CLI binary")
		fs.StringVar(&o.promptFile, "prompt", "", "prompt file (default: the built-in prompt.txt)")
		fs.StringVar(&o.workdir, "workdir", "", "scratch directory for the throwaway repo and logs (default: a temp dir)")
		fs.Uint64Var(&o.seed, "seed", 0, "seed for the run order (default: from the time)")
		fs.StringVar(&o.order, "order", "shuffle", "run order inside each rep: shuffle or fixed")
		fs.BoolVar(&o.warmRepeat, "warm-repeat", false, "repeat each run immediately once more and report it separately (doubles the cost)")
		fs.IntVar(&o.maxRuns, "max-runs", defaultMaxRuns, "refuse a batch with more runs than this (0 = no limit)")
		fs.IntVar(&o.batch, "batch", 0, "run at most this many runs now, then stop; --resume continues (0 = all)")
		fs.Int64Var(&o.maxTokens, "max-tokens", defaultMaxTokens, "stop once this session used more tokens than this (0 = no limit)")
		fs.Float64Var(&o.stopAtUsedPct, "stop-at-used-percent", 80, "stop once Codex reports the 5-hour window at least this % used (0 = never)")
		fs.DurationVar(&o.timeout, "timeout", 20*time.Minute, "time limit per run")
		fs.BoolVar(&o.dryRun, "dry-run", false, "print the plan and commands without running anything")
		fs.BoolVar(&o.resume, "resume", false, "continue the store's current batch with its own settings")
		fs.BoolVar(&o.newBatch, "new", false, "start a new batch even though the current one is unfinished")
		fs.BoolVar(&o.userConfig, "use-user-config", false, "Codex: load ~/.codex/config.toml (default: --ignore-user-config)")
		fs.Var(&extra, "codex-arg", "extra argument for codex exec (repeatable)")
		fs.Var(&ocExtra, "opencode-arg", "extra argument for opencode run (repeatable)")
	}
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	if fs.NArg() > 0 {
		return o, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if o.out == "" {
		o.out = filepath.Join(o.store, "report")
	}
	if report {
		return o, nil
	}
	o.codexArgs, o.opencodeArgs = extra, ocExtra
	o.efforts = splitList(*efforts)
	o.harnesses = splitList(*harnesses)
	if len(o.efforts) == 0 || len(o.harnesses) == 0 || o.reps < 1 {
		return o, errors.New("need at least one effort, one harness and --reps >= 1")
	}
	if o.order != "shuffle" && o.order != "fixed" {
		return o, fmt.Errorf("--order must be shuffle or fixed, not %q", o.order)
	}
	if o.resume && o.newBatch {
		return o, errors.New("use --resume or --new, not both")
	}
	if o.seed == 0 {
		o.seed = uint64(time.Now().UnixNano())
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
	return o, nil
}

func splitList(s string) []string {
	var out []string
	for _, e := range strings.Split(s, ",") {
		if e = strings.TrimSpace(e); e != "" {
			out = append(out, e)
		}
	}
	return out
}

// Planned is one run in a batch.
type Planned struct {
	Index   int    `json:"index"`
	Harness string `json:"harness"`
	Rep     int    `json:"rep"`
	Effort  string `json:"effort"`
	Kind    string `json:"kind"` // "run", or "warm-repeat" for the immediate repeat
}

// buildPlan lists the runs: every harness and effort once per rep, shuffled
// inside each rep (seeded, so the order is reproducible) to spread drift in
// time and cache state across efforts.
func buildPlan(o options, prompt string, now time.Time) *Plan {
	p := &Plan{
		Batch: newBatchID(now, o.seed), CreatedAt: now.UTC(), Harnesses: o.harnesses, Model: o.model,
		Efforts: o.efforts, Reps: o.reps, Seed: o.seed, Order: o.order,
		Prompt: strings.TrimSpace(prompt), PromptID: promptID(strings.TrimSpace(prompt)),
	}
	rng := rand.New(rand.NewPCG(o.seed, 0x9e3779b97f4a7c15))
	type cell struct{ h, e string }
	for rep := 1; rep <= o.reps; rep++ {
		var cells []cell
		for _, h := range o.harnesses {
			for _, e := range o.efforts {
				cells = append(cells, cell{h, e})
			}
		}
		if o.order == "shuffle" {
			rng.Shuffle(len(cells), func(i, j int) { cells[i], cells[j] = cells[j], cells[i] })
		}
		for _, c := range cells {
			p.Items = append(p.Items, Planned{Index: len(p.Items), Harness: c.h, Rep: rep, Effort: c.e, Kind: "run"})
			if o.warmRepeat {
				p.Items = append(p.Items, Planned{Index: len(p.Items), Harness: c.h, Rep: rep, Effort: c.e, Kind: "warm-repeat"})
			}
		}
	}
	return p
}

func loadPrompt(path string) (string, error) {
	if path == "" {
		b, err := assets.ReadFile("prompt.txt")
		return string(b), err
	}
	b, err := os.ReadFile(path)
	return string(b), err
}

// shellQuote joins argv for display, quoting arguments with spaces.
func shellQuote(argv []string) string {
	out := make([]string, len(argv))
	for i, a := range argv {
		if a == "" || strings.ContainsAny(a, " \t'\"$`\\") {
			a = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
		}
		out[i] = a
	}
	return strings.Join(out, " ")
}

// resetRepo recreates the throwaway repo at the same path with the same
// files and the same commit, so every run sees an identical starting state
// (and an identical cwd in the harness's environment context).
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

func runOne(o options, h harness, plan *Plan, p Planned, repo, logs string) Result {
	r := Result{
		Harness: h.Name(), Batch: plan.Batch, PlanIndex: p.Index, Rep: p.Rep, Effort: p.Effort, Kind: p.Kind,
		ModelRequested: plan.Model, PromptID: plan.PromptID, NotLogged: h.NotLogged(),
	}
	if err := resetRepo(repo); err != nil {
		r.Error = "reset repo: " + err.Error()
		return r
	}
	argv, stdin := h.Command(o, p.Effort, repo, plan.Prompt)
	ctx, cancel := context.WithTimeout(context.Background(), o.timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = repo
	cmd.Stdin = strings.NewReader(stdin)
	cmd.Env = append(append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1"), h.Env(o)...)
	var stdout bytes.Buffer
	name := fmt.Sprintf("%s-%03d", plan.Batch, p.Index)
	if f, err := os.Create(filepath.Join(logs, name+".stderr")); err == nil {
		defer f.Close()
		cmd.Stderr = f
	}
	cmd.Stdout = &stdout
	r.StartedAt = time.Now().UTC()
	runErr := cmd.Run()
	r.EndedAt = time.Now().UTC()
	r.WallSeconds = r.EndedAt.Sub(r.StartedAt).Seconds()
	_ = os.WriteFile(filepath.Join(logs, name+".jsonl"), stdout.Bytes(), 0o600)
	if runErr != nil {
		r.Error = h.Name() + ": " + runErr.Error()
		if ctx.Err() != nil {
			r.Error = h.Name() + " timed out after " + o.timeout.String()
		}
	}
	d, err := h.Collect(o, stdout.Bytes(), r.StartedAt)
	if err != nil {
		r.Error = joinErr(r.Error, err.Error())
		r.SessionID = d.SessionID
		return r
	}
	r.fill(d)
	r.TestsPass = runTests(repo)
	r.FirekeeperTotal = crossCheck(o, h, r.SessionID)
	return r
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

func runReport(args []string, stdout, stderr io.Writer) int {
	opts, err := parseFlags(args, stderr, true)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		fmt.Fprintln(stderr, "effort-bench report:", err)
		return 2
	}
	return writeStoreReport(opts, stdout, stderr, newRedactor(opts))
}

// writeStoreReport aggregates every stored run that matches the filters.
func writeStoreReport(o options, stdout, stderr io.Writer, red *redactor) int {
	runs, err := loadRuns(o.store)
	if err != nil {
		fmt.Fprintln(stderr, "effort-bench: read store:", err)
		return 1
	}
	var keep []Result
	for _, r := range runs {
		if (o.filterModel == "" || r.Model == o.filterModel || r.ModelRequested == o.filterModel) &&
			(o.filterHarness == "" || r.Harness == o.filterHarness) &&
			(o.filterBatch == "" || r.Batch == o.filterBatch) {
			keep = append(keep, r)
		}
	}
	if len(keep) == 0 {
		fmt.Fprintln(stderr, "effort-bench: no stored runs match")
		return 1
	}
	seed := o.seed
	if seed == 0 {
		seed = 1
	}
	rep := buildReport(keep, seed)
	if err := os.MkdirAll(o.out, 0o700); err != nil {
		fmt.Fprintln(stderr, "effort-bench:", err)
		return 1
	}
	if err := rep.writeAll(o.out, red); err != nil {
		fmt.Fprintln(stderr, "effort-bench:", err)
		return 1
	}
	rep.writeMarkdown(stdout, red)
	// The real path goes to stderr only, so a screenshot of stdout stays redacted.
	fmt.Fprintf(stderr, "\nwrote %s (summary.md, results.csv, results.json, chart.html) from %d stored runs\n", o.out, len(keep))
	return 0
}
