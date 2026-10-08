// Sign-in and sign-up page. It posts JSON to the v1 auth endpoints, which
// set an HttpOnly session cookie, then returns to the dashboard route the
// reader came from (the URL fragment survives the server's redirect).

import { realAPI, auth } from "./api.js";
import { mockAuth, MOCK_PASSWORD } from "./mock_auth.js";

const params = new URLSearchParams(location.search);
const mock = params.get("mock") === "1";
const signupMode = document.body.dataset.signup || "closed";
const accounts = document.body.dataset.accounts !== "false";
const isSignup = /\/signup$/.test(location.pathname);
const app = document.getElementById("app");
const authAPI = mock ? mockAuth : realAPI;

const REASONS = {
  expired: "Your session ended. Sign in again to continue.",
  required: "Sign in to continue.",
  signed_out: "You are signed out.",
};

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

// After signing in, return to the page that sent the reader here. Only the
// link page qualifies; anything else goes to the dashboard.
const afterLogin = params.get("next") === "link" ? "link" : "./";

// href keeps ?mock=1 and the dashboard route across the sign-in pages.
function href(path) {
  return path + (mock ? "?mock=1" : "") + location.hash;
}

// pageHref is href for pages that carry ?next= along, such as signup.
function pageHref(path) {
  const qs = new URLSearchParams();
  if (mock) qs.set("mock", "1");
  if (params.get("next") === "link") qs.set("next", "link");
  return path + (qs.size ? `?${qs}` : "") + location.hash;
}

function authError(err) {
  if (err.code === "rate_limited") return "Too many attempts. Wait a minute and try again.";
  if (err.code === "invalid_credentials") return "Incorrect email or password.";
  return err.message || "Something went wrong.";
}

function closedView() {
  return el("div", { class: "login" },
    el("h1", null, "Create account"),
    notice("Signup is closed on this server. Ask its administrator to create an account for you.", true),
    el("a", { href: href("login") }, "Sign in"));
}

function formView() {
  const email = el("input", { type: "email", id: "email", name: "email", autocomplete: "username", required: true, maxlength: "254" });
  const password = el("input", {
    type: "password", id: "password", name: "password", required: true, maxlength: "256",
    autocomplete: isSignup ? "new-password" : "current-password", minlength: isSignup ? "10" : undefined,
  });
  const invite = el("input", { type: "text", id: "invite", name: "invite", autocomplete: "off", spellcheck: "false" });
  const errBox = el("div");
  const reason = REASONS[params.get("reason")];
  if (reason) errBox.append(notice(reason, params.get("reason") !== "signed_out"));
  const submit = el("button", { type: "submit" }, isSignup ? "Create account" : "Sign in");

  const form = el("form", { class: "login", method: "post", novalidate: false },
    el("h1", null, isSignup ? "Create account" : "Sign in"),
    errBox,
    mock && el("p", null, `Mock mode: any email with the password “${MOCK_PASSWORD}” signs in; anything else fails.`),
    !accounts && !isSignup && el("p", null, "This server has no accounts yet. Use a read token below, or ask the administrator to run `firekeeper serve admin create-account`."),
    el("label", { for: "email" }, "Email"),
    email,
    el("label", { for: "password" }, isSignup ? "Password (10 characters or more)" : "Password"),
    password,
    isSignup && signupMode === "invite" && el("label", { for: "invite" }, "Invite code"),
    isSignup && signupMode === "invite" && invite,
    submit,
    isSignup
      ? el("a", { class: "login-switch", href: pageHref("login") }, "Have an account? Sign in")
      : signupMode !== "closed" && el("a", { class: "login-switch", href: pageHref("signup") }, "New here? Create an account"),
  );
  form.addEventListener("submit", async (e) => {
    e.preventDefault();
    submit.disabled = true;
    errBox.replaceChildren();
    try {
      if (isSignup) await authAPI.signup(email.value, password.value, invite.value.trim());
      else await authAPI.login(email.value, password.value);
    } catch (err) {
      password.value = "";
      errBox.append(notice(authError(err), true));
      submit.disabled = false;
      password.focus();
      return;
    }
    auth.clear(); // a stale read token must not shadow the new session
    location.replace(href(afterLogin));
  });

  // A server with tokens but no accounts can only be read with a token.
  if (!accounts && !isSignup && !mock) {
    const tokenInput = el("input", { type: "password", id: "token", autocomplete: "off", required: true });
    const tokenForm = el("form", null,
      el("p", null, "Enter a read token created with `firekeeper serve token create`. It is kept in this tab's sessionStorage only."),
      el("label", { for: "token" }, "Read token"),
      tokenInput,
      el("button", { type: "submit" }, "Use token"));
    tokenForm.addEventListener("submit", (e) => {
      e.preventDefault();
      auth.set(tokenInput.value.trim());
      location.replace(href("./"));
    });
    form.append(el("details", { open: true }, el("summary", null, "Use a read token"), tokenForm));
  }
  return form;
}

if (mock) document.getElementById("mock-badge").hidden = false;
document.title = `${isSignup ? "Create account" : "Sign in"} · Firekeeper`;
app.replaceChildren(isSignup && signupMode === "closed" ? closedView() : formView());
document.getElementById("email")?.focus();
