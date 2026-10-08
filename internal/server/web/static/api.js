// Client for the v1 dashboard API. Paths are relative so the UI also works
// when the server is mounted under a prefix.

export class APIError extends Error {
  constructor(status, code, message) {
    super(message);
    this.status = status;
    this.code = code;
  }
}

const TOKEN_KEY = "firekeeper.token";

const signInWaiters = [];

export const auth = {
  get: () => sessionStorage.getItem(TOKEN_KEY) || "",
  set: (t) => sessionStorage.setItem(TOKEN_KEY, t),
  clear: () => sessionStorage.removeItem(TOKEN_KEY),
  // onUnauthorized is called when the server answers 401.
  onUnauthorized: () => {},
  // waitForSignIn resolves after signedIn(); the live stream parks on it
  // instead of retrying with credentials the server already refused.
  waitForSignIn: () => new Promise((resolve) => signInWaiters.push(resolve)),
  signedIn: () => signInWaiters.splice(0).forEach((resolve) => resolve()),
};

// The browser session's CSRF token. The server returns it from login,
// signup and GET v1/account; it lives in memory only and must accompany
// every write.
let csrfToken = "";

function authHeaders(extra) {
  const h = { ...extra };
  const t = auth.get();
  if (t) h.Authorization = "Bear" + "er " + t;
  return h;
}

async function getJSON(path, params) {
  const qs = new URLSearchParams();
  for (const [k, v] of Object.entries(params || {})) {
    if (v !== undefined && v !== null && v !== "") qs.set(k, String(v));
  }
  const url = qs.size ? `${path}?${qs}` : path;
  let res;
  try {
    res = await fetch(url, { headers: authHeaders({ Accept: "application/json" }) });
  } catch {
    throw new APIError(0, "network", "Could not reach the Firekeeper server.");
  }
  let body = null;
  try {
    body = await res.json();
  } catch {
    // Fall through with an empty body.
  }
  if (res.status === 401) auth.onUnauthorized();
  if (!res.ok) {
    throw new APIError(res.status, body?.code || "http_error", body?.error || `Request failed (${res.status}).`);
  }
  return body;
}

async function sendJSON(method, path, body) {
  const headers = authHeaders({ Accept: "application/json", "Content-Type": "application/json" });
  if (csrfToken) headers["X-CSRF-Token"] = csrfToken;
  let res;
  try {
    res = await fetch(path, { method, headers, body: body === undefined ? undefined : JSON.stringify(body) });
  } catch {
    throw new APIError(0, "network", "Could not reach the Firekeeper server.");
  }
  let data = null;
  try {
    data = await res.json();
  } catch {
    // Fall through with an empty body.
  }
  if (!res.ok) {
    throw new APIError(res.status, data?.code || "http_error", data?.error || `Request failed (${res.status}).`);
  }
  return data;
}

const postJSON = (path, body) => sendJSON("POST", path, body);

const sessionPath = (uid) => `v1/sessions/${encodeURIComponent(uid)}`;

export const realAPI = {
  mock: false,

  // account returns who is signed in: { id, email, single_user }. It also
  // picks up the session's CSRF token after a page reload.
  async account() {
    const a = await getJSON("v1/account");
    csrfToken = a.csrf_token || "";
    return a;
  },

  // exportURL downloads everything stored for the account as JSON Lines.
  // It is a plain link, so it works with the session cookie.
  exportURL: "v1/account/export",

  // deleteAccount removes the account and all its data. confirm must be the
  // account's email. The server ends the browser session.
  async deleteAccount(confirm) {
    await sendJSON("DELETE", "v1/account", { confirm });
    csrfToken = "";
  },

  async login(email, password) {
    const r = await postJSON("v1/auth/login", { email, password });
    csrfToken = r.csrf_token || "";
    return r.account;
  },

  async signup(email, password, inviteCode) {
    const r = await postJSON("v1/auth/signup", { email, password, invite_code: inviteCode || undefined });
    csrfToken = r.csrf_token || "";
    return r.account;
  },

  async logout() {
    try {
      await postJSON("v1/auth/logout");
    } catch (err) {
      // An expired session is already signed out.
      if (err.status !== 401) throw err;
    }
    csrfToken = "";
  },

  // Tokens belong to the signed-in account. The secret comes back from
  // createToken once and is never listed.
  async listTokens() {
    return (await getJSON("v1/tokens")).tokens;
  },

  createToken(name, scope, machineID) {
    return postJSON("v1/tokens", { name, scope, machine_id: machineID || "" });
  },

  revokeToken(id) {
    return sendJSON("DELETE", `v1/tokens/${encodeURIComponent(id)}`);
  },

  // approveLink approves the code a machine printed during `firekeeper login`.
  approveLink(userCode, machineName) {
    return postJSON("v1/link/approve", { user_code: userCode, machine_name: machineName });
  },

  listSessions(filters, cursor, limit = 50) {
    return getJSON("v1/sessions", { ...filters, cursor, limit });
  },

  getSession(uid) {
    return getJSON(sessionPath(uid));
  },

  listEvents(uid, afterSeq, limit = 200) {
    return getJSON(`${sessionPath(uid)}/events`, { after_seq: afterSeq, limit });
  },

  listMachines() {
    return getJSON("v1/machines");
  },

  // usage returns token sums for [from, to] (YYYY-MM-DD, UTC) grouped by
  // the listed keys.
  usage(from, to, groupBy) {
    return getJSON("v1/usage", { from, to, group_by: groupBy.join(",") });
  },

  // subscribe opens /v1/stream and calls onEvent(type, data). onStatus gets
  // true while connected. It returns a function that closes the stream.
  subscribe(onEvent, onStatus) {
    // EventSource cannot send an Authorization header, so read the SSE
    // stream with fetch and reconnect after drops.
    const ctl = new AbortController();
    const dispatch = (block) => {
      let type = "message";
      let data = "";
      for (const line of block.split("\n")) {
        if (line.startsWith("event:")) type = line.slice(6).trim();
        else if (line.startsWith("data:")) data += line.slice(5).trim();
      }
      if (!["session.updated", "event.appended", "machine.status"].includes(type)) return;
      try {
        onEvent(type, JSON.parse(data));
      } catch {
        // Ignore malformed events.
      }
    };
    (async () => {
      while (!ctl.signal.aborted) {
        try {
          const res = await fetch("v1/stream", {
            headers: authHeaders({ Accept: "text/event-stream" }),
            signal: ctl.signal,
          });
          if (res.status === 401) {
            auth.onUnauthorized();
            await auth.waitForSignIn();
            continue;
          } else if (res.ok && res.body) {
            onStatus(true);
            const reader = res.body.pipeThrough(new TextDecoderStream()).getReader();
            let buf = "";
            for (;;) {
              const { value, done } = await reader.read();
              if (done) break;
              buf += value.replace(/\r\n/g, "\n");
              let i;
              while ((i = buf.indexOf("\n\n")) >= 0) {
                dispatch(buf.slice(0, i));
                buf = buf.slice(i + 2);
              }
            }
          }
        } catch {
          if (ctl.signal.aborted) return;
        }
        onStatus(false);
        await new Promise((r) => setTimeout(r, 3000));
      }
    })();
    return () => ctl.abort();
  },
};
