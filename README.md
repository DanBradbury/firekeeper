# Firekeeper

**Tend every coding agent from one bonfire.**

Firekeeper is a terminal dashboard for developers working across multiple AI
coding harnesses. It brings sessions, process state, model usage, and quotas
into one local view without changing how those tools are launched.

Current support includes Codex, Kimi Code, GitHub Copilot CLI, Claude Code, and
OpenCode process discovery, with detailed local session state and metadata for
Codex, Kimi, Copilot, and Claude Code.
More harnesses and providers are planned.

## Highlights

- Discover local coding-agent processes automatically.
- Group parent and child processes into one runtime.
- See whether Codex, Kimi, and Copilot sessions are active, waiting, or need input.
- Inspect session metadata, working directory, model, Git branch, and tokens.
- View Codex limits, recent token usage, active models, and local Kimi token usage.
- Jump to a selected agent terminal or Herdr pane on macOS.
- Show a static Codex character portrait in Settings.
- Keep existing CLI workflows: running `firekeeper` needs no daemon, remote argument, or wrapper, and uploads nothing. Reporting is a separate, opt-in mode (see [Local-only and reporting modes](#local-only-and-reporting-modes)).
- Navigate everything from a pixel-art, keyboard-driven TUI.

## Install

Firekeeper currently builds from source and requires Go 1.26 or newer. From a
repository checkout:

```sh
go build -o firekeeper .
```

Or install directly with Go:

```sh
go install github.com/DanBradbury/firekeeper@latest
```

Move the resulting binary somewhere on your `PATH`, then launch it:

```sh
firekeeper
```

For local development:

```sh
go run .
```

### Herdr plugin

Firekeeper can also run as a dashboard tab inside Herdr 0.8.0 or newer. Plugin
installation builds Firekeeper locally and therefore has the same Go 1.26 or
newer requirement:

```sh
herdr plugin install DanBradbury/firekeeper
herdr plugin action invoke firekeeper.dashboard.open-dashboard
```

For plugin development from a repository checkout, build the plugin binary and
link the checkout instead:

```sh
go build -o firekeeper .
herdr plugin link "$PWD"
herdr plugin pane open --plugin firekeeper.dashboard --entrypoint dashboard --placement tab --focus
```

If [just](https://github.com/casey/just) is installed, use `just` shortcuts:

```sh
just run       # launch Firekeeper
just test      # run all tests
just check     # test, vet, format, and diff checks
just --list    # show all recipes
```

## Using Firekeeper

Firekeeper opens on an animated camp scene. Press Tab to move between four
views:

- **Animation** — home scene with provider characters, live-session party sidebar, and RPG-style command menu.
- **Processes** — running agent harnesses and their session details.
- **Usage** — Codex quotas plus Codex, Kimi, and Copilot CLI token history and model usage.
- **Settings** — configure Codex, Copilot, and Kimi character sprites.

The Animation scene uses a pixel-art beach battle backdrop. Each character’s
provider label identifies its harness. Each crest shows total discovered
runtimes in gold and currently active runtimes in red. An MMORPG-style party
sidebar stacks every discovered local session with its provider's configured
headshot, working-directory name, and latest observable state. Runtimes without
session metadata still appear with an unknown directory and state, except for
Codex process groups, which are hidden until they can be matched to a session.
Active provider runtimes periodically use their attack animation, with a random
2–5 second idle pause between attacks. Characters only appear while their
provider has a discovered local runtime. Newly discovered providers play a
one-shot reveal effect before their character, crest, and label appear.

### Global controls

| Key | Action |
| --- | --- |
| Tab / Shift+Tab | Switch views |
| `q` / Ctrl+C | Quit |

### Home and menu

| Key | Action |
| --- | --- |
| `M` | Open or close menu |
| Arrow keys / `HJKL` | Navigate open menus |
| Enter on **STATUS** | Show active Codex sessions |
| Esc / Backspace | Return or close menu |
| Up / Down or `J` / `K` | Select and scroll party sessions when menu is closed |
| Enter | Switch to the selected party session when the menu is closed |
| Space | Pause or resume animation |
| `[` / `]` | Resize character sprite |

### Processes

| Key | Action |
| --- | --- |
| Up / Down or `J` / `K` | Select runtime |
| Enter | Expand session metadata and process tree |
| `s` | Switch to terminal containing selected runtime |
| Page Up / Page Down | Move through runtime groups |
| Home / End | Jump to first or last runtime |
| `r` | Refresh immediately |

Terminal switching supports Ghostty (1.3+), Terminal.app, and iTerm2 on macOS.
Ghostty sessions are matched by working directory; Terminal.app and iTerm2 are
matched by TTY. macOS may request Automation permission on first use. Sessions
without a controlling TTY cannot be selected this way.
Firekeeper uses exact TTY matching when the installed Ghostty scripting API
provides it. Ghostty 1.3 does not expose TTYs, so after its first best-effort
working-directory match Firekeeper remembers the pane's stable ID for repeated
switches. The initial match remains ambiguous when multiple Ghostty panes share
the same working directory.

When the selected Codex or Copilot runtime is inside
[Herdr](https://herdr.dev/), Firekeeper also focuses its Herdr pane before
bringing the containing terminal forward. Detection is read-only and happens
only when switching. Firekeeper checks all running Herdr sessions and matches by
native agent session ID when available, then by process ID, with a unique
working-directory match as a fallback. The optional Herdr integrations
(`herdr integration install codex` and `herdr integration install copilot`)
improve matching but are not required. Named Herdr sessions and attached
Ghostty or Terminal.app clients are supported; if Herdr is unavailable or no
unambiguous pane is found, Firekeeper falls back to normal terminal switching.

### Usage

| Key | Action |
| --- | --- |
| Left / Right or `H` / `L` | Switch between Codex, Copilot, and Kimi |
| `r` | Refresh selected provider |

Press `Enter` on any supported provider to browse historical sessions. Use
Up/Down to select a session; press `Enter` or `Esc` to return. Codex history is
read from local `state_5.sqlite`; Copilot history is read from local
`session-store.db`. Each browser is limited to the 500 most recent sessions.
Usage overviews assign distinct, consistent colors to as many as 10 displayed
models per provider, including model totals and stacked daily bars.

### Settings

**Player Selection** configures Codex, Copilot, and Kimi sprites: Wizard,
Warrior, or Mage. **Background Selection** switches the Animation scene between
Beach and None, then selects Day or Night. Choices save to Firekeeper’s user
config and load automatically on next launch. Add future background variants as
`assets/bg_<scene>_<time>.png`, such as `bg_forest_day.png` and
`bg_forest_night.png`.

| Key | Action |
| --- | --- |
| Up / Down or `J` / `K` | Select a setting |
| Enter | Edit selected setting |
| Left / Right or `H` / `L` | Change value while editing |
| Enter / Esc | Save or cancel editing |

Codex usage requires an authenticated Codex CLI installation. Firekeeper makes
a short-lived local stdio request to Codex when this view opens; it does not
require a persistent app server or changes to existing Codex commands. Codex
reads local rollout token deltas to render stable model colors in daily usage
bars. Very large rollout files are read from their newest 8 MiB, so oldest
tokens within that tail window can be undercounted.
Kimi usage reads local `usage.record` events from Kimi Code sessions under
`KIMI_CODE_HOME` (default `~/.kimi-code`). It shows session, model, input,
cache, output, and total token history. Kimi quota/reset data is exposed by
Kimi Code’s `/usage` flow; Firekeeper does not extract credentials or call
undocumented quota endpoints.
Copilot usage reads local history from read-only `session-store.db` and, when
an authenticated Copilot/GitHub CLI token is available, fetches plan AI-credit
allowance, remaining credits, and reset timing from GitHub. Token lookup honors
`COPILOT_GITHUB_TOKEN`, `GH_TOKEN`, and `GITHUB_TOKEN`, then `gh auth token`.
Local daily token bars are split and color-coded by model.
Plan data is best effort because its Copilot endpoint is internal and may
change.

## Rendering

Firekeeper preserves source pixel dimensions and uses nearest-neighbor scaling
for crisp terminal artwork.

```sh
firekeeper --renderer auto
firekeeper --renderer kitty --sprite-cols 32 --sprite-rows 16
firekeeper --renderer blocks --sprite-cols 32 --sprite-rows 16
```

`auto` selects Kitty graphics in compatible terminals and falls back to
portable true-color half blocks elsewhere. Kitty, Ghostty, WezTerm, Konsole,
Warp, iTerm2, and current Windows Terminal versions provide best results. Try
running outside tmux first when testing Kitty rendering.

## Local-only and reporting modes

Firekeeper has two modes. They never mix by accident.

**Local-only mode** is plain `firekeeper`. It reads process lists and local
provider files on your machine to draw the dashboard. It starts no daemon,
proxy, or wrapper, changes no agent launch command, and makes no upload.
Codex usage may start a short-lived local `codex app-server`, as described
under Usage.

**Reporting mode** is a set of explicit subcommands: `snapshot`, `export`,
`report`, `backfill`, `daemon`, and `serve`. `report`, `backfill`, and `daemon`
send session metadata and full transcripts to a dashboard server that you run
(see [docs/self-hosting.md](docs/self-hosting.md), or [docs/hosting.md](docs/hosting.md) for HTTPS on a VPS). Nothing is sent until you
allow providers with `--provider`, `backfill --all`, or the config file, and point at a server.
`report --dry-run` shows what would be sent without opening a connection.
Agent CLIs never depend on any of these commands.

### What is uploaded

For each allowed provider, per session: metadata (provider, session id,
working directory, project name, branch, model, state, title, timestamps,
event count, token totals) and every transcript event: prompts, assistant
replies, tool calls and their output, plus each event's original record
(`raw`). Tool output over 64 KiB is cut in `text` but kept in `raw`. Machine
details (id, name, hostname, OS, Firekeeper version) go with each request.
Sessions in an excluded directory or a repository containing
`.firekeeper-ignore` are never read.

### What redaction does and does not catch

**Redaction is best effort, not a guarantee.** Every event passes through it
before leaving your machine. It replaces common, well-formed secret shapes
with `[REDACTED:kind]` markers: AWS access key IDs, GitHub tokens, `sk-` style
API keys, bearer tokens, PEM private keys, JWTs, and `KEY=value` assignments
whose key names mention SECRET, TOKEN, PASSWORD, or KEY. It rewrites your home
directory to `~` and replaces configured private paths.

It does **not** reliably catch passwords written in prose, secrets in unusual
formats, values split across lines or events, encoded or encrypted data,
source code, customer data, or anything else that is sensitive without looking
like a credential. Treat uploaded transcripts as sensitive, and only upload to
a server you control and trust.

## Subcommands

Running `firekeeper` with no subcommand starts the dashboard as usual. Opt-in
reporting subcommands are being added; `firekeeper --help` lists them.

```sh
firekeeper snapshot --json   # print currently discovered sessions as JSON
firekeeper report --dry-run  # show what one upload pass would send
firekeeper report --provider codex --provider copilot
firekeeper backfill --all --dry-run   # plan importing all supported local history
firekeeper backfill --all             # confirm and import past sessions
firekeeper serve             # run the dashboard at http://127.0.0.1:7777/
firekeeper daemon --provider codex   # report every 15 seconds until stopped
firekeeper daemon install   # detect providers and run the daemon at login
firekeeper login --server https://dash.example   # link this machine without copying a token
firekeeper whoami            # server, account and machine this machine reports as
firekeeper logout            # revoke and remove this machine's token
```

`report` runs one pass: it discovers running sessions, reads transcript
events added since the last pass, redacts them, and uploads them to a
dashboard server in batches of at most 500 events. It prints one line per
session with event, batch, duplicate, and redaction counts, never transcript
text.

| Flag | Meaning |
| --- | --- |
| `--server URL` | Dashboard server. Default `http://127.0.0.1:7777`. |
| `--provider NAME` | Upload this provider's sessions. Repeatable. Codex, Copilot, and Claude Code (`claude`) have transcript readers today. |
| `--dry-run` | Read and redact, print counts, upload nothing. |
| `--since DURATION` | Skip transcript files not modified within the duration, for example `24h`. |
| `--token TOKEN` | Ingest token for the server. Default `$FIREKEEPER_TOKEN`, then the config file. |

Every flag here can also come from the config file (see
[Configuration](#configuration)).

Upload is opt-in. With no `--provider`, `report` uploads nothing and behaves
like `--dry-run`. A dry run never opens a network connection and never moves
read offsets.

Redaction is best effort. It catches common secret shapes (cloud and GitHub
tokens, API keys, bearer tokens, private keys, JWTs, `SECRET=...`-style
assignments) and rewrites your home directory to `~`, but it cannot recognize
every secret. Treat uploaded transcripts as sensitive.

Sessions are never read when their working directory is unknown, when the
directory or any parent up to its Git root contains a `.firekeeper-ignore`
file, or when the directory, its Git root, or one of the repository's remotes
matches `exclude` in the config file. The decision is made before the
transcript is located, so an excluded transcript is never opened, and each
skipped session is printed with the reason, in dry runs too.

Read offsets live in `~/.firekeeper/state.json` and advance only after the
server accepts a batch, so an interrupted pass can be rerun without losing
or duplicating events. A transcript that shrinks is re-read from the start;
the server drops events it already has. `report`, `daemon`, and `backfill`
share these offsets: each takes a lock on `~/.firekeeper/state.lock` while it
loads or saves them, so they can run at the same time without one moving
another's offsets backwards. A pass that cannot get the lock within 30
seconds fails with a message saying so.

### Backfill

`backfill` imports the sessions already on this machine, including ones that
have ended, in one pass, then exits. Run it once when you start using the
dashboard; `daemon` or `report` keeps new activity flowing afterwards.

```sh
firekeeper backfill --all --dry-run              # detect providers and print the plan
firekeeper backfill --all                        # print the plan, ask, upload all detected providers
firekeeper backfill --all --yes                  # upload without the confirmation prompt
firekeeper backfill --provider codex             # import only Codex
```

It first prints a plan per provider: sessions, files, bytes, an estimated
event and request count, and the range of last-activity dates, plus how many
sessions were excluded or are already fully uploaded. The plan holds counts
and dates only, never transcript text. Uploading needs `--all`, `--provider`,
or a configured provider allowlist, and a
`y` at the confirmation prompt; without a terminal to ask on, `backfill`
refuses unless `--yes` is given. With no provider opt-in, or with `--dry-run`,
it prints the plan and stops without opening a network connection.

`--all` detects Codex, Copilot, and Claude Code from their local data directories
or executables on `PATH`, honoring provider home overrides. It allows those
providers for this pass, replacing any configured allowlist without changing
the config file. Use either `--all` or `--provider`, not both. After `login`,
the saved server and token are used automatically. Kimi and OpenCode have no
transcript upload adapters yet; Claude Code subagent files are not imported.

Sessions upload newest first, so the dashboard is useful early, with one
progress line per session on stderr. Uploads go through the same redaction,
batching, and offsets as `report`, so Ctrl-C or a failure can be rerun to
resume, running it twice uploads nothing the second time, and a later
`report` or `daemon` pass sends only new events. A failed request is retried
with backoff (1, 2, 4, then 8 seconds); after 5 failed requests in a row the
pass stops. Ended sessions show as `ENDED`; a session that is still running
keeps the state discovery reports for it.

| Flag | Meaning |
| --- | --- |
| `--server URL` | Dashboard server. Default `http://127.0.0.1:7777`. |
| `--provider NAME` | Import this provider's sessions. Repeatable. Codex, Copilot, and Claude Code (`claude`) can be backfilled. |
| `--all` | Import all supported providers found locally, for this pass only. |
| `--since DURATION` | Only import transcripts modified within the duration, for example `720h`. |
| `--after YYYY-MM-DD` | Only import transcripts modified after this local date. Use `--since` or `--after`, not both. |
| `--limit N` | Import at most N sessions, newest first. Run again to continue. |
| `--dry-run` | Print the plan, upload nothing. |
| `--yes` | Upload without asking. Required when input is not a terminal. |
| `--token TOKEN` | Ingest token for the server. Default `$FIREKEEPER_TOKEN`, then the config file. |

Sessions are excluded exactly as by `report`, and the plan lists each one
with the reason. For Codex and Copilot sessions whose working directory is in
the provider's local database, the check happens before the transcript is
opened; otherwise the start of the transcript (at most 64 KiB) is read to
learn the directory, and nothing more of an excluded transcript is read.
Claude Code also reads at most 64 KiB to learn the directory; unknown directories
are excluded. Historical Claude metadata uses the first available branch and
model in that head, with file modification time as last activity.

`serve` runs the dashboard: the v1 API under `/v1/` and the web UI at `/`,
on one port. It prints the URL on startup and runs until interrupted. On
Ctrl-C or `SIGTERM` it stops accepting connections, gives in-flight requests
five seconds, and closes the database.

```sh
firekeeper serve                         # http://127.0.0.1:7777/
firekeeper report --provider codex       # in another terminal
```

| Flag | Meaning |
| --- | --- |
| `--listen ADDR` | Address to listen on. Default `127.0.0.1:7777`. |
| `--db PATH` | Dashboard database. Default `~/.firekeeper/dashboard.db`; a missing directory is created with mode `0700`. |
| `--file-link TEMPLATE` | Link changed files to their repository host, for example `https://github.com/me/{project}/blob/{ref}/{path}`. Placeholders: `{project}`, `{ref}` (the commit, else the branch), `{commit}`, `{branch}`, and `{path}`. Must be an `http` or `https` URL containing `{path}`. Default `repo_url_template` from the config file. |
| `--insecure` | Allow a `--listen` address other than loopback with no token or account. The API is then open. |
| `--trusted-proxy IP\|CIDR` | A reverse proxy whose `X-Forwarded-For` header is believed. Repeatable. Default none (the header is ignored and the connection's address identifies the client); default `trusted_proxies` in the config file. Set it when `serve` runs behind Caddy or nginx, or every visitor shares one failed-attempt limit. `0.0.0.0/0` and `::/0` are refused. See [docs/hosting.md](docs/hosting.md#client-addresses-behind-a-proxy). |
| `--signup MODE` | Who may create accounts: `closed` (default), `invite`, or `open`. See [Accounts](#accounts). |
| `--max-bytes SIZE` | Most transcript data one account may store, such as `500MB` or `2GiB`. Default `1GiB`; `0` for no limit. See [Test-bed safeguards](#test-bed-safeguards). |
| `--max-sessions N` | Most sessions one account may store. Default `5000`; `0` for no limit. |
| `--max-ingest-per-minute N` | Most ingest requests one account may make per minute. Default `120`; `0` for no limit. |
| `--banner TEXT` | Show this text at the top of every page, such as `Test bed: data may be wiped`. Plain text, at most 300 bytes. |

The web UI has two views, picked in the top bar. **Sessions** lists sessions
and opens transcripts. **Usage** charts daily token use by model for the last
7, 30, or 90 days or a custom range, with totals by project and a daily
table. Days are UTC, and only events with a timestamp are counted. Cost
appears only when a per-model price table is configured; Firekeeper never
ships prices; set them under `[prices]` in the config file.

A session's page shows the commit it started on and a **Files changed**
panel: the files its tool calls edited, wrote, or patched (Claude Code
`Edit`, `MultiEdit`, `Write`, and `NotebookEdit`; Codex `apply_patch`,
including patches run through a shell tool; Copilot `edit`, `create`,
`write`, `str_replace_editor`, and `apply_patch`). Paths are relative to the
session's working directory, or absolute when outside it. They are read from
transcript events after redaction, so a home directory shows as `~` and a
secret-shaped file name as a `[REDACTED:...]` marker. Files changed by shell
commands such as `sed -i` or `>` are not detected, and the list says what
the agent asked to change, not what the repository ended up with. Events
ingested before this feature are not scanned.

The reporter reads the commit, and the branch when the provider gives none,
from the repository's `HEAD` reflog as it was at the session's start time. It
runs no Git command and sends neither when the reflog has no entry before
the session started, which is common for sessions older than the reflog's
retention.

### Tokens

```sh
firekeeper serve token create --name laptop --scope ingest --machine MACHINE_ID
firekeeper serve token create --name me --scope read
firekeeper serve token list
firekeeper serve token revoke NAME_OR_ID
```

`create` prints the token once; only its SHA-256 hash is stored. Ingest tokens
(for `report`, bound to one machine id, see `~/.firekeeper/machine-id`) can
only call `/v1/ingest` and `/v1/heartbeat`; read tokens can only read. On a
server with tokens but no accounts, the web UI's `/login` page asks for a read
token and keeps it in `sessionStorage`; once an account exists the browser
signs in with email and password instead. Once any active token exists, every `/v1/*` request
needs an `Authorization: Bearer TOKEN` header (or a browser session; see
[Accounts](#accounts)). Failed attempts are rate limited per IP. `token create`
takes `--account EMAIL` to assign the token to an account.

With no tokens and no accounts the API is open, which is only acceptable on
loopback. `serve` refuses a non-loopback `--listen` address unless a token or
an account exists or `--insecure` is passed, and then warns that anyone who can
reach the port can read every stored transcript and upload new ones.

### Linking a machine

On a server with accounts, a machine connects without anyone copying a token:

```sh
firekeeper login --server https://dash.example
```

It prints a URL and a short code and waits. Open the URL (sign in if asked),
check that the page shows the same code, name the machine, and approve; the
command then finishes and stores an ingest token, bound to this machine and
your account, in `~/.firekeeper/config.toml` with mode `0600`, along with the
server URL. Codes expire after 10 minutes and work once. `--name` suggests the
machine name on the approval page (default: the host name).

```sh
firekeeper whoami   # server, account email and machine name; never the token
firekeeper logout   # revokes the token on the server if reachable, then removes it from the file
```

After that, `firekeeper report --provider codex` and
`firekeeper daemon install` use the stored server and token with no extra
flags. `login` never enables a provider: with none in the allowlist,
`report` and `daemon` upload nothing, whichever server is configured. `login`
refuses a plain `http://` server that is not on this machine, so a token never
crosses a network unencrypted; put HTTPS in front of a hosted server.

The **Tokens** page of the web UI (`/#/tokens`) lists the account's tokens
with machine name, scope, created and last-used times, and revokes them. It
can also create a token for CI or a headless machine, shown once; an ingest
token needs the machine id from that machine's `~/.firekeeper/machine-id`. A
revoked token is refused on its next request. Linking and token management
need a signed-in browser session, so a leaked token cannot add machines or
mint tokens.

### Accounts

One server can hold several people's data. Every machine, session, token and
transcript belongs to an account, and an account can never list, read, stream
or upload into another's.

- **Single user, the default.** A server with no tokens and no accounts needs
  no signup or login; everything belongs to a built-in default account.
  Existing databases are upgraded to this automatically.
- **Accounts.** Creating the first account turns login on for everyone, even
  while the server is running. People sign in at `/login` with email and
  password (stored as argon2id hashes), which sets a 14-day browser session
  cookie. Browser sessions can read but not upload; uploads use ingest tokens,
  which a signed-in person creates by [linking a machine](#linking-a-machine).

`serve` prints which mode it started in: `single-user` (open),
`single-user, token-protected` (no accounts, API needs a token), or
`multi-user`.

In multi-user mode every dashboard page load without a valid session
redirects to `/login`, and a session that ends while the page is open (it
expired, was signed out elsewhere, or the account was disabled) sends the
reader back there with a notice. After signing in, the reader returns to the
view they were on. The header shows the signed-in email and a **Sign out**
button. Static assets and the `/login` and `/signup` pages are the only
routes that need no credentials. `/signup` follows `--signup`: closed shows a
"signup is closed" notice, invite asks for a code. To check these screens
without accounts, open `/?mock=1`: it uses synthetic fixtures and simulates
sign-in (password `firekeeper-mock`), and never contacts the API.

The landing page is a **Machines** overview with one card per machine in the
account: name, hostname, OS, version, online or offline, session counts by
state, and last activity. A machine is offline when its last heartbeat is
more than 90 seconds old; cards update from the live stream and the clock, so
no reload is needed. A new account with no machines shows how to link one,
and a machine that has linked but uploaded nothing shows as online with zero
sessions. **Sessions** lists every machine's sessions, newest first, with the
machine name on each row; machine, provider, project, and state filters narrow
it, and search spans all machines. The **Usage** page also filters token totals,
daily charts, and the project table by project within the selected date range.
On either page, enter an exact project name or choose a suggestion from the
loaded results; clear the Project field to show all projects. Project filters
stay in the page URL for sharing and reloading.
In mock mode, `?mock=1&machines=none` and `?mock=1&machines=linked`
show those two empty states.

```sh
firekeeper serve admin create-account --email you@example.com   # asks for the password twice
printf '%s' "$PASSWORD" | firekeeper serve account create --email you@example.com --password-stdin  # for scripts
firekeeper serve account list
firekeeper serve account disable you@example.com   # or: enable
firekeeper serve invite create [--ttl 168h] [--account EMAIL]
firekeeper serve token create --name laptop --scope ingest --machine MACHINE_ID --account you@example.com
firekeeper serve --signup invite                    # closed (default) | invite | open
```

`--signup closed` refuses every signup; the owner creates accounts with
`admin create-account`, which works whatever `--signup` says and reads the
password only from a terminal prompt (never a flag or argument), or with
`account create --password-stdin` in scripts. `invite` lets a person sign up with a one-time code from
`invite create`; `open` lets anyone who can reach the server. Without
`--account`, `token create` and `invite create` act for the single-user
default account, whose existing tokens keep working. Data uploaded before
accounts existed stays with the default account and is not visible to new
accounts. Put HTTPS in front of any server reachable beyond loopback; the
session cookie is only marked `Secure` when it sees HTTPS (or
`X-Forwarded-Proto: https`).

### Test-bed safeguards

These make it safe to let other people upload their transcripts to a server
you run. They matter once you open `--signup` beyond yourself.

```sh
firekeeper serve admin create-invite [--ttl 168h] [--account EMAIL]  # prints a one-time code
firekeeper serve admin list-accounts                                  # accounts, sessions, events, stored size
firekeeper serve admin disable-account --email them@example.com       # blocks sign-in and tokens, keeps data
firekeeper serve admin reset-password --email them@example.com        # asks for the new password twice
firekeeper serve admin delete-account --email them@example.com --yes  # without --yes, only shows what it would remove
```

The admin commands work on the database directly and need no network or
credentials, only access to the database file. There is no email, so a
forgotten password is an operator action: `reset-password` also signs the
person out everywhere but leaves their tokens working. `delete-account`
removes the account, its tokens, and all its sessions and events (including
search entries), and cannot be undone. Use `serve account enable EMAIL` to
re-enable a disabled account.

- **Limits.** Each account may store at most `--max-bytes` of event text and
  raw payloads, at most `--max-sessions` sessions, and make at most
  `--max-ingest-per-minute` ingest requests a minute. An over-limit upload
  gets a JSON `413` (`storage_limit`, `session_limit`) or `429`
  (`rate_limited`, with `Retry-After`) and **stores nothing from that
  request**, so a reporter's retry cannot lose or duplicate events. A `429`
  clears by itself; a `413` keeps failing until the account deletes data or
  the operator raises the limit. Re-sending data already stored always works. The single-user default account is never limited.
  The account page shows usage against these limits.
- **Account page.** Signed-in people open `#/account` (their email in the
  header). It shows what they store, **Download my data** (a JSON Lines
  export of everything stored for the account: `GET /v1/account/export`), and
  **Delete my account** (type your email to confirm: `DELETE /v1/account`).
  Both need a browser session; tokens cannot delete an account.
- **Privacy notice.** `/privacy` is a plain page, readable without
  JavaScript and without signing in, linked from signup and every page
  footer. It says that transcripts are uploaded and stored on the server,
  that redaction is best effort and secrets may get through, that the
  operator can read stored data, that the server is a test bed whose data may
  be wiped, and how to delete everything. Edit
  `internal/server/web/static/privacy.html` if your deployment differs.
- **Banner.** `--banner TEXT` puts a notice above every page, including the
  sign-in and privacy pages.

### Daemon

`daemon` runs the `report` pass in a loop, in the foreground, until it gets
Ctrl-C or `SIGTERM`. It is optional: nothing else in Firekeeper, and no
agent CLI, needs it running. Each cycle runs one pass and then sends a
heartbeat, so the dashboard shows the machine online even when there is
nothing new.

```sh
firekeeper daemon --provider codex --provider copilot
```

| Flag | Meaning |
| --- | --- |
| `--server URL` | Dashboard server. Default `http://127.0.0.1:7777`. |
| `--provider NAME` | Upload this provider's sessions. Repeatable. Required: the daemon refuses to start without one. |
| `--interval DURATION` | Time between passes. Default `15s`, minimum `5s`. |
| `--quiet` | Log only to `~/.firekeeper/daemon.log`, not to stderr. |

- The provider allowlist works exactly as it does for `report`. Heartbeats
  list only sessions the pass was allowed to read, never sessions from
  other providers or ignored directories.
- When a pass or heartbeat fails, including when any one session fails to
  upload, the daemon retries after 2 seconds, doubling up to 5 minutes,
  with each wait shortened by up to a quarter at random. The first
  successful cycle returns to the normal interval.
- The first Ctrl-C or `SIGTERM` lets the batch in flight finish, saves
  its offset, and exits without starting another. A second one exits at
  once. Offsets are saved after every batch, so even `kill -9` loses
  nothing; the next run re-sends at most the one unconfirmed batch, and
  the server drops the duplicates.
- Only one daemon runs per home directory. It holds an exclusive lock on
  `~/.firekeeper/daemon.lock`; a second one exits with an error. The lock
  is released when the process exits, however it exits.
- It logs to `~/.firekeeper/daemon.log`, and to stderr. The log carries
  counts, session ids, and errors, never transcript text. At 10 MiB it is
  rotated to `daemon.log.1`, and up to three old files (`.1` to `.3`) are
  kept.
- `--quiet` logs only to `daemon.log`, not to stderr. The installed
  service uses it.
- `daemon` itself does not detach. To run it in the background at login,
  install it as a service (below), or run it under `tmux` or `nohup`.

Idle cost was measured on Linux (12 cores) with the daemon at the default
interval, a local `serve`, and no new transcript events, by reading
`/proc/<pid>/stat` CPU ticks for the daemon and its reaped children over
120 seconds. The daemon itself used 0.2% of one core and about 18 MiB
resident. The `ps`, `lsof`, and `git` commands that session discovery runs
each pass used another 1.3%, so the cost scales with how many agent
processes are running and with `--interval`. Between passes the daemon is
blocked on a timer and does no work.

`export` is a placeholder that prints `not implemented` and exits with
status 2.

### Running the daemon at login

```sh
firekeeper daemon install
firekeeper daemon status
firekeeper daemon logs -f
firekeeper daemon uninstall
```

With no provider flags or configured allowlist, `daemon install` detects
Codex, Copilot, and Claude Code from their CLI executables on `PATH` or existing
local data directories, honoring `CODEX_HOME`, `COPILOT_HOME`, and
`CLAUDE_CONFIG_DIR`. Installing opts into transcript uploads for those detected
providers; it prints and saves the allowlist in the service definition. Use
`--dry-run` to preview it, or `--provider` to select providers explicitly.
Environment and config-file allowlists take precedence over detection, including
an empty config-file allowlist (which prevents installation). If no supported
provider is detected, installation stops with guidance. Rerun install after
adding a provider. Kimi and OpenCode are not detected for uploading because they
have no transcript readers yet.

`daemon install` takes the same `--server`, `--provider`, and `--interval`
flags as `daemon`, writes the ones you pass into a per-user service
definition, and starts the service. Settings left to the config file are read
each time the daemon starts, so after editing the file, restart the service
(`install` again) to apply it. The service runs the binary you ran `install` with
(symlinks resolved) as `firekeeper daemon --quiet ...`, so after moving or
reinstalling Firekeeper, or to change flags, run `install` again; it
replaces the definition and restarts the service. `install` copies `PATH`,
`CODEX_HOME`, `COPILOT_HOME`, `KIMI_CODE_HOME`, `CLAUDE_CONFIG_DIR`, `XDG_CONFIG_HOME`, and `FIREKEEPER_CONFIG` from
your shell into the definition when they are set. It refuses to install a
temporary `go run` build. Nothing here needs or uses `sudo`.

- macOS: writes `~/Library/LaunchAgents/dev.firekeeper.daemon.plist` and
  loads it with `launchctl bootstrap gui/$UID`. launchd restarts the daemon
  if it exits with an error, at most every 30 seconds, but not after a
  clean stop. Errors from before the daemon opens its log, such as another
  daemon holding the lock, go to `~/.firekeeper/daemon.stderr.log`.
- Linux (best effort, not yet tested on real hardware): writes
  `~/.config/systemd/user/firekeeper.service` (or under
  `$XDG_CONFIG_HOME`) and runs `systemctl --user daemon-reload`, `enable`,
  and `restart`. Startup errors go to `journalctl --user -u
  firekeeper.service`. User services stop when you log out unless lingering
  is enabled for your user (`loginctl enable-linger`), which Firekeeper
  does not do for you.

| Command | Meaning |
| --- | --- |
| `daemon install [flags] [--dry-run]` | Write the service definition and start it. `--dry-run` prints the file and the commands without writing or running anything. |
| `daemon uninstall [--purge]` | Stop the service and remove its definition. `~/.firekeeper` is left alone unless `--purge` is passed, which deletes the read offsets (`state.json`), the machine id, and the daemon's logs and lock file. A dashboard database from `serve` is never deleted. |
| `daemon status` | Show whether the service is installed and running, its pid, and its last exit status. Exits 0 when running and 3 otherwise. |
| `daemon logs [-n LINES] [-f]` | Print the last lines of `~/.firekeeper/daemon.log` (default 50); `-f` keeps printing new lines, across rotations, until interrupted. |

`daemon` has no `--token` flag, and `install` never writes a token into a
service file. Put the ingest token in the config file (`token = "..."`); the
daemon reads it from there, or from `$FIREKEEPER_TOKEN` when run by hand.

### Configuration

`report`, `backfill`, `daemon`, and `serve` read `~/.firekeeper/config.toml`,
or the file `$FIREKEEPER_CONFIG` names. Precedence is flags, then environment
(`FIREKEEPER_SERVER`, `FIREKEEPER_TOKEN`, `FIREKEEPER_PROVIDERS` as a comma
list, `FIREKEEPER_INTERVAL`), then the file, then defaults. The file is
optional; unknown keys are an error, so a typo cannot silently drop an
exclusion. It holds a token, so keep it private (`chmod 600`).

```toml
server = "https://dash.example:7777"
token = "fk_..."                 # ingest token
providers = ["codex", "claude"]  # upload allowlist; empty uploads nothing
interval = "30s"                 # daemon only
exclude = [
  "~/clients",                   # this directory and everything below it
  "~/work/*-secret",             # globs: * stays within one path segment
  "github.com/acme",             # every repository with an acme remote
  "gitlab.example.com/team/*-private",
]
repo_url_template = "https://github.com/me/{project}/blob/{ref}/{path}"  # serve: file links
trusted_proxies = ["172.29.77.0/24"]  # serve: proxies whose X-Forwarded-For is believed

[redact]
paths = ["~/clients"]            # also scrubbed to [REDACTED:path] in uploads

[prices."gpt-5"]                 # serve: cost per million tokens on Usage
input = 1.25
output = 10
cache = 0.125
```

`exclude` entries that start with `/` or `~` are directory globs, matched
against a session's working directory and its Git root and their parents.
Anything else is a Git remote pattern, matched against every `url` in the
repository's `.git/config` after both are reduced to `host/owner/repo`, so
`git@github.com:acme/api.git` and `https://github.com/acme/api` both read as
`github.com/acme/api`. `.firekeeper-ignore` still works as before.

`firekeeper config show` prints the merged configuration with the token
masked and notes where each layered setting came from. `login` and `logout`
edit only the top-level `server` and `token` keys, keep the rest of the file
as it is, and leave it mode `0600`.

## How session discovery works

Firekeeper scans local processes every two seconds and collapses related
processes into runtime groups. The scan requests full command lines on macOS
and Linux so long installation paths do not hide harness names. For Codex, it
locates open rollout files and
queries `~/.codex/state_5.sqlite` read-only for available session metadata. For
Kimi Code, it maps runtime PIDs to their working directory and reads session
`state.json` under `KIMI_CODE_HOME` without modifying it. For
Copilot CLI, it maps runtime PIDs to `~/.copilot/session-state` and reads
`workspace.yaml`, `events.jsonl`, and `session-store.db` without modifying them.
For Claude Code, it uses an explicit `--session-id` or `--resume` id when the
command line has one, and otherwise maps the runtime's working directory to
its project folder under `~/.claude/projects` and picks the most recently
written transcript there; several Claude Code runtimes in one directory may be
matched to the wrong transcript. Transcripts are read, never modified.
`CODEX_HOME`, `KIMI_CODE_HOME`, `COPILOT_HOME`, and `CLAUDE_CONFIG_DIR` overrides are honored. Provider events supply
best-effort `ACTIVE`, `WAITING`, and `NEEDS INPUT` states. OpenCode currently
exposes process information without provider-specific session metadata.
Codex Desktop helper processes that cannot be matched to rollout metadata are
not shown as sessions.

Discovery is also callable without the TUI through
`internal/session.Discover(ctx, opts)`. It returns normalized session metadata,
including the project basename (Git root or working directory) and
a stable UUID stored in `~/.firekeeper/machine-id`, created on first use.
Provider enrichment failures return usable runtime metadata with a warning.

All monitoring stays local. Firekeeper does not proxy prompts or replace agent
clients.

## Status

Firekeeper is an early-stage macOS-focused project. Process discovery works
without integrations, but provider internals can change between CLI releases.
Expect adapters and metadata handling to evolve.

## Artwork

Pixel artwork comes from Calciumtrice under CC BY 3.0. See
[ASSETS.md](ASSETS.md) for attribution and source links. Artwork files live in
`assets/`.
