# Releasing

Releases are cut by tagging `main`; GitHub Actions (`.github/workflows/release.yml`)
runs [GoReleaser](https://goreleaser.com) and publishes everything. Nothing is built
on a laptop.

## What a release produces

- **GitHub Release** `vX.Y.Z` with `pano_X.Y.Z_{darwin,linux}_{arm64,amd64}.tar.gz`
  (binary + LICENSE, NOTICE, README, CHANGELOG), `checksums.txt` (SHA-256) and an
  SBOM per archive. Notes are grouped from Conventional Commit prefixes
  (`feat` → Features, `fix` → Fixes). `0.x` tags are marked *pre-release*.
- **Homebrew cask** pushed to [`orron/homebrew-tap`](https://github.com/OrRon/homebrew-tap):
  `brew install orron/tap/pano`, `brew upgrade pano`. The cask clears the
  quarantine bit after install (pano is not notarized yet), runs `pano off`
  before uninstall/upgrade, and `--zap` trashes `~/.pano`.
- `go install github.com/orron/pano/cmd/pano@latest` resolves the same tag on
  its own; nothing to do.

## One-time setup (already done unless the tap is missing)

1. Create the public repository `OrRon/homebrew-tap` (empty, or with a README).
2. Create a fine-grained personal access token scoped to that one repository
   with **Contents: read and write**.
3. Add it to `OrRon/pano` → Settings → Secrets → Actions as
   `HOMEBREW_TAP_GITHUB_TOKEN`. The default `GITHUB_TOKEN` cannot push to another
   repository, which is why the cask stanza names this secret explicitly.

## Cutting a release

```sh
git switch main && git pull
make test lint                      # green, 0 issues
make release-check                  # goreleaser validates .goreleaser.yaml
# CHANGELOG.md: move [Unreleased] under a new "## [X.Y.Z] - YYYY-MM-DD" heading
git commit -am "chore(release): vX.Y.Z"
git tag -a vX.Y.Z -m "pano vX.Y.Z"
git push origin main vX.Y.Z         # the tag push triggers the release workflow
```

`make release-snapshot` builds the archives locally into `dist/` without
publishing (needs `goreleaser` on PATH; `brew install goreleaser`).

## After the workflow is green

```sh
brew update && brew install orron/tap/pano     # or brew upgrade pano
pano version --check                           # "✓ up to date"
pano on                                        # Gatekeeper must not complain
```

Then watch the update notice appear for anyone on the previous version — it
checks once a day, so give it a day before assuming it is broken.

## Versioning

Semantic versioning; `0.x` is the beta line and may break the config file,
the control API (`api.Version`) or the store schema between minors — each such
break gets a CHANGELOG entry under **Changed** and, if it needs user action,
a `pano doctor` check. The update notice, the cask and `go install` all
compare tags, so never reuse or move one.
