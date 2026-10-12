# syntax=docker/dockerfile:1

# idle-deck container image (D32: one static cgo-free binary).
#
# Multi-stage: the builder compiles both Go binaries; the opencode stage
# extracts the prebuilt agent CLI from its npm platform package; the
# runtime ships only the artifacts — the two Go binaries, the opencode
# binary, and the system tools the remote needs (git, gh, ripgrep).
#
# CGO_ENABLED=0 is asserted at the build step, not inherited from the
# environment (ADR 0017 / D32): a dependency that reintroduces cgo would
# silently break `go install` for users, so the build must not depend on
# the host's environment being clean.

FROM golang:1.27 AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /out/idle-deck . && \
    CGO_ENABLED=0 go build -ldflags="-s -w" -o /out/idle-deck-remote ./cmd/remote

# opencode v2 ships prebuilt native binaries as npm platform packages
# (@opencode/cli-linux-x64, @opencode/cli-linux-arm64, + musl variants).
# Extract the one matching TARGETARCH from the tarball — no source build,
# no Node at runtime. The ACP runner (remote/acp.go) and the health probe
# (cmd/remote/health.go) are verified against this line: `opencode acp`
# with no flags, working directory from the process cwd / session/new.
FROM node:20-slim AS opencode-stage
ARG TARGETARCH
ARG OPENCODE_VERSION=2.0.26
RUN case "$TARGETARCH" in \
        amd64) ARCH=x64 ;; \
        arm64) ARCH=arm64 ;; \
        *) echo "unsupported TARGETARCH: $TARGETARCH" >&2; exit 1 ;; \
    esac && \
    npm pack "@opencode/cli-linux-${ARCH}@${OPENCODE_VERSION}" && \
    mkdir -p /out && \
    tar xzf "opencode-cli-linux-${ARCH}-${OPENCODE_VERSION}.tgz" && \
    cp package/bin/opencode /out/opencode

FROM ubuntu:24.04
# git: the remote owns checkout and worktrees (D27).
# gh: the remote opens Draft PRs via `gh pr create`.
# ripgrep: opencode runtime dependency.
# curl + gpg: installing gh from its apt keyring.
# ca-certificates: TLS for GitHub + the model provider.
# tzdata: correct time zone in logs and git.
RUN apt-get update && apt-get install -y --no-install-recommends \
        git ca-certificates tzdata curl gpg ripgrep adduser \
    && curl -fsSL https://cli.github.com/packages/githubcli-archive-keyring.gpg \
        | gpg --dearmor -o /usr/share/keyrings/githubcli-archive-keyring.gpg \
    && echo "deb [arch=$(dpkg --print-architecture) signed-by=/usr/share/keyrings/githubcli-archive-keyring.gpg] https://cli.github.com/packages stable main" \
        > /etc/apt/sources.list.d/github-cli.list \
    && apt-get update && apt-get install -y --no-install-recommends gh \
    && rm -rf /var/lib/apt/lists/* \
    # The base image ships an `ubuntu` user at uid 1000; the non-root user
    # the image runs as is uid 1000 per the plan, so the placeholder is
    # removed first (its home dir is not needed at runtime).
    && userdel --remove ubuntu 2>/dev/null || true \
    && adduser --disabled-password --gecos "" --uid 1000 idle-deck
COPY --from=builder /out/idle-deck /usr/local/bin/idle-deck
COPY --from=builder /out/idle-deck-remote /usr/local/bin/idle-deck-remote
COPY --from=opencode-stage /out/opencode /usr/local/bin/opencode
USER idle-deck
ENTRYPOINT ["idle-deck"]
