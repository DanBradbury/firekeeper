// Simulated sign-in for ?mock=1, so the login and signup screens can be
// checked without a server. It accepts any email with the password below,
// signs up any email with a 10+ character password, and keeps the
// "session" in this tab's sessionStorage. Nothing is sent anywhere.

import { APIError } from "./api.js";

export const MOCK_PASSWORD = "firekeeper-mock";
const KEY = "firekeeper.mock.account";
const delay = (v) => new Promise((resolve) => setTimeout(() => resolve(v), 150));

function read() {
  try {
    return sessionStorage.getItem(KEY) || "";
  } catch {
    return "";
  }
}

function write(email) {
  try {
    if (email) sessionStorage.setItem(KEY, email);
    else sessionStorage.removeItem(KEY);
  } catch {
    // Private windows may refuse storage; the mock then stays signed out.
  }
}

const unauthorized = () => new APIError(401, "unauthorized", "session expired or invalid");

export const mockAuth = {
  signedIn: () => read() !== "",

  async login(email, password) {
    if (password !== MOCK_PASSWORD) {
      await delay();
      throw new APIError(401, "invalid_credentials", "incorrect email or password");
    }
    write(email.trim().toLowerCase());
    return delay({ id: "mock-account", email: read() });
  },

  async signup(email, password) {
    if (password.length < 10) {
      await delay();
      throw new APIError(400, "invalid_request", "password must be 10 to 256 characters");
    }
    write(email.trim().toLowerCase());
    return delay({ id: "mock-account", email: read() });
  },

  async account() {
    if (!read()) throw unauthorized();
    return delay({
      id: "mock-account", email: read(), single_user: false,
      usage: { machines: 1, sessions: 12, events: 3400, tokens: 1, stored_bytes: 8400000 },
      limits: { max_bytes: 1073741824, max_sessions: 5000, ingest_per_minute: 120 },
    });
  },

  async deleteAccount(confirm) {
    if (confirm !== read()) {
      await delay();
      throw new APIError(400, "invalid_request", "confirm must be the account's email address");
    }
    write("");
    return delay(undefined);
  },

  async logout() {
    write("");
    return delay(undefined);
  },

  // check rejects like the real API once the mock session is gone.
  check() {
    if (!read()) throw unauthorized();
  },
};
