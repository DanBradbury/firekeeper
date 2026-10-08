# Releasing Firekeeper

Releases are built by [GoReleaser](https://goreleaser.com) from
`.goreleaser.yaml` and published by `.github/workflows/release.yml` when a tag
matching `v*` is pushed.

Each release contains:

- `firekeeper_<version>_<os>_<arch>.tar.gz` for `darwin/arm64`,
  `darwin/amd64`, `linux/amd64`, and `linux/arm64`. Each archive holds the
  `firekeeper` binary, `README.md`, and `ASSETS.md` (the artwork attribution
  the CC BY 3.0 license requires). Binaries are built with `CGO_ENABLED=0`.
- `firekeeper_<version>_checksums.txt` with the SHA-256 of every archive.
  `install.sh` refuses to install an archive whose checksum does not match.
- A Homebrew cask pushed to
  [`DanBradbury/homebrew-tap`](https://github.com/DanBradbury/homebrew-tap)
  as `Casks/firekeeper.rb`. Homebrew quarantines cask downloads and the
  binary is not notarized, so the cask's `postflight` hook removes the
  `com.apple.quarantine` attribute; without it macOS would show a Gatekeeper
  prompt on first launch. `brew uninstall --zap firekeeper` also deletes
  `~/.firekeeper`.

`<version>` is the tag without its leading `v`. The binary reports it through
`firekeeper --version`, along with the commit and build date, and the
reporter sends it to the dashboard as `machine.version`.

## One-time setup

These steps need repository-owner access and cannot be done from a pull
request.

1. **Create the tap repository.** Create a public repository named
   `DanBradbury/homebrew-tap` with a `main` branch (initialize it with a
   README). Homebrew maps `brew install DanBradbury/tap/firekeeper` to this
   repository by name, so the name must be exactly `homebrew-tap`. GoReleaser
   creates `Casks/firekeeper.rb` on the first release.
2. **Create a token for the tap.** Create a fine-grained personal access
   token (GitHub → Settings → Developer settings → Personal access tokens →
   Fine-grained tokens) with:
   - Resource owner: `DanBradbury`
   - Repository access: only `DanBradbury/homebrew-tap`
   - Repository permissions: **Contents: Read and write** (Metadata: Read is
     added automatically)
   - An expiry you are willing to renew; an expired token makes the release
     job fail at the Homebrew step after the GitHub release is published.
3. **Store the token as a secret.** In `DanBradbury/firekeeper` → Settings →
   Secrets and variables → Actions, add a repository secret named
   `HOMEBREW_TAP_GITHUB_TOKEN` with the token from step 2.
4. **Allow the workflow to create releases.** In `DanBradbury/firekeeper` →
   Settings → Actions → General → Workflow permissions, make sure workflows
   may use read and write permissions, or leave the default and rely on the
   `permissions: contents: write` block in `release.yml`. Either way, no
   other secret is needed: the release itself uses the built-in
   `GITHUB_TOKEN`.
5. **Choose a license.** The repository has no license file yet. Pick one and
   add `LICENSE` before the first public release; then add it to the
   `archives.files` list in `.goreleaser.yaml`.

## Cutting a release

1. Make sure `main` is green in CI. CI runs `goreleaser release --snapshot
   --clean` on every pull request, so a broken release config fails there
   first.
2. Tag and push from an up-to-date `main`:

   ```sh
   git switch main && git pull
   git tag -a v0.1.0 -m "Firekeeper v0.1.0"
   git push origin v0.1.0
   ```

3. Watch the **Release** workflow. It runs the tests, builds every target,
   publishes the GitHub release with the archives and checksums, and pushes
   the cask to the tap.
4. Check both install paths on a Mac (see below).

### Prereleases

Tags with a prerelease suffix, such as `v0.1.0-rc1`, publish as GitHub
prereleases. The Homebrew cask is not updated for a prerelease, and the
installer's default "latest" lookup skips prereleases, so only people who ask
for one get it:

```sh
curl -fsSL https://raw.githubusercontent.com/DanBradbury/firekeeper/main/install.sh | sh -s -- --version v0.1.0-rc1
```

### If a release fails

- **Fails before the GitHub release is published:** fix the problem on
  `main`, delete the tag (`git push --delete origin v0.1.0` and
  `git tag -d v0.1.0`), and tag again.
- **GitHub release published but the Homebrew step failed** (usually an
  expired or missing `HOMEBREW_TAP_GITHUB_TOKEN`): fix the secret, delete
  the GitHub release and the tag, and tag again; or cut the next patch
  version.

## Testing a release locally

With GoReleaser v2 installed:

```sh
goreleaser check
goreleaser release --snapshot --clean
./dist/firekeeper_darwin_arm64_v8.0/firekeeper --version   # on Apple Silicon
```

Snapshot builds are versioned `<next patch>-snapshot-<commit>` and never
publish anything. `dist/` is ignored by Git.

`install.sh` can install from a local directory laid out like GitHub release
downloads, which is how its tests run without the network:

```sh
mkdir -p /tmp/rel/download/v0.0.1-snapshot-abc1234
cp dist/*.tar.gz dist/*_checksums.txt /tmp/rel/download/v0.0.1-snapshot-abc1234/
FIREKEEPER_DOWNLOAD_URL=file:///tmp/rel/download FIREKEEPER_INSTALL_DIR=/tmp/rel/bin \
  sh install.sh --version v0.0.1-snapshot-abc1234
```

`FIREKEEPER_RELEASES_API` similarly overrides the URL used to look up the
latest release tag (default
`https://api.github.com/repos/DanBradbury/firekeeper/releases/latest`).

## Checking a release on a Mac

On an Apple Silicon Mac, after each release:

```sh
brew install DanBradbury/tap/firekeeper
firekeeper --version
firekeeper daemon install --provider codex --dry-run
brew uninstall firekeeper

curl -fsSL https://raw.githubusercontent.com/DanBradbury/firekeeper/main/install.sh | sh
~/.local/bin/firekeeper --version
~/.local/bin/firekeeper daemon install --provider codex --dry-run
```

`firekeeper --version` must print the tagged version, not `dev`, and the
Homebrew-installed binary must start without a Gatekeeper prompt (check
with `xattr /opt/homebrew/bin/firekeeper`, which should list no
`com.apple.quarantine`). Drop
`--dry-run` to install the LaunchAgent for real, then remove it with
`firekeeper daemon uninstall`.
