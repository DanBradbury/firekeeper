// Approve a machine that ran `firekeeper login`. The command prints a short
// code and opens this page with the code and a suggested machine name in the
// URL fragment (fragments are never sent to the server). The signed-in
// person checks the code matches the terminal, names the machine, and
// approves; the machine then receives its own token.

import { realAPI, APIError } from "./api.js";
import { mockAuth } from "./mock_auth.js";

const mock = new URLSearchParams(location.search).get("mock") === "1";
const app = document.getElementById("app");
const hash = new URLSearchParams(location.hash.replace(/^#/, ""));

// The mock approves one code so the screen can be tried without a server.
const MOCK_CODE = "ABCD-EFGH";
const api = mock
  ? {
    async csrf() { mockAuth.check(); },
    async approveLink(code, name) {
      await new Promise((r) => setTimeout(r, 150));
      if (code.replace(/[^A-Za-z0-9]/g, "").toUpperCase() !== MOCK_CODE.replace("-", "")) {
        throw new APIError(404, "link_invalid", "that code is unknown, expired, or already used");
      }
      return { ok: true, machine: { id: "mock-machine", name } };
    },
  }
  : { csrf: () => realAPI.account(), approveLink: (c, n) => realAPI.approveLink(c, n) };

function el(tag, attrs, ...children) {
  const n = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v === undefined || v === null || v === false) continue;
    if (k === "class") n.className = v;
    else if (k === "text") n.textContent = v;
    else n.setAttribute(k, v === true ? "" : String(v));
  }
  for (const c of children) {
    if (c === undefined || c === null || c === false) continue;
    n.append(c instanceof Node ? c : String(c));
  }
  return n;
}

const notice = (message, isError) => el("div", { class: isError ? "notice error" : "notice", role: isError ? "alert" : "status", text: message });

function linkError(err) {
  switch (err.code) {
    case "link_invalid": return "That code is unknown, expired, or already used. Run firekeeper login again for a fresh one.";
    case "rate_limited": return "Too many wrong codes. Wait a minute and try again.";
    case "session_required": return "Linking needs a signed-in account. Sign in, then open the link again.";
    case "unauthorized": return "Your session ended. Sign in again, then open the link again.";
    default: return err.message || "Something went wrong.";
  }
}

function doneView(machine) {
  return el("div", { class: "login" },
    el("h1", null, "Machine linked"),
    notice(`“${machine.name}” is linked. Return to its terminal: firekeeper login finishes on its own.`),
    el("p", null, "Nothing uploads until you choose providers on that machine (firekeeper report --provider NAME)."),
    el("a", { href: "./#/tokens" }, "Manage tokens"));
}

function formView() {
  const code = el("input", { type: "text", id: "code", name: "code", required: true, maxlength: "9", autocomplete: "off", spellcheck: "false", autocapitalize: "characters", value: hash.get("code") || "" });
  const name = el("input", { type: "text", id: "name", name: "name", required: true, maxlength: "64", autocomplete: "off", value: hash.get("name") || "" });
  const errBox = el("div");
  const submit = el("button", { type: "submit" }, "Approve this machine");
  const form = el("form", { class: "login", method: "post" },
    el("h1", null, "Link a machine"),
    errBox,
    mock && el("p", null, `Mock mode: the code ${MOCK_CODE} approves; anything else fails.`),
    el("p", null, "Check that the code below matches the one in the terminal where you ran firekeeper login. Only approve a machine you control."),
    el("label", { for: "code" }, "Code"),
    code,
    el("label", { for: "name" }, "Machine name"),
    name,
    submit);
  form.addEventListener("submit", async (e) => {
    e.preventDefault();
    submit.disabled = true;
    errBox.replaceChildren();
    try {
      const r = await api.approveLink(code.value.trim(), name.value.trim());
      app.replaceChildren(doneView(r.machine));
    } catch (err) {
      errBox.append(notice(linkError(err), true));
      submit.disabled = false;
    }
  });
  return form;
}

async function boot() {
  if (mock) document.getElementById("mock-badge").hidden = false;
  try {
    // Picks up the session's CSRF token; a 401 means no session.
    await api.csrf();
  } catch (err) {
    if (err.status === 401) {
      const qs = new URLSearchParams({ next: "link" });
      if (mock) qs.set("mock", "1");
      location.replace(`login?${qs}${location.hash}`);
      return;
    }
    app.replaceChildren(notice(linkError(err), true));
    return;
  }
  app.replaceChildren(formView());
  (hash.get("code") ? document.getElementById("name") : document.getElementById("code"))?.focus();
}

boot();
