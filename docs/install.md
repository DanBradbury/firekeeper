# Install and uninstall

Install Firekeeper, then run it beside your existing agent terminals.
No Firekeeper account, server, or background service is required.

## Choose one installation method

These methods install a prebuilt binary; you do not need Go.

### macOS — Homebrew

```sh
brew install DanBradbury/tap/firekeeper
```

### macOS or Linux — install script

```sh
curl -fsSL https://raw.githubusercontent.com/DanBradbury/firekeeper/main/install.sh | sh
```

### Windows — PowerShell 5.1 or 7

```powershell
irm https://raw.githubusercontent.com/DanBradbury/firekeeper/main/install.ps1 | iex
```

The script commands download and execute code. You can inspect
[install.sh](../install.sh) or [install.ps1](../install.ps1) first, or get a
binary from [GitHub Releases](https://github.com/DanBradbury/firekeeper/releases).
See [Windows verification](#verify-before-you-install-windows) for a step-by-step
manual download and checksum check.

## Launch the dashboard

Start a supported coding agent as usual, then open a second terminal and run:

```sh
firekeeper
```

Run it from any directory on the same machine and as the same user as your
agents. The **Processes** view should populate within a couple of seconds.
Use Up/Down to select, `c` to read a conversation, Esc to return, and `?`
for help. Quitting with `q` from a main view leaves your agents running.
See [First run](../README.md#first-run) or the
[missing-session checklist](https://firekeeper.ai/docs/troubleshooting/#missing-session).

### If your shell says `firekeeper: command not found`

The macOS/Linux script installs to `~/.local/bin` by default but does not
change your shell configuration. For the current bash or zsh session:

```sh
export PATH="$HOME/.local/bin:$PATH"
firekeeper --version
```

Add that `export` line to `~/.zshrc` for zsh or `~/.bashrc` for interactive
bash to keep it for new sessions. Other shells need their own PATH syntax;
follow the installer's hint. If you set `FIREKEEPER_INSTALL_DIR`, use that
directory instead.

The Windows script updates the current session and your user PATH. Terminals
already open elsewhere may need to be reopened. If you installed manually or
disabled PATH changes, add the installation directory to your user PATH.

## Install locations and version pinning

The install script puts the binary in `~/.local/bin` (set
`FIREKEEPER_INSTALL_DIR` to change that) and prints a `PATH` hint if that
directory is not on your `PATH`. It never uses `sudo`. It verifies the
download against the release's SHA-256 checksums and installs nothing if they
do not match. Pin a release with
`curl -fsSL https://raw.githubusercontent.com/DanBradbury/firekeeper/main/install.sh | sh -s -- --version vX.Y.Z`.

The PowerShell script installs `firekeeper.exe` to
`%LOCALAPPDATA%\Programs\firekeeper` (set `FIREKEEPER_INSTALL_DIR` to an
absolute path to change that; a relative one is refused), adds that directory to your user `PATH`, and updates the current
session's `PATH` so `firekeeper` works straight away. It needs no administrator
rights, verifies the download against the release's SHA-256 checksums like the
shell script, and installs nothing if they do not match. Piping into `iex`
cannot take parameters, so set `$env:FIREKEEPER_VERSION = 'vX.Y.Z'` first to pin
a release, or save the script and run it with `-Version vX.Y.Z`,
`-InstallDir DIR`, or `-NoPathUpdate` (or set `FIREKEEPER_NO_MODIFY_PATH` to
any value before piping). Downloads made this way carry no
"downloaded from the internet" mark, so SmartScreen does not prompt; the
binaries are not code-signed.

## Verify before you install (Windows)

The one-liner runs whatever `install.ps1` is on `main` at that moment, and its
checksum check compares the zip with `checksums.txt` from the same GitHub
release. That catches a corrupted download, not a compromised repository or
release: a SHA-256 match proves integrity against the release, not who made
it. To read what you run first, pin the script to a release tag. Pick the tag
from the [releases page](https://github.com/DanBradbury/firekeeper/releases):

```powershell
$v = 'v0.8.0'
Invoke-WebRequest "https://raw.githubusercontent.com/DanBradbury/firekeeper/$v/install.ps1" -OutFile install.ps1 -UseBasicParsing
notepad .\install.ps1   # read it before running it
powershell -NoProfile -ExecutionPolicy Bypass -File .\install.ps1 -Version $v
```

A saved script does not run under Windows' default execution policy, so
`-ExecutionPolicy Bypass` applies to that one `powershell` process only. Do not
change the machine or user policy (`Set-ExecutionPolicy Unrestricted`) for it.

Or skip the script: download the zip and `checksums.txt` yourself, compare the
hashes with `Get-FileHash`, and unzip only if they match.

```powershell
$v = 'v0.8.0'
$arch = if ($env:PROCESSOR_ARCHITECTURE -eq 'ARM64') { 'arm64' } else { 'amd64' }
$zip = "firekeeper_$($v.TrimStart('v'))_windows_$arch.zip"
$base = "https://github.com/DanBradbury/firekeeper/releases/download/$v"
Invoke-WebRequest "$base/$zip" -OutFile $zip -UseBasicParsing
Invoke-WebRequest "$base/checksums.txt" -OutFile checksums.txt -UseBasicParsing
$want = ((Get-Content checksums.txt) -match "  $([regex]::Escape($zip))$" -split '\s+')[0]
$got = (Get-FileHash -LiteralPath $zip -Algorithm SHA256).Hash
if (-not $want -or $got -ne $want) { throw "SHA-256 mismatch for $zip; do not use it" }
Expand-Archive -LiteralPath $zip -DestinationPath (Join-Path $env:LOCALAPPDATA 'Programs\firekeeper') -Force
```

The release assets are `firekeeper_<version>_windows_amd64.zip`,
`firekeeper_<version>_windows_arm64.zip`, and `checksums.txt` (for example
`firekeeper_0.8.0_windows_amd64.zip`). The zip holds `firekeeper.exe` with
its license files. Add `%LOCALAPPDATA%\Programs\firekeeper` to your user
`PATH` yourself (Settings, "Edit environment variables for your account"). A zip
downloaded in a browser is marked as from the internet, so Windows may show a
SmartScreen prompt for the unsigned binary.

## Supported platforms and updates

Releases cover macOS (Apple Silicon and Intel), Linux (amd64 and arm64), and
Windows 10 1809+ and 11 (amd64 and arm64, as `.zip` archives). macOS is the main target; Linux
is best effort; Windows is supported with the differences listed under
[Windows](reference.md#windows). Release tags with a suffix, such as
`v0.1.0-rc1`, are prereleases: the install scripts can fetch them with
`--version` or `-Version`, and Homebrew does not.

When you start `firekeeper` in a terminal it checks GitHub for a newer
release (one unauthenticated request per launch) and asks whether to
update. Homebrew installs use `brew upgrade`; others re-run the install script
(`install.ps1` on Windows) for that exact version. Choose "Skip this version" to stop asking about it, or
set `FIREKEEPER_NO_UPDATE_CHECK=1` to turn the check off. Builds from source
never check.

`firekeeper --help` lists the subcommands. `firekeeper --version` (or
`firekeeper version`) prints the version, commit, and build date.

## From source

Building from source requires Go 1.26 or newer. From a repository checkout:

```sh
go build -o firekeeper .
```

Or install with Go:

```sh
go install github.com/DanBradbury/firekeeper@latest
```

Put the binary on your `PATH`, then run `firekeeper`. A build made this way
reports its version as `dev`. For development, use `go run .`. If
[just](https://github.com/casey/just) is installed, `just run`, `just test`,
`just check`, and `just --list` wrap the common commands.

## Uninstall

1. If you installed the daemon, remove the service first, while the binary
   still exists: `firekeeper daemon uninstall`. This removes the launchd agent,
   systemd user service, or, on Windows, the "Firekeeper Daemon" Task
   Scheduler task.
2. Remove the binary: `brew uninstall firekeeper`, or
   `rm ~/.local/bin/firekeeper` (or wherever you installed it). On Windows,
   delete `%LOCALAPPDATA%\Programs\firekeeper` and remove it from your user
   `PATH` (Settings, "Edit environment variables for your account").

Neither step touches `~/.firekeeper`. It holds your configuration
(`config.toml`, which may contain a server token), the daemon's read
offsets (`state.json`), the machine id, and logs and lock files. Run
`firekeeper logout` before uninstalling if you want this machine's token
revoked on the server. To delete all of it:

```sh
rm -rf ~/.firekeeper
```

On Windows, in PowerShell:

```powershell
Remove-Item -LiteralPath (Join-Path $HOME '.firekeeper') -Recurse -Force
```

`-LiteralPath` keeps a profile path with `[` or `]` from being read as a
wildcard pattern, and PowerShell always sets `$HOME`.
