Detailed installation notes for Firekeeper. For the short version, see the [README](../README.md#install).

# Install and uninstall

```sh
brew install DanBradbury/tap/firekeeper
curl -fsSL https://raw.githubusercontent.com/DanBradbury/firekeeper/main/install.sh | sh
```

Use either one, then run `firekeeper`. Both install a prebuilt binary, so no Go
toolchain is needed. Check it with `firekeeper --version`.

Firekeeper is two binaries. `firekeeper` is built from this repository.
`firekeeper-server`, the optional dashboard server, is built and released from
a separate repository to `DanBradbury/firekeeper-server-releases`. The Homebrew
formula for `firekeeper` depends on the `firekeeper-server` formula in the same
tap, so `brew install DanBradbury/tap/firekeeper` installs both. Installing the
server does not start it, and plain `firekeeper` never calls it. The install
script installs only `firekeeper`; when you run `firekeeper serve` without a
server it offers to download one (see [Installing the server from
`serve`](reference.md#installing-the-server-from-serve)).

The install script puts the binary in `~/.local/bin` (set
`FIREKEEPER_INSTALL_DIR` to change that) and prints a `PATH` hint if that
directory is not on your `PATH`. It never uses `sudo`. It verifies the
download against the release's SHA-256 checksums and installs nothing if they
do not match. Pin a release with
`curl -fsSL https://raw.githubusercontent.com/DanBradbury/firekeeper/main/install.sh | sh -s -- --version vX.Y.Z`.

Releases cover macOS (Apple Silicon and Intel) and Linux (amd64 and arm64).
macOS is the main target; Linux is best effort. Release tags with a suffix,
such as `v0.1.0-rc1`, are prereleases: the install script can fetch them with
`--version`, and Homebrew does not.

When you start `firekeeper` in a terminal it checks GitHub for a newer
release (one unauthenticated request per launch) and asks whether to
update. Homebrew installs use `brew upgrade`; others re-run the install script
for that exact version. Choose "Skip this version" to stop asking about it, or
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
   still exists: `firekeeper daemon uninstall`.
2. Remove the binaries: `brew uninstall firekeeper firekeeper-server`, or
   `rm ~/.local/bin/firekeeper ~/.local/bin/firekeeper-server` (or wherever you
   installed them). Homebrew will not remove `firekeeper-server` on its own
   while it is installed as a dependency; run `brew autoremove` or name it as
   above.

Neither step touches `~/.firekeeper`. It holds your configuration
(`config.toml`, which may contain a server token), the daemon's read
offsets (`state.json`), the machine id, and logs and lock files. Run
`firekeeper logout` before uninstalling if you want this machine's token
revoked on the server. To delete all of it:

```sh
rm -rf ~/.firekeeper
```
