# effort-bench

Does reasoning effort change the **cache-read ratio**, or only the **volume**?
This runs one small, fixed coding task through `codex exec` (and optionally
`opencode run`) at each reasoning effort, many times, and compares per run,
per request and per effort:

total tokens, fresh input, cache read, cache write, output, reasoning output,
cache-hit % (cache read ÷ input), where in the run the cache misses happen,
model requests, tool calls, wall time, whether the task's tests pass, the gap
since the previous run, the local time of day, and how far Codex's own 5-hour
window moved.

Runs accumulate in a local **store** across batches, 5-hour windows and days.
`report` aggregates everything in it: n, mean, median, sd and 95% bootstrap
intervals per effort, pairwise Mann-Whitney U tests with Holm correction,
and bootstrap intervals for mean differences.

Numbers come from the harness's local records: Codex rollouts
(`$CODEX_HOME/sessions/YYYY/MM/DD/rollout-*-<thread id>.jsonl`, the files
Firekeeper reads) and OpenCode's database. Each run's total is cross-checked
against `firekeeper window --provider <harness> --json` when `firekeeper` is on
`PATH`. The benchmark uploads nothing and the HTML has no scripts or external
requests. (The agent runs themselves talk to the model, as any run does.)

## Run it

Prerequisites: Go, `git`, `python3` (checks the task's tests; optional), the
Codex CLI logged in. Optional: `firekeeper` on `PATH`; OpenCode 2.x for
`--harness opencode`.

```sh
# from a firekeeper checkout
go run ./scripts/effort-bench --dry-run --model gpt-6.1-sol --reps 50   # plan and commands, runs nothing
go run ./scripts/effort-bench --model gpt-6.1-sol --reps 50             # starts the batch; stops by itself at 80% of the 5-hour window
go run ./scripts/effort-bench --resume                                  # next window: continue the same batch
go run ./scripts/effort-bench report --model gpt-6.1-sol                # aggregate everything stored, run nothing
```

The batch is planned once (efforts shuffled inside each rep with a recorded
seed) and saved to the store. A run that stops (5-hour window, `--batch`,
`--max-tokens`, three failures in a row, Ctrl-C) prints how to resume; `--resume`
picks up exactly the runs that are left with the batch's own settings, and
refuses to start while the last run saw the window above the threshold and the
window has not reset yet. Starting a different batch while one is unfinished
needs `--new`. Runs are deduplicated by harness and session id.

Store: `~/.firekeeper-effort-bench/` (`runs.jsonl`, `plan.json`; `--store`
changes it). Each batch run and `report` write `<store>/report/`:
`summary.md` (also printed), `results.csv` (one row per run),
`requests.csv` (one row per model request), `results.json`, and `chart.html`.

Output is redacted by default: the throwaway repo, work dir, store, Codex home,
temp dir, home directory, user name and host name are replaced, and session ids
are cut to their last 8 characters (`--no-redact` keeps them). Local time of
day is shown. The store itself keeps full session ids (to deduplicate and
match) and redacted error text, and no paths; `plan.json` keeps the
benchmark's own prompt so `--resume` sends the same text.

### Flags

| flag | default | |
|---|---|---|
| `--model` | harness default | pin it; Codex `-m`, OpenCode `openai/<model>` |
| `--efforts` | `low,medium,high,xhigh` | Codex `-c model_reasoning_effort=…`, OpenCode `--model …#<effort>` |
| `--reps` | 2 | runs per effort (and harness) in the batch |
| `--harness` | `codex` | comma list: `codex`, `opencode` (see below) |
| `--max-runs` | 200 | refuse a bigger batch; 0 = no limit |
| `--batch` | 0 | run at most this many now, then stop; 0 = all |
| `--max-tokens` | 25,000,000 | stop once this invocation used more; 0 = no limit |
| `--stop-at-used-percent` | 80 | stop once Codex reports the 5-hour window this full; 0 = never |
| `--resume`, `--new` | | continue the stored batch / start another while one is unfinished |
| `--order`, `--seed` | `shuffle`, from the clock | order inside each rep; the seed is saved in the plan |
| `--warm-repeat` | off | repeat each run immediately and report repeats separately (doubles the cost) |
| `--timeout` | 20m | per run |
| `--use-user-config` | off | Codex runs pass `--ignore-user-config` unless this is set |
| `--codex-arg`, `--opencode-arg` | | extra CLI arguments, repeatable |
| `--store`, `--workdir`, `--codex`, `--codex-home`, `--opencode`, `--prompt`, `--firekeeper` | | paths and binaries; `--firekeeper off` skips the cross-check |

`report` takes `--store`, `--out`, `--model`, `--harness`, `--batch`,
`--seed` (bootstrap) and `--no-redact`.

## What one run does

1. Recreates `<workdir>/bench-repo` from `seed/` (a small Python module with two
   stubbed functions and a unittest file) with a fixed commit date, so every run
   starts from an identical tree at the same path.
2. Codex: `codex exec --json -m <model> -c model_reasoning_effort=<effort> --sandbox workspace-write --ignore-user-config -C <bench-repo> -`
   with `prompt.txt` on stdin. Each run is a new session.
3. Finds the session from the `thread.started` event, reads its rollout: every
   `token_count` event's `last_token_usage` is one request (Firekeeper's rule),
   checked against Codex's running total; the model and effort Codex used come
   from `turn_context`, the window from `rate_limits.primary`
   (`used_percent`, `resets_at`).
4. Runs `python3 -m unittest -q` to record whether the task was solved, and
   appends the run to the store.

## Reading the report

- input = ordinary + cache read + cache write (OpenAI's prompt caching guide),
  so **fresh = input − cache read − cache write**, and
  **cache-hit % = cache read ÷ input**. Reasoning is part of output.
- **Statistics**: means with 95% percentile-bootstrap intervals (4,000
  resamples, seeded, so a report is reproducible). Below 5 runs per effort the
  report prints no interval and no test verdict and says the sample is too
  small.
- **Pairwise**: two-sided Mann-Whitney U (normal approximation with tie and
  continuity corrections) for cache-hit % and total tokens, Holm-corrected
  across all effort pairs of each metric, plus a bootstrap interval for the
  difference in means. Mann-Whitney compares whole distributions, which suits
  cache-hit %: it is bimodal (most runs near the top, some with a dip).
- **Where the misses are**: for request #1, #2, … (#11+ pooled), the cached
  share and how much of the effort's uncached input lands there; a run with a
  *later miss* had a request after the first that was under half cached. This
  tells first-request (cold start) dips from mid-run cache losses.
- **Gap and time of day**: each run's gap since the previous run of the same
  harness and model (same prompt prefix; effort is a request setting, not part
  of the prompt) and since the previous run of the same effort, with
  cache-hit % binned by gap and by local time, and a scatter of cache-hit %
  against the gap in `chart.html`. OpenAI keeps a cache entry for at least 30
  minutes by default and never guarantees a hit, so gaps beyond that should
  show more cold starts.
- **5h Δ%** is the mean change in Codex's `used_percent` across a run. It is
  coarse and also moves with any other use of the account, so pause other
  Codex use during a batch.

## How many runs

From the first 8 runs on gpt-6.1-sol: totals were tight (~100–102K), while
cache-hit % had an sd of roughly 8–10 points because of occasional dips. To
detect a 5-point difference in mean cache-hit % (two-sided α = 0.05, 80%
power, normal approximation; Mann-Whitney needs about 5% more for normal data
and can need fewer for dip-heavy data):

    n per effort ≈ 2 (z₀.₉₇₅ + z₀.₈)² σ² / δ² = 15.7 σ² / 25

| sd σ | one pre-chosen comparison (e.g. low vs xhigh) | all 6 pairs, Holm (α/6 for the smallest p) |
|---|---|---|
| 8 | 40 | 62 |
| 9 | 51 | 79 |
| 10 | 63 | 97 |

Recommendation: **50 per effort** (`--reps 50`, 200 runs over four efforts, the
default caps) with low vs xhigh as the primary question, which is the X post's
question; treat the other pairs as exploratory. For every pair to carry a 5-point
claim, use about 80 (`--reps 80 --max-runs 320`). Total tokens need far fewer
runs: their spread is small.

## Cost

Measured: about 100K tokens per run (gpt-6.1-sol, this task). So 50 runs per
effort over four efforts is about 20M tokens; low and xhigh only
(`--efforts low,xhigh --reps 50`) about 10M. At OpenAI's published API prices
for gpt-6.1-sol ($2 input, $0.10 cached input, $2.50 cache write (30m), $10
output per 1M tokens) a run with ~86% cache hits is roughly $0.05–0.08, so
200 runs are roughly $9–16 API-equivalent. On a ChatGPT plan the 5-hour-window
share per run is not published: read the `5h Δ%` column after the first batch;
runs per window ≈ (80 − starting used %) ÷ Δ% per run.

## Harnesses

- **codex** (default): everything is logged, including effort, cache writes,
  reasoning tokens and the 5-hour window.
- **opencode**: `opencode run --format json --model openai/<model>#<effort> --auto "<prompt>"`
  in the throwaway repo (flags verified in OpenCode 2.3.2's source; `--auto`
  approves its tool calls and OpenCode has no sandbox like Codex's, so it runs
  commands in the throwaway repo unsandboxed). Tokens come from OpenCode's
  database, located with Firekeeper's OpenCode code and read with Firekeeper's
  SQLite runner: one assistant message per request, with input, output,
  reasoning, cache read and cache write; the effort is the recorded model
  variant. Needs OpenCode 2.x (the `session_v2` schema Firekeeper supports) and
  the model available to OpenCode's `openai` provider (`opencode models openai`).
  OpenCode does not log Codex's 5-hour window (n/a), and loads its own config.
- **cursor**: not supported. Cursor CLI runs headless (`-p`, `--model`,
  `--output-format json|stream-json`), but per its docs the JSON carries no
  token usage, and per Firekeeper's Cursor notes its local transcripts and chat
  databases hold no token counts. TODO if Cursor starts logging usage.

Each harness sends its own system prompt and tool definitions, so the cached
prefix, the number of requests, and therefore cache-hit % differ for reasons
unrelated to the model. The report keeps harnesses in separate sections and
never tests one against another: read a second harness as "what this harness
costs for this task".

## Fairness

- Same model (pinned), prompt (its hash is stored per run), repo state and
  path, sandbox, Codex user config ignored, and a fresh session per run.
- Efforts are shuffled inside every rep, so time of day, cache state and
  service drift spread across efforts; the gap and time-of-day tables show
  whether those matter.
- Batches across days accumulate; filter with `report --batch` or `--model`
  when settings changed.

## Tests

`go test ./scripts/effort-bench` covers the rollout and OpenCode parsers, the
per-request breakdown, the statistics (Mann-Whitney against Wikipedia's worked
tortoise-and-hare examples and a hand-counted tie case, Holm, bootstrap), and
end-to-end batches against fake `codex` and `opencode` CLIs (the test binary):
batch stop and resume, the 5-hour-window stop, appending and deduplication,
report aggregation, and redaction of every output file. It never runs a real
agent.
