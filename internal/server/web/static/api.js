// Client for the v1 dashboard API. Paths are relative so the UI also works
// when the server is mounted under a prefix.

export class APIError extends Error {
  constructor(status, code, message) {
    super(message);
    this.status = status;
    this.code = code;
  }
}

async function getJSON(path, params) {
  const qs = new URLSearchParams();
  for (const [k, v] of Object.entries(params || {})) {
    if (v !== undefined && v !== null && v !== "") qs.set(k, String(v));
  }
  const url = qs.size ? `${path}?${qs}` : path;
  let res;
  try {
    res = await fetch(url, { headers: { Accept: "application/json" } });
  } catch {
    throw new APIError(0, "network", "Could not reach the Firekeeper server.");
  }
  let body = null;
  try {
    body = await res.json();
  } catch {
    // Fall through with an empty body.
  }
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
    if (typeof EventSource === "undefined") return () => {};
    const es = new EventSource("v1/stream");
    es.onopen = () => onStatus(true);
    es.onerror = () => onStatus(false);
    for (const type of ["session.updated", "event.appended", "machine.status"]) {
      es.addEventListener(type, (e) => {
        let data = null;
        try {
          data = JSON.parse(e.data);
        } catch {
          return;
        }
        onEvent(type, data);
      });
    }
    return () => es.close();
  },
};
