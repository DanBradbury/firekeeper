# Firekeeper reference

The full reference for the terminal dashboard, session reporting, the daemon and configuration. New here? Start with the [README](../README.md).

## Supported providers

| Provider | Process discovery | Session state and metadata | Usage tab | Transcript upload |
| --- | --- | --- | --- | --- |
| Codex | Yes | Yes, from rollout events and `state_5.sqlite` | Limits and history | Yes |
| GitHub Copilot CLI | Yes | Yes, from `events.jsonl`, `workspace.yaml`, and `session-store.db` | Local history and plan details | Yes |
| Claude Code | Yes | Yes, from `~/.claude/projects` transcripts | Token history | Yes |
| Kimi Code | Yes | Metadata from session `state.json`; the state is shown as `ACTIVE` whenever a session matches | Token history | Yes, from the main agent's `wire.jsonl` |
| OpenCode | Yes | Yes, from `session_v2` and `session_message` in `opencode.db` | No | Yes |
| Gemini CLI | Yes | Yes, from session logs under `~/.gemini/tmp`; `NEEDS INPUT` is never shown | No | Yes, from the main session log |
| Cursor CLI | Yes | Yes, from agent transcripts under `~/.cursor/projects` and chat titles in `~/.cursor/chats`; `NEEDS INPUT` is never shown | No | Yes, from the main agent transcript, without timestamps or token counts |

States describe the latest event Firekeeper can see, not ground truth.
Kimi Code support reads the session layout under `~/.kimi-code` (wire protocol
1.5 and the sessions it migrates); the older Python Kimi CLI's `~/.kimi` data
is not read. OpenCode support reads the database layout
OpenCode has used since August 2026; older releases need an OpenCode update
first. Gemini CLI support reads the JSONL session logs Gemini CLI has written
since April 2026, and the whole-file `.json` sessions of earlier releases.
Cursor CLI support reads the agent transcripts `cursor-agent` writes; Cursor
does not document that format, so it may change without notice. Cursor's
transcripts record no timestamps or token counts, so Cursor sessions show
neither.

## Windows

Windows 10 (1809 or newer) and Windows 11 are supported, on amd64 and arm64,
from Windows PowerShell 5.1, PowerShell 7, Windows Terminal, or a classic
console. Setup is the same as elsewhere: [install](install.md) with
`install.ps1`, then run `firekeeper`; `firekeeper daemon install`,
`firekeeper login`, and `firekeeper serve` work as described below. Nothing
needs administrator rights, Git Bash, or WSL, and `sqlite3` does not have to be
installed (Firekeeper reads provider databases itself when the command is
missing). Git must be on `PATH` for project names and file links, as on every
platform.

What differs from macOS and Linux:

- **Home directory.** `~` is `%USERPROFILE%`: provider data lives in
  `%USERPROFILE%\.codex`, `.claude`, `.copilot`, and so on, and Firekeeper's own
  files in `%USERPROFILE%\.firekeeper`. The same `CODEX_HOME`-style overrides
  apply. In `config.toml`, paths may start with `~\` as well as `~/`; write
  Windows paths in single quotes (`'C:\work\clients'`) or with forward slashes
  so TOML does not read `\` as an escape.
- **Process discovery** uses the Windows API instead of `ps` and `lsof`: a
  Toolhelp process snapshot, with command lines and working directories read
  from each process's environment block (`NtQueryInformationProcess` and
  `ReadProcessMemory`, on your own processes only). An elevated program or
  another user's is listed by name only. There is no TTY, so the TTY column
  shows `—`.
- **Open files cannot be listed**, which Codex discovery uses elsewhere to tell
  which thread a runtime holds. On Windows a Codex runtime is matched to the
  newest thread in its working directory that changed in the last 24 hours, so
  a session you closed a moment ago in the same directory can be shown for a
  runtime still open there. The other providers match by command line, logs,
  or working directory first and behave the same; only Copilot CLI's last-resort
  open-file match is unavailable.
- **Terminal switching** raises the terminal window, not the tab; see
  [Processes](#processes). Herdr pane focus is not attempted on Windows.
- **File permissions.** Windows has no `0600` mode bits. `config.toml`, which
  may hold a dashboard token, and `state.json` rely on the default permissions
  of your profile folder, which admit you, administrators, and SYSTEM.
- **Redaction** matches your home directory and `redact.paths` the way
  Windows does, ignoring case and either separator: `C:\Users\you`,
  `c:\users\YOU`, `C:/Users/you`, `/c/Users/you`, WSL's `/mnt/c/Users/you`,
  `file:///c%3A/Users/you` and other percent-encoded text
  (`C%3A%5CUsers%5Cyou`), `\\localhost\c$\Users\you` and its long-path form
  `\\?\UNC\localhost\c$\Users\you`, escaped backslashes
  (`C:\\Users\\you`, as in JSON text inside a tool call), and an 8.3 short
  name such as `C:\Users\YOURNA~1`, including the hashed kind (`YO1F2B~1`)
  and the real short name Windows reports for the path.
- **`exclude` directory globs ignore case** on Windows, and either separator
  matches, so `C:\Users\you\clients` also excludes a session in
  `c:/users/you/Clients/acme`.
- **Agents inside WSL** run on the Linux side, so Firekeeper for Windows does
  not see them. Install the Linux build inside WSL to monitor those.
- **The binaries are not code-signed.** `install.ps1` downloads without
  SmartScreen's "downloaded from the internet" mark; a browser download of the
  zip may trigger a prompt.
- **Updates** re-run `install.ps1` for the new version (with `powershell`, or
  `pwsh` when Windows PowerShell is missing) instead of `install.sh`.
- **The login service** is a Task Scheduler task; see
  [Running the daemon at login](#running-the-daemon-at-login).
- **The dashboard server** for Windows is `firekeeper-server.exe`, from a
  `.zip` release asset (see [Release layout contract](#release-layout-contract)).
  Server releases published before Windows builds have no such asset, and
  `serve` then stops with `no Windows server build available for <version>`.

CI builds, vets, and tests Firekeeper on a Windows runner, including real
Task Scheduler registration and the PowerShell installer, but Windows has had
less everyday use than macOS. Please report what you find.

## Terminal dashboard

Firekeeper opens on Processes. Press Tab to move between three text views:

- **Processes** — running agent harnesses and their session details.
- **Overview** — an at-a-glance panel per provider with data: quota meters and
  reset countdowns where a supported source exists (Codex limits, Copilot AI
  credits), plus tokens today, over the last 7 days, and lifetime. Claude Code
  and Kimi expose no quota locally, so their panels show token totals and point
  to `/usage`. `r` refreshes every provider. It reads the same sources as Usage.
- **Usage** — Codex quotas plus Codex, Copilot CLI, Kimi, and Claude Code token history and model usage.

Two lookups in the Usage tab leave the machine, and neither uploads session
data. Codex usage starts a short-lived `codex app-server` and asks it for your
limits. Copilot usage, when a GitHub token is available, asks GitHub for plan
details (see [Usage](#usage)).

### Global controls

| Key | Action |
| --- | --- |
| Tab / Shift+Tab | Switch views |
| `?` | Show all hotkeys; any key closes the list without running its action (Ctrl+C still quits) |
| `q` / Ctrl+C | Quit |

### Processes

The Processes tab is laid out as a dashboard. Summary panels across the top
count sessions by state, break the selected session's tokens into input,
output, cache read, and cache write, and show live tokens per provider and
the projects and branches in use. Input counts uncached input only. Codex and
Copilot report only cached input, which shows as cache read, so their cache
write stays 0. The breakdown comes from Claude Code transcripts, Codex rollout
`token_count` events, and Copilot `model.model_call_success` events, read from
the tail of each file, so very long sessions can be undercounted. Other
harnesses show the total only. A Claude Code session that has subagent
files warns that its measured total is likely low, because background
summary calls are not in the transcript. See [Claude Code subagent
undercount](#claude-code-subagent-undercount). Below them, the sessions
table lists one runtime per row with
its provider, PID, project, short session id, title, state, model, tokens, and
uptime, and a second line with the branch and working directory. A detail
panel at the bottom shows the selected runtime's session metadata and process
tree. On smaller terminals the summary and detail panels are hidden and the
narrowest table columns drop out first; Enter still expands details inline.

| Key | Action |
| --- | --- |
| Up / Down or `J` / `K` | Select runtime |
| Enter | Expand session metadata and process tree |
| `s` | Switch to terminal containing selected runtime |
| `c` | Open the selected session's chat history |
| Page Up / Page Down | Move through runtime groups |
| Home / End | Jump to first or last runtime |
| `r` | Refresh immediately |

#### Chat history

Press `c` on a Claude Code, Codex, Copilot, Kimi Code, OpenCode, Gemini CLI,
or Cursor CLI session to
read its conversation: your prompts and the assistant's replies, newest at the
bottom. Tool calls, tool output, and context the harness injects (environment blocks,
`AGENTS.md` instructions, slash-command wrappers) are left out. The transcript
is read locally and read-only, nothing is uploaded, and new messages appear on
the next two-second refresh while the view follows the latest message. Up/Down
scroll, Page Up/Page Down move a page, `g`/`G` jump to the first or latest
message, `r` reloads, and Esc, `c`, or `q` return to Processes. The view keeps
the latest 1,000 messages. An OpenCode reply appears once it finishes.

Terminal switching supports Ghostty (1.3+), Terminal.app, and iTerm2 on macOS.
Ghostty sessions are matched by working directory; Terminal.app and iTerm2 are
matched by TTY. macOS may request Automation permission on first use. Sessions
without a controlling TTY cannot be selected this way.
Firekeeper uses exact TTY matching when the installed Ghostty scripting API
provides it. Ghostty 1.3 does not expose TTYs, so after its first best-effort
working-directory match Firekeeper remembers the pane's stable ID for repeated
switches. The initial match remains ambiguous when multiple Ghostty panes share
the same working directory.

On Windows, `s` raises the window that hosts the selected runtime: Firekeeper
walks up from the runtime through the programs that started it until one owns a
visible window, such as Windows Terminal, a classic console window, or an
editor's integrated terminal. Windows has no TTY to match, and a Windows
Terminal window does not let other programs choose its tabs, so with several
tabs in one window you land in the right window and pick the tab yourself.
Windows may refuse to bring a window forward when Firekeeper is not the
foreground program.

### Usage

| Key | Action |
| --- | --- |
| Left / Right or `H` / `L` | Switch between Codex, Copilot, Kimi, and Claude |
| `r` | Refresh selected provider |

Press `Enter` on any supported provider to browse historical sessions. Use
Up/Down to select a session; press `Enter` or `Esc` to return. Codex history is
read from local `state_5.sqlite`; Copilot history is read from local
`session-store.db`; Claude Code history is read from its transcripts. Each
browser is limited to the 500 most recent sessions.
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

Claude limits (5-hour and weekly, with reset times) are not stored locally, so
Firekeeper asks for them, and this is on by default. When Claude Code is signed
in on this machine, Firekeeper reads that sign-in (`CLAUDE_CODE_OAUTH_TOKEN`,
the macOS Keychain item `Claude Code-credentials`, or
`CLAUDE_CONFIG_DIR/.credentials.json`, default `~/.claude/.credentials.json` on
Linux and Windows) and asks Anthropic's usage endpoint, the way Claude Code's
`/usage` does. With no sign-in it does nothing and shows nothing. The endpoint
is undocumented and can change without notice, so a failure only shows a short
note on the Claude panel. The token is used for that single request: it is not
stored, refreshed, logged, or uploaded, and Claude Code's own files are never
written. On macOS the Keychain may ask you to allow access the first time.
An expired sign-in is reported, not renewed; open `claude` once to renew it.
`firekeeper claude-limits status` shows what it can read, `disable` turns it
off, and `enable` turns it back on. The setting is `[claude_limits] enabled` in
`~/.firekeeper/config.toml`.

Claude Code usage reads assistant message usage from local transcripts under
`CLAUDE_CONFIG_DIR/projects` (default `~/.claude/projects`), read-only. It
shows session, model, input, cache read, cache write, output, and total token
history. Claude Code repeats a response's usage on every content block line,
so each message ID is counted once, including messages copied into a resumed
session. Requests Claude Code does not write to the transcript, such as title
generation, are not counted, so totals can be slightly below what Claude Code
reports. Plan limits and reset times are not stored locally; use Claude
Code's `/usage`. Firekeeper does not read credentials or call Anthropic
endpoints.

#### Claude Code subagent undercount

Claude Code asks a model for a short status line for each running subagent.
Those `agent_summary` calls are not written to the transcript, so a session
that has files under `<session>/subagents/agent-*.jsonl` is likely
undercounted. Firekeeper does not estimate the missing tokens. It shows a
warning on the dashboard and sets `undercount_warning` on
`firekeeper snapshot --json`. Measured totals are unchanged, and the warning
is not uploaded. Why the calls exist is described in the
[Flamerix gist](https://gist.github.com/flamerix-ai/c3905705cc4205e5de3011fa2e74efce);
Firekeeper does not ship that hook. Exact accounting is being investigated
in [issue #21](https://github.com/DanBradbury/firekeeper/issues/21).

Copilot usage reads local history from read-only `session-store.db` and, when
an authenticated Copilot/GitHub CLI token is available, fetches plan AI-credit
allowance, remaining credits, and reset timing from GitHub. Token lookup honors
`COPILOT_GITHUB_TOKEN`, `GH_TOKEN`, and `GITHUB_TOKEN`, then `gh auth token`.
Local daily token bars are split and color-coded by model. Plan data is best
effort: it comes from an internal Copilot endpoint that is not a supported
API and may change or disappear. The local history alone is not an
account-wide quota.

### How session discovery works

Firekeeper scans local processes every two seconds with `ps` and collapses
related processes into runtime groups. The scan requests full command lines on
macOS and Linux so long installation paths do not hide harness names. Windows
has neither `ps` nor `lsof`, so Firekeeper reads the process list, command
lines, and working directories through the Windows API instead; see
[Windows](#windows) for what that changes. Sessions
that are not running are not discovered this way; past sessions reach the
dashboard only through [`backfill`](#backfill).

- **Codex:** locates open rollout files with `lsof` and queries
  `~/.codex/state_5.sqlite` read-only for available session metadata.
- **Kimi Code:** maps runtime PIDs to their working directory and picks the
  most recently updated session for that directory from session `state.json`
  files under `KIMI_CODE_HOME`, read without modifying them. Several Kimi
  runtimes in one directory are all matched to the newest session.
- **Copilot CLI:** maps runtime PIDs to `~/.copilot/session-state` and reads
  `workspace.yaml`, `events.jsonl`, and `session-store.db` without modifying
  them.
- **Claude Code:** uses an explicit `--session-id` or `--resume` id when the
  command line has one, and otherwise maps the runtime's working directory to
  its project folder under `~/.claude/projects` and picks the most recently
  written transcript there. Several Claude Code runtimes in one directory may
  be matched to the wrong transcript. Transcripts are read, never modified.
- **OpenCode:** uses an explicit `--session`/`-s` id when the command line has
  one, and otherwise matches the runtime's working directory to the session
  directories in OpenCode's database and picks the most recently updated
  session there. Several OpenCode runtimes in one directory, or one started
  with a directory argument from elsewhere, may be matched to the wrong
  session. `opencode serve` and the other helper subcommands are listed
  without a session. The database is read, never modified, with
  `sqlite3 -readonly` when `sqlite3` is on `PATH` and in-process otherwise.
  `NEEDS INPUT` is never reported
  for OpenCode, because its permission prompts are not in the database.
- **Gemini CLI:** recognizes `gemini` and the node process it relaunches as
  one runtime, and lists the `mcp`, `extensions`, `skills`, `hooks`, and
  `gemma` subcommands without a session. It uses an explicit `--session-id`,
  or a `--resume` session id, when the command line has one, and otherwise
  maps the runtime's working directory to its project folder under
  `~/.gemini/tmp` (through `~/.gemini/projects.json`) and picks the most
  recently written session log there. Several Gemini CLI runtimes in one
  directory may be matched to the wrong log. Logs are read, never modified.
  Gemini CLI records tool calls only after they finish, so a tool call
  waiting for approval shows as `ACTIVE`, and `NEEDS INPUT` is never reported.
- **Cursor CLI:** recognizes `cursor-agent` and the bundled node process it
  runs (under `~/.local/share/cursor-agent/versions`) as one runtime, and
  lists `login`, `status`, `update`, `mcp`, and the other helper subcommands
  without a session. It uses a `--resume` chat id when the command line has
  one, and otherwise maps the runtime's working directory to its project
  folder under `~/.cursor/projects` and its chat folder under
  `~/.cursor/chats`, and picks the most recently written chat there. Several
  Cursor CLI runtimes in one directory may be matched to the wrong chat.
  Chat titles and models are read from each chat's `store.db` with
  `sqlite3 -readonly` (or in-process when `sqlite3` is not installed).
  Nothing is modified. Approval prompts are not recorded, so a tool call
  waiting for approval shows as `ACTIVE`, and `NEEDS INPUT` is never reported.

`CODEX_HOME`, `KIMI_CODE_HOME`, `COPILOT_HOME`, `CLAUDE_CONFIG_DIR`,
`OPENCODE_DB`, `XDG_DATA_HOME` (for OpenCode's
`~/.local/share/opencode/opencode.db`), `GEMINI_CLI_HOME` (for Gemini
CLI's `~/.gemini`), and `CURSOR_CONFIG_DIR` (for Cursor CLI's `~/.cursor`)
overrides are honored. Provider events
supply best-effort `ACTIVE`, `WAITING`, and `NEEDS INPUT` states. Codex
Desktop helper processes that cannot be matched to rollout metadata are not
shown as sessions.

Discovery is also callable without the TUI through
`internal/session.Discover(ctx, opts)` and, from the shell, through
`firekeeper snapshot --json`. Both return normalized session metadata,
including the project basename (Git root or working directory) and a stable
UUID stored in `~/.firekeeper/machine-id`, created on first use. Provider
enrichment failures return usable runtime metadata with a warning.

All monitoring stays local. Firekeeper does not proxy prompts or replace agent
clients.

## Experimental: local OpenTelemetry receiver

`firekeeper telemetry enable` writes Claude Code's telemetry environment into
`~/.claude/settings.json` and turns on a listener in the Firekeeper daemon.
The listener binds to `127.0.0.1` only, appends `api_request` usage to
`~/.firekeeper/telemetry.jsonl`, and does not upload it or record prompt
text. It is off until you enable it. With telemetry for a session, the
dashboard shows the exact total and a breakdown of background calls that
are not in the transcripts. Without it, transcript totals stay as they are,
a session with subagent files keeps the undercount warning, and the
dashboard says that `firekeeper telemetry enable` gets exact totals.
`otel-listen` and `otel-reconcile` are the same store, for a one-off capture.
See [docs/research/otel-telemetry.md](research/otel-telemetry.md).

## Reporting

Reporting is a set of explicit subcommands: `report`, `backfill`, `daemon`,
`login`, `logout`, `whoami`, `config`, and `snapshot`. `report`, `backfill`,
and `daemon` send session metadata and full transcripts to a dashboard server
that speaks the [open API](api.md). Nothing is sent until you allow
providers with `--provider`, `backfill --all`, the `FIREKEEPER_PROVIDERS`
variable, or the config file, and point at a server (`http://127.0.0.1:7777`
by default). `report --dry-run` shows what would be sent without opening a
connection. Agent CLIs never depend on any of these commands.

```sh
firekeeper snapshot --json            # print currently discovered sessions as JSON
firekeeper report --dry-run           # show what one upload pass would read
firekeeper report --provider codex --provider copilot
firekeeper backfill --all --dry-run   # plan importing all supported local history
firekeeper backfill --all             # confirm, then import past sessions
firekeeper daemon --provider codex    # report every 15 seconds until stopped
firekeeper daemon install             # detect providers and run the daemon at login
firekeeper login --server https://dash.example   # link this machine without copying a token
firekeeper whoami                     # server, account and machine this machine reports as
firekeeper logout                     # revoke and remove this machine's token
firekeeper config show                # print the merged configuration, token masked
```

`export` is a placeholder that prints `not implemented` and exits with
status 2. `serve` hands over to the separate `firekeeper-server` binary and exits
with status 2 when it cannot find it (see the
[README](../README.md#the-dashboard-server)).

### Installing the server from `serve`

When `serve` cannot find `firekeeper-server`, it can offer to install it. This
is off unless a release source is configured, which release builds of
Firekeeper do not do yet; until then `serve` prints its instructions and exits
with status 2, as above.

When on, the offer is made only if stdin and stderr are terminals and neither
`CI` nor `FIREKEEPER_NO_PROMPT` is set. Otherwise `serve` behaves as if it were
off, with no network use. The prompt is:

```text
firekeeper-server is not installed. Download it from <host> and install it to <dir>? [y/N]
```

The default is no. Only `y` or `yes` proceeds; then Firekeeper looks up the
latest release, downloads the archive for your OS and architecture, checks its
SHA-256 against the release's `checksums.txt`, extracts only the
`firekeeper-server` file (`firekeeper-server.exe` from the zip on Windows), installs it atomically, runs `firekeeper-server
version` once, and hands over to it. Any failed check installs nothing. It
never uses `sudo`.

`<dir>` is the directory of the running `firekeeper` (symlinks resolved) if
that is writable, otherwise `~/.local/bin` (created; a `PATH` hint is printed
if it is not on your `PATH`). If `firekeeper` lives under a Homebrew prefix
Firekeeper does not write there. It offers to run `brew install
DanBradbury/tap/firekeeper-server` instead, and downloads directly only if
`brew` is not installed.

Release builds of `firekeeper` have the offer switched on, pointing at
`DanBradbury/firekeeper-server-releases`. Builds made with plain `go build`
leave it off. To try it against another release server, or to turn it on in
such a build, point it at one:

```sh
FIREKEEPER_SERVER_RELEASES_URL=https://releases.example/firekeeper-server firekeeper serve
```

Setting `FIREKEEPER_SERVER_RELEASES_URL` enables the offer. The URL must be
https; plain http is accepted only for `127.0.0.1`, `::1` and `localhost`, and
only through this variable. Redirects to non-https are refused. The release config
bakes the default in with `-ldflags "-X
github.com/DanBradbury/firekeeper/internal/serverinstall.ReleasesURL=..."`; the
environment variable wins over it.

#### Updating the server from `serve`

When `serve` finds a server, it asks the release source for the latest version
(one unauthenticated GET, 2 second timeout, nothing about you or your
sessions), runs `firekeeper-server version`, and, if the release is newer,
offers:

```
firekeeper-server update available: 0.4.0 -> 0.5.0
  1) Update now
  2) Not now
  3) Skip this version
Choose [2]:
```

Enter or anything unknown is "not now". `3` stops the offer for that release
until a newer one appears (remembered in `~/.firekeeper/server-update.json`).
`1` installs the same way as the first install (SHA-256 checked against
`checksums.txt`, archive layout enforced) over the existing file, or runs
`brew upgrade` when the server lives in a Homebrew Cellar. It then starts the
new server.

Before it replaces the file, it keeps a copy. After the install it runs the
new server's `license --json`; if that says `not covered by this build` (the
release is newer than the license's update period, so the server would refuse
to start), the old file is put back and `serve` says so. If the directory is
not writable, or the server is not named `firekeeper-server`
(`firekeeper-server.exe` on Windows), nothing is
changed and `serve` continues with what it has.

It never runs for `serve --help` or the operator commands (`token`,
`account`, `admin`, `invite`, `prices`), when a build has no release source,
when `FIREKEEPER_SERVER_BIN` names the server, in CI, when stdin or stderr is
not a terminal, or when `FIREKEEPER_NO_PROMPT` or `FIREKEEPER_NO_UPDATE_CHECK`
is set.

#### Release layout contract

For whoever publishes the server (task A6). The repository is the public,
binaries-only `DanBradbury/firekeeper-server-releases`.

- Latest release: `https://api.github.com/repos/DanBradbury/firekeeper-server-releases/releases/latest`,
  read for `tag_name` (for example `v1.4.0`; letters, digits and `._+-` only).
- Assets, at
  `https://github.com/DanBradbury/firekeeper-server-releases/releases/download/<tag>/<asset>`:
  - `firekeeper-server_<version>_<os>_<arch>.tar.gz`, where `<version>` is the
    tag without a leading `v`, `<os>` is `darwin` or `linux`, and `<arch>` is
    `amd64` or `arm64`. The archive holds exactly two regular files at its
    top level: `firekeeper-server` and `LICENSE`. Any other entry, a link, a
    nested or absolute path, or a duplicate makes the client reject it.
  - `firekeeper-server_<version>_windows_<arch>.zip` for Windows, where
    `<arch>` is `amd64` or `arm64` (for example
    `firekeeper-server_0.8.0_windows_amd64.zip` and
    `firekeeper-server_0.8.0_windows_arm64.zip`). The zip holds, at its top
    level, the regular file `firekeeper-server.exe` and optionally `LICENSE`,
    under the same rules as the tar.gz. GoReleaser produces this layout from
    `binary: firekeeper-server` with a `format_overrides` entry of `zip` for
    `windows`.
  - `checksums.txt` in `sha256sum` format (`<hex>  <asset>` per line), with a
    line for every archive. A missing line or a mismatch aborts the install.
    On Windows a missing line for the zip means the release has no Windows
    build, and the client says `no Windows server build available for <tag>`.
- Size caps: 200 MB for the archive and for each entry.

For a custom base URL `B` (the override or `-X` value, anything other than the
production repository), the same files are served from `B/latest` (JSON with
`tag_name`) and `B/download/<tag>/<asset>`.

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

- `report` and `daemon` look back only `scan_lookback` (24 hours by default)
  for sessions that are not running, so a session that ended earlier than
  that, for example while the daemon was stopped for days, waits for a
  `backfill`. With `--live-only` or `scan_ended = false` they see only
  sessions running at the time of a pass.
- Claude Code subagent transcripts, Kimi Code subagent logs, OpenCode subagent
  sessions, Gemini CLI subagent logs, and Cursor CLI subagent transcripts are
  not imported. The local undercount warning for Claude Code subagents is not
  uploaded. See [Claude Code subagent undercount](#claude-code-subagent-undercount).
- A Cursor CLI session whose workspace directory no longer exists is not
  uploaded: Cursor's transcripts do not record the directory, so Firekeeper
  finds it on disk, and without it the exclusion rules cannot be checked.
- Provider file formats are read as found and change between CLI releases.
  Parsers keep records they cannot interpret as `meta` events, but the text
  shown for those events may be thin.
- The macOS terminal switch, and the launchd service, are macOS only. Linux is
  best effort: process discovery and the transcript readers use `ps`, `lsof`,
  and plain files, and the systemd user service has not been tested on real
  hardware. Windows has its own discovery, window switch, locks, and login
  service; see [Windows](#windows) for what differs.

### Report

`report` runs one pass: it discovers running sessions, reads transcript
events added since the last pass, redacts them, and uploads them to a
dashboard server in batches of at most 500 events. It prints one line per
session with event, batch, duplicate, and redaction counts, never transcript
text.

It then looks in the transcript folders of each allowed provider for sessions
that are not running, such as a scripted `-p` run that started and ended
between passes, and uploads what is new in them with state `ENDED`. It
compares each transcript's size and modification time with the saved read
offsets, so a transcript already uploaded is not read again, and a running
session is read once, not twice. The search covers transcripts modified in
the last `scan_lookback` (default 24 hours, combined with `--since` when
that is shorter); older history is `backfill`'s job. One pass reads at most
50 such sessions, newest first, and spends at most 10 seconds on them; the
rest wait for the next pass. The exclusion rules apply before a transcript is
opened, as for running sessions, and providers that are not allowed are not
searched. `--live-only`, or `scan_ended = false` in the config file, turns
this off and reads running sessions only.

| Flag | Meaning |
| --- | --- |
| `--server URL` | Dashboard server. Default `http://127.0.0.1:7777`, `$FIREKEEPER_SERVER`, or the config file. |
| `--provider NAME` | Upload this provider's sessions. Repeatable. `codex`, `copilot`, and `claude` have transcript readers today. |
| `--dry-run` | Read and redact, print counts, upload nothing. |
| `--since DURATION` | Skip transcript files not modified within the duration, for example `24h`. |
| `--live-only` | Read only sessions whose process is running; skip ended sessions found in the transcript folders. Default `scan_ended` in the config file (on). |
| `--token TOKEN` | Ingest token for the server. Default `$FIREKEEPER_TOKEN`, then the config file. |

Every flag here except `--dry-run` and `--since` can also come from the
environment or the config file; `--live-only` comes from the file only, as
`scan_ended` (see [Configuration](#configuration)).

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

`--all` detects Codex, Copilot, Kimi Code, Claude Code, OpenCode, Gemini CLI,
and Cursor CLI from their local data directories or executables on `PATH`, honoring provider home
overrides. It allows those providers for this pass, replacing any configured allowlist
without changing the config file. Use either `--all` or `--provider`, not
both. After `login`, the saved server and token are used automatically.
Claude Code subagent transcripts, Kimi Code subagent logs, OpenCode subagent
sessions, Gemini CLI subagent logs, and Cursor CLI subagent transcripts are
not imported. Cursor's editor writes its Agents window sessions to the same
transcript folders, so `backfill` imports those too.

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

### Linking a machine

On a server that supports linking, a machine connects without anyone copying a token:

```sh
firekeeper login --server https://dash.example
```

It prints a URL and a short code and waits. Open the URL (sign in if asked),
check that the page shows the same code, name the machine, and approve; the
command then finishes and stores an ingest token, bound to this machine and
your account, in `~/.firekeeper/config.toml` (or the file `$FIREKEEPER_CONFIG`
names) with mode `0600`, along with the server URL. Codes expire after 10
minutes and work once. `--name` suggests the machine name on the approval page
(default: the host name). On a server where nobody could approve the code,
`login` fails with the server's message.

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
| `--live-only` | Read only running sessions, as before transcript scanning. Default `scan_ended` in the config file (on). |
| `--quiet` | Log only to `~/.firekeeper/daemon.log`, not to stderr. |

- The provider allowlist works exactly as it does for `report`. Heartbeats
  list only sessions the pass was allowed to read, never sessions from
  other providers or ignored directories.
- Each pass also scans the transcript folders as `report` does, so sessions
  that start and end between passes, or while the laptop sleeps, are
  uploaded with state `ENDED`; they never show as active. The first pass
  after a start looks back `scan_lookback` (24 hours by default); later
  passes look only at transcripts modified since twice the interval before
  the previous pass began. The window moves on only after a pass that read
  every changed session without error, so sessions left over by the 50 per
  pass limit or the 10 second budget, or that failed to upload, are picked
  up next time. The window is kept in memory, not in `state.json`. The log
  shows counts, never paths.
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
`daemon install` detects Codex, Copilot, Kimi Code, Claude Code, OpenCode,
Gemini CLI, and Cursor CLI from their existing local data directories or CLI
executables on `PATH`, honoring `CODEX_HOME`, `COPILOT_HOME`, `KIMI_CODE_HOME`,
`CLAUDE_CONFIG_DIR`, `XDG_DATA_HOME`, `GEMINI_CLI_HOME`, and
`CURSOR_CONFIG_DIR`.
Installing opts into transcript uploads for those detected providers; it prints
and saves the allowlist in the service definition. Use `--dry-run` to preview
it, or `--provider` to select providers explicitly. Environment and config-file
allowlists take precedence over detection, including an empty config-file
allowlist (which prevents installation). If no supported provider is detected,
installation stops with guidance. Rerun install after adding a provider.

`daemon install` takes the same `--server`, `--provider`, `--interval`, and
`--live-only` flags as `daemon`, writes the ones you pass into a per-user service
definition, and starts the service. Settings left to the config file are read
each time the daemon starts, so after editing the file, restart the service
(`install` again) to apply it. The service runs the binary you ran `install` with
(symlinks resolved) as `firekeeper daemon --quiet ...`, so after moving or
reinstalling Firekeeper, or to change flags, run `install` again; it
replaces the definition and restarts the service. `install` copies `PATH`,
`CODEX_HOME`, `COPILOT_HOME`, `KIMI_CODE_HOME`, `CLAUDE_CONFIG_DIR`,
`XDG_DATA_HOME`, `OPENCODE_DB`, `GEMINI_CLI_HOME`, `CURSOR_CONFIG_DIR`,
`XDG_CONFIG_HOME`, and `FIREKEEPER_CONFIG` from your shell into the definition
when they are set (not on Windows, where a task has no environment of its own;
see below). It refuses to install a temporary `go run` build. Nothing
here needs or uses `sudo`.

- macOS: writes `~/Library/LaunchAgents/dev.firekeeper.daemon.plist` and
  loads it with `launchctl bootstrap gui/$UID`. launchd restarts the daemon
  if it exits with an error, at most every 30 seconds, but not after a
  clean stop. Errors from before the daemon opens its log, such as another
  daemon holding the lock, go to `~/.firekeeper/daemon.stderr.log`.
- Windows: writes `~/.firekeeper/daemon-task.xml` and registers it with
  `schtasks` as the per-user task "Firekeeper Daemon", which starts at your
  logon, runs as you without elevation, and has no console window. Task
  Scheduler restarts the daemon every minute if it exits with an error, but
  not after a clean stop. The task cannot carry environment variables, so the
  daemon gets your user environment as Windows hands it to logon programs:
  variables you set persistently (`setx`, or System Properties) are seen, while
  ones you only exported in a PowerShell session are not. Task Scheduler keeps
  no output, so a failure before the daemon opens `daemon.log` is only visible
  by running `firekeeper daemon` in a terminal with the same flags.
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
| `daemon uninstall [--purge]` | Stop the service and remove its definition. `~/.firekeeper` is left alone unless `--purge` is passed, which deletes the read offsets (`state.json`), the machine id, and the daemon's logs and lock file. |
| `daemon status` | Show whether the service is installed and running, its pid, and its last exit status. Exits 0 when running and 3 otherwise. |
| `daemon logs [-n LINES] [-f]` | Print the last lines of `~/.firekeeper/daemon.log` (default 50); `-f` keeps printing new lines, across rotations, until interrupted. |

`daemon` has no `--token` flag, and `install` never writes a token into a
service file. The daemon reads the ingest token from the config file, where
`firekeeper login` puts it, or from `$FIREKEEPER_TOKEN` when run by hand.

### Configuration

`report`, `backfill`, `daemon`, `login`, `whoami`, `logout`, and
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
scan_ended = true                # report and daemon: also upload ended sessions found on disk
scan_lookback = "24h"            # how far back that first looks
exclude = [
  "~/clients",                   # this directory and everything below it
  "~/work/*-secret",             # globs: * stays within one path segment
  "github.com/acme",             # every repository with an acme remote
  "gitlab.example.com/team/*-private",
]

[redact]
paths = ["~/clients"]            # also scrubbed to [REDACTED:path] in uploads
```

`exclude` entries that start with `/` or `~` are directory globs, matched
against a session's working directory and its Git root and their parents.
Anything else is a Git remote pattern, matched against every `url` in the
repository's `.git/config` after both are reduced to `host/owner/repo`, so
`git@github.com:acme/api.git` and `https://github.com/acme/api` both read as
`github.com/acme/api`. `.firekeeper-ignore` still works as before. Keys that
only the separate dashboard server reads (`prices`, `repo_url_template`,
`trusted_proxies`, and the `[github]` table) are accepted and ignored here, so
one file can serve both programs.

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
| `~/.firekeeper/daemon.log`, `daemon.lock` | `daemon` | log and single-instance lock |
