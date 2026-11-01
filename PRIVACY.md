# Firekeeper Privacy Policy

Last updated: October 31, 2026

This policy covers the Firekeeper client software in this repository ("the
Software"), by Dan Bradbury ("**I**", "**me**"). Who handles your data depends
on how you use it:

| How you use Firekeeper | Who receives your data |
| --- | --- |
| The terminal dashboard (`firekeeper`) | Session data stays on your machine. Update checks and provider usage lookups contact the services listed in section 1.3. |
| `telemetry`, `otel-listen`, and `otel-reconcile` | Nobody. The listener accepts Claude Code's telemetry on 127.0.0.1 and writes usage counts to a file on this machine. It does not upload them. |
| Uploading to a server (`report`, `backfill`, `daemon`) | The operator of the server you configure, under their own privacy notice. The Software never sends session data anywhere else, and I receive nothing from it. |

A dashboard server is separate software with its own terms and privacy notice.
This policy does not cover it.

One more network use, off by default: if a release source is configured and
`firekeeper serve` cannot find `firekeeper-server`, it asks whether to download
it. This prompt is the only place the Software downloads a binary. It appears
only in an interactive terminal, and nothing is fetched unless you answer
yes. The download is a plain file request to the release host (a GitHub
repository), which sees your IP address like any web server; no session data,
identifiers or other information are sent. Plain `firekeeper` uploads nothing
and starts no daemon.

## 1. The Firekeeper software

### 1.1 No telemetry

The Software contains no analytics, crash reporting, or usage tracking. It
never sends anything to me. Release checks and provider usage lookups are
described in section 1.3.

### 1.2 What it reads and writes on your machine

- **Reads:** the process list (`ps`), open files (`lsof`), and your coding
  agents' local session files and databases (for example under `~/.codex`,
  `~/.copilot`, `~/.claude`, and `~/.kimi-code`, or the directories named by
  `CODEX_HOME`, `COPILOT_HOME`, `CLAUDE_CONFIG_DIR`, and `KIMI_CODE_HOME`).
  These are read-only. For Claude Code, Firekeeper also checks whether a
  session has files under `subagents/`. It uses that only to warn that token
  totals may be low, and it does not read those files' contents for the warning.
  The Claude limits lookup also reads Claude Code's existing OAuth sign-in
  when available, as described in section 1.3.
  Sessions in repositories containing a `.firekeeper-ignore` file, or matching
  `exclude` in the config, are never read.
- **Writes**, under `~/.firekeeper/`: `config.toml` (server address and, after
  `firekeeper login`, an access token, stored with mode `0600`), `machine-id`
  (a random identifier for this machine), `state.json` (how far each
  transcript has been uploaded), and the daemon's lock and log files. The log
  does not contain transcript text.
- On macOS, switching to a session's terminal uses AppleScript to ask
  Ghostty, Terminal.app, or iTerm2 which window holds it.
- **Opt-in `telemetry`**, only after `firekeeper telemetry enable`: the
  daemon listens on 127.0.0.1 and appends Claude Code `api_request` usage
  (time, session id, model, query source, token counts, cost) to
  `~/.firekeeper/telemetry.jsonl`, mode `0600`. It does not record prompt or
  transcript text and it does not upload the file. Enable writes the
  exporter variables into `~/.claude/settings.json` and keeps a backup of
  that file. `disable` removes those variables. `otel-listen` and
  `otel-reconcile` use the same local file.

### 1.3 Network requests the terminal dashboard makes

The terminal dashboard can make the following requests that leave your machine.
None sends session data, and none goes to me:

- **Update check**: when you start `firekeeper` in a terminal, it asks
  GitHub's API for the latest release on each launch and offers to
  update if there is a newer one. The request is an unauthenticated `GET`
  that carries no identifier. Nothing installs unless you answer yes. The
  result is cached in `~/.firekeeper/update.json`. Set
  `FIREKEEPER_NO_UPDATE_CHECK=1` to turn it off. Development builds never
  check.
- **Codex usage** starts your own `codex app-server` for a moment and asks it
  for your rate limits. Codex contacts OpenAI under your account, governed by
  OpenAI's privacy policy.
- **Claude Code on the web**: only when you run `firekeeper backfill
  --claude-cloud`, or turn on `[claude_cloud] enabled = true` for the daemon
  (off by default), Firekeeper reads your Claude Code login (the macOS
  keychain on macOS; `~/.claude/.credentials.json` on Linux and Windows) and
  the organization id in `~/.claude.json`, and requests your cloud sessions
  from `api.anthropic.com`, an undocumented API governed by Anthropic's terms
  and privacy policy. Those files are read, never written, and the token is
  never refreshed. The token stays in memory and is never stored, logged, or
  uploaded. The sessions are cached unredacted in `~/.firekeeper/cloud/claude/`
  (mode `0600`) and then uploaded as in section 1.4.
- **Copilot plan details**: if a GitHub token is available, Firekeeper sends
  it to GitHub's API (or `GITHUB_API_URL`) to read your Copilot plan. This is
  governed by GitHub's privacy statement. The token is not stored or logged.
- **Claude limits**: enabled by default when a Claude Code OAuth sign-in is
  available. Firekeeper reads `CLAUDE_CODE_OAUTH_TOKEN`, the macOS Keychain
  item `Claude Code-credentials`, or Claude Code's `.credentials.json`, and
  uses the token to request five-hour and weekly limits from Anthropic's
  undocumented usage endpoint. The token is sent only to Anthropic for that
  request, not to a Firekeeper server; Firekeeper never stores, logs, or
  refreshes it. On macOS, the first lookup may ask for Keychain access.
  Run `firekeeper claude-limits disable` to turn this lookup off.

### 1.4 Uploading transcripts

`report`, `backfill`, and `daemon` upload only for providers you allow, only
to the server you configure. For each session they send:

- **metadata:** provider, session id, working directory, project name, Git
  branch, model, state, title, timestamps, event count, and token totals.
- **every transcript event:** your prompts, the assistant's replies, tool
  calls and their output (which can include source code, file contents, and
  command output), and each event's original record;
- **machine details:** machine id, host name, operating system, and
  Firekeeper version.

Before anything leaves your machine, Firekeeper replaces common secret formats
(API keys, tokens, private keys, `KEY=value` secrets) with `[REDACTED]`
markers and rewrites your home directory to `~`. **This redaction is best
effort.** It misses secrets in unusual formats, passwords in prose, personal
data, and confidential code. Assume that anything in a session may be
uploaded as written. `--dry-run` shows what would be sent without sending it.

### 1.4 The website contact form

If you fill in the contact form on firekeeper.ai, your name, work email,
company, and the other answers you give are sent to Formspree, a third-party
form service, which emails them to me. I use them only to reply to you. This is
separate from the Firekeeper client, which never sends anything to that form.

### 1.5 Buying the server

If you buy a license for the dashboard server, checkout is hosted by Stripe, a
third-party payment processor. Your card details go to Stripe and never to me.
Stripe gives Lucky Daisy LLC, which sells the server, your name or company, email
address, billing address and tax ID if you gave them, and the order and amount.
These are used to send you your license key, to handle tax and accounting, and
to reply to you, and are kept as long as the law requires for business records.
Stripe's own privacy policy covers what it does with your data. This is separate
from the Firekeeper client, which never contacts Stripe.

## 2. Changes

I will change the "Last updated" date when I change this policy.

## 3. Contact

dan.luckydaisy@gmail.com
