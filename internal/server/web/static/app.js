// Firekeeper dashboard: a session list, a transcript viewer, and token
// usage charts, routed by URL fragment. All transcript text is inserted with textContent, never as
// HTML.

import { realAPI, auth } from "./api.js";

// A machine with no heartbeat for this long counts as offline.
const OFFLINE_AFTER_MS = 90e3;

const PROVIDERS = ["codex", "copilot", "kimi", "claude"];
const STATES = ["ACTIVE", "WAITING", "NEEDS_INPUT", "ENDED", "UNKNOWN"];
const SESSION_PAGE = 50;
const EVENT_PAGE = 200;
const PREVIEW_CHARS = 160;
const CHUNK_SIZE = 100;

const app = document.getElementById("app");
const liveDot = document.getElementById("live");

let api = realAPI;
let current = null; // { dispose(), onStream(type, data) }
let lastListHash = "#/sessions"; // the list, with the filters the reader came from

// ---------- DOM helpers ----------

function el(tag, attrs, ...children) {
  const n = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v === undefined || v === null || v === false) continue;
    if (k === "class") n.className = v;
    else if (k === "text") n.textContent = v;
    // The CSP forbids inline style attributes; CSSOM assignment is allowed.
    else if (k === "css") for (const [p, pv] of Object.entries(v)) n.style.setProperty(p, pv);
    else if (k.startsWith("on")) n.addEventListener(k.slice(2), v);
    else n.setAttribute(k, v === true ? "" : String(v));
  }
  for (const c of children) {
    if (c === undefined || c === null || c === false) continue;
    n.append(c instanceof Node ? c : String(c));
  }
  return n;
}

function notice(message, isError) {
  return el("p", { class: isError ? "notice error" : "notice", text: message });
}

// ---------- formatting ----------

function compact(n) {
  n = Number(n) || 0;
  if (n < 1000) return String(n);
  if (n < 1e6) return `${(n / 1e3).toFixed(n < 1e4 ? 1 : 0)}k`;
  return `${(n / 1e6).toFixed(n < 1e7 ? 2 : 1)}M`;
}

function relative(ts) {
  if (!ts) return "never";
  const t = Date.parse(ts);
  if (Number.isNaN(t)) return "unknown";
  const s = Math.round((Date.now() - t) / 1000);
  if (s < 0) return "just now";
  if (s < 45) return `${s}s ago`;
  const m = Math.round(s / 60);
  if (m < 60) return `${m}m ago`;
  const h = Math.round(m / 60);
  if (h < 36) return `${h}h ago`;
  const d = Math.round(h / 24);
  if (d < 60) return `${d}d ago`;
  return new Date(t).toLocaleDateString();
}

function absolute(ts) {
  if (!ts) return "";
  const t = Date.parse(ts);
  return Number.isNaN(t) ? "" : new Date(t).toLocaleString();
}

function timeEl(ts) {
  return el("time", { "data-ts": ts || "", datetime: ts || undefined, title: absolute(ts), text: relative(ts) });
}

function refreshTimes() {
  for (const t of app.querySelectorAll("time[data-ts]")) {
    t.textContent = relative(t.dataset.ts || null);
  }
}

function tokensText(t) {
  t = t || {};
  return `${compact(t.input)} in · ${compact(t.output)} out · ${compact(t.cache)} cache`;
}

function tokensTitle(t) {
  t = t || {};
  const f = (n) => (Number(n) || 0).toLocaleString();
  return `input ${f(t.input)}, output ${f(t.output)}, cache ${f(t.cache)}`;
}

function badge(state) {
  const s = STATES.includes(state) ? state : "UNKNOWN";
  return el("span", { class: `badge ${s}`, text: s.replace("_", " ") });
}

// ---------- routing ----------

function parseRoute() {
  const h = location.hash.replace(/^#/, "") || "/";
  // The landing page is the machines overview; the session list lives at
  // /sessions. An old "#/?machine=..." link still opens the list.
  if (h === "/" || h === "/machines") return { view: "machines" };
  if (h === "/account") return { view: "account" };
  if (h === "/usage" || h.startsWith("/usage?")) {
    return { view: "usage", params: new URLSearchParams(h.slice(7)) };
  }
  if (h === "/tokens") return { view: "tokens" };
  if (h.startsWith("/s/")) {
    try {
      return { view: "session", uid: decodeURIComponent(h.slice(3)) };
    } catch {
      return { view: "list", params: new URLSearchParams() };
    }
  }
  const q = h.indexOf("?");
  return { view: "list", params: new URLSearchParams(q >= 0 ? h.slice(q + 1) : "") };
}

const sessionHref = (uid) => `#/s/${encodeURIComponent(uid)}`;

// ---------- sign in ----------

const accountBox = document.getElementById("account");
const mockMode = new URLSearchParams(location.search).get("mock") === "1";

// toLogin leaves for the sign-in page, keeping the current route in the
// fragment so signing in returns here. reason picks the page's notice.
let leaving = false;
function toLogin(reason) {
  if (leaving) return;
  leaving = true;
  current?.dispose();
  current = null;
  const qs = new URLSearchParams();
  if (mockMode) qs.set("mock", "1");
  if (reason) qs.set("reason", reason);
  location.replace(`login?${qs}${location.hash}`);
}

// refreshAccount shows who is signed in with a sign-out button. Single-user
// servers have no account to show; a read-token sign-in shows only the
// button.
async function refreshAccount() {
  if (!api.account) return;
  const acct = await api.account();
  accountBox.replaceChildren();
  accountBox.hidden = true;
  if (acct.single_user || !acct.email) {
    if (!auth.get()) return;
    accountBox.append(el("button", { type: "button", onclick: signOut }, "Sign out"));
    accountBox.hidden = false;
    return;
  }
  accountBox.append(
    el("a", { class: "account-email", href: "#/account", title: `${acct.email} · account settings`, text: acct.email }),
    el("button", { type: "button", onclick: signOut }, "Sign out"),
  );
  accountBox.hidden = false;
}

async function signOut() {
  try {
    if (!auth.get()) await api.logout?.();
  } catch (err) {
    app.replaceChildren(notice(err.message, true));
    return;
  }
  auth.clear();
  toLogin("signed_out");
}

// Any 401 during use means the session ended or was never there: go to the
// sign-in page with a notice rather than leaving a blank or broken view.
auth.onUnauthorized = () => {
  const hadSession = !accountBox.hidden;
  auth.clear();
  toLogin(hadSession ? "expired" : "required");
};

function render() {
  current?.dispose();
  current = null;
  app.replaceChildren();
  const r = parseRoute();
  for (const [id, view] of [["nav-machines", "machines"], ["nav-sessions", "list"], ["nav-usage", "usage"], ["nav-tokens", "tokens"]]) {
    const a = document.getElementById(id);
    if (view === r.view) a.setAttribute("aria-current", "page");
    else a.removeAttribute("aria-current");
  }
  document.title = "Firekeeper";
  if (r.view === "machines") current = machinesView();
  else if (r.view === "session") current = sessionView(r.uid);
  else if (r.view === "usage") current = usageView(r.params);
  else if (r.view === "account") current = accountView();
  else if (r.view === "tokens") current = tokensView();
  else current = listView(r.params);
  window.scrollTo(0, 0);
}

// ---------- session list ----------

function listView(params) {
  let alive = true;
  let token = 0;
  let cursor = null;
  let loaded = 0;
  let machineNames = new Map();
  let refreshTimer = null;

  const filters = {
    machine: params.get("machine") || "",
    provider: params.get("provider") || "",
    project: params.get("project") || "",
    state: params.get("state") || "",
    q: params.get("q") || "",
  };

  const machineSel = el("select", { name: "machine" }, el("option", { value: "", text: "All machines" }));
  const providerSel = el("select", { name: "provider" }, el("option", { value: "", text: "All providers" }),
    ...PROVIDERS.map((p) => el("option", { value: p, text: p })));
  const stateSel = el("select", { name: "state" }, el("option", { value: "", text: "All states" }),
    ...STATES.map((s) => el("option", { value: s, text: s.replace("_", " ") })));
  const search = el("input", { type: "search", name: "q", placeholder: "Title or transcript text", value: filters.q });
  const project = el("input", { type: "text", name: "project", placeholder: "All projects", value: filters.project, list: "session-projects" });
  const projectOptions = el("datalist", { id: "session-projects" });
  const knownProjects = new Set();
  providerSel.value = filters.provider;
  stateSel.value = filters.state;

  const tbody = el("tbody");
  const status = el("div", {}, el("div", { class: "loading", text: "Loading sessions…" }));
  const more = el("button", { type: "button", hidden: true, text: "Load more", onclick: () => load(false) });

  app.append(
    el("h1", { text: "Sessions" }),
    el("form", { class: "filters", role: "search", onsubmit: (e) => e.preventDefault() },
      el("label", {}, "Machine", machineSel),
      el("label", {}, "Provider", providerSel),
      el("label", {}, "Project", project), projectOptions,
      el("label", {}, "State", stateSel),
      el("label", {}, "Search", search)),
    el("div", { class: "panel table-wrap" },
      el("table", { class: "sessions" },
        el("thead", {}, el("tr", {},
          el("th", { scope: "col", text: "Session" }),
          el("th", { scope: "col", text: "Machine" }),
          el("th", { scope: "col", text: "Provider" }),
          el("th", { scope: "col", text: "State" }),
          el("th", { scope: "col", class: "hide-sm", text: "Model" }),
          el("th", { scope: "col", class: "hide-sm", text: "Tokens" }),
          el("th", { scope: "col", text: "Last activity" }))),
        tbody)),
    status,
    el("div", { class: "more" }, more),
  );

  function syncHash() {
    const p = new URLSearchParams();
    for (const [k, v] of Object.entries(filters)) if (v) p.set(k, v);
    const h = p.size ? `#/sessions?${p}` : "#/sessions";
    lastListHash = h;
    if (location.hash !== h) history.replaceState(null, "", h);
  }

  function onFilter() {
    filters.machine = machineSel.value;
    filters.provider = providerSel.value;
    filters.project = project.value.trim();
    filters.state = stateSel.value;
    filters.q = search.value.trim();
    syncHash();
    load(true);
  }
  machineSel.addEventListener("change", onFilter);
  providerSel.addEventListener("change", onFilter);
  stateSel.addEventListener("change", onFilter);
  project.addEventListener("change", onFilter);
  let searchTimer = null;
  search.addEventListener("input", () => {
    clearTimeout(searchTimer);
    searchTimer = setTimeout(onFilter, 300);
  });

  function row(s) {
    const href = sessionHref(s.uid);
    const title = s.title || s.session_id;
    const sub = [s.project, s.branch].filter(Boolean).join(" · ") || s.cwd || "";
    return el("tr", { onclick: (e) => { if (!e.target.closest("a")) location.hash = href; } },
      el("td", {},
        el("a", { class: "title", href, text: title }),
        sub && el("span", { class: "sub", text: sub })),
      el("td", { text: machineNames.get(s.machine_id) || s.machine_id }),
      el("td", {}, el("span", { class: "provider", text: s.provider })),
      el("td", {}, badge(s.state)),
      el("td", { class: "model hide-sm", text: s.model || "—" }),
      el("td", { class: "num hide-sm", title: tokensTitle(s.tokens), text: tokensText(s.tokens) }),
      el("td", { class: "when" }, timeEl(s.last_activity_at)));
  }

  // load fetches the first page when reset is true, or the next page.
  // refill re-reads everything already shown, for live updates.
  async function load(reset, refill = false) {
    const my = ++token;
    if (reset) {
      cursor = null;
      if (!refill) {
        loaded = 0;
        status.replaceChildren(el("div", { class: "loading", text: "Loading sessions…" }));
      }
    }
    more.disabled = true;
    const limit = refill ? Math.min(Math.max(loaded, SESSION_PAGE), 500) : SESSION_PAGE;
    try {
      const res = await api.listSessions(filters, reset ? undefined : cursor, limit);
      if (!alive || my !== token) return;
      for (const s of res.sessions) if (s.project) knownProjects.add(s.project);
      projectOptions.replaceChildren(...[...knownProjects].sort().map((p) => el("option", { value: p })));
      const rows = res.sessions.map(row);
      if (reset) tbody.replaceChildren(...rows);
      else tbody.append(...rows);
      loaded = reset ? rows.length : loaded + rows.length;
      cursor = res.next_cursor || null;
      more.hidden = !cursor;
      status.replaceChildren(loaded === 0
        ? notice(api.mock || Object.values(filters).some(Boolean) ? "No sessions match these filters." : "No sessions yet. Run firekeeper report to send some.")
        : "");
    } catch (err) {
      if (!alive || my !== token) return;
      status.replaceChildren(notice(err.message, true));
    } finally {
      if (my === token) more.disabled = false;
    }
  }

  // Machine names label rows, so fetch them before the first page.
  api.listMachines().then((res) => {
    machineNames = new Map(res.machines.map((m) => [m.id, m.name || m.hostname || m.id]));
    for (const m of res.machines) machineSel.append(el("option", { value: m.id, text: machineNames.get(m.id) }));
  }).catch(() => {
    // The list still works with raw machine ids.
  }).then(() => {
    if (!alive) return;
    if (filters.machine && !machineNames.has(filters.machine)) {
      machineSel.append(el("option", { value: filters.machine, text: filters.machine }));
    }
    machineSel.value = filters.machine;
    load(true);
  });

  return {
    dispose() {
      alive = false;
      clearTimeout(searchTimer);
      clearTimeout(refreshTimer);
    },
    onStream(type) {
      if (type !== "session.updated" && type !== "machine.status") return;
      clearTimeout(refreshTimer);
      refreshTimer = setTimeout(() => load(true, true), 1000);
    },
  };
}

// ---------- machines overview ----------

const isOnline = (m, now = Date.now()) => {
  const t = Date.parse(m.last_heartbeat_at || "");
  return !Number.isNaN(t) && now - t < OFFLINE_AFTER_MS;
};

function machineCard(m, online) {
  const counts = m.state_counts || {};
  const total = Number(m.session_count) || 0;
  const chips = STATES.filter((st) => counts[st] > 0).map((st) => {
    const qs = new URLSearchParams({ machine: m.id, state: st });
    return el("a", { class: `state-chip ${st}`, href: `#/sessions?${qs}`, title: `${st.replace("_", " ")} sessions on ${m.name || m.id}` },
      el("b", { text: String(counts[st]) }), ` ${st.replace("_", " ").toLowerCase()}`);
  });
  const hostOS = [m.hostname, m.os, m.version && `v${m.version}`].filter(Boolean).join(" · ");
  const sessionsHref = `#/sessions?${new URLSearchParams({ machine: m.id })}`;
  return el("li", { class: `machine ${online ? "online" : "offline"}`, "data-machine": m.id },
    el("div", { class: "machine-head" },
      el("h2", { text: m.name || m.hostname || m.id }),
      el("span", { class: `presence ${online ? "online" : "offline"}` }, online ? "● Online" : "○ Offline")),
    el("p", { class: "machine-meta", text: hostOS || m.id }),
    el("dl", { class: "machine-facts" },
      el("div", {}, el("dt", { text: "Heartbeat" }), el("dd", {}, timeEl(m.last_heartbeat_at))),
      el("div", {}, el("dt", { text: "Last activity" }), el("dd", {}, timeEl(m.last_activity_at)))),
    total === 0
      ? el("p", { class: "machine-empty", text: "Linked, no sessions uploaded yet." })
      : el("div", { class: "state-chips" }, ...chips),
    el("a", { class: "machine-link", href: sessionsHref, text: total === 0 ? "Filter sessions" : `View ${total} session${total === 1 ? "" : "s"} →` }));
}

function linkInstructions() {
  return el("div", { class: "panel onboarding" },
    el("h2", { text: "No machines yet" }),
    el("p", { text: "Link a computer to this account, then send it your sessions:" }),
    el("pre", { class: "cmd", text: "firekeeper login\nfirekeeper daemon install" }),
    el("p", { class: "footnote", text: "Nothing is uploaded unless you opt a provider in. Machines appear here as soon as they link." }));
}

function machinesView() {
  let alive = true;
  let machines = null;
  let refreshTimer = null;
  const body = el("div", {}, el("div", { class: "loading", text: "Loading machines…" }));
  const summary = el("p", { class: "summary" });
  app.append(
    el("div", { class: "page-head" }, el("h1", { text: "Machines" }), summary,
      el("a", { class: "all-sessions", href: "#/sessions", text: "All sessions →" })),
    body);

  // paint draws from the last fetch, so the 90-second offline cutoff takes
  // effect on the clock without another request.
  function paint() {
    if (!machines) return;
    const now = Date.now();
    if (machines.length === 0) {
      summary.textContent = "";
      body.replaceChildren(linkInstructions());
      return;
    }
    const rows = machines.map((m) => ({ m, online: isOnline(m, now) }));
    rows.sort((a, b) => b.online - a.online
      || (b.m.last_activity_at || "").localeCompare(a.m.last_activity_at || "")
      || (a.m.name || a.m.id).localeCompare(b.m.name || b.m.id));
    const up = rows.filter((r) => r.online).length;
    const sessions = machines.reduce((n, m) => n + (Number(m.session_count) || 0), 0);
    summary.textContent = `${up} of ${machines.length} online · ${sessions} session${sessions === 1 ? "" : "s"}`;
    body.replaceChildren(el("ul", { class: "machines" }, ...rows.map((r) => machineCard(r.m, r.online))));
  }

  async function load() {
    try {
      const res = await api.listMachines();
      if (!alive) return;
      machines = res.machines || [];
      paint();
    } catch (err) {
      if (alive && !machines) body.replaceChildren(notice(err.message, true));
    }
  }
  load();
  const tick = setInterval(paint, 5000);

  return {
    dispose() {
      alive = false;
      clearInterval(tick);
      clearTimeout(refreshTimer);
    },
    onStream(type) {
      if (type !== "machine.status" && type !== "session.updated") return;
      clearTimeout(refreshTimer);
      refreshTimer = setTimeout(load, type === "machine.status" ? 300 : 1500);
    },
  };
}

// ---------- message text ----------

// renderText splits text on fenced code blocks. Prose keeps its whitespace
// and gets inline code spans; fences become <pre> blocks. An unclosed fence
// runs to the end of the text.
function renderText(text) {
  const frag = document.createDocumentFragment();
  if (!text) {
    frag.append(el("div", { class: "empty", text: "(no text)" }));
    return frag;
  }
  const lines = text.split("\n");
  let prose = [];
  let code = null;
  let lang = "";
  const flushProse = () => {
    const s = prose.join("\n").replace(/^\n+|\n+$/g, "");
    prose = [];
    if (s) frag.append(inlineProse(s));
  };
  for (const line of lines) {
    const fence = /^\s{0,3}(`{3,}|~{3,})\s*([\w.+#-]*)\s*$/.exec(line);
    if (code === null) {
      if (fence) {
        flushProse();
        code = [];
        lang = fence[2] || "";
      } else {
        prose.push(line);
      }
    } else if (fence && !fence[2]) {
      frag.append(codeEl(code.join("\n"), lang));
      code = null;
    } else {
      code.push(line);
    }
  }
  if (code !== null) frag.append(codeEl(code.join("\n"), lang));
  flushProse();
  return frag;
}

function inlineProse(s) {
  const div = el("div", { class: "prose" });
  const parts = s.split(/(`[^`\n]+`)/);
  for (const p of parts) {
    if (p.length > 2 && p.startsWith("`") && p.endsWith("`")) div.append(el("code", { class: "inline", text: p.slice(1, -1) }));
    else if (p) div.append(p);
  }
  return div;
}

function codeEl(text, lang) {
  return el("pre", {}, lang && el("span", { class: "lang", text: lang }), el("code", { text }));
}

// toolBody shows tool text verbatim, pretty-printing it when it is JSON.
function toolBody(text) {
  if (!text) return el("div", { class: "empty", text: "(no text)" });
  const t = text.trim();
  if ((t.startsWith("{") || t.startsWith("[")) && t.length < 200000) {
    try {
      return el("pre", {}, el("code", { text: JSON.stringify(JSON.parse(t), null, 2) }));
    } catch {
      // Not JSON; show as is.
    }
  }
  return el("pre", {}, el("code", { text }));
}

function previewOf(ev) {
  let s = ev.text || "";
  if (!s && ev.raw && typeof ev.raw === "object" && typeof ev.raw.type === "string") s = ev.raw.type;
  s = s.replace(/\s+/g, " ").trim();
  return s.length > PREVIEW_CHARS ? s.slice(0, PREVIEW_CHARS) + "…" : s;
}

// ---------- transcript ----------

const COLLAPSED = new Set(["tool_call", "tool_result", "meta"]);
const ROLE_LABEL = { tool_call: "tool call", tool_result: "tool result" };

// rawToggle returns the "show raw" button for ev and a setter. The raw
// record is stringified on first use.
function rawToggle(ev, container, ui) {
  let pre = null;
  const btn = el("button", { type: "button", class: "raw-toggle", "aria-pressed": "false", text: "show raw" });
  const set = (on) => {
    if (on && !pre) {
      pre = el("pre", { class: "raw" }, el("code", { text: JSON.stringify(ev.raw ?? null, null, 2) }));
      container.append(pre);
    }
    if (pre) pre.hidden = !on;
    btn.setAttribute("aria-pressed", String(on));
    btn.textContent = on ? "hide raw" : "show raw";
    if (on) ui.raw.add(ev.seq);
    else ui.raw.delete(ev.seq);
  };
  btn.addEventListener("click", (e) => {
    e.preventDefault();
    e.stopPropagation();
    const on = !ui.raw.has(ev.seq);
    set(on);
    // A collapsed event opens so the raw record is visible.
    const d = container.closest("details");
    if (on && d && !d.open) d.open = true;
  });
  return { btn, set };
}

function headBits(ev) {
  const role = ROLE_LABEL[ev.role] || ev.role || "meta";
  const bits = [el("span", { class: "role", text: role })];
  if (ev.tool_name) bits.push(el("span", { class: "tool-name", text: ev.tool_name }));
  return bits;
}

function headTail(ev) {
  const tok = ev.tokens || {};
  const bits = [];
  if (ev.model) bits.push(el("span", { text: ev.model }));
  if (tok.input || tok.output || tok.cache) bits.push(el("span", { title: tokensTitle(tok), text: tokensText(tok) }));
  if (ev.ts) bits.push(el("span", { title: absolute(ev.ts), text: new Date(ev.ts).toLocaleTimeString() }));
  bits.push(el("span", { class: "seq", text: `#${ev.seq}` }));
  return bits;
}

// eventEl renders one event. ui holds per-seq view state (open details,
// shown raw records) so an event renders the same after it is unmounted and
// mounted again.
function eventEl(ev, ui) {
  const role = ev.role || "meta";
  const article = el("article", { class: `event ${role}`, "data-seq": ev.seq });
  const body = el("div", { class: "body" });
  const raw = rawToggle(ev, body, ui);

  if (COLLAPSED.has(role)) {
    // Bodies of collapsed events are built on first open, so a long
    // transcript only pays for what the reader expands.
    const details = el("details");
    const summary = el("summary", {}, ...headBits(ev), el("span", { class: "preview", text: previewOf(ev) }), ...headTail(ev), raw.btn);
    let built = false;
    const build = () => {
      if (details.open && !built) {
        built = true;
        body.prepend(role === "meta" ? renderText(ev.text) : toolBody(ev.text));
      }
    };
    details.addEventListener("toggle", () => {
      build();
      ui.open.set(ev.seq, details.open);
    });
    details.append(summary, body);
    article.append(details);
    if (ui.open.get(ev.seq) ?? ui.expanded) {
      details.open = true;
      build();
    }
    if (ui.raw.has(ev.seq)) raw.set(true);
    return article;
  }

  article.append(
    el("div", { class: "event-head" }, ...headBits(ev), el("span", { class: "spacer" }), ...headTail(ev), raw.btn),
  );
  body.append(renderText(ev.text));
  article.append(body);
  if (ui.raw.has(ev.seq)) raw.set(true);
  return article;
}

function sessionHeader(s) {
  const fact = (k, v) => (v || v === 0 ? el("div", {}, el("dt", { text: k }), el("dd", {}, v)) : null);
  return el("section", { class: "panel session-head" },
    el("h1", { text: s.title || s.session_id }),
    el("dl", { class: "facts" },
      fact("state", badge(s.state)),
      fact("provider", s.provider),
      fact("machine", s.machine_id),
      fact("project", s.project),
      fact("branch", s.branch),
      fact("commit", s.commit ? el("code", { title: s.commit, text: s.commit.slice(0, 12) }) : null),
      fact("model", s.model),
      fact("tokens", el("span", { title: tokensTitle(s.tokens), text: tokensText(s.tokens) })),
      fact("events", (s.event_count || 0).toLocaleString()),
      fact("started", s.started_at ? timeEl(s.started_at) : null),
      fact("last activity", timeEl(s.last_activity_at)),
      fact("cwd", s.cwd),
      fact("session", s.session_id)));
}

// filesPanel lists the files a session's tool calls changed. Paths are
// relative to the session's cwd unless the file was outside it. A path
// links to the repository host only when the server has a link template.
function filesPanel(s, ui) {
  const files = s.files || [];
  const details = el("details", { class: "panel files" },
    el("summary", {}, el("span", { text: "Files changed" }), el("span", { class: "count", text: files.length.toLocaleString() })));
  details.open = ui.filesOpen;
  details.addEventListener("toggle", () => { ui.filesOpen = details.open; });
  if (!files.length) {
    details.append(el("p", { class: "empty", text: "No file edits found in this transcript." }));
    return details;
  }
  const list = el("ul");
  for (const f of files) {
    const name = f.url
      ? el("a", { href: f.url, target: "_blank", rel: "noopener noreferrer", text: f.path })
      : el("span", { text: f.path });
    const seqs = f.first_seq === f.last_seq ? `#${f.first_seq}` : `#${f.first_seq}–${f.last_seq}`;
    list.append(el("li", {},
      el("span", { class: "path" + (f.absolute ? " outside" : ""), title: f.absolute ? "Outside the session's working directory" : null }, name),
      el("span", { class: "meta", title: `Changed by ${f.changes} tool ${f.changes === 1 ? "call" : "calls"}; events ${seqs}` },
        `${f.changes}× · ${seqs}`)));
  }
  details.append(list);
  return details;
}

function sessionView(uid) {
  let alive = true;
  let session = null;
  let lastSeq = -1;
  let hasMore = true;
  let loading = false;
  let shown = 0;
  let headTimer = null;

  const head = el("div", {}, el("div", { class: "loading", text: "Loading session…" }));
  const list = el("div", { class: "events" });
  const status = el("div");
  const sentinel = el("div", { class: "sentinel", "aria-hidden": "true" });
  const count = el("span", { class: "count" });
  const nextBtn = el("button", { type: "button", hidden: true, text: "Load more", onclick: () => loadMore() });
  const ui = { expanded: false, open: new Map(), raw: new Set(), filesOpen: true };
  const expandBtn = el("button", { type: "button", text: "Expand tool events" });
  expandBtn.addEventListener("click", () => {
    ui.expanded = !ui.expanded;
    ui.open.clear();
    expandBtn.textContent = ui.expanded ? "Collapse tool events" : "Expand tool events";
    for (const d of list.querySelectorAll(".event > details")) d.open = ui.expanded;
  });

  // Events are kept in memory and rendered in chunks. A chunk far from the
  // viewport is swapped for an empty box of its measured height, so the DOM
  // stays small however long the transcript grows.
  const chunks = [];
  const chunkOf = new WeakMap();
  const mount = (c) => {
    c.node.replaceChildren(...c.events.map((ev) => eventEl(ev, ui)));
    c.node.style.height = "";
    c.mounted = true;
  };
  const unmount = (c) => {
    c.node.style.height = `${c.node.offsetHeight}px`;
    c.node.replaceChildren();
    c.mounted = false;
  };
  const chunkObserver = "IntersectionObserver" in window
    ? new IntersectionObserver((entries) => {
      for (const e of entries) {
        const c = chunkOf.get(e.target);
        if (!c) continue;
        if (e.isIntersecting && !c.mounted) mount(c);
        else if (!e.isIntersecting && c.mounted) unmount(c);
      }
    }, { rootMargin: "2500px 0px" })
    : null;

  function appendEvent(ev) {
    let c = chunks.at(-1);
    if (!c || c.events.length >= CHUNK_SIZE) {
      c = { node: el("div", { class: "chunk" }), events: [], mounted: true };
      chunks.push(c);
      chunkOf.set(c.node, c);
      list.append(c.node);
      chunkObserver?.observe(c.node);
    }
    c.events.push(ev);
    // An unmounted chunk keeps its old height until it is mounted again.
    if (c.mounted) c.node.append(eventEl(ev, ui));
  }

  app.append(
    el("a", { class: "back", href: lastListHash, text: "← Sessions" }),
    head,
    el("div", { class: "toolbar" }, expandBtn, count),
    list,
    sentinel,
    status,
    el("div", { class: "more" }, nextBtn),
  );

  function updateCount() {
    const total = session?.event_count ?? 0;
    count.textContent = hasMore
      ? `${shown.toLocaleString()} of ${Math.max(total, shown).toLocaleString()} events loaded`
      : `${shown.toLocaleString()} events`;
  }

  async function loadHead() {
    try {
      const s = await api.getSession(uid);
      if (!alive) return;
      session = s;
      head.replaceChildren(sessionHeader(s), filesPanel(s, ui));
      document.title = `${s.title || s.session_id} · Firekeeper`;
      updateCount();
    } catch (err) {
      if (!alive) return;
      head.replaceChildren(notice(err.status === 404 ? "Session not found." : err.message, true));
      if (err.status === 404) {
        hasMore = false;
        observer.disconnect();
      }
    }
  }

  async function loadMore() {
    if (loading || !alive) return;
    loading = true;
    nextBtn.disabled = true;
    status.replaceChildren(el("div", { class: "loading", text: "Loading events…" }));
    try {
      const res = await api.listEvents(uid, lastSeq, EVENT_PAGE);
      if (!alive) return;
      for (const ev of res.events) {
        if (ev.seq <= lastSeq) continue;
        appendEvent(ev);
        lastSeq = ev.seq;
        shown++;
      }
      hasMore = res.has_more;
      status.replaceChildren(!hasMore && shown === 0 ? notice("No events stored for this session.") : "");
    } catch (err) {
      if (!alive) return;
      // Keep what is loaded; the button retries.
      status.replaceChildren(notice(err.status === 404 ? "Session not found." : err.message, true));
      if (err.status === 404) hasMore = false;
    } finally {
      if (alive) {
        loading = false;
        nextBtn.disabled = false;
        nextBtn.hidden = !hasMore || "IntersectionObserver" in window;
        updateCount();
        // Keep filling while the sentinel is still on screen.
        if (hasMore && sentinelNear()) queueMicrotask(loadMore);
      }
    }
  }

  function sentinelNear() {
    const r = sentinel.getBoundingClientRect();
    return r.top < window.innerHeight + 1500;
  }

  const observer = "IntersectionObserver" in window
    ? new IntersectionObserver((entries) => {
      if (entries.some((e) => e.isIntersecting) && hasMore) loadMore();
    }, { rootMargin: "0px 0px 1500px 0px" })
    : { observe() {}, disconnect() {} };
  observer.observe(sentinel);

  loadHead();
  loadMore();

  return {
    dispose() {
      alive = false;
      observer.disconnect();
      chunkObserver?.disconnect();
      clearTimeout(headTimer);
      document.title = "Firekeeper";
    },
    onStream(type, data) {
      if (!data || data.uid !== uid) return;
      if (type === "event.appended" && !hasMore && !loading) {
        // Live tail: the reader has reached the end, so fetch the new events.
        hasMore = true;
        loadMore();
      } else if (type === "event.appended") {
        hasMore = true;
      }
      if (type === "session.updated" || type === "event.appended") {
        clearTimeout(headTimer);
        headTimer = setTimeout(loadHead, 1000);
      }
    },
  };
}

// ---------- usage ----------

const SVG_NS = "http://www.w3.org/2000/svg";
const RANGES = [7, 30, 90];
const SERIES_SLOTS = 7; // colored models; the rest fold into "Other"
const METRICS = {
  total: { label: "All tokens", of: (r) => tokTotal(r.tokens) },
  input: { label: "Input", of: (r) => r.tokens.input || 0 },
  output: { label: "Output", of: (r) => r.tokens.output || 0 },
  cache: { label: "Cache", of: (r) => r.tokens.cache || 0 },
  cost: { label: "Cost", of: (r) => r.cost || 0, priced: true },
};

// modelSlots keeps each model's color for the whole page visit, so changing
// the range or metric never repaints a model that is still shown.
const modelSlots = new Map();

function svg(tag, attrs, ...children) {
  const n = document.createElementNS(SVG_NS, tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v === undefined || v === null) continue;
    if (k === "css") for (const [p, pv] of Object.entries(v)) n.style.setProperty(p, pv);
    else n.setAttribute(k, String(v));
  }
  for (const c of children) if (c) n.append(c);
  return n;
}

function tokTotal(t) {
  t = t || {};
  return (t.input || 0) + (t.output || 0) + (t.cache || 0);
}

function money(n) {
  n = Number(n) || 0;
  if (n === 0) return "0.00";
  if (n < 0.01) return n.toFixed(4);
  return n.toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 });
}

const isoDay = (t) => new Date(t).toISOString().slice(0, 10);
const todayUTC = () => isoDay(Date.now());
const addDays = (day, n) => isoDay(Date.parse(day) + n * 86400e3);
const validDay = (d) => /^\d{4}-\d{2}-\d{2}$/.test(d || "") && !Number.isNaN(Date.parse(d));
const shortDay = (day) => new Date(day).toLocaleDateString(undefined, { month: "short", day: "numeric", timeZone: "UTC" });

function modelName(m) {
  return m || "(unknown model)";
}

// assignSlots gives each of the top models a stable slot 1..SERIES_SLOTS.
function assignSlots(models) {
  const used = new Set();
  const want = [];
  for (const m of models) {
    const s = modelSlots.get(m);
    if (s && !used.has(s)) used.add(s);
    else want.push(m);
  }
  for (const m of want) {
    let s = 1;
    while (used.has(s)) s++;
    modelSlots.set(m, s);
    used.add(s);
  }
}

// niceMax rounds a positive maximum up to 1, 2, 2.5, or 5 times a power of ten.
function niceMax(v) {
  if (!(v > 0)) return 1;
  const p = 10 ** Math.floor(Math.log10(v));
  for (const m of [1, 2, 2.5, 5, 10]) if (v <= m * p) return m * p;
  return 10 * p;
}

// barPath draws a bar segment with 4px rounded top corners when it is the
// top of its stack and wide enough to show them.
function barPath(x, y, w, h, roundTop) {
  const r = roundTop ? Math.min(4, w / 2, h) : 0;
  if (!r) return `M${x},${y + h}V${y}H${x + w}V${y + h}Z`;
  return `M${x},${y + h}V${y + r}Q${x},${y} ${x + r},${y}H${x + w - r}Q${x + w},${y} ${x + w},${y + r}V${y + h}Z`;
}

function usageView(params) {
  let alive = true;
  let token = 0;
  let refreshTimer = null;
  let data = null; // { daily, projects, from, to, priced }

  let range = params.get("range") || "30";
  let from = params.get("from");
  let to = params.get("to");
  let metric = params.get("metric") || "total";
  let project = params.get("project") || "";
  if (!(metric in METRICS)) metric = "total";
  if (range === "custom" && !(validDay(from) && validDay(to) && from <= to)) range = "30";
  if (range !== "custom" && !RANGES.includes(Number(range))) range = "30";

  const rangeSel = el("select", { name: "range" },
    ...RANGES.map((n) => el("option", { value: String(n), text: `Last ${n} days` })),
    el("option", { value: "custom", text: "Custom" }));
  const fromIn = el("input", { type: "date", name: "from" });
  const toIn = el("input", { type: "date", name: "to" });
  const metricSel = el("select", { name: "metric" });
  const projectIn = el("input", { type: "text", name: "project", placeholder: "All projects", value: project, list: "usage-projects" });
  const projectOptions = el("datalist", { id: "usage-projects" });
  const knownProjects = new Set();
  const fromLabel = el("label", {}, "From", fromIn);
  const toLabel = el("label", {}, "To", toIn);

  const status = el("div", {}, el("div", { class: "loading", text: "Loading usage…" }));
  const tiles = el("div", { class: "tiles" });
  const chartTitle = el("h2");
  const legend = el("ul", { class: "legend" });
  const chartBox = el("div", { class: "chart" });
  const tip = el("div", { class: "chart-tip", role: "status", hidden: true });
  const dailyTable = el("div", { class: "table-wrap" });
  const dailyDetails = el("details", { class: "daily" }, el("summary", { text: "Show daily table" }), dailyTable);
  const projectTable = el("div", { class: "panel table-wrap" });
  const content = el("div", { hidden: true },
    tiles,
    el("section", { class: "panel chart-panel" }, chartTitle, legend, el("div", { class: "chart-wrap" }, chartBox, tip), dailyDetails),
    el("h2", { text: "Totals by project" }),
    projectTable);
  const rangeNote = el("p", { class: "range-note" });

  app.append(
    el("h1", { text: "Usage" }),
    el("form", { class: "filters", onsubmit: (e) => e.preventDefault() },
      el("label", {}, "Range", rangeSel), fromLabel, toLabel,
      el("label", {}, "Project", projectIn), projectOptions, el("label", {}, "Chart", metricSel)),
    rangeNote,
    status,
    content,
  );

  function bounds() {
    if (range === "custom") return [from, to];
    const t = todayUTC();
    return [addDays(t, 1 - Number(range)), t];
  }

  function syncControls() {
    const [f, t] = bounds();
    rangeSel.value = range;
    fromIn.value = f;
    toIn.value = t;
    const custom = range === "custom";
    fromIn.disabled = !custom;
    toIn.disabled = !custom;
    rangeNote.textContent = `${shortDay(f)} – ${shortDay(t)} (UTC days). Tokens are summed from stored transcript events that have a timestamp.`;
  }

  function syncHash() {
    const p = new URLSearchParams();
    if (range !== "30") p.set("range", range);
    if (range === "custom") {
      p.set("from", from);
      p.set("to", to);
    }
    if (metric !== "total") p.set("metric", metric);
    if (project) p.set("project", project);
    const h = p.size ? `#/usage?${p}` : "#/usage";
    if (location.hash !== h) history.replaceState(null, "", h);
  }

  // fillMetrics lists the chart metrics. Cost exists only with a price
  // table, so a requested cost metric waits for the first response.
  function fillMetrics(priced) {
    const opts = Object.entries(METRICS).filter(([, m]) => !m.priced || priced);
    if (!opts.some(([k]) => k === metric)) {
      if (data) metric = "total";
      else opts.push([metric, METRICS[metric]]);
    }
    metricSel.replaceChildren(...opts.map(([k, m]) => el("option", { value: k, text: m.label })));
    metricSel.value = metric;
  }

  rangeSel.addEventListener("change", () => {
    if (rangeSel.value === "custom") {
      [from, to] = bounds();
      range = "custom";
    } else {
      range = rangeSel.value;
    }
    syncControls();
    syncHash();
    load();
  });
  const onDate = () => {
    if (!validDay(fromIn.value) || !validDay(toIn.value)) return;
    from = fromIn.value;
    to = toIn.value;
    if (from > to) [from, to] = [to, from];
    syncControls();
    syncHash();
    load();
  };
  fromIn.addEventListener("change", onDate);
  toIn.addEventListener("change", onDate);
  projectIn.addEventListener("change", () => {
    project = projectIn.value.trim();
    syncHash();
    load();
  });
  metricSel.addEventListener("change", () => {
    metric = metricSel.value;
    syncHash();
    if (data) drawChart();
  });

  async function load(quiet = false) {
    const my = ++token;
    const [f, t] = bounds();
    if (!quiet) status.replaceChildren(el("div", { class: "loading", text: "Loading usage…" }));
    try {
      const [daily, projects] = await Promise.all([
        api.usage(f, t, ["day", "model"], project),
        api.usage(f, t, ["project"], project),
      ]);
      if (!alive || my !== token) return;
      data = { daily, projects, from: f, to: t, priced: !!daily.priced };
      for (const r of projects.rows) if (r.group.project) knownProjects.add(r.group.project);
      projectOptions.replaceChildren(...[...knownProjects].sort().map((p) => el("option", { value: p })));
      fillMetrics(data.priced);
      if (daily.rows.length === 0) {
        content.hidden = true;
        status.replaceChildren(notice(`No token usage recorded${project ? ` for project "${project}"` : ""} between ${shortDay(f)} and ${shortDay(t)}.`));
        return;
      }
      status.replaceChildren();
      content.hidden = false;
      drawTiles();
      drawChart();
      drawProjects();
    } catch (err) {
      if (!alive || my !== token) return;
      status.replaceChildren(notice(err.message, true));
    }
  }

  function drawTiles() {
    const t = data.projects.totals;
    const tile = (label, value, title) => el("div", { class: "tile", title },
      el("div", { class: "tile-label", text: label }), el("div", { class: "tile-value", text: value }));
    const f = (n) => (Number(n) || 0).toLocaleString();
    const list = [
      tile("Total tokens", compact(tokTotal(t.tokens)), f(tokTotal(t.tokens))),
      tile("Input", compact(t.tokens.input), f(t.tokens.input)),
      tile("Output", compact(t.tokens.output), f(t.tokens.output)),
      tile("Cache", compact(t.tokens.cache), f(t.tokens.cache)),
    ];
    if (data.priced) {
      const partial = t.unpriced_tokens > 0;
      list.push(tile(partial ? "Cost (partial)" : "Cost", money(t.cost),
        partial ? `Excludes ${f(t.unpriced_tokens)} tokens from models without a price` : "From the configured price table"));
    }
    tiles.replaceChildren(...list);
  }

  // series ranks models by the chosen metric over the range; the top
  // SERIES_SLOTS keep their own color and the rest become "Other".
  function series() {
    const m = METRICS[metric];
    const byModel = new Map();
    for (const r of data.daily.rows) byModel.set(r.group.model, (byModel.get(r.group.model) || 0) + m.of(r));
    const ranked = [...byModel.entries()].filter(([, v]) => v > 0).sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]));
    let top = ranked.map(([k]) => k);
    let other = false;
    if (top.length > SERIES_SLOTS) {
      top = top.slice(0, SERIES_SLOTS - 1);
      other = true;
    }
    assignSlots(top);
    const list = top.map((k) => ({ key: k, label: modelName(k), color: `var(--series-${modelSlots.get(k)})`, total: byModel.get(k) }));
    if (other) {
      const shown = new Set(top);
      const rest = ranked.filter(([k]) => !shown.has(k));
      list.push({ key: null, label: `Other (${rest.length} models)`, color: "var(--series-other)", total: rest.reduce((a, [, v]) => a + v, 0) });
    }
    return list;
  }

  function drawChart() {
    const m = METRICS[metric];
    const fmt = metric === "cost" ? money : compact;
    chartTitle.textContent = {
      total: "Daily tokens by model", cost: "Daily cost by model",
    }[metric] || `Daily ${m.label.toLowerCase()} tokens by model`;
    const ser = series();
    const keyOf = new Map(ser.filter((x) => x.key !== null).map((x, i) => [x.key, i]));
    const otherIdx = ser.findIndex((x) => x.key === null);

    // One column per UTC day in range, including days with no usage.
    const days = [];
    for (let d = data.from; d <= data.to; d = addDays(d, 1)) days.push(d);
    const dayIdx = new Map(days.map((d, i) => [d, i]));
    const cols = days.map(() => ser.map(() => 0));
    const costPartial = days.map(() => 0);
    for (const r of data.daily.rows) {
      const i = dayIdx.get(r.group.day);
      if (i === undefined) continue;
      const si = keyOf.has(r.group.model) ? keyOf.get(r.group.model) : otherIdx;
      if (si >= 0) cols[i][si] += m.of(r);
      costPartial[i] += r.unpriced_tokens || 0;
    }

    legend.replaceChildren(...ser.map((x) => el("li", {},
      el("span", { class: "swatch", css: { background: x.color } }),
      el("span", { class: "legend-label", text: x.label }),
      el("span", { class: "legend-value", text: fmt(x.total) }))));

    const width = Math.max(chartBox.clientWidth || 600, 280);
    const height = 240;
    const pad = { l: 48, r: 8, t: 10, b: 24 };
    const pw = width - pad.l - pad.r;
    const ph = height - pad.t - pad.b;
    const max = niceMax(Math.max(0, ...cols.map((c) => c.reduce((a, b) => a + b, 0))));
    const step = pw / days.length;
    const gap = Math.min(2, step * 0.25);
    const bw = Math.min(Math.max(step - gap, 1), 48);
    const y = (v) => pad.t + ph - (v / max) * ph;

    const root = svg("svg", { width, height, viewBox: `0 0 ${width} ${height}`, role: "img",
      "aria-label": `${chartTitle.textContent}, ${shortDay(data.from)} to ${shortDay(data.to)}. The daily table below lists the same values.` });
    for (let i = 0; i <= 4; i++) {
      const v = (max / 4) * i;
      root.append(
        svg("line", { x1: pad.l, x2: width - pad.r, y1: y(v), y2: y(v), class: i === 0 ? "axis" : "grid" }),
        svg("text", { x: pad.l - 6, y: y(v) + 4, "text-anchor": "end", class: "tick" }, document.createTextNode(fmt(v))));
    }
    const labelEvery = Math.max(1, Math.ceil(days.length / Math.max(2, Math.floor(pw / 64))));
    days.forEach((d, i) => {
      if (i % labelEvery !== 0) return;
      root.append(svg("text", { x: pad.l + i * step + step / 2, y: height - 6, "text-anchor": "middle", class: "tick" },
        document.createTextNode(shortDay(d))));
    });

    const bars = svg("g");
    const hits = svg("g");
    days.forEach((d, i) => {
      const x = pad.l + i * step + (step - bw) / 2;
      let base = 0;
      const top = cols[i].reduce((a, v, si) => (v > 0 ? si : a), -1);
      cols[i].forEach((v, si) => {
        if (v <= 0) return;
        const y0 = y(base);
        const y1 = y(base + v);
        base += v;
        // A 2px surface gap separates stacked segments.
        const h = Math.max(y0 - y1 - (base > v ? Math.min(2, (y0 - y1) / 2) : 0), 0.5);
        bars.append(svg("path", { d: barPath(x, y1, bw, h, si === top), css: { fill: ser[si].color } }));
      });
      // The hit target spans the whole column so thin bars are easy to hover.
      const hit = svg("rect", { x: pad.l + i * step, y: pad.t, width: step, height: ph, class: "hit" });
      hit.addEventListener("mouseenter", () => showTip(d, cols[i], ser, fmt, costPartial[i], pad.l + i * step + step / 2, width));
      hit.addEventListener("mouseleave", hideTip);
      hits.append(hit);
    });
    root.append(bars, hits);
    chartBox.replaceChildren(root);
    hideTip();
    if (dailyDetails.open) drawDaily();
  }

  function showTip(day, col, ser, fmt, partial, cx, width) {
    const total = col.reduce((a, b) => a + b, 0);
    const lines = ser.map((x, si) => [x, col[si]]).filter(([, v]) => v > 0).reverse();
    tip.replaceChildren(...[
      el("div", { class: "tip-head" }, el("strong", { text: shortDay(day) }), el("span", { text: fmt(total) })),
      ...lines.map(([x, v]) => el("div", { class: "tip-row" },
        el("span", { class: "swatch", css: { background: x.color } }),
        el("span", { class: "tip-label", text: x.label }),
        el("span", { class: "tip-value", text: fmt(v) }))),
      lines.length === 0 ? el("div", { class: "tip-row muted", text: "No usage" }) : null,
      metric === "cost" && partial > 0 ? el("div", { class: "tip-row muted", text: `${compact(partial)} tokens unpriced` }) : null,
    ].filter(Boolean));
    tip.hidden = false;
    const w = tip.offsetWidth;
    tip.style.left = `${Math.min(Math.max(cx - w / 2, 0), width - w)}px`;
  }
  function hideTip() {
    tip.hidden = true;
  }

  function drawDaily() {
    const priced = data.priced;
    const rows = data.daily.rows.slice().reverse();
    const f = (n) => (Number(n) || 0).toLocaleString();
    dailyTable.replaceChildren(el("table", { class: "data" },
      el("thead", {}, el("tr", {},
        el("th", { scope: "col", text: "Day" }), el("th", { scope: "col", text: "Model" }),
        el("th", { scope: "col", class: "num", text: "Input" }), el("th", { scope: "col", class: "num", text: "Output" }),
        el("th", { scope: "col", class: "num", text: "Cache" }), el("th", { scope: "col", class: "num", text: "Total" }),
        priced && el("th", { scope: "col", class: "num", text: "Cost" }))),
      el("tbody", {}, ...rows.map((r) => el("tr", {},
        el("td", { text: r.group.day }), el("td", { class: "model", text: modelName(r.group.model) }),
        el("td", { class: "num", text: f(r.tokens.input) }), el("td", { class: "num", text: f(r.tokens.output) }),
        el("td", { class: "num", text: f(r.tokens.cache) }), el("td", { class: "num", text: f(tokTotal(r.tokens)) }),
        priced && costCell(r))))));
  }
  dailyDetails.addEventListener("toggle", () => {
    if (dailyDetails.open && data) drawDaily();
  });

  function costCell(r) {
    const partial = r.unpriced_tokens > 0;
    return el("td", { class: "num", title: partial ? `Excludes ${(r.unpriced_tokens).toLocaleString()} tokens from models without a price` : undefined },
      money(r.cost), partial ? el("span", { class: "partial", text: " *" }) : null);
  }

  function drawProjects() {
    const priced = data.priced;
    const t = data.projects.totals;
    const all = tokTotal(t.tokens) || 1;
    const f = (n) => (Number(n) || 0).toLocaleString();
    const anyPartial = data.projects.rows.some((r) => r.unpriced_tokens > 0);
    const line = (name, r, cls) => el("tr", { class: cls },
      el("td", { class: "project", text: name }),
      el("td", { class: "num", text: f(r.tokens.input) }),
      el("td", { class: "num", text: f(r.tokens.output) }),
      el("td", { class: "num hide-sm", text: f(r.tokens.cache) }),
      el("td", { class: "num", text: f(tokTotal(r.tokens)) }),
      el("td", { class: "share hide-sm" },
        el("span", { class: "share-track" },
          el("span", { class: "share-bar", css: { width: `${((tokTotal(r.tokens) / all) * 100).toFixed(1)}%` } })),
        el("span", { class: "share-pct", text: `${((tokTotal(r.tokens) / all) * 100).toFixed(1)}%` })),
      priced && costCell(r));
    projectTable.replaceChildren(...[
      el("table", { class: "data" },
        el("thead", {}, el("tr", {},
          el("th", { scope: "col", text: "Project" }),
          el("th", { scope: "col", class: "num", text: "Input" }),
          el("th", { scope: "col", class: "num", text: "Output" }),
          el("th", { scope: "col", class: "num hide-sm", text: "Cache" }),
          el("th", { scope: "col", class: "num", text: "Total" }),
          el("th", { scope: "col", class: "hide-sm", text: "Share" }),
          priced && el("th", { scope: "col", class: "num", text: "Cost" }))),
        el("tbody", {}, ...data.projects.rows.map((r) => line(r.group.project || "(no project)", r))),
        el("tfoot", {}, line("Total", { ...t, group: {} }, "total"))),
      anyPartial && el("p", { class: "footnote", text: "* Cost covers priced models only; some tokens came from models with no price in the configured table." }),
    ].filter(Boolean));
  }

  const resize = "ResizeObserver" in window
    ? new ResizeObserver(() => {
      if (data && !content.hidden) drawChart();
    })
    : null;
  resize?.observe(chartBox);

  syncControls();
  fillMetrics(false);
  load();

  return {
    dispose() {
      alive = false;
      resize?.disconnect();
      clearTimeout(refreshTimer);
    },
    onStream(type) {
      if (type !== "event.appended") return;
      clearTimeout(refreshTimer);
      refreshTimer = setTimeout(() => load(true), 5000);
    },
  };
}

// ---------- account ----------

function bytesText(n) {
  n = Number(n) || 0;
  if (n < 1024) return `${n} B`;
  const units = ["KiB", "MiB", "GiB", "TiB"];
  let i = -1;
  do {
    n /= 1024;
    i++;
  } while (n >= 1024 && i < units.length - 1);
  return `${n.toFixed(n < 10 ? 2 : 1)} ${units[i]}`;
}

// ofLimit renders "used of limit", or just "used" when there is no limit.
const ofLimit = (used, max, fmt) => (max > 0 ? `${fmt(used)} of ${fmt(max)}` : fmt(used));

// accountView is the signed-in person's own page: what is stored, a full
// export, and permanent deletion confirmed with the account's email.
function accountView() {
  let alive = true;
  const box = el("div", { class: "account-page" });
  app.append(box);
  box.append(el("p", { text: "Loading…" }));

  (async () => {
    let acct;
    try {
      acct = await api.account();
    } catch (err) {
      if (alive) box.replaceChildren(notice(err.message, true));
      return;
    }
    if (!alive) return;
    if (acct.single_user || !acct.email) {
      box.replaceChildren(el("h1", { text: "Account" }),
        notice("This server is in single-user mode, so there is no account to export or delete.", false));
      return;
    }
    const u = acct.usage || {};
    const l = acct.limits || {};
    const status = el("div");
    const confirm = el("input", { type: "email", id: "confirm-email", autocomplete: "off", spellcheck: "false", placeholder: acct.email });
    const del = el("button", { type: "button", disabled: true }, "Delete my account");
    confirm.addEventListener("input", () => {
      del.disabled = confirm.value.trim().toLowerCase() !== acct.email.toLowerCase();
    });
    del.addEventListener("click", async () => {
      del.disabled = true;
      status.replaceChildren();
      try {
        await api.deleteAccount(confirm.value.trim());
      } catch (err) {
        status.append(notice(err.message, true));
        del.disabled = false;
        return;
      }
      auth.clear();
      toLogin("deleted");
    });

    box.replaceChildren(
      el("h1", { text: "Account" }),
      el("section", null,
        el("h2", { text: acct.email }),
        el("dl", null,
          el("dt", { text: "Sessions" }), el("dd", { text: ofLimit(u.sessions || 0, l.max_sessions, (n) => n.toLocaleString()) }),
          el("dt", { text: "Events" }), el("dd", { text: (u.events || 0).toLocaleString() }),
          el("dt", { text: "Stored" }), el("dd", { text: ofLimit(u.stored_bytes || 0, l.max_bytes, bytesText) }),
          l.ingest_per_minute > 0 && el("dt", { text: "Upload rate" }),
          l.ingest_per_minute > 0 && el("dd", { text: `${l.ingest_per_minute} requests per minute` }),
        ),
        el("p", null, "Transcripts are stored on this server and the operator can read them. ", el("a", { href: "privacy" }, "Privacy notice"), "."),
      ),
      el("section", null,
        el("h2", { text: "Download my data" }),
        el("p", { text: "Everything stored for your account as a JSON Lines file: machines, sessions, changed files and every event, as uploaded." }),
        api.exportURL
          ? el("a", { href: api.exportURL, download: "firekeeper-export.jsonl" }, "Download firekeeper-export.jsonl")
          : el("p", { text: "Export is not available in mock mode." }),
      ),
      el("section", { class: "danger" },
        el("h2", { text: "Delete my account" }),
        el("p", { text: "Permanently removes your account, your access tokens, and every machine, session and event you uploaded, including the search index. This cannot be undone. Your local session files are not touched." }),
        el("label", { for: "confirm-email", text: "Type your email address to confirm" }),
        confirm,
        del,
        status,
      ),
    );
  })();

  return { dispose() { alive = false; }, onStream() {} };
}

// ---------- tokens ----------

function tokensView() {
  let alive = true;
  const list = el("div", {}, el("div", { class: "loading", text: "Loading tokens…" }));
  const secretBox = el("div");
  const formErr = el("div");

  const name = el("input", { type: "text", name: "name", required: true, maxlength: "64", autocomplete: "off", placeholder: "ci-runner" });
  const scope = el("select", { name: "scope" },
    el("option", { value: "ingest", text: "ingest (a machine uploads)" }),
    el("option", { value: "read", text: "read (view only)" }));
  const machine = el("input", { type: "text", name: "machine_id", maxlength: "128", autocomplete: "off", placeholder: "contents of ~/.firekeeper/machine-id" });
  const machineLabel = el("label", {}, "Machine id", machine);
  const submit = el("button", { type: "submit", text: "Create token" });
  const syncScope = () => {
    machineLabel.hidden = scope.value !== "ingest";
    machine.required = scope.value === "ingest";
  };
  scope.addEventListener("change", syncScope);
  syncScope();

  async function reload() {
    try {
      const toks = await api.listTokens();
      if (alive) list.replaceChildren(table(toks));
    } catch (err) {
      if (alive) list.replaceChildren(notice(tokenError(err), true));
    }
  }

  function tokenError(err) {
    if (err.code === "session_required") return "Managing tokens needs a signed-in account. Create one with firekeeper serve account create, then sign in.";
    return err.message;
  }

  function table(toks) {
    if (!toks.length) return el("p", { class: "empty", text: "No tokens yet. Link a machine or create one below." });
    const row = (t) => el("tr", { class: t.revoked_at ? "revoked" : undefined },
      el("td", { class: "project", text: t.name }),
      el("td", { text: t.scope }),
      el("td", { class: "model hide-sm", title: t.machine_id, text: t.machine_id ? t.machine_id.slice(0, 8) : "—" }),
      el("td", {}, timeEl(t.created_at)),
      el("td", {}, t.last_used_at ? timeEl(t.last_used_at) : el("span", { class: "muted", text: "never" })),
      el("td", {}, t.revoked_at
        ? el("span", { class: "muted", text: "revoked" })
        : el("button", { type: "button", text: "Revoke", onclick: () => revoke(t) })));
    return el("table", { class: "data" },
      el("thead", {}, el("tr", {},
        ...["Name", "Scope"].map((h) => el("th", { scope: "col", text: h })),
        el("th", { scope: "col", class: "hide-sm", text: "Machine" }),
        ...["Created", "Last used", ""].map((h) => el("th", { scope: "col", text: h })))),
      el("tbody", {}, ...toks.map(row)));
  }

  async function revoke(t) {
    if (!confirm(`Revoke "${t.name}"? Anything using it stops uploading at its next request.`)) return;
    try {
      await api.revokeToken(t.id);
    } catch (err) {
      list.prepend(notice(tokenError(err), true));
      return;
    }
    await reload();
  }

  function showSecret(created) {
    const code = el("code", { class: "secret", text: created.token });
    const copy = el("button", { type: "button", text: "Copy", onclick: async () => {
      try {
        await navigator.clipboard.writeText(created.token);
        copy.textContent = "Copied";
      } catch {
        const r = document.createRange();
        r.selectNodeContents(code);
        getSelection().removeAllRanges();
        getSelection().addRange(r);
      }
    } });
    secretBox.replaceChildren(el("div", { class: "panel secret-panel", role: "status" },
      el("p", { text: `Token “${created.name}” created. Copy it now: it is shown once and cannot be recovered.` }),
      el("div", { class: "secret-row" }, code, copy),
      el("p", { class: "muted", text: created.scope === "ingest"
        ? "On the machine, run: FIREKEEPER_TOKEN=… firekeeper report --provider NAME, or set token in ~/.firekeeper/config.toml."
        : "Send it as an Authorization: Bearer header." })));
  }

  const form = el("form", { class: "panel token-form", onsubmit: async (e) => {
    e.preventDefault();
    submit.disabled = true;
    formErr.replaceChildren();
    try {
      const created = await api.createToken(name.value.trim(), scope.value, machine.value.trim());
      showSecret(created);
      name.value = "";
      machine.value = "";
      await reload();
    } catch (err) {
      formErr.append(notice(tokenError(err), true));
    }
    submit.disabled = false;
  } },
    el("h2", { text: "Create a token" }),
    el("p", { class: "muted", text: "For CI and headless machines. A browser-linked laptop does not need one." }),
    formErr,
    el("label", {}, "Name", name),
    el("label", {}, "Scope", scope),
    machineLabel,
    submit);

  app.append(
    el("h1", { text: "Tokens" }),
    el("p", { class: "range-note" }, "Link a laptop without copying secrets: run ",
      el("code", { class: "inline", text: `firekeeper login --server ${location.origin}` }), " on it."),
    el("div", { class: "panel table-wrap" }, list),
    secretBox,
    form);
  reload();
  return {
    dispose() { alive = false; },
    onStream() {},
  };
}

// ---------- boot ----------

async function boot() {
  if (new URLSearchParams(location.search).get("mock") === "1") {
    const { createMockAPI } = await import("./mock.js");
    try {
      api = await createMockAPI();
    } catch (err) {
      app.replaceChildren(notice(`Mock fixtures failed to load: ${err.message}`, true));
      return;
    }
    document.getElementById("mock-badge").hidden = false;
    if (!api.signedIn()) {
      toLogin();
      return;
    }
  }
  window.addEventListener("hashchange", () => {
    if (parseRoute().view === "list") lastListHash = location.hash || "#/sessions";
    render();
  });
  if (parseRoute().view === "list") lastListHash = location.hash || "#/sessions";
  api.subscribe(
    (type, data) => current?.onStream(type, data),
    (on) => {
      liveDot.classList.toggle("on", on);
      liveDot.title = on ? "Live updates connected" : "Live updates disconnected";
    },
  );
  setInterval(refreshTimes, 30000);
  try {
    await refreshAccount();
  } catch {
    // A 401 already left for the sign-in page; other errors show in the views.
  }
  if (!leaving) render();
}

boot();
