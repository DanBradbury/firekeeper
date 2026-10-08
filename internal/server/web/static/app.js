// Firekeeper dashboard: a session list and a transcript viewer, routed by
// URL fragment. All transcript text is inserted with textContent, never as
// HTML.

import { realAPI } from "./api.js";

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
let lastListHash = "#/"; // the list, with the filters the reader came from

// ---------- DOM helpers ----------

function el(tag, attrs, ...children) {
  const n = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v === undefined || v === null || v === false) continue;
    if (k === "class") n.className = v;
    else if (k === "text") n.textContent = v;
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

function render() {
  current?.dispose();
  current = null;
  app.replaceChildren();
  const r = parseRoute();
  current = r.view === "session" ? sessionView(r.uid) : listView(r.params);
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
    state: params.get("state") || "",
    q: params.get("q") || "",
  };

  const machineSel = el("select", { name: "machine" }, el("option", { value: "", text: "All machines" }));
  const providerSel = el("select", { name: "provider" }, el("option", { value: "", text: "All providers" }),
    ...PROVIDERS.map((p) => el("option", { value: p, text: p })));
  const stateSel = el("select", { name: "state" }, el("option", { value: "", text: "All states" }),
    ...STATES.map((s) => el("option", { value: s, text: s.replace("_", " ") })));
  const search = el("input", { type: "search", name: "q", placeholder: "Title or transcript text", value: filters.q });
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
    const h = p.size ? `#/?${p}` : "#/";
    lastListHash = h;
    if (location.hash !== h) history.replaceState(null, "", h);
  }

  function onFilter() {
    filters.machine = machineSel.value;
    filters.provider = providerSel.value;
    filters.state = stateSel.value;
    filters.q = search.value.trim();
    syncHash();
    load(true);
  }
  machineSel.addEventListener("change", onFilter);
  providerSel.addEventListener("change", onFilter);
  stateSel.addEventListener("change", onFilter);
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
      fact("model", s.model),
      fact("tokens", el("span", { title: tokensTitle(s.tokens), text: tokensText(s.tokens) })),
      fact("events", (s.event_count || 0).toLocaleString()),
      fact("started", s.started_at ? timeEl(s.started_at) : null),
      fact("last activity", timeEl(s.last_activity_at)),
      fact("cwd", s.cwd),
      fact("session", s.session_id)));
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
  const ui = { expanded: false, open: new Map(), raw: new Set() };
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
      head.replaceChildren(sessionHeader(s));
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
  }
  window.addEventListener("hashchange", () => {
    if (parseRoute().view === "list") lastListHash = location.hash || "#/";
    render();
  });
  if (parseRoute().view === "list") lastListHash = location.hash || "#/";
  api.subscribe(
    (type, data) => current?.onStream(type, data),
    (on) => {
      liveDot.classList.toggle("on", on);
      liveDot.title = on ? "Live updates connected" : "Live updates disconnected";
    },
  );
  setInterval(refreshTimes, 30000);
  render();
}

boot();
