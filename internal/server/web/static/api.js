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

export const auth = {
  get: () => sessionStorage.getItem(TOKEN_KEY) || "",
  set: (t) => sessionStorage.setItem(TOKEN_KEY, t),
  clear: () => sessionStorage.removeItem(TOKEN_KEY),
  // onUnauthorized is called when the server answers 401.
  onUnauthorized: () => {},
};

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

const sessionPath = (uid) => `v1/sessions/${encodeURIComponent(uid)}`;

export const realAPI = {
  mock: false,

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
