// Viewer for a shared Firekeeper session page. The page carries its data in
// three non-executable script blocks: fk-doc (the session), fk-events (one
// event per line), and fk-end (present only when the page was finished).
// All transcript text is inserted with textContent, never as HTML.
(() => {
  "use strict";

  const PAGE = 200;
  const PREVIEW_CHARS = 160;
  const ROLES = ["user", "assistant", "tool_call", "tool_result", "system", "meta"];
  const ROLE_LABEL = { tool_call: "tool call", tool_result: "tool result" };
  // Tool events and uninterpreted records start collapsed.
  const COLLAPSED = new Set(["tool_call", "tool_result", "meta"]);
  // Uninterpreted records are bookkeeping, so they start filtered out.
  const DEFAULT_OFF = new Set(["meta"]);

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

  // ---------- formatting ----------

  function compact(n) {
    n = n || 0;
    if (n < 1000) return String(n);
    if (n < 1e6) return (n / 1e3).toFixed(n < 1e4 ? 1 : 0) + "k";
    return (n / 1e6).toFixed(n < 1e7 ? 1 : 0) + "M";
  }

  function tokensText(t) {
    t = t || {};
    return `in ${compact(t.input)} · out ${compact(t.output)} · cache ${compact(t.cache)}`;
  }

  function tokensTitle(t) {
    t = t || {};
    const f = (n) => (n || 0).toLocaleString();
    return `input ${f(t.input)}, output ${f(t.output)}, cache ${f(t.cache)}`;
  }

  function when(ts) {
    if (!ts) return null;
    const d = new Date(ts);
    return isNaN(d) ? null : el("time", { datetime: ts, title: d.toISOString(), text: d.toLocaleString() });
  }

  function previewOf(ev) {
    let s = ev.text || "";
    if (!s && ev.raw && typeof ev.raw === "object" && typeof ev.raw.type === "string") s = ev.raw.type;
    s = s.replace(/\s+/g, " ").trim();
    return s.length > PREVIEW_CHARS ? s.slice(0, PREVIEW_CHARS) + "…" : s;
  }

  // ---------- text rendering ----------

  // renderText shows prose with fenced code blocks and inline code spans.
  function renderText(text) {
    const frag = document.createDocumentFragment();
    if (!text) {
      frag.append(el("div", { class: "empty", text: "(no text)" }));
      return frag;
    }
    let prose = [];
    let code = null;
    let lang = "";
    const flushProse = () => {
      const s = prose.join("\n").replace(/^\n+|\n+$/g, "");
      prose = [];
      if (s) frag.append(inlineProse(s));
    };
    for (const line of text.split("\n")) {
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
    for (const p of s.split(/(`[^`\n]+`)/)) {
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

  // highlight wraps each case-insensitive occurrence of q under root in
  // <mark>. q is already lower case.
  function highlight(root, q) {
    if (!q) return;
    const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT);
    const nodes = [];
    for (let n = walker.nextNode(); n; n = walker.nextNode()) {
      if (n.nodeValue.toLowerCase().includes(q)) nodes.push(n);
    }
    for (const n of nodes) {
      const s = n.nodeValue;
      const lower = s.toLowerCase();
      const frag = document.createDocumentFragment();
      let at = 0;
      for (let i = lower.indexOf(q); i >= 0; i = lower.indexOf(q, at)) {
        if (i > at) frag.append(s.slice(at, i));
        frag.append(el("mark", { text: s.slice(i, i + q.length) }));
        at = i + q.length;
      }
      if (at < s.length) frag.append(s.slice(at));
      n.replaceWith(frag);
    }
  }

  // ---------- page ----------

  function readBlock(id) {
    const n = document.getElementById(id);
    return n ? n.textContent : null;
  }

  function boot() {
    const app = document.getElementById("app");
    let doc;
    try {
      doc = JSON.parse(readBlock("fk-doc"));
    } catch {
      app.replaceChildren(el("p", { class: "note warn", text: "This page's session data is damaged and cannot be shown." }));
      return;
    }
    const s = doc.session || {};
    const events = [];
    let damaged = 0;
    for (const line of (readBlock("fk-events") || "").split("\n")) {
      if (!line.trim()) continue;
      try {
        events.push(JSON.parse(line));
      } catch {
        damaged++;
      }
    }
    const complete = document.getElementById("fk-end") !== null && damaged === 0;

    // Search text per event, built on first use.
    const hay = new WeakMap();
    const rawHay = new WeakMap();
    const haystack = (ev) => {
      let h = hay.get(ev);
      if (h === undefined) {
        h = [ev.text, ev.tool_name, ev.model].filter(Boolean).join("\n").toLowerCase();
        hay.set(ev, h);
      }
      return h;
    };
    const rawHaystack = (ev) => {
      let h = rawHay.get(ev);
      if (h === undefined) {
        h = ev.raw == null ? "" : JSON.stringify(ev.raw).toLowerCase();
        rawHay.set(ev, h);
      }
      return h;
    };

    const roleCounts = new Map();
    const toolCounts = new Map();
    for (const ev of events) {
      const role = ROLES.includes(ev.role) ? ev.role : "meta";
      ev.role = role;
      roleCounts.set(role, (roleCounts.get(role) || 0) + 1);
      if (ev.tool_name) toolCounts.set(ev.tool_name, (toolCounts.get(ev.tool_name) || 0) + 1);
    }

    const state = {
      q: "",
      roles: new Set(ROLES.filter((r) => !DEFAULT_OFF.has(r))),
      tool: "",
      searchRaw: false,
      expanded: false,
    };
    // Per-seq view state, so an event looks the same after re-filtering.
    const ui = { open: new Map(), raw: new Set() };
    let matches = [];
    let rendered = 0;

    // ----- header -----

    const fact = (k, v) => (v || v === 0 ? el("div", {}, el("dt", { text: k }), el("dd", {}, v)) : null);
    const header = el("section", { class: "panel" },
      el("h1", { text: s.title || s.session_id || "Session" }),
      el("dl", { class: "facts" },
        fact("state", s.state ? el("span", { class: "badge " + s.state, text: s.state }) : null),
        fact("provider", s.provider),
        fact("project", s.project),
        fact("commit", s.commit ? el("code", { title: s.commit, text: s.commit.slice(0, 12) }) : null),
        fact("model", s.model),
        fact("tokens", el("span", { title: tokensTitle(s.tokens), text: tokensText(s.tokens) })),
        fact("events", events.length.toLocaleString()),
        fact("started", when(s.started_at)),
        fact("last activity", when(s.last_activity_at))),
      el("dl", { class: "facts long" },
        fact("branch", s.branch),
        fact("cwd", s.cwd),
        fact("machine", s.machine_id),
        fact("session", s.session_id)),
      el("p", { class: "note" },
        `Exported from Firekeeper${doc.version ? " " + doc.version : ""}`,
        doc.generated_at ? " on " : "", when(doc.generated_at), ". ",
        "Common secret shapes were redacted before upload and again on export. Redaction is best effort, not a guarantee.",
        doc.raw ? "" : " Original provider records (raw) were left out."),
      complete ? null : el("p", { class: "note warn",
        text: `This page is incomplete: the export stopped early${damaged ? ` and ${damaged} damaged ${damaged === 1 ? "event was" : "events were"} skipped` : ""}. ${events.length.toLocaleString()} events are shown.` }));

    // ----- files -----

    const seqLink = (seq) => el("a", { href: `#e${seq}`, text: `#${seq}` });
    let filesPanel = null;
    const files = doc.files || [];
    if (files.length || s.files_source) {
      const fromGit = s.files_source === "git";
      filesPanel = el("details", { class: "panel files", open: files.length <= 30 },
        el("summary", {},
          el("span", { text: "Files changed" }),
          el("span", { class: "count", text: files.length.toLocaleString() }),
          fromGit ? null : el("span", { class: "hint", text: "best effort" })),
        el("p", { class: "note", text: fromGit
          ? (s.commit ? `From Git: changes since commit ${s.commit.slice(0, 12)}.` : "From Git.")
          : "Best effort: only files the agent's edit tools changed. Files changed by shell commands or scripts are missing." }));
      if (files.length) {
        const list = el("ul");
        for (const f of files) {
          const name = f.url
            ? el("a", { href: f.url, target: "_blank", rel: "noopener noreferrer", text: f.path })
            : el("span", { text: f.path });
          const meta = f.changes > 0
            ? el("span", { class: "meta", title: `Changed by ${f.changes} tool ${f.changes === 1 ? "call" : "calls"}` },
              `${f.changes}× · `, seqLink(f.first_seq), f.first_seq === f.last_seq ? null : "–", f.first_seq === f.last_seq ? null : seqLink(f.last_seq))
            : el("span", { class: "meta", title: "Git reports this file changed, but no edit tool call named it" }, "git");
          list.append(el("li", {},
            el("span", { class: "path" + (f.absolute ? " outside" : ""), title: f.absolute ? "Outside the session's working directory" : null }, name),
            meta));
        }
        filesPanel.append(list);
      }
    }

    // ----- filters -----

    const search = el("input", { type: "search", placeholder: "Filter by text, tool, or model  ( / )", "aria-label": "Filter events" });
    let searchTimer = 0;
    search.addEventListener("input", () => {
      clearTimeout(searchTimer);
      searchTimer = setTimeout(() => {
        state.q = search.value.trim().toLowerCase();
        apply();
      }, 150);
    });

    let toolSelect = null;
    if (toolCounts.size) {
      toolSelect = el("select", { "aria-label": "Tool" }, el("option", { value: "", text: "All tools" }));
      for (const [name, n] of [...toolCounts].sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]))) {
        toolSelect.append(el("option", { value: name, text: `${name} (${n.toLocaleString()})` }));
      }
      toolSelect.addEventListener("change", () => {
        state.tool = toolSelect.value;
        apply();
      });
    }

    const expandBtn = el("button", { type: "button", text: "Expand tool events" });
    expandBtn.addEventListener("click", () => {
      state.expanded = !state.expanded;
      ui.open.clear();
      expandBtn.textContent = state.expanded ? "Collapse tool events" : "Expand tool events";
      for (const d of list.querySelectorAll(".event > details")) d.open = state.expanded;
    });

    const chips = el("div", { class: "row" });
    const roleBoxes = new Map();
    for (const role of ROLES) {
      if (!roleCounts.get(role)) continue;
      const box = el("input", { type: "checkbox" });
      box.checked = state.roles.has(role);
      box.addEventListener("change", () => {
        if (box.checked) state.roles.add(role);
        else state.roles.delete(role);
        apply();
      });
      roleBoxes.set(role, box);
      chips.append(el("label", { class: "chip " + role, title: role === "meta" ? "Records the parser could not interpret" : null },
        box, ROLE_LABEL[role] || role, el("span", { class: "n", text: roleCounts.get(role).toLocaleString() })));
    }
    if (doc.raw) {
      const box = el("input", { type: "checkbox" });
      box.addEventListener("change", () => {
        state.searchRaw = box.checked;
        apply();
      });
      chips.append(el("label", { class: "chip", title: "Also match the filter against each event's original record" }, box, "search raw"));
    }
    const count = el("span", { class: "count", "aria-live": "polite" });
    chips.append(count);

    const filters = el("section", { class: "filters", "aria-label": "Filters" },
      el("div", { class: "row" }, search, toolSelect, expandBtn),
      chips);

    // ----- events -----

    const list = el("div", { class: "events" });
    const moreBtn = el("button", { type: "button", text: "Show more" });
    moreBtn.addEventListener("click", () => renderMore());
    const more = el("div", { class: "more" }, moreBtn);
    const sentinel = el("div", { class: "sentinel", "aria-hidden": "true" });

    app.replaceChildren(header, filesPanel || "", filters, list, sentinel, more,
      el("footer", { text: "Shared from Firekeeper. This page works offline and loads nothing from the network." }));

    function rawToggle(ev, container) {
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
        const d = container.closest("details");
        if (on && d && !d.open) d.open = true;
      });
      return { btn, set };
    }

    function headBits(ev) {
      const bits = [el("span", { class: "role", text: ROLE_LABEL[ev.role] || ev.role })];
      if (ev.tool_name) {
        const name = el("span", { class: "tool-name", text: ev.tool_name });
        highlight(name, state.q);
        bits.push(name);
      }
      return bits;
    }

    function headTail(ev) {
      const tok = ev.tokens || {};
      const bits = [];
      if (ev.model) bits.push(el("span", { text: ev.model }));
      if (tok.input || tok.output || tok.cache) bits.push(el("span", { title: tokensTitle(tok), text: tokensText(tok) }));
      if (ev.ts) {
        const d = new Date(ev.ts);
        if (!isNaN(d)) bits.push(el("span", { title: d.toISOString(), text: d.toLocaleTimeString() }));
      }
      bits.push(el("a", { class: "seq", href: `#e${ev.seq}`, title: "Link to this event", text: `#${ev.seq}` }));
      return bits;
    }

    function eventEl(ev) {
      const role = ev.role;
      const article = el("article", { class: `event ${role}`, id: `e${ev.seq}` });
      const body = el("div", { class: "body" });
      const raw = ev.raw == null ? null : rawToggle(ev, body);
      const q = state.q;

      if (COLLAPSED.has(role)) {
        const details = el("details");
        const preview = el("span", { class: "preview", text: previewOf(ev) });
        highlight(preview, q);
        const summary = el("summary", {}, ...headBits(ev), preview, ...headTail(ev), raw && raw.btn);
        let built = false;
        const build = () => {
          if (details.open && !built) {
            built = true;
            const content = role === "meta" ? renderText(ev.text) : toolBody(ev.text);
            body.prepend(content);
            highlight(body, state.q);
          }
        };
        details.addEventListener("toggle", () => {
          build();
          ui.open.set(ev.seq, details.open);
        });
        details.append(summary, body);
        article.append(details);
        // A collapsed event whose text matches the filter opens, so the
        // match is visible.
        const textMatch = q && (ev.text || "").toLowerCase().includes(q);
        if (ui.open.get(ev.seq) ?? (state.expanded || textMatch)) {
          details.open = true;
          build();
        }
        if (raw && ui.raw.has(ev.seq)) raw.set(true);
        return article;
      }

      const head = el("div", { class: "event-head" }, ...headBits(ev), el("span", { class: "spacer" }), ...headTail(ev), raw && raw.btn);
      body.append(renderText(ev.text));
      highlight(body, q);
      article.append(head, body);
      if (raw && ui.raw.has(ev.seq)) raw.set(true);
      return article;
    }

    function keep(ev) {
      if (!state.roles.has(ev.role)) return false;
      if (state.tool && ev.tool_name !== state.tool) return false;
      if (!state.q) return true;
      return haystack(ev).includes(state.q) || (state.searchRaw && rawHaystack(ev).includes(state.q));
    }

    function apply() {
      matches = events.filter(keep);
      rendered = 0;
      list.replaceChildren();
      renderMore();
      const total = events.length.toLocaleString();
      count.textContent = matches.length === events.length ? `${total} events` : `${matches.length.toLocaleString()} of ${total} events`;
      if (!matches.length) list.append(el("p", { class: "empty", text: events.length ? "No events match these filters." : "No events stored for this session." }));
    }

    function renderMore(upTo) {
      const end = Math.min(matches.length, Math.max(rendered + PAGE, upTo === undefined ? 0 : upTo + 1));
      const frag = document.createDocumentFragment();
      for (; rendered < end; rendered++) frag.append(eventEl(matches[rendered]));
      list.append(frag);
      more.hidden = rendered >= matches.length;
    }

    // reveal shows the event with seq, clearing filters that hide it, and
    // scrolls to it.
    function reveal(seq) {
      let i = matches.findIndex((ev) => ev.seq === seq);
      if (i < 0) {
        if (!events.some((ev) => ev.seq === seq)) return;
        state.q = "";
        search.value = "";
        state.tool = "";
        if (toolSelect) toolSelect.value = "";
        const role = events.find((ev) => ev.seq === seq).role;
        state.roles.add(role);
        roleBoxes.get(role).checked = true;
        apply();
        i = matches.findIndex((ev) => ev.seq === seq);
      }
      if (i >= rendered) renderMore(i);
      const node = document.getElementById(`e${seq}`);
      if (!node) return;
      for (const t of list.querySelectorAll(".event.target")) t.classList.remove("target");
      node.classList.add("target");
      node.scrollIntoView({ block: "start" });
    }

    const fromHash = () => {
      const m = /^#e(\d+)$/.exec(location.hash);
      if (m) reveal(Number(m[1]));
    };
    window.addEventListener("hashchange", fromHash);

    if ("IntersectionObserver" in window) {
      new IntersectionObserver((entries) => {
        if (entries.some((e) => e.isIntersecting) && rendered < matches.length) renderMore();
      }, { rootMargin: "0px 0px 1200px 0px" }).observe(sentinel);
    }

    document.addEventListener("keydown", (e) => {
      const typing = e.target instanceof HTMLInputElement || e.target instanceof HTMLSelectElement || e.target instanceof HTMLTextAreaElement;
      if (e.key === "/" && !typing) {
        e.preventDefault();
        search.focus();
      } else if (e.key === "Escape" && e.target === search && search.value) {
        search.value = "";
        state.q = "";
        apply();
      }
    });

    apply();
    fromHash();
  }

  if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", boot);
  else boot();
})();
