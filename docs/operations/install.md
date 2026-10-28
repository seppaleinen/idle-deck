# Installing idle-deck

How to install idle-deck on your machine. Choose the method that fits your workflow.

## Prerequisites

- **Go 1.27+** (for `go install`) — [Download Go](https://go.dev/dl/)
- **Docker** (for container images) — [docker.com](https://www.docker.com/products/docker-desktop/)
- **Homebrew** (for Homebrew install, macOS/Linux) — [brew.sh](https://brew.sh)

---

## Option 1: `go install` (recommended)

The simplest way to install idle-deck. Produces a static, cgo-free binary (D32, ADR 0017).

```bash
go install github.com/seppaleinen/idle-deck@v0.1.0
```

### Verify

```bash
idle-deck --version
# Expected output:
# idle-deck v0.1.0
```

The binary is installed to `$GOBIN` (defaults to `$GOPATH/bin`). Ensure that directory is on your `PATH`.

### Version selection

| Command | Resolves to |
|---|---|
| `go install github.com/seppaleinen/idle-deck@v0.1.0` | Tagged release v0.1.0 (recommended, reproducible) |
| `go install github.com/seppaleinen/idle-deck@latest` | Latest version on `main` (moving target, not a tag) |

---

## Option 2: Docker

Multi-arch images (linux/amd64 + linux/arm64) are published to GHCR.

### Pull a specific version

```bash
docker pull ghcr.io/seppaleinen/idle-deck:v0.1.0
```

### Pull latest

```bash
docker pull ghcr.io/seppaleinen/idle-deck:latest
```

### Run the container

```bash
docker run --rm \
  -e IDLE_DECK_GITHUB_TOKEN="${IDLE_DECK_GITHUB_TOKEN}" \
  -e IDLE_DECK_REPOS="${IDLE_DECK_REPOS}" \
  -e IDLE_DECK_HARNESS_URL="${IDLE_DECK_HARNESS_URL}" \
  -e IDLE_DECK_HARNESS_TOKEN="${IDLE_DECK_HARNESS_TOKEN}" \
  -e IDLE_DECK_DB=/data/idle-deck.db \
  -v idle-deck-data:/data \
  ghcr.io/seppaleinen/idle-deck:v0.1.0 run
```

See [Operator Surface](operator-surface.md) §4 for all environment variables.

### Docker Compose

```yaml
services:
  idle-deck:
    image: ghcr.io/seppaleinen/idle-deck:v0.1.0
    restart: unless-stopped
    environment:
      IDLE_DECK_GITHUB_TOKEN: ${IDLE_DECK_GITHUB_TOKEN}
      IDLE_DECK_REPOS: ${IDLE_DECK_REPOS}
      IDLE_DECK_HARNESS_URL: ${IDLE_DECK_HARNESS_URL}
      IDLE_DECK_HARNESS_TOKEN: ${IDLE_DECK_HARNESS_TOKEN}
      IDLE_DECK_DB: /data/idle-deck.db
    volumes:
      - idle-deck-data:/data

volumes:
  idle-deck-data:
```

---

## Option 3: Homebrew (macOS and Linux)

idle-deck is available in the `seppaleinen/homebrew-idle-deck` tap.

### Install the tap

```bash
brew tap seppaleinen/idle-deck
```

### Install

```bash
brew install idle-deck
```

The formula provides both a `stable` version (from GitHub releases) and a `head` version (tracks `main`):

```bash
brew install idle-deck          # stable: from GitHub release
brew install --HEAD idle-deck   # head: tracks main
```

---

## Build from source

```bash
git clone https://github.com/seppaleinen/idle-deck.git
cd idle-deck
CGO_ENABLED=0 go build -o idle-deck .
./idle-deck --version
```

### Why cgo-free matters

idle-deck is built with `CGO_ENABLED=0` (D32, ADR 0017):

- **No C toolchain** required on your machine
- **Cross-platform** — the same build works on macOS, Linux, ARM, and AMD64
- **Static binary** — no external runtime dependencies
- **`go install` works everywhere** Go works

---

## Platform support

| Platform | `go install` | Homebrew | Docker |
|---|---|---|---|
| macOS (Apple Silicon) | ✅ | ✅ | ✅ |
| macOS (Intel) | ✅ | ✅ | ✅ |
| Linux (amd64) | ✅ | ✅ | ✅ |
| Linux (arm64) | ✅ | ✅ | ✅ |
| Windows (WSL2) | ✅ | ✅ (WSL) | ✅ |

---

## D-refs

- **D32**: One static cgo-free binary, `go install`
- **D9**: Go 1.27
- **D47**: Post-MVP scope — dashboard acknowledged; install paths follow

---

## See also

- [Release Process](release.md) — How releases are cut and published
- [Operator Surface](operator-surface.md) — Configuration and running idle-deck
- [Getting Started](../../GETTING-STARTED.md) — First-time setup guide
