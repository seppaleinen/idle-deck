# Release Process

How idle-deck releases are cut, built, and published.

## Overview

| Artifact | How it's built | Where it's published |
|---|---|---|
| Static binary (multi-platform) | `release.yml` on tag push | GitHub Releases |
| Docker image (multi-arch) | `ci.yml` on tag push | GHCR |
| Homebrew formula | `release.yml` updates tap | `seppaleinen/homebrew-idle-deck` |
| `go install` | Built from source on user machine | N/A (module proxy) |

---

## Triggering a release

1. Ensure `main` is green (CI passes)
2. Update version in `main.go` if needed:
   ```go
   const version = "v0.1.0"
   ```
3. Create and push an annotated tag:
   ```bash
   git tag -a v0.1.0 -m "Release v0.1.0"
   git push origin v0.1.0
   ```
   **Do not** use `git tag v0.1.0` (lightweight). The release workflow expects annotated tags.

---

## What happens on tag push

### 1. CI workflow (`ci.yml`) — Docker images

- Builds multi-arch image (linux/amd64, linux/arm64)
- Pushes to GHCR with **two tags**:
  - Version tag: `ghcr.io/seppaleinen/idle-deck:0.1.0`
  - `latest` tag: `ghcr.io/seppaleinen/idle-deck:latest`
- Uses GitHub Actions cache for fast builds

### 2. Release workflow (`release.yml`) — Binaries + Homebrew

**Runs after CI passes** (implicitly, because it only triggers on tags):

1. **Builds 4 platform-specific binaries** with `CGO_ENABLED=0`:
   - `darwin/arm64` (Apple Silicon)
   - `darwin/amd64` (Intel Mac)
   - `linux/arm64`
   - `linux/amd64`
2. **Creates GitHub Release** with all 4 `.tar.gz` assets
3. **Calculates SHA256** for each asset
4. **Updates Homebrew tap** (`seppaleinen/homebrew-idle-deck`):
   - Updates `VERSION` constant
   - Fills in all 4 `sha256` values in the formula
   - Commits and pushes to the tap repo

---

## Post-release verification

After the tag is pushed, wait for both workflows to complete (check Actions tab), then verify:

### Docker

```bash
docker pull ghcr.io/seppaleinen/idle-deck:v0.1.0
docker pull ghcr.io/seppaleinen/idle-deck:latest
```

### go install

```bash
go install github.com/seppaleinen/idle-deck@v0.1.0
idle-deck --version  # should print "idle-deck v0.1.0"
```

### Homebrew

```bash
brew update
brew install seppaleinen/idle-deck/idle-deck
idle-deck --version  # should print "idle-deck v0.1.0"
```

### GitHub Release

Check https://github.com/seppaleinen/idle-deck/releases — should show v0.1.0 with 4 assets.

---

## Rollback / fix a release

If a release has a problem:

1. **Do not delete the tag** — it breaks `go install` and Docker caches
2. **Fix on `main`**, then cut a patch version:
   ```bash
   # Fix the bug on main
   git commit -m "fix: ..."
   # Cut patch
   git tag -a v0.1.1 -m "Release v0.1.1"
   git push origin v0.1.1
   ```
3. The old release stays on GHCR; new version is independent

---

## Required secrets

| Secret | Where | Purpose |
|---|---|---|
| `GITHUB_TOKEN` | Built-in | Push to tap repo, create release |

The built-in `GITHUB_TOKEN` has `contents: write` and `packages: write` by default on tag pushes. No additional secrets needed.

---

## Local dry-run

To test the binary build locally before pushing a tag:

```bash
VERSION=0.1.0-test
mkdir -p dist
for GOOS in darwin linux; do
  for GOARCH in amd64 arm64; do
    OUT="dist/idle-deck_${VERSION}_${GOOS}_${GOARCH}"
    CGO_ENABLED=0 GOOS=$GOOS GOARCH=$GOARCH go build -ldflags="-s -w" -o "$OUT" .
    tar -czf "${OUT}.tar.gz" -C dist "$(basename $OUT)"
  done
done
ls -la dist/
```

---

## D-refs

- **D32**: One static cgo-free binary, `go install`
- **D47**: Post-MVP scope acknowledged — install paths include Homebrew and Docker
- **D48/D51**: Draft PRs only; merge on explicit instruction (for PRs). Releases are direct tag pushes.
- **D9**: Go 1.27

---

## See also

- [Install Guide](install.md) — User-facing installation instructions
- [Operator Surface](operator-surface.md) — Configuration reference
- [ADR 0017](../adr/0017-the-artifact.md) — The artifact decision