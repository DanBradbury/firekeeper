# effort-bench

Does Codex's reasoning effort change the **cache-read ratio**, or only the
**volume**? This runs one small, fixed coding task through `codex exec` at
each effort (default `low,medium,high,xhigh`, 2 reps each = 8 runs) and
compares, per run and as means per effort:

total tokens, fresh input, cache read, cache write, output, reasoning output,
cache-hit % (cache read ÷ input), model requests, tool calls, wall time,
whether the task's tests pass, and how far Codex's own 5-hour window moved.

All numbers come from the local Codex rollouts
(`$CODEX_HOME/sessions/YYYY/MM/DD/rollout-*-<thread id>.jsonl`), the same files
Firekeeper reads, and are cross-checked against `firekeeper window --provider
codex --json` when `firekeeper` is on `PATH`. The benchmark uploads nothing and
the HTML chart has no scripts or external requests. (The Codex runs themselves
talk to the model, as any Codex run does.)

## Run it

Prerequisites: Go (to `go run`), `git`, `python3` (to check the task's tests;
optional), and the Codex CLI, logged in. `firekeeper` on `PATH` is optional.

```sh
# from a firekeeper checkout
go run ./scripts/effort-bench --dry-run --model <model>   # see the plan and exact commands, runs nothing
go run ./scripts/effort-bench --model <model>             # the 8-run matrix
```

Pin `--model` to the model you want to talk about (the same one for every run).
Results land in `$TMPDIR/firekeeper-effort-bench/results-<time>/`:

- `summary.md`: the means table and every run (also printed to stdout)
- `results.csv`, `results.json`: per-run numbers
- `chart.html`: stacked token bars, cache-hit % and reasoning tokens per effort

Output is redacted by default: the throwaway repo, work dir, Codex home, home
directory, user name and host name are replaced, and session ids are cut to
their last 8 characters. `--no-redact` keeps them. The real results path is
printed on stderr only.

### Flags

| flag | default | |
|---|---|---|
| `--model` | Codex default | `codex exec -m`; pin it |
| `--efforts` | `low,medium,high,xhigh` | values for `-c model_reasoning_effort=...`; levels depend on the model |
| `--reps` | 2 | runs per effort |
| `--order` | `shuffle` | shuffles efforts inside each rep (seeded by `--seed`, so it is reproducible); `fixed` keeps the list order |
| `--warm-repeat` | off | after each run, repeat it immediately and report the repeat separately (doubles the cost) |
| `--max-runs` | 10 | refuses to start a bigger matrix |
| `--max-tokens` | 4,000,000 | stops after the run that crosses it |
| `--stop-at-used-percent` | 80 | stops once Codex reports the 5-hour window at least this full |
| `--timeout` | 20m | per run |
| `--use-user-config` | off | by default runs pass `--ignore-user-config` so your MCP servers, profiles and model settings do not leak into the comparison |
| `--codex-arg` | | extra `codex exec` argument, repeatable |
| `--codex`, `--codex-home`, `--workdir`, `--out`, `--prompt`, `--firekeeper` | | binaries and paths; `--firekeeper off` skips the cross-check |

## What one run does

1. Recreates `<workdir>/bench-repo` from `seed/` (a 20-line Python module with
   two stubbed functions and a unittest file) with a fixed commit date, so
   every run starts from an identical tree at the same path.
2. Runs, with `prompt.txt` on stdin:
   `codex exec --json -m <model> -c model_reasoning_effort=<effort> --sandbox workspace-write --ignore-user-config -C <bench-repo> -`
   Each `codex exec` is a new session (new thread id).
3. Takes the thread id from the `thread.started` event, finds that rollout,
   and sums `last_token_usage` over its `token_count` events (Firekeeper's rule),
   checking it against Codex's running `total_token_usage`. It also reads the
   model and effort Codex actually used from `turn_context`, the first
   request's cached tokens (cold or warm start), and `rate_limits.primary.used_percent`.
4. Runs `python3 -m unittest -q` in the repo to record whether the task was solved.

## Reading the numbers

- Codex's `input_tokens` includes cached tokens. Following OpenAI's prompt
  caching guide, input = ordinary + cache read + cache write, so here
  **fresh input = input − cache read − cache write** and
  fresh + cache read + cache write + output = total.
- **cache-hit % = cache read ÷ input**, summed over a run (and over runs for the means).
- Reasoning tokens are part of output. Whether earlier reasoning is carried
  into later requests' input depends on the model and Codex version, and higher
  effort can also change the number of requests and tool calls, which changes
  how much (mostly cached) prefix is re-sent. So compare `requests` and input
  per effort, not only output.
- A run's first request is usually **cold** (nothing cached yet); later
  requests re-send the growing conversation and hit the cache. Short tasks
  therefore show lower hit rates than a long interactive session like an
  11M-token 5-hour window, where almost all input is a cached prefix.
- `5h window Δ%` is the change in Codex's own primary rate-limit
  `used_percent` across the run, when Codex logged it. It is coarse and also
  moves with anything else using the same account at the time.

## Fairness

- Same model (pinned), same prompt, same repo state and path, same sandbox,
  user config ignored, a fresh session per run.
- Efforts are shuffled within each rep so time-of-day drift and cache
  warm-up do not always favor the same effort. Use more reps for tighter means;
  agent runs vary a lot (the table shows min–max).
- Prompt-cache entries are kept for a minimum lifetime (30 minutes by default
  on current OpenAI models; older models differ), and a hit is never
  guaranteed. Runs back to back may find the shared system prompt cached from
  the previous run; `first request` in the run table shows whether that
  happened. `--warm-repeat` measures the immediate-repeat case deliberately.
- Stop other Codex use on the account during the matrix if you want the
  window Δ% to mean anything.

## Other harnesses

`firekeeper window --provider` can read codex, claude, copilot, cursor,
opencode, gemini and kimi transcripts, so the same idea works for any harness
that can run the same OpenAI model (for example Cursor CLI or OpenCode). It is
not built in: each harness sends its own system prompt and tool schemas, so the
cached prefix size, request count and therefore cache-hit % differ for reasons
unrelated to the model, and not every harness exposes a reasoning-effort
setting or logs cache writes and reasoning tokens. Treat a cross-harness
comparison as "what this harness costs for this task", not as a model
property. Claude Code runs a different model and is not comparable here.

## Cost

An estimate, not a measurement: a run of this task is roughly 4–10 model
requests over a 15–30k token context, so about 100–250k tokens, 80–90% of
it cache reads, with output from a few thousand tokens (low) to a few tens of
thousands (xhigh). The default 8-run matrix is therefore about 1–2M tokens.
Run with `--reps 1` first to see the real numbers; `--max-tokens` and
`--stop-at-used-percent` cap a run that goes long.

## Tests

`go test ./scripts/effort-bench` parses a fixture rollout and runs the whole
matrix end to end against a fake `codex` (the test binary), including the
redaction checks. It never runs the real Codex.
