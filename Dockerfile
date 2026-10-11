# syntax=docker/dockerfile:1

# idle-deck container image (D32: one static cgo-free binary).
#
# Multi-stage: the builder compiles both Go binaries and builds
# opencode v2.0.25 from source; the runtime ships only the artifacts —
# the two Go binaries, the standalone opencode binary, and the
# system tools the remote needs (git, gh). Nothing the build needed
# (Go, Node, apt metadata) survives into the final image.
#
# CGO_ENABLED=0 is asserted at the build step, not inherited from the
# environment (ADR 0017 / D32): a dependency that reintroduces cgo would
# silently break `go install` for users, so the build must not depend on
# the host's environment being clean.

FROM golang:1.27 AS gobuilder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /out/idle-deck . && \
    CGO_ENABLED=0 go build -ldflags="-s -w" -o /out/idle-deck-remote ./cmd/remote

# opencode v2.0.25 built from source (anomalyco/opencode).
# The source build is required because v2 has no Linux release assets;
# opencode v2 requires bun/rust/python/zig and a ghostty checkout.
# The homebrew formula for v2.0.25 is the source of truth for these
# steps — this stage mirrors that recipe.
FROM ubuntu:24.04 AS opencode-builder
ARG OPENCODE_VERSION=2.0.25
ARG TARGETARCH

# Build deps: git, python (3.14), bun, rust, zig, icu4c, ripgrep.
# ripgrep is a runtime dependency (the agent uses it); the rest are
# build-only.
RUN apt-get update && apt-get install -y --no-install-recommends \
        git ca-certificates curl build-essential pkg-config xz-utils \
        python3 python3-pip libssl-dev \
    && rm -rf /var/lib/apt/lists/*

# --- python 3.14 via uv (system python is 3.12 on ubuntu:24.04) ---
RUN curl -fsSL https://astral.sh/uv/install.sh | sh && \
    ln -sf /root/.cargo/bin/uv /usr/local/bin/uv && \
    uv python install 3.14 && \
    ln -sf $(uv python find 3.14) /usr/local/bin/python3

# --- zig 0.16 (needed by opencode-pty build) ---
RUN case "$TARGETARCH" in \
        amd64) ZIG_ARCH=x86_64 ;; \
        aarch64) ZIG_ARCH=aarch64 ;; \
        *) echo "unsupported arch $TARGETARCH" && exit 1 ;; \
    esac && \
    curl -fsSL "https://ziglang.org/download/0.16.0/${ZIG_ARCH}-linux-gnu.tar.xz" \
      | tar -xJ -C /usr/local && \
    ln -s "/usr/local/zig-linux-${ZIG_ARCH}-0.16.0/zig" /usr/local/bin/zig

# --- rust (opencode-pty build requires cargo) ---
RUN curl --proto '=https' --tlsv1.2 -sSf https://sh.rustup.rs \
    | sh -s -- -y --no-modify-path --profile minimal --default-toolchain stable && \
    ln -sf /root/.cargo/bin/cargo /usr/local/bin/cargo && \
    ln -sf /root/.cargo/bin/rustc /usr/local/bin/rustc

# --- bun (opencode cli build) ---
RUN curl -fsSL https://bun.sh/install | bash -s "bun-v1.2.19" && \
    ln -sf /root/.bun/bin/bun /usr/local/bin/bun && \
    ln -sf /root/.bun/bin/bunx /usr/local/bin/bunx

# --- clone opencode v2.0.25 ---
RUN git clone --depth 1 --branch v${OPENCODE_VERSION} \
        https://github.com/anomalyco/opencode.git /build/opencode && \
    cd /build/opencode && \
    mkdir -p /build/opencode/.yarn-cache

# --- fetch opencode-pty resource (v0.2.0) and build it ---
RUN git clone --depth 1 --branch v0.2.0 \
        https://github.com/anomalyco/opencode-pty.git /build/opencode-pty && \
    cd /build/opencode-pty && \
    cargo install --root /build/opencode-pty-root --path . && \
    ls -l /build/opencode-pty-root/bin/opencode-pty

# --- fetch ghostty at pinned revision (required by opencode-pty) ---
RUN git clone --depth 1 https://github.com/ghostty-org/ghostty.git /build/ghostty && \
    cd /build/ghostty && \
    git fetch --depth 1 origin ab0b9da9e88fcb4b0533a1854e84628f663930af && \
    git checkout ab0b9da9e88fcb4b0533a1854e84628f663930af

# --- set build env vars per homebrew formula ---
ENV OPENCODE_VERSION=${OPENCODE_VERSION}
ENV OPENCODE_CHANNEL=latest
ENV GHOSTTY_SOURCE_DIR=/build/ghostty
ENV OPENCODE_PTY_BIN=/build/opencode-pty-root/bin/opencode-pty

# --- patch bun build (splitting fix required for bun 1.x) ---
RUN cd /build/opencode/packages/cli && \
    sed -i 's/splitting: true/splitting: false/' script/build.ts

# --- install deps and build opencode single binary ---
RUN cd /build/opencode && \
    bun install --frozen-lockfile && \
    cd packages/cli && \
    bun --bun ./script/build.ts --single --skip-install && \
    ls -l dist/cli-*/bin/opencode && \
    cp dist/cli-*/bin/opencode /out/opencode

FROM ubuntu:24.04
# runtime deps: git (D27), gh (Draft PRs), ripgrep (opencode runtime dep),
# ca-certificates (TLS), tzdata (logs), curl (health checks).
RUN apt-get update && apt-get install -y --no-install-recommends \
        git ca-certificates tzdata curl gpg ripgrep \
    && curl -fsSL https://cli.github.com/packages/githubcli-archive-keyring.gpg \
        | gpg --dearmor -o /usr/share/keyrings/githubcli-archive-keyring.gpg \
    && echo "deb [arch=$(dpkg --print-architecture) signed-by=/usr/share/keyrings/githubcli-archive-keyring.gpg] https://cli.github.com/packages stable main" \
        > /etc/apt/sources.list.d/github-cli.list \
    && apt-get update && apt-get install -y --no-install-recommends gh \
    && rm -rf /var/lib/apt/lists/* \
    # The base image ships an `ubuntu` user at uid 1000; remove it and
    # create the non-root user the image runs as.
    && userdel --remove ubuntu 2>/dev/null || true \
    && adduser --disabled-password --gecos "" --uid 1000 idle-deck

COPY --from=gobuilder /out/idle-deck /usr/local/bin/idle-deck
COPY --from=gobuilder /out/idle-deck-remote /usr/local/bin/idle-deck-remote
COPY --from=opencode-builder /out/opencode /usr/local/bin/opencode

USER idle-deck
ENTRYPOINT ["idle-deck"]
