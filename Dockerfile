# syntax=docker/dockerfile:1

# idle-deck container image (D32: one static cgo-free binary).
#
# Multi-stage: the builder compiles both binaries and installs the agent
# CLI; the runtime ships only the artifacts — the two Go binaries, the
# standalone opencode binary, and the system tools the remote needs
# (git, gh). Nothing the build needed (Go, Node, apt metadata) survives
# into the final image.
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

# The agent CLI (opencode) is a standalone compiled binary downloaded by the
# npm postinstall. Install it in the builder so only the binary ships to
# runtime — the image needs no Node at runtime. Version pinned to 1.18.35,
# the release the ACP runner (cmd/remote/acp.go) was verified against; the
# health probe (cmd/remote/health.go) drives the same CLI to verify model
# reachability per D31.
RUN apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates curl nodejs npm \
    && npm i -g opencode-ai@1.18.35 \
    && cp "$(npm root -g)/opencode-ai/bin/opencode.exe" /out/opencode \
    && rm -rf /root/.npm /var/lib/apt/lists/*

FROM ubuntu:24.04
# git: the remote owns checkout and worktrees (D27).
# gh: the remote opens Draft PRs via `gh pr create` (remote/workspace.go).
# curl + gpg: installing gh from its apt keyring.
# ca-certificates: TLS for GitHub + the model provider.
# tzdata: correct time zone in logs and git.
RUN apt-get update && apt-get install -y --no-install-recommends \
        git ca-certificates tzdata curl gpg adduser \
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
COPY --from=builder /out/opencode /usr/local/bin/opencode
USER idle-deck
ENTRYPOINT ["idle-deck"]