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
- Keep existing CLI workflows: no daemon, remote argument, or wrapper required.
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

## Subcommands

Running `firekeeper` with no subcommand starts the dashboard as usual. Opt-in
reporting subcommands are being added; `firekeeper --help` lists them.

```sh
firekeeper snapshot --json   # print currently discovered sessions as JSON
firekeeper report --dry-run  # show what one upload pass would send
firekeeper report --provider codex --provider copilot
firekeeper serve             # run the dashboard at http://127.0.0.1:7777/
firekeeper daemon --provider codex   # report every 15 seconds until stopped
firekeeper daemon install --provider codex   # run the daemon at login
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
| `--token TOKEN` | Ingest token for the server. Default `$FIREKEEPER_TOKEN`. |

Upload is opt-in. With no `--provider`, `report` uploads nothing and behaves
like `--dry-run`. A dry run never opens a network connection and never moves
read offsets.

Redaction is best effort. It catches common secret shapes (cloud and GitHub
tokens, API keys, bearer tokens, private keys, JWTs, `SECRET=...`-style
assignments) and rewrites your home directory to `~`, but it cannot recognize
every secret. Treat uploaded transcripts as sensitive.

Sessions are never read when their working directory is unknown, or when the
directory or any parent up to its Git root contains a `.firekeeper-ignore`
file.

Read offsets live in `~/.firekeeper/state.json` and advance only after the
server accepts a batch, so an interrupted pass can be rerun without losing
or duplicating events. A transcript that shrinks is re-read from the start;
the server drops events it already has.

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
| `--insecure` | Allow a `--listen` address other than loopback. |

### Tokens

```sh
firekeeper serve token create --name laptop --scope ingest --machine MACHINE_ID
firekeeper serve token create --name me --scope read
firekeeper serve token list
firekeeper serve token revoke NAME_OR_ID
```

`create` prints the token once; only its SHA-256 hash is stored. Ingest tokens
(for `report`, bound to one machine id, see `~/.firekeeper/machine-id`) can
only call `/v1/ingest` and `/v1/heartbeat`; read tokens can only read, and the
web UI asks for one on a login page and keeps it in `sessionStorage`. Once any
active token exists, every `/v1/*` request needs `Authorization: Bearer TOKEN` header.
Failed attempts are rate limited per IP.

With no tokens the API is open, which is only acceptable on loopback.
`serve` refuses a non-loopback `--listen` address unless a token exists or
`--insecure` is passed, and then warns that anyone who can reach the port can
read every stored transcript and upload new ones.

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
firekeeper daemon install --provider codex --provider copilot
firekeeper daemon status
firekeeper daemon logs -f
firekeeper daemon uninstall
```

`daemon install` takes the same `--server`, `--provider`, and `--interval`
flags as `daemon`, writes them into a per-user service definition, and
starts the service. The service runs the binary you ran `install` with
(symlinks resolved) as `firekeeper daemon --quiet ...`, so after moving or
reinstalling Firekeeper, or to change flags, run `install` again; it
replaces the definition and restarts the service. `install` copies `PATH`,
`CODEX_HOME`, `COPILOT_HOME`, `KIMI_CODE_HOME`, and `XDG_CONFIG_HOME` from
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

The installed daemon does not send a bearer token yet: `daemon` has no
`--token` flag, and `install` does not write secrets into service files.
Use it with a dashboard that has no tokens, on loopback.

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
