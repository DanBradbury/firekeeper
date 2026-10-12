# Firekeeper Privacy Policy

Last updated: October 11, 2026

Firekeeper is software by Dan Bradbury ("**I**", "**me**"). Who handles your
data depends on how you use it:

| How you use Firekeeper | Who receives your data |
| --- | --- |
| The terminal dashboard (`firekeeper`) | Nobody. It stays on your machine, apart from the two provider lookups in section 1.3. |
| A server you run yourself (`firekeeper serve`) | You, as that server's operator. I receive nothing. |
| A server someone else runs | That server's operator, under their own privacy notice (its `/privacy` page). I receive nothing. |
| The Hosted Instance at `firekeeper.danbradbury.net` | Me, as described in section 2. |

## 1. The Firekeeper software

### 1.1 No telemetry

The Software contains no analytics, crash reporting, usage tracking, or
update checks. It never sends anything to me.

### 1.2 What it reads and writes on your machine

- **Reads:** the process list (`ps`), open files (`lsof`), and your coding
  agents' local session files and databases (for example under `~/.codex`,
  `~/.copilot`, `~/.claude`, and `~/.kimi-code`, or the directories named by
  `CODEX_HOME`, `COPILOT_HOME`, `CLAUDE_CONFIG_DIR`, and `KIMI_CODE_HOME`).
  These are read-only.
  Sessions in repositories containing a `.firekeeper-ignore` file, or matching
  `exclude` in the config, are never read.
- **Writes**, under `~/.firekeeper/`: `config.toml` (server address and, after
  `firekeeper login`, an access token, stored with mode `0600`), `machine-id`
  (a random identifier for this machine), `state.json` (how far each
  transcript has been uploaded), and the daemon's lock and log files. The log
  does not contain transcript text.
- On macOS, switching to a session's terminal uses AppleScript to ask
  Ghostty, Terminal.app, or iTerm2 which window holds it.

### 1.3 Network requests the terminal dashboard makes

The Usage tab makes two requests that leave your machine. Neither sends
session data, and neither goes to me:

- **Codex usage** starts your own `codex app-server` for a moment and asks it
  for your rate limits. Codex contacts OpenAI under your account, governed by
  OpenAI's privacy policy.
- **Copilot plan details**: if a GitHub token is available, Firekeeper sends
  it to GitHub's API (or `GITHUB_API_URL`) to read your Copilot plan. This is
  governed by GitHub's privacy statement. The token is not stored or logged.

### 1.4 Uploading transcripts

`report`, `backfill`, and `daemon` upload only for providers you allow, only
to the server you configure. For each session they send:

- **metadata:** provider, session id, working directory, project name, Git
  branch, model, state, title, timestamps, event count, and token totals;
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

## 2. The Hosted Instance

This section applies only to the server I run at
`firekeeper.danbradbury.net`, an invite-only personal test bed. For it, I am
the data controller (the person responsible for your data).

### 2.1 What I collect

| Data | Why | Source |
| --- | --- | --- |
| Email address | Your sign-in name. I may also use it to tell you about changes to these terms or to the Hosted Instance. | You, at sign-up |
| Password | Signing in. Stored only as an argon2id hash; I cannot read it. | You |
| Session cookie and CSRF token | Keeping you signed in for up to 14 days and protecting your requests. | Created at sign-in |
| Access tokens | Letting your machines upload. Stored hashed, with their name, scope, machine, creation, last-use, and revocation times. | You, or machine linking |
| Machine details | Showing your machines and whether they are online: machine id, name, host name, OS, Firekeeper version, last heartbeat. | Your reporter |
| Session metadata and transcripts | The purpose of the service: listing, reading, searching, and charting your sessions (section 1.4 lists what these contain). | Your reporter |
| Model prices you enter | Showing spend in usage charts. | You |
| Invite codes | Controlling sign-up. | Me |

**IP addresses** are used in memory to rate-limit requests and to set the
`Secure` flag correctly. Firekeeper does not write them to its database, and
the web server in front of it (Caddy) keeps no access log in the configuration
in this repository. My hosting provider, [VPS PROVIDER], may log network
traffic under its own policy.

**Cookies.** The Hosted Instance sets one strictly necessary, HttpOnly session
cookie. There are no analytics, advertising, or third-party cookies, and no
third-party scripts.

### 2.2 How I use it

Only to run the Hosted Instance for you: authenticate you, store and show your
data, search it, compute usage, enforce limits, keep the service secure,
debug problems, and make backups. I do **not** sell your data, share it for
advertising, profile you, or use it to train machine-learning models.

For people in the EU and UK, the legal bases are: performing our agreement
(running your account and storing what you upload); my legitimate interest in
keeping the service secure and working (rate limiting, backups, debugging);
and complying with the law where it requires me to keep or disclose data.

### 2.3 Who can see it

- **Me.** As operator I can read every account's stored data, including full
  transcripts, directly from the database and its backups. I do so only as
  section 2.2 and the [terms](EULA.md) allow.
- **Other users** cannot see your data through the dashboard or the API.
- **Service providers:** DigitalOcean hosts the server and its disk in
  us-west. They process data only to provide hosting.
- **Legal requests:** I will disclose data if the law requires it, and will
  tell you first unless I am legally prevented.

### 2.4 How long I keep it

- Your data stays until you delete it, I delete your account, or the test bed
  is wiped.
- Deleting your account removes your account, tokens, machines, sessions,
  events, and search entries from the live database at once.
- Backups run daily and the most recent 14 are kept, so deleted data can
  remain in backups for up to 14 days before it is gone for good. If a
  backup is restored, I will re-delete accounts that were deleted after it was
  taken.
- Expired browser sessions and used invite and link codes are not needed after
  they expire and may be removed at any time.

### 2.5 Security

The Hosted Instance is served only over HTTPS, passwords and tokens are stored
hashed, and each account's data is isolated from the others. It is still a
small test bed run by one person: do not upload anything you could not afford
to have exposed. If I learn of a breach affecting your data, I will tell you
without undue delay and, where required, the relevant authority.

### 2.6 Your choices and rights

- **See and download your data:** the account page's *Download my data*, or
  `GET /v1/account/export`, gives you everything stored for your account.
- **Delete your data:** *Delete my account* on the account page, or
  `DELETE /v1/account`. Revoke individual tokens at any time.
- **Stop uploading:** stop the daemon, remove a provider from your allowlist,
  or run `firekeeper logout`.
- **Correct your data**, or object to or restrict how I use it: email me.

Depending on where you live (for example the EU, UK, or California), you may
have further rights, including to complain to your local data-protection
authority. I will answer requests within 30 days. I do not sell or share
personal information as defined by California law.

### 2.7 International transfer

The Hosted Instance is located in the United States. If you upload from
elsewhere, your data is transferred there.

## 3. Changes

I will change the "Last updated" date when I change this policy, and tell
Hosted Instance users by email or a dashboard notice at least 14 days before
a material change takes effect.

## 4. Contact

dan.luckydaisy@gmail.com
