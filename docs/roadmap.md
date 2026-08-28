# Roadmap

Things that are deliberately not done yet, in rough priority order. Open an
issue if one of these matters to you.

## Distribution

- [ ] **Homebrew tap.** `.goreleaser.yaml` already has a `homebrew_casks`
      block pointing at `orron/homebrew-tap`, and `release.yml` expects a
      `HOMEBREW_TAP_GITHUB_TOKEN` secret. Neither the tap repo nor the secret
      exists yet, so a tagged release would fail at the publish step. To
      enable: create `github.com/orron/homebrew-tap`, add a fine-grained token
      with `contents: write` on it as the `HOMEBREW_TAP_GITHUB_TOKEN` repo
      secret, then tag. Until then install with
      `go install github.com/orron/pano/cmd/pano@latest`.
- [ ] First tagged release (`v0.1.0`) via goreleaser once the above is settled
      (or the `homebrew_casks` block is removed for the first release).

## Platforms

- [ ] Linux: native `pano on/off` and `pano ca install` (system proxy and
      trust-store integration); today they print manual instructions.
- [ ] Windows: unsupported.
