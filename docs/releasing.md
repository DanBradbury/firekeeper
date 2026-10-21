# Releasing Firekeeper

This is the maintainer's guide to cutting a release of the open client. The
commercial server has its own pipeline and is not covered here.

## Tag rules

These are hard rules.

- A tag is permanent. Never move, delete and re-push, or reuse a tag, even if
  the release is bad.
- The original project used this same Go module path
  (`github.com/DanBradbury/firekeeper`) and published `v0.0.1` to `v0.0.9`,
  `v0.1.0`, `v0.1.0-rc1`, `v0.1.1` and `v0.2.1` to `v0.2.14`. The Go module
  proxy and checksum database remember the first content they saw for each of
  those names, so none of them can ever be reused for this repository (`v0.0.1`
  already caused a checksum mismatch).
- Every new tag must be higher than the current highest tag, `v0.4.0`.
- Tags with a suffix, such as `v0.5.0-rc1`, publish as GitHub prereleases and
  do not update the Homebrew tap.

## Before tagging

1. Make sure `main` has the commit you want to release and that CI is green on
   it (`ci.yml`, including the GoReleaser snapshot job, and `security.yml`).
2. Check that `internal/serverinstall.ReleasesURL` is still not set in
   `.goreleaser.yaml` (see "Switching the server install prompt on" below).
3. Pick the next version above `v0.4.0` and check it is unused:
   `git ls-remote --tags origin 'refs/tags/vX.Y.Z'` prints nothing.

## Cutting the release

```sh
git checkout main && git pull --ff-only
git tag -a vX.Y.Z -m "vX.Y.Z"
git push origin vX.Y.Z
```

Pushing a `v*` tag starts `.github/workflows/release.yml`. It runs GoReleaser
(`release --clean`) with `GORELEASER_CURRENT_TAG` set to the pushed tag, which:

- builds `firekeeper` for darwin and linux, amd64 and arm64, with the version,
  commit and date injected through `-ldflags`;
- publishes a GitHub release with one `firekeeper_<version>_<os>_<arch>.tar.gz`
  per platform and a `checksums.txt` (SHA-256), plus a changelog from GitHub;
- for a non-prerelease tag, pushes the `firekeeper` formula to
  `DanBradbury/homebrew-tap` under `Formula/`.

## Secrets and settings

Set these once under the repository's Settings > Secrets and variables >
Actions.

| Secret | Used by | Notes |
| --- | --- | --- |
| `HOMEBREW_TAP_GITHUB_TOKEN` | `release.yml` | A fine-grained personal access token limited to the `DanBradbury/homebrew-tap` repository with Contents: read and write, and nothing else. The built-in `GITHUB_TOKEN` cannot push to another repository. |
| `CLOUDFLARE_API_TOKEN` | `landing.yml` | A token with the Account > Cloudflare Pages > Edit permission. |
| `CLOUDFLARE_ACCOUNT_ID` | `landing.yml` | The Cloudflare account that owns the `usefirekeeper` Pages project. |

The two Cloudflare secrets only deploy the landing page; releases do not need
them. The `firekeeper` formula and the future `firekeeper-server` formula are
independent files in the same tap. The free client never depends on the
commercial server.

## Verifying a release

1. Watch the Release workflow finish. Open the release page and check that it
   lists four archives and `checksums.txt`.
2. Check a download against the checksums:

   ```sh
   sha256sum -c checksums.txt --ignore-missing   # Linux
   shasum -a 256 -c checksums.txt --ignore-missing   # macOS
   ```

3. Confirm the tap picked it up and the binary reports the new version:

   ```sh
   brew update && brew upgrade firekeeper
   firekeeper --version
   ```

   A version of `dev` means the `-X` paths in `.goreleaser.yaml` are wrong.

## If a release is bad

Do not move or delete the tag and do not reuse its number. Fix the problem on
`main`, wait for green CI, and cut the next patch tag (for example `v0.4.1`
after a bad `v0.4.0`). If the bad release must not be installed in the
meantime, mark it as a prerelease or delete only the GitHub release page, and
revert the tap formula commit in `DanBradbury/homebrew-tap`. The tag itself
stays.

## Switching the server install prompt on

`firekeeper serve` can offer to download `firekeeper-server` (see
[Installing the server from `serve`](reference.md#installing-the-server-from-serve)).
That prompt ships disabled: `serverinstall.ReleasesURL` is empty and release
builds must not set it. Do this only after the server pipeline has published a
release to `DanBradbury/firekeeper-server-releases` in the layout described in
[Release layout contract](reference.md#release-layout-contract), and after
license checks exist in the server.

Then, in a normal pull request:

1. In `.goreleaser.yaml`, add this line to the build's `ldflags` (the
   commented TODO there shows the spot):

   ```yaml
   - -X github.com/DanBradbury/firekeeper/internal/serverinstall.ReleasesURL=https://github.com/DanBradbury/firekeeper-server-releases
   ```

   The value is `serverinstall.ProductionBase`.
2. Update the README and `docs/reference.md` sentences that say the prompt is
   off, and the "Releases and tags" section of `AGENTS.md`.
3. Test with a snapshot build, then release as above with a new tag.

Until then, `FIREKEEPER_SERVER_RELEASES_URL` is the only way to enable it.
