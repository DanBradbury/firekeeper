# Opt-in local OpenTelemetry receiver

Research for [issue #21](https://github.com/DanBradbury/firekeeper/issues/21).
This note answers the issue's questions from Anthropic's Claude Code
monitoring docs, then describes the experimental commands on this branch.
Those commands are off unless you run them. They listen on `127.0.0.1` only
and do not upload.

The Mac test at the bottom is the check this branch cannot do from CI: whether
Claude Code's hidden `agent_summary` calls show up in telemetry, and under
what `query_source` name.

## Recommendation

Build the experiment. Do not fold it into the dashboard or the reporter until
a real Claude Code session confirms the `query_source` string and the size of
the gap.

`firekeeper otel-listen` and `firekeeper otel-reconcile` are that experiment.
Plain `firekeeper` still starts no listener and uploads nothing. This branch
does not include the hook from the [Flamerix gist](https://gist.github.com/flamerix-ai/c3905705cc4205e5de3011fa2e74efce)
linked in the issue.

## 1. What Claude Code exports

Sources, read for this note:

- [Monitoring](https://code.claude.com/docs/en/monitoring-usage) (Claude Code Docs)
- [Observability with OpenTelemetry](https://code.claude.com/docs/en/agent-sdk/observability) (Agent SDK; the CLI is what exports)
- [Environment variables](https://code.claude.com/docs/en/env-vars)
- [OTLP exporter specification](https://opentelemetry.io/docs/specs/otel/protocol/exporter/) for how the endpoint URL is joined with `/v1/logs`

Telemetry is off until `CLAUDE_CODE_ENABLE_TELEMETRY=1`. Claude Code then
exports three independent signals. Metrics are counters. Events go out as
OpenTelemetry logs. Traces are a separate beta.

### Environment variables

| Variable | Role | Values that matter here |
| --- | --- | --- |
| `CLAUDE_CODE_ENABLE_TELEMETRY` | Required switch. | `1` |
| `OTEL_LOGS_EXPORTER` | Where events go. `api_request` is one of these events. | `otlp` (also `console`, `none`) |
| `OTEL_METRICS_EXPORTER` | Where counters go. Not required for per-request lines. | `otlp`, `prometheus`, `console`, `none` |
| `OTEL_EXPORTER_OTLP_PROTOCOL` | Protocol for every signal. Claude Code has no default, so this or the per-signal variable must be set. | `grpc`, `http/json`, `http/protobuf` |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | Base URL. For HTTP, the SDK appends `/v1/logs`, `/v1/metrics`, or `/v1/traces`. | `http://127.0.0.1:4318` |
| `OTEL_EXPORTER_OTLP_LOGS_PROTOCOL` | Overrides the protocol for logs. | same three values |
| `OTEL_EXPORTER_OTLP_LOGS_ENDPOINT` | Full logs URL, not a base. The signal path is not appended again. | `http://127.0.0.1:4318/v1/logs` |
| `OTEL_EXPORTER_OTLP_HEADERS` | Static headers. Not used for this local listener. | |
| `OTEL_LOGS_EXPORT_INTERVAL` | How often events are flushed, in milliseconds. Default 5000. | `5000` (docs also show `1000` while debugging) |
| `OTEL_METRIC_EXPORT_INTERVAL` | Metrics flush. Default 60000. | irrelevant when metrics are `none` |
| `OTEL_TRACES_EXPORTER` | Beta traces. Requires `CLAUDE_CODE_ENHANCED_TELEMETRY_BETA=1` as well. | leave unset |
| `OTEL_LOG_USER_PROMPTS` | Puts prompt text on `user_prompt` events. Default off. | leave unset |
| `OTEL_LOG_ASSISTANT_RESPONSES` | Puts assistant text on `assistant_response` events. Default off. | leave unset |
| `OTEL_LOG_TOOL_DETAILS` | Tool arguments and real custom names. Default off. | leave unset |
| `OTEL_LOG_TOOL_CONTENT` | Tool output on spans. Default off. Requires traces. | leave unset |
| `OTEL_LOG_RAW_API_BODIES` | Full Messages API request and response JSON, including the conversation. Default off. | leave unset |

The quick start in the monitoring doc uses `OTEL_EXPORTER_OTLP_PROTOCOL=grpc`
and `OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4317`. This listener speaks
OTLP/HTTP on `127.0.0.1:4318`, which is the HTTP port, so the protocol has to
be `http/protobuf` or `http/json`. gRPC is not accepted.

For `http/protobuf` and `http/json`, Claude Code sends `Content-Length`. The
listener also accepts `Content-Encoding: gzip`.

Claude Code does not pass `OTEL_*` variables to subprocesses it spawns (Bash,
hooks, MCP, language servers). A session started by a daemon that was launched
before the variables were exported will not export. Stop that daemon and start
`claude` from the shell that has the variables. The listener prints the block
to copy.

Managed settings and `~/.claude/settings.json` can set the same variables.
Project `.claude/settings.json` cannot turn telemetry on or choose a collector.
A project can set an exporter to `none`.

### `api_request` is a log event

The monitoring doc files it under **Events**, which are "exported via
OpenTelemetry logs/events (when `OTEL_LOGS_EXPORTER` is configured)".

- Event name: `claude_code.api_request`
- Attribute `event.name`: `"api_request"`
- Logged for each API request to Claude

It is not a metric. Two counters are incremented for the same request, and
they are the wrong place to look for the hidden calls:

| Metric | What it is |
| --- | --- |
| `claude_code.token.usage` | Counter of tokens. Attribute `type` is `input`, `output`, `cacheRead`, or `cacheCreation`. |
| `claude_code.cost.usage` | Counter of estimated USD cost. |

On those counters, `query_source` is only `"main"`, `"subagent"`, or
`"auxiliary"`. That cannot name `agent_summary`. Summing the counters and the
log events would count every request twice. This listener records log records
only, and only when `event.name` is exactly `api_request` (or the log record's
event name is `claude_code.api_request` and the attribute is absent).
`api_request_body` is a different event and is ignored. `user_prompt` is
ignored.

The same per-request token fields also exist on the beta `claude_code.llm_request`
span. The span's detailed `query_source` is gated by `ENABLE_BETA_TRACING_DETAILED`.
The log event's `query_source` is not. Traces are not required.

### Fields on the log event

From the monitoring doc's API request event. Standard attributes are attached
to every event as well. `session.id` is Claude Code's session id (the same id
as the transcript file name). `/clear` replaces it.

| Field | Meaning |
| --- | --- |
| `event.timestamp` | ISO 8601 |
| `event.sequence` | Per-process counter, not per session. A resumed session can repeat or rewind it. |
| `session.id` | Session id. On by default for events. |
| `model` | Model id for that request |
| `query_source` | Subsystem that issued the request. Documented examples: `repl_main_thread`, `compact`, or a subagent name. |
| `input_tokens` | Input tokens, excluding cache read and cache write |
| `output_tokens` | Output tokens |
| `cache_read_tokens` | Tokens read from the prompt cache |
| `cache_creation_tokens` | Tokens written to the prompt cache |
| `cache_creation_5m_tokens`, `cache_creation_1h_tokens` | Split of the cache write by TTL. Requires a recent Claude Code. Both can be 0 even when `cache_creation_tokens` is not. |
| `cost_usd` | Estimated USD |
| `cost_usd_micros` | Same estimate as an integer number of millionths of a dollar |
| `duration_ms`, `speed`, `effort` | Timing and request mode. Not stored by this listener. |
| `request_id` | Server request id (`request-id` or, on Bedrock, `x-amzn-requestid`). This is the join key to `requestId` on a transcript assistant line. |
| `client_request_id` | Client UUID. Not stored. |
| `prompt.id` | Ties the request to one user prompt. Not stored. |

`input_tokens` excludes cache on purpose. The doc says the GenAI convention's
`gen_ai.usage.input_tokens` would be `input_tokens + cache_read_tokens + cache_creation_tokens`.
Firekeeper's transcript total is that same sum: input, output, and cache
(read + creation) added together. The listener stores the four counts
separately and totals them the same way.

Numeric attributes have been numbers since Claude Code v2.1.122 (see
[claude-code#54471](https://github.com/anthropics/claude-code/issues/54471)).
Older exports used strings. The decoder accepts both.

### Does `agent_summary` show up, and under what name?

The monitoring doc does **not** list `agent_summary` as a `query_source` value.
It describes the log attribute as the subsystem name, with examples
`repl_main_thread`, `compact`, and a subagent name. The metric label is only
`main`, `subagent`, or `auxiliary`.

The gist linked from the issue reports a measurement, not a doc: grouping
`claude_code.api_request` log events by `query_source` showed the hidden
status-line calls as the literal string `agent_summary`. Those calls are not
written to the session JSONL. The gist also says the string is the query-source
id compiled into the client.

So the expected name, if that measurement still holds, is `query_source=agent_summary`
on a `claude_code.api_request` **log** event. It will not appear as its own
metric series. This listener copies that attribute through unchanged, capped
at 256 bytes, so a Mac run can confirm or correct the string. Nothing in this
repository has observed a live Claude Code export.

`repl_main_thread` and the metric category `main` are treated as the main
conversation when the summary counts "other non-main" requests. `agent_summary`
is counted on its own line. `compact`, subagent names, and anything else are
the other non-main bucket. That split is a label for the experiment, not a
claim that every non-main call is waste. Subagent work can also be real usage
that lands in a sidechain transcript.

## 2. What the user has to set

Run the listener, then export the variables in the shell that starts Claude
Code. `otel-listen` prints the block, with the bound address filled in:

```sh
export CLAUDE_CODE_ENABLE_TELEMETRY=1
export OTEL_LOGS_EXPORTER=otlp
export OTEL_METRICS_EXPORTER=none
export OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf
export OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:4318
export OTEL_LOGS_EXPORT_INTERVAL=5000
```

Leave `OTEL_LOG_USER_PROMPTS`, `OTEL_LOG_ASSISTANT_RESPONSES`,
`OTEL_LOG_TOOL_DETAILS`, `OTEL_LOG_TOOL_CONTENT`, and `OTEL_LOG_RAW_API_BODIES`
unset. The listener drops those fields if they arrive, and it rejects a body
over 8 MiB, so a raw-body batch can fail the whole export, including the
usage events in it.

Use `127.0.0.1`, not `localhost`. `localhost` may be IPv6, and the listener
does not bind `::1`.

`http/json` is accepted on the same port. If a batch never arrives, set the
logs URL explicitly:

```sh
export OTEL_EXPORTER_OTLP_LOGS_PROTOCOL=http/json
export OTEL_EXPORTER_OTLP_LOGS_ENDPOINT=http://127.0.0.1:4318/v1/logs
```

There is no config-file key yet. Proposed shape, not implemented:

```toml
# absent means off. The dashboard must not start this.
[otel]
port = 4318                 # 127.0.0.1 only; any other host is refused
out = "~/.firekeeper/telemetry.jsonl"
```

No server URL belongs in that table. Uploading stays on `report` / `daemon`.

## 3. Deduping against transcript usage

Do not add the two totals. Telemetry is a per-request ledger. The transcript
is a different cut of the same work, with pieces missing.

| Source | Grain | What it includes |
| --- | --- | --- |
| `api_request` log | One event per API request | Every call Claude Code chooses to emit, including calls that never hit the JSONL |
| Transcript assistant lines | One usage per `message.id` | Lines in the session file. Usage is repeated on every content block; Firekeeper counts the first. Sidechain lines in that file count. `<synthetic>` lines count here; the Usage tab skips them. |
| `cost-state` `modelUsage` | Latest cumulative snapshot | Claude's own session total. Local notes in `internal/transcript/claude/NOTES.md` say it does not match the transcript sum: it includes calls that are not in the file (title generation is the example there) and the snapshot can lag. |

`otel-reconcile` prints all three and two gaps:

- telemetry minus transcript
- telemetry minus `modelUsage`, when a `cost-state` record exists

The transcript side is the newest `projects/*/<session id>.jsonl` under
`CLAUDE_CONFIG_DIR` (default `~/.claude`), plus
`<session>/subagents/agent-*.jsonl`. Message ids are counted once across
those files. A Mac run showed the subagent files hold the
`agent:builtin:general-purpose` tokens, and that telemetry matches the
main file's `modelUsage` exactly. After the subagent files are included,
the gap is only the sources that are still not on disk:
`agent_summary`, `prompt_suggestion`, and `generate_session_title`.

The join key, when both sides have it, is telemetry `request_id` and the
transcript's `requestId`. The monitoring doc says they match. This listener
stores `request_id` and drops a repeated export of the same
`(session id, request id)` so a retried OTLP batch is not counted twice.
Events with no `request_id` are deduped only when the stored record is
identical.

`modelUsage` is not added on top of the transcript total. Discovery already
uses the latest `cost-state` as a base and adds later messages for the TUI.
The reconcile command shows the raw snapshot so it can be compared with a
hand measurement of `modelUsage`.

## 4. Codex and Copilot CLI

Neither emits `claude_code.api_request`. This listener does not parse them.

**Codex** has its own OpenTelemetry stack, off by default. The crate README
is [codex-rs/otel/README.md](https://github.com/openai/codex/blob/main/codex-rs/otel/README.md).
`~/.codex/config.toml` has separate `[otel]` exporters for logs
(`otel.exporter`), traces (`otel.trace_exporter`), and metrics
(`otel.metrics_exporter`). The log exporter defaults to none. OTLP HTTP
(JSON or binary) and gRPC are both supported, with signal-specific URLs such
as `.../v1/logs`. Prompt text and final answers are additional opt-ins
(`log_user_prompt`, `otel.log_agent_responses`). A collector guide
([base14, checked against Codex 0.160.0](https://docs.base14.io/instrument/component/collecting-codex-telemetry/))
describes token usage as the metric `codex.turn.token_usage` broken down by
`token_type`, plus token attributes on spans. That metric name is from the
guide, not re-checked against a Codex binary here. It is not a log line with
a `query_source` of `agent_summary`.

**Copilot CLI** gained OpenTelemetry in v1.0.4. The maintainer note on
[github/copilot-cli#2471](https://github.com/github/copilot-cli/issues/2471)
says it turns on with `COPILOT_OTEL_ENABLED=true` or by setting
`OTEL_EXPORTER_OTLP_ENDPOINT`, and that it exports traces, token-usage
metrics, and operation-duration metrics over OTLP HTTP, with
`OTEL_EXPORTER_OTLP_HEADERS` and `OTEL_RESOURCE_ATTRIBUTES`. `copilot help
monitoring` is the in-product reference and was not run here. The SDK and
enterprise docs are
[OpenTelemetry instrumentation for Copilot SDK](https://docs.github.com/en/copilot/how-tos/copilot-sdk/observability/opentelemetry)
and
[OpenTelemetry for agent monitoring](https://docs.github.com/en/copilot/concepts/enterprise/opentelemetry).
The VS Code monitoring guide
([Monitor agent usage with OpenTelemetry](https://code.visualstudio.com/docs/agents/guides/monitoring-agents))
says the CLI runtime speaks OTLP/HTTP even when gRPC is configured, and that
token counts use GenAI semantic-convention attributes
(`gen_ai.usage.input_tokens`, `gen_ai.usage.output_tokens`, cache read, cache
creation). Those are span attributes, not Claude's log event.

Kimi, OpenCode, Gemini CLI, and Cursor CLI are out of scope for this
experiment. A future receiver would be a separate decoder per product, not a
reuse of the `api_request` allowlist.

## 5. Dependency and attack surface

The listener is in the `firekeeper` binary because the command has to be
there to test. It does not run until `otel-listen` is invoked. `report`,
`daemon`, and the TUI do not listen and do not read the JSONL file.

| Choice | Cost |
| --- | --- |
| Decode | `go.opentelemetry.io/proto/otlp` v1.11.1 (logs, common, and resource messages only) and `google.golang.org/protobuf` v1.36.12. Both are pure Go. No cgo. |
| Not linked | The collector gRPC service and grpc-gateway. They live in the same Go package as `ExportLogsServiceRequest`. The export body is decoded as `logs/v1.LogsData`, whose first field is the same repeated `ResourceLogs` as the export request. |
| Binary size | About 3.0 MiB on an unstripped linux/amd64 `go build` (12.5 MiB to 15.5 MiB). Release builds strip symbols, so the shipped delta will be smaller. |
| Network | No outbound connection. Bind address is `127.0.0.1` only, checked again on the socket that came back from `listen`. |
| Body | 8 MiB cap, compressed and uncompressed. Read and write timeouts. No pprof, no extra routes. `/v1/metrics` and `/v1/traces` are drained and acknowledged so a mis-set exporter does not retry forever; they are not parsed. |
| What is stored | `ts`, `session_id`, `model`, `query_source`, the four token counts, `cost_usd`, `request_id`, `event_sequence`. Strings over 256 bytes or containing newlines are dropped. The log body is never copied. Parse errors are fixed sentences, not the payload. |
| File | Appended JSONL, mode `0600`, default `~/.firekeeper/telemetry.jsonl`. |
| Who can connect | Any process on this machine that can connect to `127.0.0.1`. That is the whole attack surface of the open port. It cannot be pointed at another interface through this command, and it does not upload. |

If `OTEL_LOG_RAW_API_BODIES` or the prompt switches are on, Claude Code will
send conversation text at this socket. The process has to parse the export to
find the usage attributes, then discards everything else. Leave those switches
unset so the text never leaves Claude Code.

## Commands

```sh
firekeeper otel-listen [--port 4318] [--out file.jsonl]
firekeeper otel-reconcile <file.jsonl>
```

`otel-listen` appends JSONL (default `~/.firekeeper/telemetry.jsonl`) and, on
Ctrl-C, prints per session: tokens by `query_source`, the `agent_summary`
request count, and the count of other non-main requests. `otel-reconcile`
prints the transcript total, including subagent files, and a gap that is
only the untranscribed sources. Neither prints prompt or transcript text.

`firekeeper telemetry enable` writes the exporter variables into
`~/.claude/settings.json` under `env` (honored for telemetry; a project
settings file cannot turn it on) and sets `[telemetry] enabled = true` in
the Firekeeper config. It keeps a `settings.json.firekeeper-backup` from
the first change and does not touch other settings. `disable` removes only
those env keys and turns the listener off. `status` reports whether the
env is set, whether anything is accepting connections on `127.0.0.1`, and
whether an event arrived in the last two minutes.

The daemon serves that listener when `[telemetry] enabled` is true. It
stays off otherwise. Uploads are unchanged: with no provider allowlisted,
the daemon does not upload, and telemetry records are not sent to a server.

## Testing on a Mac

Build this branch. A release binary from `main` does not have the commands.

```sh
go build -o /tmp/firekeeper .
```

Terminal A:

```sh
/tmp/firekeeper otel-listen --port 4318 --out "$HOME/firekeeper-otel.jsonl"
```

Leave it running. It prints the `export` lines and then `recorded N
api_request event(s)` as batches arrive.

Terminal B, a new shell:

```sh
export CLAUDE_CODE_ENABLE_TELEMETRY=1
export OTEL_LOGS_EXPORTER=otlp
export OTEL_METRICS_EXPORTER=none
export OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf
export OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:4318
export OTEL_LOGS_EXPORT_INTERVAL=5000
unset OTEL_LOG_USER_PROMPTS OTEL_LOG_ASSISTANT_RESPONSES OTEL_LOG_TOOL_DETAILS OTEL_LOG_TOOL_CONTENT OTEL_LOG_RAW_API_BODIES

claude daemon stop --any
claude --model haiku
```

Prompt, in that session:

```text
Use one subagent to run `sleep 20` eight times, one after another, not in
parallel. When all eight have finished, reply with the single word done.
```

Wait until it replies, then another 10 seconds so the last 5 second log flush
lands. Ctrl-C terminal A, then:

```sh
/tmp/firekeeper otel-reconcile "$HOME/firekeeper-otel.jsonl"
```

Earlier manual measurement, no hook, for comparison: `modelUsage` 385,117
tokens, transcripts 265,651 tokens, 5 summary forks, about 160 seconds. The
runs will not match token-for-token (cache, model revision, and the exact
prompt all move the totals). What to look for:

- an `agent_summary` line, and whether that string is exactly `agent_summary`
- a request count near 5 if the timer is still about 30 seconds and the agent
  runs about 160 seconds
- telemetry total above the transcript total
- `modelUsage` near the previous 385,117 shape relative to the transcript, if
  this run is similar
- `gap telemetry-transcript` close to the `agent_summary` token total if those
  calls are the missing piece, or larger if other calls are missing too

If terminal A stays silent:

1. Confirm the endpoint is `http://127.0.0.1:4318` and not `localhost` or port 4317.
2. Stop the daemon again and start `claude` from the same shell as the exports.
3. Switch to JSON and the full logs URL:

```sh
export OTEL_EXPORTER_OTLP_LOGS_PROTOCOL=http/json
export OTEL_EXPORTER_OTLP_LOGS_ENDPOINT=http://127.0.0.1:4318/v1/logs
```

4. Start Claude Code with `claude --debug-file /tmp/claude-otel-debug.txt` and
   look for `[3P telemetry]` lines. The monitoring doc says those are
   third-party exporter failures. Lines prefixed `[Anthropic telemetry]` are
   Anthropic's own telemetry and do not describe this listener.

Do not set `OTEL_LOG_RAW_API_BODIES`. Do not install the gist hook for this
run; the comparison above is the no-hook measurement.

## Open questions

- The monitoring doc never names `agent_summary`. The Mac run is what confirms
  the string, and whether a current build still emits it on the log event.
- Whether those calls are inside `modelUsage`, only inside telemetry, or in
  neither. The two gaps are printed so the three numbers can be compared
  without adding them.
- Whether a background agent or the daemon emits the summary from a process
  that did not inherit `OTEL_*`. The monitoring doc says Claude Code does not
  pass those variables to subprocesses.
- Whether `http/protobuf` plus the base endpoint is enough on the Claude Code
  build under test, or whether the logs URL has to be set in full.
- Codex and Copilot exports are a different schema. Parsing them is a later
  decoder, after this gap is confirmed.
- gRPC on 4317 is not implemented. HTTP covers `http/protobuf` and `http/json`.
