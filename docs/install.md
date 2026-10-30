Detailed installation notes for Firekeeper. For the short version, see the [README](../README.md#install).

# Install and uninstall

```sh
brew install DanBradbury/tap/firekeeper
curl -fsSL https://raw.githubusercontent.com/DanBradbury/firekeeper/main/install.sh | sh
```

On Windows, in PowerShell (Windows PowerShell 5.1 or PowerShell 7):

```powershell
irm https://raw.githubusercontent.com/DanBradbury/firekeeper/main/install.ps1 | iex
```

Use whichever fits your system, then run `firekeeper`. Each installs a prebuilt binary, so no Go
toolchain is needed. Check it with `firekeeper --version`.

The install script puts the binary in `~/.local/bin` (set
`FIREKEEPER_INSTALL_DIR` to change that) and prints a `PATH` hint if that
directory is not on your `PATH`. It never uses `sudo`. It verifies the
download against the release's SHA-256 checksums and installs nothing if they
do not match. Pin a release with
`curl -fsSL https://raw.githubusercontent.com/DanBradbury/firekeeper/main/install.sh | sh -s -- --version vX.Y.Z`.

The PowerShell script installs `firekeeper.exe` to
`%LOCALAPPDATA%\Programs\firekeeper` (set `FIREKEEPER_INSTALL_DIR` to change
that), adds that directory to your user `PATH`, and updates the current
session's `PATH` so `firekeeper` works straight away. It needs no administrator
rights, verifies the download against the release's SHA-256 checksums like the
shell script, and installs nothing if they do not match. Piping into `iex`
cannot take parameters, so set `$env:FIREKEEPER_VERSION = 'vX.Y.Z'` first to pin
a release, or save the script and run it with `-Version vX.Y.Z`,
`-InstallDir DIR`, or `-NoPathUpdate` (or set `FIREKEEPER_NO_MODIFY_PATH` to
any value before piping). Downloads made this way carry no
"downloaded from the internet" mark, so SmartScreen does not prompt; the
binaries are not code-signed.

Releases cover macOS (Apple Silicon and Intel), Linux (amd64 and arm64), and
Windows (amd64 and arm64, as `.zip` archives). macOS is the main target; Linux
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
Remove-Item -Recurse -Force "$env:USERPROFILE\.firekeeper"
```
