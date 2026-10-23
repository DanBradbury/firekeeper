# Firekeeper

**Tend every coding agent from one bonfire.**

[![CI](https://github.com/DanBradbury/firekeeper/actions/workflows/ci.yml/badge.svg)](https://github.com/DanBradbury/firekeeper/actions/workflows/ci.yml)
[![License: Apache 2.0](https://img.shields.io/badge/license-Apache%202.0-blue.svg)](LICENSE)

Firekeeper is a keyboard-driven terminal dashboard for the coding agents
running on your machine. It shows what Codex, GitHub Copilot CLI, Claude Code,
Kimi Code, OpenCode, Gemini CLI, and Cursor CLI are doing right now: which
sessions are working or waiting for you, which model and project they use, and
how many tokens they have spent.

It runs on your hardware and changes nothing about how you launch your
agents. Nothing starts and nothing is uploaded unless you ask.

![Firekeeper terminal dashboard showing four agent sessions across Claude, Codex, Copilot, and OpenCode](docs/firekeeper-tui.jpg)

## Why

If you use more than one coding agent, each keeps its history in its own
format in its own folder. Simple questions get hard. What was that agent doing
when it stopped and waited for you? Which project is using the most tokens?
What did it change?

Firekeeper answers those from one place, and lets you take the history
elsewhere if you want to.

## Install

```sh
brew install DanBradbury/tap/firekeeper
```

or

```sh
curl -fsSL https://raw.githubusercontent.com/DanBradbury/firekeeper/main/install.sh | sh
```

Both install a prebuilt binary, so you do not need a Go toolchain. Firekeeper
is two binaries: `firekeeper`, the terminal dashboard and reporter, and
`firekeeper-server`, the optional [dashboard server](#the-dashboard-server). The
Homebrew formula installs both. The install script installs only `firekeeper`;
`firekeeper serve` offers to download the server later if you want it. Then run:

```sh
firekeeper
```

Releases cover macOS (Apple Silicon and Intel) and Linux (amd64 and arm64).
macOS is the main target; Linux is best effort. See [Install and
uninstall](docs/install.md) for pinning a version, building from source, and
removing Firekeeper.

## What you get

- **See what is running.** Agent processes are grouped with their child
  processes, with state, model, working directory, Git branch, and tokens.
  On macOS, press `s` to jump to the terminal that holds a session.
- **Understand usage.** A Usage tab shows token history by model, and Codex
  limits, read from local files.
- **Read what happened.** Press `c` on a session to read its prompts and
  replies in the terminal, read-only, following new messages live.
- **Take your history with you.** `firekeeper snapshot --json` prints the
  sessions it finds, and you can optionally send redacted transcripts to a
  server (see below).

Start an agent in another terminal and it appears within a couple of seconds.
Press `?` for every key.

## Supported agents

| Agent | Live dashboard | Transcript upload |
| --- | --- | --- |
| Claude Code | Yes | Yes |
| Codex | Yes | Yes |
| GitHub Copilot CLI | Yes | Yes |
| Kimi Code | Yes | Yes |
| OpenCode | Yes | Yes |
| Gemini CLI | Yes | Yes |
| Cursor CLI | Yes | Yes |

States describe the latest event Firekeeper can see, not ground truth, and
agent CLIs can change their on-disk formats between releases. The
[reference](docs/reference.md#supported-providers) lists exactly what is read
for each one, and what each version needs.

## Private by default

- Plain `firekeeper` uploads no session data and starts no daemon. Two lookups
  in the Usage tab do contact other services; the
  [reference](docs/reference.md#terminal-dashboard) names them.
- Uploading is a separate set of commands that you run on purpose, and it is
  opt-in per agent. `firekeeper report --dry-run` shows what would be sent
  without opening a connection.
- Every event is redacted on your machine before it leaves. Redaction is best
  effort, not a guarantee. [What is uploaded and what is not
  caught](docs/reference.md#what-is-uploaded-and-what-is-not-caught) says
  what it misses.
- A repository containing a `.firekeeper-ignore` file is never read.
- Whoever runs the server you upload to can read what you send. With a server
  you run yourself, that is you.

The code that reads your sessions is in this repository, so you can check all
of this. [PRIVACY.md](PRIVACY.md) describes what the client reads, writes,
and sends.

## Send sessions to a server (optional)

```sh
firekeeper backfill --all --dry-run   # preview importing your existing history
firekeeper login --server https://dash.example   # link this machine in the browser
firekeeper daemon install             # report in the background, starting at login
```

`report`, `backfill`, `daemon`, and `login` are documented in the
[reference](docs/reference.md#reporting).

### The dashboard server

A web dashboard for browsing, searching, and charting uploaded sessions is a
separate commercial product, `firekeeper-server`, made by Lucky Daisy LLC. It is
not part of this repository. **It is free to run on your own machine**: with no
license key, `firekeeper serve` starts a dashboard for you alone on this
computer, with every feature, so you can browse your sessions outside the
terminal and see what it does. Running it for a team, on other machines or as a
hosted service needs a license key; to get one, or a free evaluation key, email
dan.luckydaisy@gmail.com.

`firekeeper serve [args]` runs `firekeeper-server serve [args]` in its place,
with the same arguments, environment, and exit status. It looks in
`$FIREKEEPER_SERVER_BIN`, then next to the `firekeeper` executable, then on your
`PATH`, then in `~/.local/bin`.

If it finds nothing, `firekeeper serve` offers to download the server for you.
It asks `[y/N]` first, only in an interactive terminal, and downloads nothing
unless you answer `y`. On `y` it fetches the latest release from the public
`firekeeper-server-releases` repository, checks it against the published
checksums, installs it, and starts it. With no license key it starts the free
local dashboard; for shared use it reads a key from `--license FILE`,
`FIREKEEPER_LICENSE`, `FIREKEEPER_LICENSE_FILE` or `~/.firekeeper/license.key`.
When you already have the server, `firekeeper serve` checks for a newer
release the way the client does for itself and offers `1) Update now`,
`2) Not now` or `3) Skip this version`. An update that your license's update
period does not cover is undone and the version you had is kept. Set
`FIREKEEPER_NO_PROMPT=1` to never be asked about installing, and
`FIREKEEPER_NO_UPDATE_CHECK=1` to stop the update check as well. That prompt is the only place
Firekeeper downloads a binary; plain `firekeeper` uploads nothing and starts no
daemon, and neither `report`, `daemon` nor anything else fetches software. See
[Installing the server from `serve`](docs/reference.md#installing-the-server-from-serve).

The protocol is open. The reporter speaks the HTTP API in
[docs/api.md](docs/api.md) and the event format in
[docs/event.schema.json](docs/event.schema.json), so other tools can accept
sessions too. The Go packages in [pkg/](pkg/README.md) (`event`, `redact`, and
`contract`, which carries the schema and example API bodies with validators)
are the stable, importable pieces of that contract.

## Documentation

- [Install and uninstall](docs/install.md)
- [Reference](docs/reference.md): terminal dashboard, reporting, the daemon,
  and configuration
- [HTTP API](docs/api.md) and [event schema](docs/event.schema.json)
- [PRIVACY.md](PRIVACY.md)

## Status

Firekeeper is an early-stage project. macOS is the main target, Linux is best
effort, and Windows has not been tried. Expect adapters and metadata handling
to evolve as the agent CLIs change. Bug reports and requests for more agents
are welcome as [issues](https://github.com/DanBradbury/firekeeper/issues).

## License

Apache License 2.0. See [LICENSE](LICENSE), [NOTICE](NOTICE), and
[THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).

That covers `firekeeper`. `firekeeper-server` is a separate commercial product
with its own license and notices.
