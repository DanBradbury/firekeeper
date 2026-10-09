// Mock API for ?mock=1. It loads the synthetic fixtures under mock/ and
// generates a long session, then answers with the same shapes and paging
// rules as the v1 API. No data here comes from a real provider.

import { auth } from "./api.js";
import { mockAuth } from "./mock_auth.js";

const LONG_UID = "m-studio:019a-long";
const LONG_COUNT = 5000;
const LATENCY_MS = 120;

const splitUID = (uid) => [uid.slice(0, uid.indexOf(":")), uid.slice(uid.indexOf(":") + 1)];
const delay = (v) => new Promise((resolve) => setTimeout(() => resolve(structuredClone(v)), LATENCY_MS));

async function load(name) {
  const res = await fetch(`mock/${name}`);
  if (!res.ok) throw new Error(`mock fixture ${name} missing`);
  return res.json();
}

// mulberry32: small seeded PRNG so the generated transcript is identical on
// every load.
function rng(seed) {
  return () => {
    seed |= 0;
    seed = (seed + 0x6d2b79f5) | 0;
    let t = Math.imul(seed ^ (seed >>> 15), 1 | seed);
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

const WORDS = ("sprite layer atlas tile alpha frame scene render kitty block pixel " +
  "scale nearest neighbor ground fire character offset width height chrome row " +
  "cache buffer compose draw order transparent menu overlay test fixture").split(" ");
const TOOLS = ["shell", "apply_patch", "read_file", "rg", "go_test"];

function sentence(r, n) {
  const w = [];
  for (let i = 0; i < n; i++) w.push(WORDS[Math.floor(r() * WORDS.length)]);
  const s = w.join(" ");
  return s[0].toUpperCase() + s.slice(1) + ".";
}

function paragraph(r) {
  const out = [];
  const n = 1 + Math.floor(r() * 4);
  for (let i = 0; i < n; i++) out.push(sentence(r, 6 + Math.floor(r() * 12)));
  return out.join(" ");
}

function codeBlock(r, seq) {
  const lines = [`func composeLayer${seq}(dst *image.RGBA, src image.Image) {`];
  const n = 2 + Math.floor(r() * 10);
  for (let i = 0; i < n; i++) {
    lines.push(`\tfor x := 0; x < ${16 * (i + 1)}; x++ { // ${WORDS[i % WORDS.length]}`);
    lines.push(`\t\tdst.Set(x, ${i}, src.At(x, ${i}))`);
    lines.push("\t}");
  }
  lines.push("}");
  return "```go\n" + lines.join("\n") + "\n```";
}

function toolOutput(r, big) {
  const n = big ? 400 + Math.floor(r() * 400) : 1 + Math.floor(r() * 25);
  const lines = [];
  for (let i = 0; i < n; i++) {
    lines.push(`${String(i + 1).padStart(4)}  ${sentence(r, 3 + Math.floor(r() * 8))}`);
  }
  return lines.join("\n");
}

// generateLong builds a deterministic transcript that cycles through user,
// assistant, tool call, and tool result events with occasional system and
// meta records.
function generateLong(start, count) {
  const r = rng(1234);
  const [machine_id, session_id] = splitUID(LONG_UID);
  const events = [];
  let t = start;
  for (let seq = 0; seq < count; seq++) {
    t += 1000 + Math.floor(r() * 8000);
    events.push(makeEvent(r, machine_id, session_id, seq, t));
  }
  return events;
}

function makeEvent(r, machine_id, session_id, seq, t) {
  const base = {
    machine_id, session_id, provider: "codex", seq, ts: new Date(t).toISOString(),
    role: "meta", text: "", tool_name: null, model: null,
    tokens: { input: 0, output: 0, cache: 0 }, raw: {},
  };
  if (seq === 0) {
    return { ...base, role: "system", text: "Synthetic session for UI development.\nNo real transcript data.", raw: { type: "session_meta" } };
  }
  const phase = seq % 6;
  if (seq % 97 === 0) {
    return { ...base, raw: { type: "unrecognized_record", n: seq, nested: { ok: true, list: [1, 2, 3] } } };
  }
  if (phase === 1) {
    const text = sentence(r, 5 + Math.floor(r() * 20));
    return { ...base, role: "user", text, raw: { type: "user_message", message: text } };
  }
  if (phase === 2 || phase === 5) {
    let text = paragraph(r);
    if (r() < 0.35) text += "\n\n" + codeBlock(r, seq) + "\n\n" + paragraph(r);
    if (r() < 0.2) text += " Use `go test ./...` before handoff.";
    return {
      ...base, role: "assistant", text, model: "gpt-5-codex",
      tokens: { input: 800 + Math.floor(r() * 4000), output: 50 + Math.floor(r() * 600), cache: Math.floor(r() * 3000) },
      raw: { type: "agent_message" },
    };
  }
  const tool = TOOLS[Math.floor(seq / 6) % TOOLS.length];
  if (phase === 3) {
    const args = { cmd: [tool, "-n", WORDS[seq % WORDS.length]], workdir: "." };
    return { ...base, role: "tool_call", tool_name: tool, model: "gpt-5-codex", text: JSON.stringify(args), raw: { type: "function_call", name: tool, arguments: args } };
  }
  const text = toolOutput(r, seq % 250 === 4);
  return { ...base, role: "tool_result", tool_name: tool, text, raw: { type: "function_call_output", exit_code: 0, bytes: text.length } };
}

function filler(n, now) {
  const out = [];
  const providers = ["codex", "copilot", "kimi", "claude"];
  for (let i = 0; i < n; i++) {
    const last = new Date(now - (i + 2) * 3600e3 * 3).toISOString();
    const id = `ci-${String(i).padStart(3, "0")}`;
    out.push({
      uid: `m-build:${id}`, machine_id: "m-build", session_id: id, provider: providers[i % 4],
      cwd: `/srv/build/job-${i}`, project: `service-${i % 5}`, branch: `job/${i}`, model: "",
      state: "ENDED", title: `Nightly job ${i}`, started_at: last, last_activity_at: last,
      event_count: 0, tokens: { input: 1000 * i, output: 90 * i, cache: 0 },
    });
  }
  return out;
}

// Synthetic usage: one record per day, model, and project for the last
// USAGE_DAYS days, with a quiet gap so empty days show in the chart.
const USAGE_DAYS = 120;
const USAGE_MODELS = [
  ["gpt-5-codex", "codex", "m-studio"], ["gpt-5", "codex", "m-studio"],
  ["claude-sonnet-5-5", "copilot", "m-laptop"], ["claude-opus-5-5", "claude", "m-laptop"],
  ["kimi-k2", "kimi", "m-laptop"], ["o4-mini", "codex", "m-build"],
  ["gpt-5-mini", "codex", "m-build"], ["claude-haiku-5-5", "claude", "m-build"],
  ["", "claude", "m-build"], ["qwen3-coder", "copilot", "m-build"],
];
const USAGE_PROJECTS = ["firekeeper", "dotfiles", "notes", "service-0", ""];
// Made-up prices for the mock only, per million tokens. The real server
// shows cost only when the user configures a price table. kimi-k2 is left
// out so the partial-cost marker shows.
const MOCK_PRICES = {
  "gpt-5-codex": { input: 1, output: 8, cache: 0.1 }, "gpt-5": { input: 1, output: 8, cache: 0.1 },
  "claude-sonnet-5-5": { input: 3, output: 15, cache: 0.3 }, "claude-opus-5-5": { input: 15, output: 75, cache: 1.5 },
  "o4-mini": { input: 1, output: 4, cache: 0.25 }, "gpt-5-mini": { input: 0.25, output: 2, cache: 0.03 },
  "claude-haiku-5-5": { input: 1, output: 5, cache: 0.1 }, "qwen3-coder": { input: 0.5, output: 2, cache: 0 },
};

function generateUsage(now) {
  const r = rng(4242);
  const today = Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), now.getUTCDate());
  const out = [];
  for (let d = USAGE_DAYS - 1; d >= 0; d--) {
    if (d >= 40 && d < 44) continue;
    const day = new Date(today - d * 86400e3).toISOString().slice(0, 10);
    const weekend = [0, 6].includes(new Date(day).getUTCDay());
    USAGE_MODELS.forEach(([model, provider, machine], mi) => {
      if (r() < 0.25 + mi * 0.06) return;
      const project = USAGE_PROJECTS[Math.floor(r() * USAGE_PROJECTS.length)];
      const scale = (weekend ? 0.3 : 1) * (1 / (1 + mi * 0.5));
      const input = Math.round((20000 + r() * 400000) * scale);
      out.push({ day, model, provider, machine, project,
        tokens: { input, output: Math.round(input * (0.05 + r() * 0.1)), cache: Math.round(input * r() * 0.8) } });
    });
  }
  return out;
}

function aggregateUsage(records, from, to, groupBy) {
  const rows = new Map();
  const add = (row, rec) => {
    row.tokens.input += rec.tokens.input;
    row.tokens.output += rec.tokens.output;
    row.tokens.cache += rec.tokens.cache;
    const p = MOCK_PRICES[rec.model];
    if (p) row.cost += (rec.tokens.input * p.input + rec.tokens.output * p.output + rec.tokens.cache * p.cache) / 1e6;
    else row.unpriced_tokens += rec.tokens.input + rec.tokens.output + rec.tokens.cache;
  };
  const blank = (group) => ({ group, tokens: { input: 0, output: 0, cache: 0 }, cost: 0, unpriced_tokens: 0 });
  const totals = blank({});
  for (const rec of records) {
    if (rec.day < from || rec.day > to) continue;
    const group = Object.fromEntries(groupBy.map((k) => [k, rec[k]]));
    const id = JSON.stringify(group);
    if (!rows.has(id)) rows.set(id, blank(group));
    add(rows.get(id), rec);
    add(totals, rec);
  }
  const sum = (t) => t.input + t.output + t.cache;
  const list = [...rows.values()].sort((a, b) =>
    (a.group.day || "").localeCompare(b.group.day || "") || sum(b.tokens) - sum(a.tokens));
  for (const row of [...list, totals]) if (!row.unpriced_tokens) delete row.unpriced_tokens;
  delete totals.group;
  return { group_by: groupBy, priced: true, rows: list, totals };
}

function shiftTimes(obj, offset, keys) {
  for (const k of keys) {
    if (obj[k]) obj[k] = new Date(Date.parse(obj[k]) + offset).toISOString();
  }
}

export async function createMockAPI() {
  const [m, s, e, f] = await Promise.all([load("machines.json"), load("sessions.json"), load("events.json"), load("files.json")]);
  const sessions = s.sessions;
  // ?mock=1&machines=none shows a new account with no machines, and
  // machines=linked shows one machine that has linked but uploaded nothing.
  // The default is three machines in mixed states: studio is online and
  // beating, laptop's heartbeat is 50s old and stops, so it goes offline
  // about 40s after load, and the build box has been offline for hours.
  const variant = new URLSearchParams(location.search).get("machines");
  const machines = m.machines.filter((x) =>
    variant === "none" ? false : variant === "linked" ? x.id === "m-new" : x.id !== "m-new");
  // Heartbeat age at load, and whether the machine keeps beating. The
  // fixture's own heartbeat times only keep its shape realistic.
  const HEARTBEAT = { "m-studio": [5, true], "m-laptop": [50, false], "m-build": [40000, false], "m-new": [10, true] };
  const beats = (x) => HEARTBEAT[x.id]?.[1];
  const beat = (x, ageS = 0) => { x.last_heartbeat_at = new Date(Date.now() - ageS * 1000).toISOString(); };
  for (const x of machines) beat(x, HEARTBEAT[x.id]?.[0] ?? 0);
  if (variant === "linked") sessions.length = 0;

  // Shift fixture times so the newest session was active a minute ago.
  const newest = Math.max(0, ...sessions.map((x) => Date.parse(x.last_activity_at || 0) || 0));
  const offset = Date.now() - 60e3 - newest;
  for (const x of sessions) shiftTimes(x, offset, ["started_at", "last_activity_at"]);
  if (variant !== "linked") sessions.push(...filler(40, Date.now()));
  if (variant === "none") sessions.length = 0;

  const events = new Map();
  for (const [uid, list] of Object.entries(e)) {
    const [machine_id, session_id] = splitUID(uid);
    const se = sessions.find((x) => x.uid === uid);
    events.set(uid, list.map((ev) => {
      const out = { machine_id, session_id, provider: se?.provider || "codex", ...ev };
      if (out.ts) out.ts = new Date(Date.parse(out.ts) + offset).toISOString();
      return out;
    }));
  }
  const long = sessions.find((x) => x.uid === LONG_UID);
  if (long) events.set(LONG_UID, generateLong(Date.parse(long.started_at), LONG_COUNT));

  const usage = generateUsage(new Date());

  const order = (a, b) => (b.last_activity_at || "").localeCompare(a.last_activity_at || "") || b.uid.localeCompare(a.uid);
  const notFound = () => Promise.reject(Object.assign(new Error("session not found"), { status: 404, code: "not_found" }));

  // Tokens live in memory only. Secrets are fake and never stored.
  const tokens = [
    { id: "a1b2c3d4", name: "studio-mac", scope: "ingest", machine_id: "m-studio-0000", created_at: new Date(Date.now() - 12 * 864e5).toISOString(), last_used_at: new Date(Date.now() - 90e3).toISOString(), revoked_at: null },
    { id: "e5f6a7b8", name: "ci-runner", scope: "ingest", machine_id: "ci-0000", created_at: new Date(Date.now() - 3 * 864e5).toISOString(), last_used_at: null, revoked_at: null },
    { id: "c9d0e1f2", name: "old-laptop", scope: "ingest", machine_id: "m-old-0000", created_at: new Date(Date.now() - 40 * 864e5).toISOString(), last_used_at: new Date(Date.now() - 30 * 864e5).toISOString(), revoked_at: new Date(Date.now() - 29 * 864e5).toISOString() },
  ];

  const api = {
    mock: true,

    // Sign-in is simulated by mock_auth.js; see the login page in mock mode.
    signedIn: () => mockAuth.signedIn(),
    account: () => mockAuth.account(),
    logout: () => mockAuth.logout(),
    deleteAccount: (confirm) => mockAuth.deleteAccount(confirm),
    exportURL: "",

    listTokens() {
      return delay([...tokens].reverse());
    },

    createToken(name, scope, machineID) {
      if (!name) return Promise.reject(Object.assign(new Error("token name is required"), { status: 400, code: "invalid_request" }));
      if (scope === "ingest" && !machineID) return Promise.reject(Object.assign(new Error("ingest tokens need a machine id"), { status: 400, code: "invalid_request" }));
      const t = { id: Math.random().toString(16).slice(2, 10), name, scope, machine_id: scope === "ingest" ? machineID : "", created_at: new Date().toISOString(), last_used_at: null, revoked_at: null };
      tokens.push(t);
      return delay({ ...t, token: "fk_MOCK-NOT-A-REAL-SECRET" });
    },

    revokeToken(id) {
      const t = tokens.find((x) => x.id === id && !x.revoked_at);
      if (!t) return Promise.reject(Object.assign(new Error("no active token with that id"), { status: 404, code: "not_found" }));
      t.revoked_at = new Date().toISOString();
      return delay({ ok: true });
    },

    listSessions(filters, cursor, limit = 50) {
      const q = (filters.q || "").toLowerCase();
      const rows = sessions
        .filter((x) => !filters.machine || x.machine_id === filters.machine)
        .filter((x) => !filters.provider || x.provider === filters.provider)
        .filter((x) => !filters.state || x.state === filters.state)
        .filter((x) => !filters.project || x.project === filters.project)
        .filter((x) => !q || x.title.toLowerCase().includes(q))
        .sort(order);
      const start = cursor ? Number(cursor) : 0;
      const page = rows.slice(start, start + limit);
      const next = start + limit < rows.length ? String(start + limit) : undefined;
      return delay({ sessions: page, next_cursor: next });
    },

    getSession(uid) {
      const se = sessions.find((x) => x.uid === uid);
      return se ? delay({ ...se, files: f[uid] || [] }) : notFound();
    },

    listEvents(uid, afterSeq = -1, limit = 200) {
      if (!sessions.some((x) => x.uid === uid)) return notFound();
      const list = events.get(uid) || [];
      const from = list.findIndex((ev) => ev.seq > afterSeq);
      const page = from < 0 ? [] : list.slice(from, from + Math.min(limit, 1000));
      const has_more = from >= 0 && from + page.length < list.length;
      return delay({ events: page, has_more });
    },

    listMachines() {
      const out = machines.map((x) => {
        const mine = sessions.filter((se) => se.machine_id === x.id);
        const state_counts = {};
        for (const se of mine) state_counts[se.state] = (state_counts[se.state] || 0) + 1;
        const last = mine.map((se) => se.last_activity_at || "").sort().pop();
        return { ...x, session_count: mine.length, state_counts, last_activity_at: last || null };
      });
      return delay({ machines: out });
    },

    usage(from, to, groupBy, project) {
      const res = aggregateUsage(project ? usage.filter((r) => r.project === project) : usage, from, to, groupBy);
      res.from = `${from}T00:00:00Z`;
      res.to = new Date(Date.parse(to) + 86400e3).toISOString();
      return delay(res);
    },

    // subscribe appends a synthetic event to the long session every few
    // seconds so live tailing can be exercised.
    subscribe(onEvent, onStatus) {
      onStatus(true);
      const r = rng(99);
      // Machines that keep beating send a heartbeat every 30s.
      const beater = setInterval(() => {
        if (!mockAuth.signedIn()) return;
        for (const x of machines) {
          if (!beats(x)) continue;
          beat(x);
          onEvent("machine.status", { machine_id: x.id });
        }
      }, 30e3);
      const timer = setInterval(() => {
        if (!mockAuth.signedIn()) {
          auth.onUnauthorized();
          return;
        }
        if (!long) return;
        const list = events.get(LONG_UID);
        const seq = list.length;
        const ev = makeEvent(r, long.machine_id, long.session_id, seq, Date.now());
        list.push(ev);
        long.event_count = list.length;
        long.last_activity_at = ev.ts;
        onEvent("event.appended", { uid: LONG_UID, machine_id: long.machine_id, session_id: long.session_id, seq });
        onEvent("session.updated", { uid: LONG_UID, machine_id: long.machine_id, session_id: long.session_id });
      }, 8000);
      return () => {
        clearInterval(timer);
        clearInterval(beater);
      };
    },
  };

  // Data calls behave like the real API once the mock session is gone: they
  // answer 401, which sends the reader back to the sign-in page.
  for (const name of ["listSessions", "getSession", "listEvents", "listMachines", "usage", "listTokens", "createToken", "revokeToken"]) {
    const call = api[name];
    api[name] = (...args) => {
      try {
        mockAuth.check();
      } catch (err) {
        auth.onUnauthorized();
        return Promise.reject(err);
      }
      return call(...args);
    };
  }
  return api;
}
