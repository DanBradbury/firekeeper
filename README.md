# Firekeeper

**Tend every coding agent from one bonfire.**

See what your AI coding agents are doing right now, and keep a searchable
record of what they did, how many tokens they used, and which files they
changed. Firekeeper works across Codex, GitHub Copilot CLI, and Claude Code,
on one machine or several, and it runs on your hardware. Nothing starts and
nothing uploads unless you ask.

## Why it exists

I use more than one coding agent, and each keeps its own history in its own
format in its own folder. When I wanted to answer simple questions, I couldn't.
What did last week's sessions use? Which project burns the most tokens? What
was that agent doing when it stopped and waited for me? What did it change?

Firekeeper started as a terminal dashboard for seeing what is running. It
grew into the place to inspect all of it: a live view of every session, a
searchable archive of past ones, and usage across projects and machines. Use
the parts you want.

## What you get

**See what is running.** A keyboard-driven terminal UI finds your agent
processes, groups each one with its child processes, and shows state, model,
working directory, Git branch, and tokens. It jumps you to the terminal that
holds a session on macOS. Plain `firekeeper` changes nothing about how you
launch your agents.

**Read what happened.** Send transcripts to a dashboard you run and get a web
UI with every session, full transcripts, and search across all of them. Each
session shows the files it changed and its Git context. Import the history
already on your machine with `backfill`, or keep it current with the daemon.

**Understand usage.** Token use charted by model and filtered by project. Add
your own per-model prices and the same charts show spend. Firekeeper ships no
price list, so the numbers are yours.

**Bring machines together.** Link a laptop, a desktop, and a dev box to one
account with a browser approval instead of a pasted token. A machines page
shows which are online, and sessions from all of them sit in one list.

**Stay in control.** Upload is opt-in per provider. Every event is redacted
before it leaves the machine, and a repository with a `.firekeeper-ignore`
file is never read. You run the server: on `127.0.0.1`, or on a host you
control. Redaction is best effort and the operator of a server can read what is
uploaded, so see [What is uploaded and what is not caught](#what-is-uploaded-and-what-is-not-caught).

## Use it how you need it

| Mode | What you run | What leaves the machine |
| --- | --- | --- |
| **Terminal dashboard only** | `firekeeper` | Firekeeper uploads no session data and starts no daemon. See [Terminal dashboard](#terminal-dashboard) for the two Usage-tab lookups that do contact other services. |
| **Local dashboard** | `firekeeper serve`, plus `firekeeper report` or `firekeeper daemon` | Transcripts go to a server on `127.0.0.1` and stay in a SQLite file on this machine. |
| **Self-hosted server, several machines** | `firekeeper serve` on a host you control, behind HTTPS; `firekeeper login` on each machine | Redacted transcripts go to that host. Whoever operates it can read them. |

Don't want a daemon? Run `firekeeper backfill --all --dry-run` to preview
importing the sessions already on your machine, then `firekeeper backfill
--all` to import them in one pass and stop there. Everything beyond plain
`firekeeper` is a separate subcommand that you run on purpose, and an
unconfigured `report` or `daemon` sends nothing.

Supported harnesses are Codex, GitHub Copilot CLI, Claude Code, Kimi Code, and
OpenCode. Codex, Copilot CLI, and Claude Code can upload transcripts. Kimi gets
local token history and OpenCode is process discovery only. The
[provider table](#supported-providers) shows what each one gets.

## Install

Firekeeper builds from source and requires Go 1.26 or newer. There is no
Homebrew formula, install script, or release binary yet. From a repository
checkout:

```sh
go build -o firekeeper .
```

Or install with Go:

```sh
go install github.com/DanBradbury/firekeeper@latest
```

Put the binary on your `PATH`, then run `firekeeper`. For development, use
`go run .`. If [just](https://github.com/casey/just) is installed, `just run`,
`just test`, `just check`, and `just --list` wrap the common commands.

`firekeeper --help` lists the subcommands and the TUI flags. There is no
`--version` flag.

### Server in a container

The dashboard server is `firekeeper serve` and needs no extra install on a
host that has the binary. To run it in Docker, the repository ships a
`Dockerfile` and a `docker-compose.yml` that builds the image from a checkout:

```sh
docker compose up -d
```

This serves the UI at `http://127.0.0.1:7777/` with data in a named volume.
The compose file starts the server with `--insecure` and publishes the port on
the host's loopback only, so create an account or token first thing. See
[docs/self-hosting.md](docs/self-hosting.md). For HTTPS on a VPS, see
[docs/hosting.md](docs/hosting.md), which is the owner's runbook for his own
instance. The `Publish and deploy` workflow in `.github/workflows/deploy.yml` builds and
pushes `ghcr.io/danbradbury/firekeeper:<tag>` when a `v*` tag is pushed.

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

## Quick start

### Terminal dashboard

```sh
firekeeper
```

Start an agent in another terminal and it appears within a couple of seconds.
Tab switches views, `M` opens the menu, `q` quits. Controls are listed under
[Terminal dashboard](#terminal-dashboard).

### Send sessions to a dashboard

This path uploads transcripts. Read [What is uploaded and what is not
caught](#what-is-uploaded-and-what-is-not-caught) first. The steps below set
up a dashboard on this machine and then link a second one.

1. **Start the server.** It listens on `127.0.0.1:7777` and, with no account
   or token yet, is open and single-user:

   ```sh
   firekeeper serve
   ```

2. **Create an account** (in another terminal). It prompts twice for a
   password of 10 to 256 characters:

   ```sh
   firekeeper serve admin create-account --email you@example.com
   ```

   The running server notices the account and from then on requires a
   sign-in or token on every request. This step is optional on a single
   machine, but it must come before you upload if you want the data in your
   account: anything uploaded while the server was still single-user belongs
   to a built-in default account that new accounts cannot see. Linking a
   machine in step 3 also needs at least one account. If you started
   `serve` with `--db`, pass the same `--db` here.

3. **Link this machine** so uploads carry an ingest token without you copying
   one. The command prints a URL and a code; open the URL, sign in, check the
   code, and approve:

   ```sh
   firekeeper login --server http://127.0.0.1:7777
   ```

   Skip this step if you skipped step 2. A single-user server accepts
   uploads with no token. `http://` is accepted only for a server on this
   machine; any other server needs `https://`.

4. **Preview an upload.** This reads and redacts transcripts of the sessions
   running right now and prints counts. It opens no network connection and
   uploads nothing:

   ```sh
   firekeeper report --dry-run
   ```

5. **Upload once.** Name the providers you allow; `codex`, `copilot`, and
   `claude` have transcript readers:

   ```sh
   firekeeper report --provider codex --provider claude
   ```

   Open `http://127.0.0.1:7777/` to see the result. `report` covers sessions
   running at that moment. To import sessions that already ended, run
   `firekeeper backfill --all --dry-run`, then `firekeeper backfill --all`.

6. **Keep it flowing.** Install the daemon as a per-user service. It detects
   Codex, Copilot CLI, and Claude Code from their data directories or
   executables, prints what it found, and starts uploading those providers.
   Preview first with `--dry-run`:

   ```sh
   firekeeper daemon install --dry-run
   firekeeper daemon install
   ```

   Installing opts into uploading the detected providers. Pass `--provider`
   to choose them yourself. The service runs the binary you installed with, so
   `go run .` is refused.

7. **Link another machine** to a server that is reachable over HTTPS (see
   [docs/self-hosting.md](docs/self-hosting.md) and
   [docs/hosting.md](docs/hosting.md)). On that machine:

   ```sh
   firekeeper login --server https://dash.example.com
   firekeeper daemon install
   ```

   The server needs an account before it will accept a non-loopback
   `--listen` address, so create one with `serve admin create-account` before
   the first start. `firekeeper whoami` shows which server and account a
   machine reports to, and `firekeeper logout` revokes its token.

Without a linked token you can use `FIREKEEPER_TOKEN` or `--token` with a token
from `firekeeper serve token create`; see [Tokens](#tokens).

## Supported providers

| Provider | Process discovery | Session state and metadata | Usage tab | Transcript upload |
| --- | --- | --- | --- | --- |
| Codex | Yes | Yes, from rollout events and `state_5.sqlite` | Limits and history | Yes |
| GitHub Copilot CLI | Yes | Yes, from `events.jsonl`, `workspace.yaml`, and `session-store.db` | Local history and plan details | Yes |
| Claude Code | Yes | Yes, from `~/.claude/projects` transcripts | No | Yes |
| Kimi Code | Yes | Metadata from session `state.json`; the state is shown as `ACTIVE` whenever a session matches | Token history | No |
| OpenCode | Yes | No | No | No |

States describe the latest event Firekeeper can see, not ground truth.
`kimi` is an accepted provider name in `--provider` and the config file
because the event format allows it, but there is no Kimi transcript reader, so
nothing is uploaded for it. `OpenCode` has no adapter beyond the process list.

## Terminal dashboard

Firekeeper opens on an animated camp scene. Press Tab to move between four
views:

- **Animation** — home scene with provider characters, live-session party sidebar, and RPG-style command menu.
- **Processes** — running agent harnesses and their session details.
- **Usage** — Codex quotas plus Codex, Kimi, and Copilot CLI token history and model usage.
- **Settings** — configure Codex, Copilot, and Kimi character sprites and the background.

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

Two lookups in the Usage tab leave the machine, and neither uploads session
data. Codex usage starts a short-lived `codex app-server` and asks it for your
limits. Copilot usage, when a GitHub token is available, asks GitHub for plan
details (see [Usage](#usage)).

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
Local daily token bars are split and color-coded by model. Plan data is best
effort: it comes from an internal Copilot endpoint that is not a supported
API and may change or disappear. The local history alone is not an
account-wide quota.

### Settings

**Player Selection** configures Codex, Copilot, and Kimi sprites: Wizard,
Warrior, or Mage. **Background Selection** switches the Animation scene between
Beach and None, then selects Day or Night. Choices save to `settings.json` in
a `firekeeper` folder under your user config directory and load automatically
on next launch. Add future background variants as
`assets/bg_<scene>_<time>.png`, such as `bg_forest_day.png` and
`bg_forest_night.png`.

| Key | Action |
| --- | --- |
| Up / Down or `J` / `K` | Select a setting |
| Enter | Edit selected setting |
| Left / Right or `H` / `L` | Change value while editing |
| Enter / Esc | Save or cancel editing |

### Rendering

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

### How session discovery works

Firekeeper scans local processes every two seconds with `ps` and collapses
related processes into runtime groups. The scan requests full command lines on
macOS and Linux so long installation paths do not hide harness names. Sessions
that are not running are not discovered this way; past sessions reach the
dashboard only through [`backfill`](#backfill).

- **Codex:** locates open rollout files with `lsof` and queries
  `~/.codex/state_5.sqlite` read-only for available session metadata.
- **Kimi Code:** maps runtime PIDs to their working directory and reads session
  `state.json` under `KIMI_CODE_HOME` without modifying it.
- **Copilot CLI:** maps runtime PIDs to `~/.copilot/session-state` and reads
  `workspace.yaml`, `events.jsonl`, and `session-store.db` without modifying
  them.
- **Claude Code:** uses an explicit `--session-id` or `--resume` id when the
  command line has one, and otherwise maps the runtime's working directory to
  its project folder under `~/.claude/projects` and picks the most recently
  written transcript there. Several Claude Code runtimes in one directory may
  be matched to the wrong transcript. Transcripts are read, never modified.
- **OpenCode:** process information only, with no provider-specific session
  metadata.

`CODEX_HOME`, `KIMI_CODE_HOME`, `COPILOT_HOME`, and `CLAUDE_CONFIG_DIR`
overrides are honored. Provider events supply best-effort `ACTIVE`, `WAITING`,
and `NEEDS INPUT` states. Codex Desktop helper processes that cannot be matched
to rollout metadata are not shown as sessions.

Discovery is also callable without the TUI through
`internal/session.Discover(ctx, opts)` and, from the shell, through
`firekeeper snapshot --json`. Both return normalized session metadata,
including the project basename (Git root or working directory) and a stable
UUID stored in `~/.firekeeper/machine-id`, created on first use. Provider
enrichment failures return usable runtime metadata with a warning.

All monitoring stays local. Firekeeper does not proxy prompts or replace agent
clients.

## Reporting and the web dashboard

Reporting is a set of explicit subcommands: `report`, `backfill`, `daemon`,
`serve`, `login`, `logout`, `whoami`, `config`, and `snapshot`. `report`,
`backfill`, and `daemon` send session metadata and full transcripts to a
dashboard server that you run; `serve` is that server. Nothing is sent until
you allow providers with `--provider`, `backfill --all`, the
`FIREKEEPER_PROVIDERS` variable, or the config file, and point at a server
(`http://127.0.0.1:7777` by default). `report --dry-run` shows what would be
sent without opening a connection. Agent CLIs never depend on any of these
commands.

```sh
firekeeper snapshot --json            # print currently discovered sessions as JSON
firekeeper report --dry-run           # show what one upload pass would read
firekeeper report --provider codex --provider copilot
firekeeper backfill --all --dry-run   # plan importing all supported local history
firekeeper backfill --all             # confirm, then import past sessions
firekeeper serve                      # run the dashboard at http://127.0.0.1:7777/
firekeeper daemon --provider codex    # report every 15 seconds until stopped
firekeeper daemon install             # detect providers and run the daemon at login
firekeeper login --server https://dash.example   # link this machine without copying a token
firekeeper whoami                     # server, account and machine this machine reports as
firekeeper logout                     # revoke and remove this machine's token
firekeeper config show                # print the merged configuration, token masked
```

`export` is a placeholder that prints `not implemented` and exits with
status 2.

### What is uploaded and what is not caught

For each allowed provider, per session, Firekeeper sends metadata (provider,
session id, working directory, project name, branch, model, state, title,
timestamps, event count, token totals) and every transcript event: prompts,
assistant replies, tool calls and their output, plus each event's original
record (`raw`). Tool output over 64 KiB is cut in `text` but kept in `raw`.
Machine details (machine id, host name, and OS) go with each request.

Sessions are never read when their working directory is unknown, when the
directory or any parent up to its Git root contains a `.firekeeper-ignore`
file, or when the directory, its Git root, or one of the repository's remotes
matches `exclude` in the config file. The decision is made before the
transcript is located, so an excluded transcript is never opened, and each
skipped session is printed with the reason, in dry runs too.

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
a server you control and trust. The server stores the text and the original
records, and whoever operates it, or holds its database file or a backup, can
read all of it.

Other limits to know about:

- `report` and `daemon` find sessions through `ps`, so they see sessions that
  are running at the time of a pass. A session that starts and ends between
  daemon passes may be missed until a `backfill`.
- Only Codex, Copilot CLI, and Claude Code have transcript readers. Claude
  Code subagent files are not imported.
- Provider file formats are read as found and change between CLI releases.
  Parsers keep records they cannot interpret as `meta` events, but the text
  shown for those events may be thin.
- The macOS terminal switch, and the launchd service, are macOS only. Linux is
  best effort: process discovery and the transcript readers use `ps`, `lsof`,
  and plain files, and the systemd user service has not been tested on real
  hardware. Windows has not been tried; the daemon lock and the service
  installer do not support it.

### Report

`report` runs one pass: it discovers running sessions, reads transcript
events added since the last pass, redacts them, and uploads them to a
dashboard server in batches of at most 500 events. It prints one line per
session with event, batch, duplicate, and redaction counts, never transcript
text.

| Flag | Meaning |
| --- | --- |
| `--server URL` | Dashboard server. Default `http://127.0.0.1:7777`, `$FIREKEEPER_SERVER`, or the config file. |
| `--provider NAME` | Upload this provider's sessions. Repeatable. `codex`, `copilot`, and `claude` have transcript readers today. |
| `--dry-run` | Read and redact, print counts, upload nothing. |
| `--since DURATION` | Skip transcript files not modified within the duration, for example `24h`. |
| `--token TOKEN` | Ingest token for the server. Default `$FIREKEEPER_TOKEN`, then the config file. |

Every flag here except `--dry-run` and `--since` can also come from the
environment or the config file (see [Configuration](#configuration)).

Upload is opt-in. With no provider from a flag, the environment, or the config
file, `report` uploads nothing and behaves like `--dry-run`; in that case the
dry run covers every provider that has a transcript reader. A dry run never
opens a network connection and never moves read offsets.

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
or a configured provider allowlist, and a `y` at the confirmation prompt;
without a terminal to ask on, `backfill` refuses unless `--yes` is given. With
no provider opt-in, or with `--dry-run`, it prints the plan and stops without
opening a network connection.

`--all` detects Codex, Copilot, and Claude Code from their local data directories
or executables on `PATH`, honoring provider home overrides. It allows those
providers for this pass, replacing any configured allowlist without changing
the config file. Use either `--all` or `--provider`, not both. After `login`,
the saved server and token are used automatically. Kimi and OpenCode have no
transcript readers; Claude Code subagent files are not imported.

Sessions upload newest first, so the dashboard is useful early, with one
progress line per session on stderr. Uploads go through the same redaction,
batching, and offsets as `report`, so Ctrl-C or a failure can be rerun to
resume, running it twice uploads nothing the second time, and a later
`report` or `daemon` pass sends only new events. A failed request that looks
transient is retried with backoff (1, 2, 4, then 8 seconds); after 5 failed
requests in a row the pass stops. Ended sessions show as `ENDED`; a session
that is still running keeps the state discovery reports for it.

| Flag | Meaning |
| --- | --- |
| `--server URL` | Dashboard server. Default `http://127.0.0.1:7777`, `$FIREKEEPER_SERVER`, or the config file. |
| `--provider NAME` | Import this provider's sessions. Repeatable. `codex`, `copilot`, and `claude` can be backfilled. |
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

### Serve

`serve` runs the dashboard: the v1 API under `/v1/` and the web UI at `/`,
on one port. It prints the URL and the authentication mode on startup and runs
until interrupted. On Ctrl-C or `SIGTERM` it stops accepting connections,
gives in-flight requests five seconds, and closes the database. The API is
described in [docs/api.md](docs/api.md).

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
| `--signup MODE` | Who may create accounts: `closed` (default), `invite`, or `open`. See [Accounts](#accounts). |
| `--max-bytes SIZE` | Most transcript data one account may store, such as `500MB` or `2GiB`. Default `1GiB`; `0` for no limit. See [Test-bed safeguards](#test-bed-safeguards). |
| `--max-sessions N` | Most sessions one account may store. Default `5000`; `0` for no limit. |
| `--max-ingest-per-minute N` | Most ingest requests one account may make per minute. Default `120`; `0` for no limit. |
| `--banner TEXT` | Show this text at the top of every page, such as `Test bed: data may be wiped`. Plain text, at most 300 bytes. |

The server also answers `GET /healthz` with `ok` and no credentials, for
container health checks.

`serve` refuses a non-loopback `--listen` address unless a token or an account
exists or `--insecure` is passed, and then warns that anyone who can reach the
port can read every stored transcript and upload new ones. The server speaks
plain HTTP; put a TLS-terminating proxy in front of any server reachable
beyond loopback (see [docs/self-hosting.md](docs/self-hosting.md)).

#### Web UI

The web UI is a set of pages in the top bar:

- **Machines** is the landing page, described under [Accounts](#accounts).
- **Sessions** lists sessions, newest first, and opens transcripts. Machine,
  provider, project, and state filters narrow the list, and the search box
  runs a full-text search over transcript text across all your machines.
- **Usage** charts daily token use by model for the last 7, 30, or 90 days or
  a custom range, with totals by project and a daily table, and can be
  filtered by project. Days are UTC, and only events with a timestamp are
  counted. Cost appears only when a per-model price table is configured;
  Firekeeper never ships prices; set them under `[prices]` in the config file
  that `serve` reads. Models are matched by exact name, and tokens from models
  without a price count as unpriced.
- **Tokens** manages the account's tokens (see [Tokens](#tokens)).

On the Sessions and Usage pages, enter an exact project name or choose a
suggestion from the loaded results; clear the Project field to show all
projects. Filters stay in the page URL for sharing and reloading.

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
(for `report`, `backfill`, and `daemon`, bound to one machine id, see
`~/.firekeeper/machine-id`) can only call `/v1/ingest` and `/v1/heartbeat`;
read tokens can only read. These commands open the database file directly, so
`--db PATH` must match the server's. The default scope is `read`, and
`--account EMAIL` assigns the token to an account (default: the single-user
account).

On a server with tokens but no accounts, the web UI's `/login` page asks for a
read token and keeps it in `sessionStorage`; once an account exists the
browser signs in with email and password instead. Once the server has started
with any active token, every `/v1/*` request needs an
`Authorization: Bearer TOKEN` header (or a browser session; see
[Accounts](#accounts)). A token created while the server is already running,
on a server with no accounts, takes effect when the server restarts; the first
account takes effect immediately. Failed attempts are rate limited per client
address as the server sees it, so behind a proxy all clients share one budget.

With no tokens and no accounts the API is open, which is only acceptable on
loopback.

The **Tokens** page of the web UI (`/#/tokens`) lists the account's tokens
with machine name, scope, created and last-used times, and revokes them. It
can also create a token for CI or a headless machine, shown once; an ingest
token needs the machine id from that machine's `~/.firekeeper/machine-id`. A
revoked token is refused on its next request. Linking and token management
need a signed-in browser session, so a leaked token cannot add machines or
mint tokens.

### Linking a machine

On a server with accounts, a machine connects without anyone copying a token:

```sh
firekeeper login --server https://dash.example
```

It prints a URL and a short code and waits. Open the URL (sign in if asked),
check that the page shows the same code, name the machine, and approve; the
command then finishes and stores an ingest token, bound to this machine and
your account, in `~/.firekeeper/config.toml` (or the file `$FIREKEEPER_CONFIG`
names) with mode `0600`, along with the server URL. Codes expire after 10
minutes and work once. `--name` suggests the machine name on the approval page
(default: the host name). On a server with no accounts, `login` fails because
nobody could approve the code; create an account first.

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
`logout` removes the token only from the config file; a token that comes from
`$FIREKEEPER_TOKEN` has to be unset there.

### Accounts

One server can hold several people's data. Every machine, session, token and
transcript belongs to an account, and an account can never list, read, stream
or upload into another's.

- **Single user, the default.** A server with no tokens and no accounts needs
  no signup or login; everything belongs to a built-in default account.
  Existing databases are upgraded to this automatically.
- **Accounts.** Creating the first account turns login on for everyone, even
  while the server is running. People sign in at `/login` with email and
  password (10 to 256 characters, stored as argon2id hashes), which sets a
  14-day browser session cookie. Browser sessions can read but not upload;
  uploads use ingest tokens, which a signed-in person creates by
  [linking a machine](#linking-a-machine).

`serve` prints which mode it started in: `single-user` (open),
`single-user, token-protected` (no accounts, API needs a token), or
`multi-user`.

In multi-user mode every dashboard page load without a valid session
redirects to `/login`, and a session that ends while the page is open (it
expired, was signed out elsewhere, or the account was disabled) sends the
reader back there with a notice. After signing in, the reader returns to the
view they were on. The header shows the signed-in email and a **Sign out**
button. Static assets and the `/login`, `/signup`, and `/privacy` pages need no
credentials. `/signup` follows `--signup`: closed
shows a "signup is closed" notice, invite asks for a code. To look at these
screens without accounts, open `/?mock=1`: it uses synthetic fixtures and
simulates sign-in (password `firekeeper-mock`), and never contacts the API.

The landing page is a **Machines** overview with one card per machine in the
account: name, hostname, OS, version, online or offline, session counts by
state, and last activity. A machine is offline when its last heartbeat is
more than 90 seconds old; cards update from the live stream and the clock, so
no reload is needed. A new account with no machines shows how to link one,
and a machine that has linked but uploaded nothing shows as online with zero
sessions. In mock mode, `?mock=1&machines=none` and `?mock=1&machines=linked`
show those two empty states. The version column is empty for machines
reporting from this build, which does not send one.

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
`account create --password-stdin` in scripts. `invite` lets a person sign up
with a one-time code from `invite create`; `open` lets anyone who can reach the
server. Without `--account`, `token create` and `invite create` act for the
single-user default account, whose existing tokens keep working. Data uploaded
before accounts existed stays with the default account and is not visible to
new accounts. Put HTTPS in front of any server reachable beyond loopback; the
session cookie is only marked `Secure` when it sees HTTPS (or
`X-Forwarded-Proto: https`). Each of these commands opens the database file
directly, so pass the same `--db PATH` as `serve` when you do not use the
default.

### Test-bed safeguards

These make it safer to let other people upload their transcripts to a server
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
  the operator raises the limit. Re-sending data already stored always works.
  The single-user default account is never limited. The account page shows
  usage against these limits.
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
| `--server URL` | Dashboard server. Default `http://127.0.0.1:7777`, `$FIREKEEPER_SERVER`, or the config file. |
| `--provider NAME` | Upload this provider's sessions. Repeatable. The daemon refuses to start unless a provider comes from a flag, `$FIREKEEPER_PROVIDERS`, or the config file. |
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
- `daemon` itself does not detach. To run it in the background at login,
  install it as a service (below), or run it under `tmux` or `nohup`.

### Running the daemon at login

```sh
firekeeper daemon install
firekeeper daemon status
firekeeper daemon logs -f
firekeeper daemon uninstall
```

With no provider flags, `$FIREKEEPER_PROVIDERS`, or configured allowlist,
`daemon install` detects Codex, Copilot, and Claude Code from their existing
local data directories or CLI executables on `PATH`, honoring `CODEX_HOME`,
`COPILOT_HOME`, and `CLAUDE_CONFIG_DIR`. Installing opts into transcript
uploads for those detected providers; it prints and saves the allowlist in the
service definition. Use `--dry-run` to preview it, or `--provider` to select
providers explicitly. Environment and config-file allowlists take precedence
over detection, including an empty config-file allowlist (which prevents
installation). If no supported provider is detected, installation stops with
guidance. Rerun install after adding a provider. Kimi and OpenCode are not
detected for uploading because they have no transcript readers.

`daemon install` takes the same `--server`, `--provider`, and `--interval`
flags as `daemon`, writes the ones you pass into a per-user service
definition, and starts the service. Settings left to the config file are read
each time the daemon starts, so after editing the file, restart the service
(`install` again) to apply it. The service runs the binary you ran `install` with
(symlinks resolved) as `firekeeper daemon --quiet ...`, so after moving or
reinstalling Firekeeper, or to change flags, run `install` again; it
replaces the definition and restarts the service. `install` copies `PATH`,
`CODEX_HOME`, `COPILOT_HOME`, `KIMI_CODE_HOME`, `CLAUDE_CONFIG_DIR`,
`XDG_CONFIG_HOME`, and `FIREKEEPER_CONFIG` from your shell into the definition
when they are set. It refuses to install a temporary `go run` build. Nothing
here needs or uses `sudo`.

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
service file. The daemon reads the ingest token from the config file, where
`firekeeper login` puts it, or from `$FIREKEEPER_TOKEN` when run by hand.

### Configuration

`report`, `backfill`, `daemon`, `serve`, `login`, `whoami`, `logout`, and
`config show` read `~/.firekeeper/config.toml`, or the file
`$FIREKEEPER_CONFIG` names (a missing file named that way is an error).
Precedence is flags, then environment (`FIREKEEPER_SERVER`,
`FIREKEEPER_TOKEN`, `FIREKEEPER_PROVIDERS` as a comma list,
`FIREKEEPER_INTERVAL`), then the file, then defaults. The file is optional;
unknown keys are an error, so a typo cannot silently drop an exclusion. It
holds a token, so keep it private (`chmod 600`).

```toml
server = "https://dash.example.com"
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
`github.com/acme/api`. `.firekeeper-ignore` still works as before. The prices
above are placeholders, not real rates.

`firekeeper config show` prints the merged configuration with the token
masked and notes where each layered setting came from. `login` and `logout`
edit only the top-level `server` and `token` keys, keep the rest of the file
as it is, and leave it mode `0600`.

Files Firekeeper reads and writes outside the provider data it only reads:

| Path | Written by | Holds |
| --- | --- | --- |
| `~/.firekeeper/machine-id` | any command that identifies the machine | this machine's id |
| `~/.firekeeper/state.json`, `state.lock` | `report`, `backfill`, `daemon` | per-file read offsets and their lock |
| `~/.firekeeper/config.toml` | you; `login` and `logout` edit `server` and `token` | reporting configuration |
| `~/.firekeeper/dashboard.db` | `serve` and its admin commands | the dashboard database, including transcripts |
| `~/.firekeeper/daemon.log`, `daemon.lock` | `daemon` | log and single-instance lock |
| `settings.json` under the user config directory | the terminal dashboard | sprite and background choices |

## Documentation

- [docs/self-hosting.md](docs/self-hosting.md): run the server in a container,
  create tokens, put TLS in front of it, and back up its database.
- [docs/hosting.md](docs/hosting.md): the owner's runbook for his own instance
  behind HTTPS on a VPS, including backups and upgrades. The instance it
  describes is a personal test bed, not a service for others; run your own.
- [docs/api.md](docs/api.md): the v1 HTTP API.
- [docs/event.schema.json](docs/event.schema.json): the normalized transcript
  event.
- [ASSETS.md](ASSETS.md): artwork credits.

## Status

Firekeeper is an early-stage project. macOS is the main target; Linux is best
effort and Windows has not been tried. Process discovery works without
integrations, but provider internals can change between CLI releases. Expect
adapters and metadata handling to evolve. Firekeeper cannot detect every
session state without provider cooperation, and no licence file is included in
the repository yet.

## Artwork

Pixel artwork comes from Calciumtrice under CC BY 3.0. See
[ASSETS.md](ASSETS.md) for the asset credit notes. Artwork files live in
`assets/`.
