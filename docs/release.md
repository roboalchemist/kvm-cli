# Release & Distribution

How `kvm-cli` is built, released, and distributed.

## TL;DR

```bash
git checkout main && git pull
git tag -a v0.4.0 -m "kvm-cli v0.4.0"
git push origin v0.4.0
# CI runs GoReleaser: builds archives, creates the GitHub release, updates the tap.
brew update && brew upgrade kvm-cli
```

## Architecture

| Concern | Mechanism | File |
|---------|-----------|------|
| Cross-platform binaries + GitHub release | GoReleaser | `.goreleaser.yml` |
| Release trigger (tag → build/publish) | GitHub Actions | `.github/workflows/release.yml` |
| CI (build, vet, test, secret scan) | GitHub Actions | `.github/workflows/ci.yml` |
| Homebrew formula | GoReleaser `brews` | `roboalchemist/homebrew-tap` |

Release flow:

```
git tag vX.Y.Z ──push──▶ .github/workflows/release.yml
                              │  actions/setup-go
                              │  goreleaser release --clean
                              ▼
                    ┌─────────┴──────────┐
                    ▼                    ▼
              darwin/linux         GitHub release
              amd64+arm64          + archives + checksums
                                   + Homebrew formula
```

## Required secret

To publish the Homebrew formula, GoReleaser needs a token with `repo` scope:

```bash
gh secret set HOMEBREW_TAP_TOKEN --repo roboalchemist/kvm-cli --body "$(gh auth token)"
```

Without it, run `goreleaser release --clean --skip=homebrew` to publish binaries only.

## Installing

```bash
brew tap roboalchemist/tap && brew install kvm-cli
# or
go install github.com/roboalchemist/kvm-cli@latest
```

## Semantic versioning

- `vMAJOR.MINOR.PATCH`; breaking changes bump MAJOR (MINOR while pre-1.0).
- Prereleases (`v0.5.0-rc1`) are marked automatically (`prerelease: auto`).
