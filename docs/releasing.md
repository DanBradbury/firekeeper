# Releasing

Releases are built by GoReleaser (`.goreleaser.yaml`) and published by
`.github/workflows/release.yml` when a `v*` tag is pushed. Pull requests run
`goreleaser release --snapshot --clean` in CI, so a broken release config is
caught before tagging.

Each release has `firekeeper_<version>_<os>_<arch>.tar.gz` archives for
darwin and linux on amd64 and arm64 (`CGO_ENABLED=0`), plus `checksums.txt`
(SHA-256). `install.sh` and the Homebrew formula both use them.

## One-time setup (repository owner)

The Homebrew tap needs a repository and a secret that only the owner can
create.

1. Create the public repository `DanBradbury/homebrew-tap`. Give it an initial
   commit (a README is enough) so it has a default branch. GoReleaser writes
   `Formula/firekeeper.rb` into it.
2. Create a fine-grained personal access token at
   <https://github.com/settings/personal-access-tokens/new>:
   - Resource owner: `DanBradbury`.
   - Repository access: only `DanBradbury/homebrew-tap`.
   - Permissions: **Contents: Read and write**. Nothing else.
   - Set an expiry and note it; the release will fail to update the tap after
     that date.
3. Add it to this repository as an Actions secret named
   `HOMEBREW_TAP_GITHUB_TOKEN`: Settings, Secrets and variables, Actions,
   New repository secret. (`gh secret set HOMEBREW_TAP_GITHUB_TOKEN --repo DanBradbury/firekeeper`)
4. Confirm Actions has permission to create releases. The workflow requests
   `contents: write` itself, so the default setting is enough.

Until steps 1 to 3 are done, a tagged release still publishes the GitHub
release but the Homebrew step fails. Use a `-rc` tag first (below), which
skips the tap, to check the rest.

## Cutting a release

```sh
git checkout main && git pull
git tag v0.1.0
git push origin v0.1.0
```

- The `Release` workflow publishes the GitHub release with archives,
  `checksums.txt`, and a generated changelog, and pushes the formula to the
  tap.
- A tag with a suffix, such as `v0.1.0-rc1`, is published as a GitHub
  **prerelease** and does not update the tap.
- The `Publish and deploy` workflow (`deploy.yml`) also triggers on `v*` tags
  and builds and deploys the container image. It is separate from this one.
  Push a tag only when you want both.

After the tag builds, check from a clean shell:

```sh
brew install DanBradbury/tap/firekeeper
firekeeper --version
curl -fsSL https://raw.githubusercontent.com/DanBradbury/firekeeper/main/install.sh | sh
```

## Trying the release config locally

```sh
brew install goreleaser
goreleaser release --snapshot --clean
./dist/firekeeper_darwin_arm64_v8.0/firekeeper --version
```

Snapshot builds do not publish or push anything. `dist/` is git-ignored.

## Notes

- GoReleaser reports that `brews` is deprecated in favor of `homebrew_casks`.
  It is used on purpose: a formula avoids the macOS Gatekeeper quarantine
  prompt that an unsigned cask binary triggers. It still works in GoReleaser 2.
- Version, commit, and build date are set with
  `-ldflags -X github.com/DanBradbury/firekeeper/internal/version.Version=...`
  (and `Commit`, `Date`). `go build` and `go install` leave them at `dev`. The
  linker silently ignores an `-X` path that names no variable, so CI's snapshot
  job fails if the built binary still reports `dev`.
- `scripts/test-install.sh` tests `install.sh` against a fake local release
  directory, with no network. Run it and `shellcheck install.sh
  scripts/test-install.sh` after changing the installer.
