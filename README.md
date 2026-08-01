# Firekeeper

**Tend every coding agent from one bonfire.**

Firekeeper is a terminal dashboard for developers working across multiple AI
coding harnesses. It brings sessions, process state, model usage, and quotas
into one local view without changing how those tools are launched.

Current support includes Codex, GitHub Copilot CLI, and OpenCode process
discovery, with detailed local session state and metadata for Codex and Copilot.
More harnesses and providers are planned.

## Highlights

- Discover local coding-agent processes automatically.
- Group parent and child processes into one runtime.
- See whether Codex and Copilot sessions are active, waiting, or need input.
- Inspect session metadata, working directory, model, Git branch, and tokens.
- View Codex limits, reset times, recent token usage, and active models.
- Jump to a selected agent terminal session on macOS.
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

## Using Firekeeper

Firekeeper opens on an animated camp scene. Press Tab to move between three
views:

- **Animation** — home scene and RPG-style command menu.
- **Processes** — running agent harnesses and their session details.
- **Usage** — Codex quotas plus Codex and Copilot CLI token history and model usage.

### Global controls

| Key | Action |
| --- | --- |
| Tab / Shift+Tab | Switch views |
| `q` / Ctrl+C | Quit |

### Home and menu

| Key | Action |
| --- | --- |
| `M` | Open or close menu |
| Arrow keys / `HJKL` | Navigate |
| Enter on **STATUS** | Show active Codex sessions |
| Esc / Backspace | Return or close menu |
| Left / Right | Cycle character animations when menu is closed |
| Up / Down | Change animation speed when menu is closed |
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

Terminal switching currently supports Terminal.app and iTerm2 on macOS. macOS
may request Automation permission on first use. Sessions without a controlling
TTY cannot be selected this way.

### Usage

| Key | Action |
| --- | --- |
| Left / Right or `H` / `L` | Switch between Codex and Copilot |
| `r` | Refresh selected provider |

Codex usage requires an authenticated Codex CLI installation. Firekeeper makes
a short-lived local stdio request to Codex when this view opens; it does not
require a persistent app server or changes to existing Codex commands.
Copilot usage comes from the local read-only `session-store.db`; account
allowance and remaining quota stay in GitHub billing because GitHub does not
provide an equivalent individual quota source through Copilot CLI.

## Rendering

Firekeeper preserves source pixel dimensions and uses nearest-neighbor scaling
for crisp terminal artwork.

```sh
firekeeper --renderer auto
firekeeper --renderer kitty --sprite-cols 16 --sprite-rows 8
firekeeper --renderer blocks --sprite-cols 16 --sprite-rows 8
```

`auto` selects Kitty graphics in compatible terminals and falls back to
portable true-color half blocks elsewhere. Kitty, Ghostty, WezTerm, Konsole,
Warp, iTerm2, and current Windows Terminal versions provide best results. Try
running outside tmux first when testing Kitty rendering.

## How session discovery works

Firekeeper scans local processes every two seconds and collapses related
processes into runtime groups. For Codex, it locates open rollout files and
queries `~/.codex/state_5.sqlite` read-only for available session metadata. For
Copilot CLI, it maps runtime PIDs to `~/.copilot/session-state` and reads
`workspace.yaml`, `events.jsonl`, and `session-store.db` without modifying them.
`CODEX_HOME` and `COPILOT_HOME` overrides are honored. Provider events supply
best-effort `ACTIVE`, `WAITING`, and `NEEDS INPUT` states. OpenCode currently
exposes process information without provider-specific session metadata.

All monitoring stays local. Firekeeper does not proxy prompts or replace agent
clients.

## Development demos

Run the layered forest scene:

```sh
go run . --demo forest
```

Press `L`, Tab, or arrow keys to cycle its layers. Press Space to pause the
campfire. Export a static preview with:

```sh
go run . --demo forest --forest-output forest-demo.png
```

Run the standalone Kitty graphics example:

```sh
go run ./cmd/kitty-demo
```

## Status

Firekeeper is an early-stage macOS-focused project. Process discovery works
without integrations, but provider internals can change between CLI releases.
Expect adapters and metadata handling to evolve.

## Artwork

Pixel artwork comes from Calciumtrice under CC BY 3.0. See
[ASSETS.md](ASSETS.md) for attribution and source links.
