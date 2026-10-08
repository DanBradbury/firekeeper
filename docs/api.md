# Firekeeper dashboard API (v1)

The dashboard server (`firekeeper serve`) accepts session metadata and
transcript events from reporters and serves them to dashboard clients. This
document describes the v1 HTTP API as implemented in `internal/server/api`.
The contract it follows lives in `AGENTS.md` under "Reporting and dashboard".

- Default listen address: `127.0.0.1:7777`.
- No authentication yet. Bearer tokens are planned; until then, keep the
  server on loopback.
- Request and response bodies are JSON (`Content-Type: application/json`),
  except `/v1/stream`, which is `text/event-stream`.
- Timestamps are RFC 3339 strings in UTC, or `null` when unknown.

## Errors

Every error is a JSON object with a standard HTTP status:

```json
{ "error": "human-readable message", "code": "machine_readable_code" }
```

| Status | `code` | When |
| --- | --- | --- |
| 400 | `bad_request` | Malformed JSON, query parameter, or `uid`. |
| 400 | `invalid_request` | Well-formed body that breaks a rule: missing `machine.id` or `session_id`, unknown provider, role, or state, an invalid cursor, or an event whose `machine_id`/`session_id` does not match its batch. |
| 404 | `not_found` | Unknown session. |
| 413 | `payload_too_large` | Body over 5 MiB. |
| 413 | `too_many_events` | More than 500 events in one ingest request. |
| 500 | `internal` | Server-side failure. Details are not exposed. |
| 503 | `unavailable` | Stream requested while the server is shutting down. |

## Shared objects

### Machine

```json
{ "id": "string", "name": "string", "hostname": "string", "os": "string", "version": "string" }
```

`id` is required on every write. On upsert, empty strings keep the stored
values.

### Event

One normalized transcript record. The JSON shape is pinned by
`docs/event.schema.json`.

```json
{
  "machine_id": "string",
  "session_id": "string",
  "provider": "codex | copilot | kimi | claude",
  "seq": 0,
  "ts": "RFC3339 or null",
  "role": "user | assistant | tool_call | tool_result | system | meta",
  "text": "string",
  "tool_name": "string or null",
  "model": "string or null",
  "tokens": { "input": 0, "output": 0, "cache": 0 },
  "raw": {}
}
```

Events are keyed by `(machine_id, session_id, seq)`. `seq` must not be
negative.

### Session

Returned by the read endpoints:

```json
{
  "uid": "m1:0199-abc",
  "machine_id": "m1",
  "session_id": "0199-abc",
  "provider": "codex",
  "cwd": "/path/to/repo",
  "project": "repo",
  "branch": "main",
  "model": "gpt-5",
  "state": "ACTIVE | WAITING | NEEDS_INPUT | ENDED | UNKNOWN",
  "title": "string",
  "started_at": "RFC3339 or null",
  "last_activity_at": "RFC3339 or null",
  "event_count": 42,
  "tokens": { "input": 0, "output": 0, "cache": 0 }
}
```

`event_count` and `tokens` are always recomputed from stored events, never
taken from a request. `state` describes the latest observable provider event.
It is a best-effort reading, not guaranteed ground truth.

### Session uid

A session's `uid` is `<machine_id>:<session_id>`. Escape it when you put it in
a path (for example, `m1:a%2Fb` for session `a/b`). The server splits the uid
at its first `:`, so a machine id must not contain `:`. A session id may.

## Write endpoints

### `POST /v1/ingest`

Upserts session metadata and appends events. Each request runs in a single
transaction.

```json
{
  "machine": { "id": "m1", "name": "laptop", "hostname": "laptop.local", "os": "darwin", "version": "0.4.0" },
  "sessions": [
    {
      "meta": {
        "session_id": "0199-abc", "provider": "codex", "cwd": "/repo", "project": "repo",
        "branch": "main", "model": "gpt-5", "state": "ACTIVE", "title": "Fix parser",
        "started_at": "2026-10-07T12:00:00Z", "last_activity_at": "2026-10-07T12:05:00Z"
      },
      "events": [ { "provider": "codex", "seq": 0, "role": "user", "text": "...", "tokens": {"input": 0, "output": 0, "cache": 0}, "raw": {} } ]
    }
  ]
}
```

- Limits: at most 500 events across all sessions, and at most 5 MiB of body.
  A request over either limit is rejected with 413. Reporters split larger
  batches.
- Idempotency: a stored event key is never overwritten. Re-sending an event
  counts it as a duplicate.
- Metadata: empty strings keep the stored values. `started_at` keeps its first
  value. `last_activity_at` only moves forward. An empty `state` keeps the
  stored state, or `UNKNOWN` for a new session.
- Ingest also counts as a heartbeat for the machine.

Response `200`:

```json
{ "accepted": 3, "duplicates": 0 }
```

### `POST /v1/heartbeat`

Marks the machine online and updates session state.

```json
{
  "machine": { "id": "m1" },
  "sessions": [ { "session_id": "0199-abc", "state": "WAITING", "last_activity_at": "2026-10-07T12:06:00Z" } ]
}
```

`state` is required and must be a valid state. Sessions the server has never
ingested are ignored. `last_activity_at` only moves forward.

Response `200`: `{ "ok": true }`.

## Read endpoints

### `GET /v1/sessions`

Lists sessions, newest `last_activity_at` first. Sessions with no activity
time come last. Ties are broken by `machine_id`, then `session_id` (both
descending), so the order is stable.

| Parameter | Meaning |
| --- | --- |
| `machine` | Exact machine id. |
| `provider` | Exact provider. Must be a valid provider. |
| `project` | Exact project name. |
| `state` | Exact state. Must be a valid state. |
| `q` | Case-insensitive substring of the title, or a full-text phrase match on event text. FTS operators are not interpreted. |
| `limit` | Page size, default 50. Values above 500 are clamped to 500, and values below 1 are rejected. |
| `cursor` | The `next_cursor` from the previous page. Treat it as opaque. |

Filters combine with AND. Response `200`:

```json
{ "sessions": [ /* Session */ ], "next_cursor": "eyJr..." }
```

`next_cursor` is omitted on the last page. Pagination is keyset-based, so
pages neither skip nor repeat rows when sessions are added. A session whose
activity changes while you are paging can still move to a page you have
already read.

### `GET /v1/sessions/{uid}`

Returns one Session. A malformed uid gets 400, and an unknown session gets 404.

### `GET /v1/sessions/{uid}/events`

Returns a session's events in `seq` order.

| Parameter | Meaning |
| --- | --- |
| `after_seq` | Return events with `seq` greater than this value. The default, `-1`, starts from the first event. |
| `limit` | Default 200. Values above 1000 are clamped to 1000, and values below 1 are rejected. |

Response `200`:

```json
{ "events": [ /* Event */ ], "has_more": false }
```

To page through, pass the last returned `seq` as `after_seq`. Unknown sessions
get 404.

### `GET /v1/machines`

Lists every machine ordered by `id`:

```json
{
  "machines": [
    { "id": "m1", "name": "laptop", "hostname": "laptop.local", "os": "darwin", "version": "0.4.0",
      "last_heartbeat_at": "2026-10-07T12:06:00Z", "session_count": 4 }
  ]
}
```

Clients decide whether a machine is online from `last_heartbeat_at`.

### `GET /v1/usage`

Token sums from stored events, for usage charts.

| Parameter | Meaning |
| --- | --- |
| `from` | Start of the range: a `YYYY-MM-DD` date (start of that UTC day) or an RFC 3339 time. Default: 30 days before `to`. |
| `to` | End of the range: a `YYYY-MM-DD` date includes that whole UTC day; an RFC 3339 time is exclusive. Default: the end of today (UTC). |
| `group_by` | Comma-separated keys from `day`, `model`, `provider`, `machine`, `project`, each at most once. Empty returns only totals. |

The range must be non-empty and at most 400 days. `day` is the event's UTC
date. `model` is the event's model, falling back to its session's model, and
may be `""` when neither is known. `machine` is the machine id, and `project`
is the session's project.

Only events with a timestamp count; events with `ts: null` cannot be placed
in a range and are left out, so totals here can be lower than a session's
`tokens`. Each counter is summed as stored, so whether `input` already
includes `cache` depends on the provider.

```json
{
  "from": "2026-10-01T00:00:00Z",
  "to": "2026-10-05T00:00:00Z",
  "group_by": ["day", "model"],
  "priced": true,
  "rows": [
    { "group": { "day": "2026-10-01", "model": "gpt-5" },
      "tokens": { "input": 100, "output": 10, "cache": 50 }, "cost": 0.000205 },
    { "group": { "day": "2026-10-02", "model": "o3" },
      "tokens": { "input": 1000, "output": 100, "cache": 0 }, "cost": 0, "unpriced_tokens": 1100 }
  ],
  "totals": { "tokens": { "input": 1100, "output": 110, "cache": 50 }, "cost": 0.000205, "unpriced_tokens": 1100 }
}
```

Rows are sorted by `day` ascending when grouped by day, then by total tokens
descending. Days with no usage are omitted; clients fill gaps. An empty range
returns `"rows": []` with zero totals.

Cost appears only when the server has a per-model price table (per million
tokens of each kind). Firekeeper ships no prices. Then `priced` is `true`,
and every row and the totals carry `cost`, computed from priced models only.
`unpriced_tokens`, when present, counts the tokens from models with no price
that `cost` leaves out. Without a table, `priced` is `false` and `cost` is
never sent.

## Live stream

### `GET /v1/stream`

A server-sent events stream of store changes. The server sends
`: connected` when the stream opens and `: keepalive` every 15 seconds.

```
event: event.appended
data: {"uid":"m1:0199-abc","machine_id":"m1","session_id":"0199-abc","seq":41}
```

| Event | Data | Meaning |
| --- | --- | --- |
| `session.updated` | `uid`, `machine_id`, `session_id` | A session's metadata, state, or totals may have changed. |
| `event.appended` | `uid`, `machine_id`, `session_id`, `seq` | New events were stored. `seq` is the highest new seq in that ingest. Seqs can arrive out of order, so it is not a high-water mark. |
| `machine.status` | `machine_id` | The machine sent an ingest or heartbeat. |

Events are notifications, not data. Clients refetch with the read endpoints,
for example `GET /v1/sessions/{uid}/events?after_seq=<last seq you hold>`.

**Delivery is best effort.** The server drops notifications rather than slow
down ingest:

- If a client falls more than 256 notifications behind, the server closes
  its connection.
- If the server-wide notification buffer fills, notifications are discarded
  for every client.

**No resume.** Events carry no `id:` and the server ignores `Last-Event-ID`,
because notifications are not stored. After any reconnect, clients refetch the
session list and the events of any open session, then continue from the
stream. A dropped connection is a cue to resync, not an error.

The stream also ends when the server shuts down. Because streams are
long-lived, a graceful shutdown should close the store, which ends every
stream, before waiting on in-flight requests.
