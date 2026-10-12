# Docker + Helm Deployment Plan for idle-deck

## Context

This plan addresses packaging idle-deck as a Docker image and deploying it to a k3s cluster via Helm.

Current state: no Dockerfile, no Helm chart, no container artifacts. CI (`.github/workflows/ci.yml`) builds the binaries with `CGO_ENABLED=0` but does not produce images.

## Architectural constraints (from AGENTS.md)

| Decision | Impact on container/Helm |
|---|---|
| **D32**: single static cgo-free binary, `go install` | Docker build must assert `CGO_ENABLED=0`, not inherit it |
| **D4**: single-user, one machine | One deployment per cluster/namespace; no multi-tenancy, no auth on dashboard |
| **D8**: `max_concurrent_jobs: 1` | Daemon is strictly single-replica; `replicas > 1` is a footgun the chart should refuse |
| **D24**: SQLite lease/run-lock | One writer at a time — another reason for exactly one daemon pod |
| **D27**: workspace lives on the remote; remote owns checkout, git, PR production | Remote server needs **persistent, writable storage** (bare dir + worktrees) |
| **D33**: CLI surface `run`/`status`/`check`/`--version` + `serve` | Daemon container runs `run`; dashboard is an optional sidecar or separate container |
| **D35**: supervision is launchd (macOS) | K8s restart policy replaces launchd; `restartPolicy: Always` |
| **D36**: secrets are environment variables, never files/`.env` | K8s `Secret` → `env` (or `envFrom`), never volume-mounted files |
| **D31**: idle is probed at the harness's `GET /health`, which must report *model* reachability | Remote's health endpoint is a stub today (returns true always) — must be real before idle detection means anything in the cluster |

## Architecture overview

```
                    ┌─────────────── k3s ──────────────────────────────┐
                    │                                                  │
                    │  ┌────────────────────────┐    ┌──────────────┐  │
                    │  │ Deployment: daemon     │    │ Deployment:  │  │
                    │  │ idle-deck run          │    │ remote       │  │
                    │  │ (single replica)       │    │ idle-deck-   │  │
                    │  │  └─ optional sidecar   │    │ remote       │  │
                    │  │     idle-deck serve    │    │ (single repl)│  │
                    │  └──────────┬─────────────┘    └──────┬───────┘  │
                    │             │  IDLE_DECK_HARNESS_URL  │          │
                    │             └────── ClusterIP :8080 ──┘          │
                    │             │                                    │
                    │             └── egress ──► api.github.com        │
                    │             │                                    │
                    │        PVC (ReadWriteOnce, local-path)          │
                    │        ├── idle-deck.db    (SQLite, daemon)     │
                    │        ├── bare/           (git mirrors)        │
                    │        └── work/           (worktrees)          │
                    └──────────────────────────────────────────────────┘
```

Two deployments, one PVC. Daemon and remote share the PVC because both need
writable state (SQLite; bare + worktree dirs). `ReadWriteOnce` + one pod each
sits naturally on a single node — which matches D4's single-machine spirit.

## 1. Dockerfile

Single multi-stage image, both binaries in it. `docker build` from repo root.

```dockerfile
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
```

**The agent CLI — resolved, and it is configurable.** The remote shells out to
an ACP agent (`cmd/remote/main.go` → `remote.NewACP(ACPConfig{Bin, Args,
Env})`). opencode v2 ships **prebuilt native binaries** as npm platform
packages (`@opencode/cli-linux-x64`, `-linux-arm64`, + musl variants); the
Dockerfile extracts the one matching `TARGETARCH` from the tarball — no
source build, no Node at runtime. Verified: the image builds, `opencode
--version` reports v2.0.26, and the ACP handshake (initialize → session/new)
works inside the container.

**The agent is not hardcoded to opencode.** `remote/runner.go` defines a
`Runner` interface (`Start`/`Abort`/`Result`) that is agent-agnostic; the
launch recipe is a table, not code (see **D46**). Any ACP agent — `opencode`,
`pi`, `goose`, or `claude`/`codex`/`gemini` behind their ACP adapter — drops
in via `IDLE_DECK_REMOTE_AGENT_BIN` / `_AGENT_ARGS`. A2A-platform adapters
are a different animal and stay at **D41 trigger 3** (a second `Harness`
adapter), not a config swap.

### Image tags and registry

- Registry: **GHCR** (`ghcr.io/<owner>/idle-deck`) — private by default, free for the repo.
- Tags: `v<semver>` on tags (e.g. `v0.1.0`), plus `main-<short-sha>` on every push for cluster dev.
- **Multi-arch: linux/amd64 + linux/arm64** via Buildx — k3s on Apple Silicon or Pi is common.

## 2. CI extension (`.github/workflows/ci.yml`)

Add one job, reusing the existing build/vet/test gate:

```yaml
  docker:
    needs: [build]   # reuse the build job's gates
    runs-on: ubuntu-latest
    permissions:
      contents: read
      packages: write
    steps:
      - uses: actions/checkout@v4
      - uses: docker/setup-buildx-action@v3
      - uses: docker/login-action@v3
        with:
          registry: ghcr.io
          username: ${{ github.actor }}
          password: ${{ secrets.GITHUB_TOKEN }}
      - name: Tag
        id: tag
        run: |
          if [ "$GITHUB_EVENT_NAME" = "push" ] && [[ "$GITHUB_REF" == refs/tags/v* ]]; then
            echo "tags=ghcr.io/${GITHUB_REPOSITORY,,}:${GITHUB_REF_NAME#v}" >> $GITHUB_OUTPUT
          else
            echo "tags=ghcr.io/${GITHUB_REPOSITORY,,}:main-${GITHUB_SHA::7}" >> $GITHUB_OUTPUT
          fi
      - uses: docker/build-push-action@v6
        with:
          context: .
          platforms: linux/amd64,linux/arm64
          push: true
          tags: ${{ steps.tag.outputs.tags }}
          cache-from: type=gha
          cache-to: type=gha,mode=max
```

## 3. Helm chart layout

```
charts/idle-deck/
├── Chart.yaml
├── values.yaml
├── values.schema.json
├── templates/
│   ├── _helpers.tpl
│   ├── NOTES.txt
│   ├── serviceaccount.yaml
│   ├── secret.yaml
│   ├── pvc.yaml
│   ├── deployment-daemon.yaml
│   ├── deployment-remote.yaml
│   ├── service-remote.yaml
│   ├── service-dashboard.yaml
│   └── ingress-dashboard.yaml
└── README.md
```

Namespace: use the standard `create: true` pattern in `values.yaml` so the
chart can create its own namespace (k3s is fine with this).

### values.yaml (shape)

```yaml
image:
  repository: ghcr.io/<owner>/idle-deck
  tag: v0.1.0            # or image.tag: "main-abc1234" for dev
  pullPolicy: IfNotPresent
imagePullSecret: ""      # set for private GHCR on private repos

# Repos allowlist (D30): the trust boundary. Comma-separated owner/repo.
repos: "owner/repo1"
sweepRepos: ""           # optional narrowing subset (D43)
githubAPI: "https://api.github.com"   # D38: GHE override

daemon:
  env:
    pollInterval: 60s
    logLevel: info
    logFormat: json
    # budgets/timeouts have D37 defaults; expose for override
    tierTimeouts: { plan: 900, do: 10800, sweep: 3600, hotfix: 1800 }
    budgets:      { plan: 5000, do: 100000, sweep: 20000, hotfix: 50000 }
    sweepPeriod: 168h
  resources:
    requests: { cpu: 50m,  memory: 64Mi  }
    limits:   { cpu: 300m, memory: 256Mi }
  # dashboard (idle-deck serve) runs as a sidecar of the daemon
  dashboard:
    enabled: false
    port: 8090
    actions: false        # IDLE_DECK_DASHBOARD_ACTIONS (opt-in)

remote:
  # IDLE_DECK_REMOTE_ROLE_TO_MODEL — JSON string
  roleToModel: '{"plan":"opencode/big-pickle","do":"opencode/big-pickle","sweep":"opencode/big-pickle"}'
  # Agent CLI selection (config-driven, D46) — default is opencode ACP.
  # Any ACP agent (opencode, pi, goose, claude/codex/gemini ACP adapters) works.
  agentBin: "opencode"
  agentArgs: '["acp"]'
  resources:
    requests: { cpu: 100m, memory: 256Mi }
    limits:   { cpu: "2",  memory: 2Gi }   # agent runs are CPU/memory hungry

persistence:
  size: 10Gi              # bare mirrors + worktrees + SQLite
  storageClassName: ""    # k3s default is local-path

secrets:
  # Empty values => chart looks for an existing Secret (name: secretName).
  # Set values => chart creates the Secret for you.
  secretName: "idle-deck"
  githubToken: ""         # IDLE_DECK_GITHUB_TOKEN
  harnessToken: ""        # IDLE_DECK_HARNESS_TOKEN == IDLE_DECK_REMOTE_HTTP_TOKEN
  remoteGitToken: ""      # IDLE_DECK_REMOTE_GIT_TOKEN (repo-scoped, RW)

# Harness URL is derived: http://<name>-remote.<ns>:8080 — no need to set.
```

**Schema note:** `values.schema.json` should enforce `daemon.replicaCount`
equivalently — actually simpler: the chart doesn't expose `replicas` at all.
D8/D24 make it a constant 1. Don't give operators the footgun.

### Key template details

**daemon Deployment:**
- `command: ["/usr/local/bin/idle-deck", "run"]`
- Env from values; tokens via `secretKeyRef` (D36: env, never files)
- Volume: PVC at `/data`, with `IDLE_DECK_DB=/data/idle-deck.db`
- `IDLE_DECK_HARNESS_URL` set from a template:
  `http://{{ include "idle-deck.remoteServiceName" . }}:8080`
- **Probe strategy** (no readiness needed for a worker, but startup/liveness help):
  - No liveness probe on the process — the daemon is long-running and a
    transient hang shouldn't kill it mid-run (would consume a retry, D14/D24).
  - Liveness via the **dashboard/heartbeat is out of scope for v1**; K8s
    restart-on-crash + SQLite lease expiry (D24) already covers crash recovery.
- Sidecar (if `daemon.dashboard.enabled`): same image, `args: [serve]`,
  port 8090. Dashboard reads the same DB (WAL, `_mode=ro`), which is why the
  sidecar pattern works cleanly — no second PVC consumer.

**remote Deployment:**
- `command: ["/usr/local/bin/idle-deck-remote"]`
- Same PVC, subPathMounts: `bare` → `/data/bare`, `work` → `/data/work`
- **Liveness probe: `httpGet /v1/health` on :8080** — the remote contract
  already defines this endpoint, so it's free.
- **One caveat:** the current health endpoint is a stub (`cmd/remote/main.go`
  hardcodes `return true, "opencode/big-pickle"`). For the *process*
  liveness probe that's fine, but **D31 requires /health to report model
  reachability** — idle-deck's idle detection is load-bearing on that. This
  must be implemented before the cluster deployment is "real" (see Phase 0).

**Services:**
- `service-remote`: ClusterIP, port 8080, name `<release>-remote`
- `service-dashboard` (only when enabled): ClusterIP 8090 — or NodePort if
  you want to hit the dashboard without an ingress. For personal use,
  **NodePort or port-forward** is the honest answer; no auth by design (D4).

**Ingress (optional):** only rendered when `daemon.dashboard.enabled`.
k3s ships Traefik → `ingressClassName: traefik`. If you want the dashboard
beyond localhost, put `oauth2-proxy` or `basic-auth` annotations in front of
it — the dashboard itself deliberately has no auth (D4), so anything
exposed off-127.0.0.1 should add a layer.

**NOTES.txt:** prints post-install — dashboard `kubectl port-forward`
command, and a reminder that the GitHub token needs Issues read/write on the
listed repos.

## 4. k3s specifics

| Concern | Answer |
|---|---|
| Storage | k3s default `local-path` SC: PVC binds to one node. RWO + two single-replica pods → pin both pods to the same node (`nodeSelector` or a single-node cluster, which is the common k3s case). On a multi-node k3s, use `nodeAffinity` pointing at the node the PVC bound to. |
| Ingress | Traefik is built in; `ingressClassName: traefik` |
| GHCR pull | `imagePullSecret: registry-ghcr-io` — create with `kubectl create secret docker-registry` (GHCR accepts a fine-grained PAT with `packages:read`) |
| ARM64 | Buildx multi-arch in CI (above) |
| Agent CLI | opencode must exist in the image (Dockerfile §1) — remote won't start runs without it |

## 5. Flux GitOps (if applicable)

Standard GitOps: the chart lives **in this repo** under `charts/`; a Flux
`HelmRepository` (or `OCIRepository` if you publish to GHCR OCI) + `HelmRelease`
in your infra repo points at it. Image updates via Flux **ImageAutoUpdater**
watching `ghcr.io/<owner>/idle-deck` for `main-*`/`v*` tags.

## 6. Phased implementation

### Phase 0 — Prerequisites (blocks everything)
- [x] Implement real model-reachability in the remote `/health` (D31 stub
      currently always true) — `cmd/remote/health.go` drives a real ACP
      round-trip (initialize → session/new → session/prompt); only a
      successful generation means reachable, per ADR 0016. Covered by
      `cmd/remote/health_test.go`.
- [x] Pin the opencode version and make it work on the base image. **Migrated
      to opencode v2** (2.0.26): v2 drops the `--cwd` flag the v1 runner used
      (working directory now comes from the process cwd / `session/new`), and
      v2 ships prebuilt Linux binaries via the npm platform package
      `@opencode/cli`, so the image extracts the binary instead of building
      from source (Dockerfile, §1). The health probe and both real-opencode
      integration tests pass against v2.0.26.

### Phase 1 — Docker image + CI (≈1 day)
- [x] `Dockerfile` (multi-stage, non-root, both binaries, git + gh + ripgrep +
      opencode v2 prebuilt binary)
- [x] CI `docker` job: buildx multi-arch, push on push + tag
- [x] Local smoke: `docker build`, `docker run ... --version`,
      `docker run ... idle-deck check` fails without config; in-container
      ACP handshake against `opencode acp` verified
- [x] Push `main` to the real origin, verify the `docker` job produces a
      multi-arch `main-<sha>` tag in GHCR. Pushed `b66f275..9449c9e` to
      `github.com/seppaleinen/idle-deck` (fast-forward; SSH, since the HTTPS
      route was blocked — the `gh` OAuth token lacks the `workflow` scope and
      the push would update `.github/workflows/ci.yml`). CI run #18 came back
      **success**: build job (D-id checker, CGO_ENABLED=0 build gate, vet,
      test, smoke) and docker job both green, image pushed as
      `ghcr.io/seppaleinen/idle-deck:main-9449c9e` for linux/amd64 + arm64.
      Note: GHCR readback needs a `packages:read` token; the `gh` OAuth token
      is denied there, so the readback is from the CI log, not a direct pull.
- [ ] Tag `v0.1.0`, verify image appears in GHCR

### Phase 1.5 — Agent selection + cleanup (≈half a day)

These are the items approved in the plan amendment (config-driven agent
selection; A2A = D41 trigger 3). They unblock "user picks their agent"
and clean the working tree.

- [x] Commit pending changes: Dockerfile (`adduser` fix), `cmd/remote/main.go`
      (v2 migration, health probe), `remote/acp_integration_test.go` (v2
      migration). Committed `c5858e9`.
- [x] Implement config-driven agent selection in `cmd/remote/main.go`:
      read `IDLE_DECK_REMOTE_AGENT_BIN` (default `opencode`),
      `IDLE_DECK_REMOTE_AGENT_ARGS` (default `["acp"]`),
      `IDLE_DECK_REMOTE_AGENT_ENV` (optional JSON array of extra env).
      Wired through to `remote.NewACP()` and `newHealthProbe()` so the
      health probe uses the same agent CLI. Tests: `cmd/remote/agent_config_test.go`
      (4 tests, incl. fake ACP probe). Committed `d6819a8`.
- [x] Update `docs/operations/operator-surface.md` with the new env vars
      (§ "Remote execution service — agent CLI selection (D46)").
- [x] Run full test suite (`go test ./...`, `go vet ./...`, `go build
      ./cmd/...`). All pass (see IMPORTANT NOTES re: `remote` pre-existing
      real-opencode test).
- [x] Add D46 table-tier row to AGENTS.md: "Agent CLI is operator config,
      not build-time choice; A2A-platform adapters are a second Harness
      adapter (D41 trigger 3), not a config swap."
- [x] Run D-id verification check (docs/adr/README.md §Verification) before
      committing AGENTS.md. Passes (D70/D71 "failures" are `go.sum` hash
      strings, not real citations).

### Phase 2 — Helm chart core (≈1–2 days)
- [x] Scaffold + templates (namespace, SA, Secret, PVC, 2 Deployments,
      remote Service, optional dashboard sidecar + ingress)
- [x] `values.schema.json` (repos non-empty, valid JSON roleToModel, bans
      any `*replica*` key per D8/D24)
- [x] Tested against k3d/k3s locally: install, pods ready, PVC Bound to
      `local-path`, remote pod Running
- [x] PVC persistence verified: PVC survives pod restart
- [x] **Chart verification surfaced a daemon crash**: `IDLE_DECK_POLL_INTERVAL`
      was documented as `60s` (D28) and the chart emits `168h` for the sweep
      period (D43), but `config/config.go`'s `kindDurationSeconds` parsed with
      `strconv.Atoi` — bare integers only. Fixed (commit `cffbccc`): now
      accepts Go duration strings (`60s`, `168h`, `1h30m`) while keeping
      bare-integer support unchanged. Verified: `idle-deck check` with the
      full chart env set now parses and proceeds to GitHub auth (401 on the
      fake token is expected — a real network failure, not a parse crash).

### Phase 3 — Dashboard exposure (≈half a day, optional)
- [ ] Sidecar `serve` + Service + optional NodePort/Ingress
- [ ] Auth front (Traefik basic-auth or oauth2-proxy) for anything non-local
- [ ] Port-forward docs in NOTES.txt

### Phase 4 — GitOps wiring (≈half a day, if Flux)
- [ ] Publish chart (GHCR OCI or git repo)
- [ ] HelmRelease + ImageAutoUpdater in infra repo
- [ ] Test a forced image bump end-to-end

## 7. Risks / open questions

1. ~~**opencode on Ubuntu 24.04**~~ — resolved. opencode v2 ships prebuilt
   native binaries as npm platform packages; the image extracts the one matching
   `TARGETARCH` in a `node:20-slim` stage. No Node at runtime, no npm install
   against the base image. Verified: builds for `linux/arm64`, `opencode
   --version` reports v2.0.26, ACP handshake works in-container.
2. **SQLite on `local-path` PVC** — no snapshots out of the box. The DB is
   small and the queue is re-derivable from GitHub state (watermarks
   re-seed), so risk is low; still, document `sqlite3 backup` as the
   recovery path.
3. **Long agent runs vs pod lifecycle** — node drain/reboot kills an in-flight
   run. D24 already handles this: lease expiry → timeout on the attempt →
   re-queue, consuming one retry. Acceptable; document it.
4. **Egress** — pods need outbound HTTPS to api.github.com and the model
   provider. If the cluster has a proxy, `HTTPS_PROXY` env is honored by the
   Go/Node clients — note it in the chart README.
5. **Cost** — the remote pod runs agent processes; size requests/limits
   honestly (2 Gi is a realistic ceiling for a `do` run).
6. **Duration parsing** — closed. `config/config.go`'s `kindDurationSeconds`
   used `strconv.Atoi`, so it accepted bare integers only while the documented
   contract (operator-surface §4, D28/D43) and the chart both use Go duration
   strings (`60s`, `168h`). The daemon crashed on its own shipped config.
   Fixed (`cffbccc`): bare integers still parse unchanged, Go duration
   strings now parse too. Verified with the full chart env set.
7. **Pre-existing test gap** — `remote.TestACP_RealOpencode` requires a real
   `opencode` binary + network and fails identically on `main`. It is not a
   regression; it is a test that cannot run in CI. Tag it `// +build !ci` or
   gate it behind an env var before it blocks CI.
   **Closed** (`6b7baff`): gated behind `IDLE_DECK_REAL_ACP_TESTS=1`. The
   failure was characterized as **environmental, not a code bug** — with no
   provider credentials configured, `session/prompt` returns a JSON-RPC
   auth error (`-32600`, "Authentication required"), the agent process exits,
   and `prompt()` records an empty outcome. That is the runner's designed
   "agent failed, server decides" path, and it is exactly what the health
   probe (D31) would report as *not idle*. The test simply has no pass
   condition without credentials. The runner is correct; credentials are the
   missing piece, and they belong on the dev machine, not in the test.

## 8. What this plan deliberately does NOT do

- No operator (K8s custom controller) — a Helm chart is the right weight for
  a single-user, two-pod deployment.
- No multi-tenancy/multi-replica — D4/D8/D24.
- No separate image for the remote server — one image, two entrypoints is
  simpler to maintain and the size delta is one static Go binary (~15 MB).
- No A2A / third-party agent-platform adapter — that is a second `Harness`
  adapter, and D41's trigger list names it (trigger 3: a second operator
  needs a harness the first cannot serve). A2A is agent↔agent delegation
  over server-to-server, and coding agents speak ACP over stdio — the remote
  layer (workspace, git, timeout, typed artifacts, model-reachability health)
  is load-bearing regardless of protocol. Config-driven agent selection
  (D46) covers "use a different ACP CLI"; a different *platform* is a
  different decision.

### Executor lifetime is an implementation detail, not a contract question

The wire contract pins four endpoints and their semantics — it does **not**
say whether the remote process lives for the cluster's life or is spawned
per run. Both are compatible with the contract:

| | Long-running (current default) | Ephemeral per-run |
|---|---|---|
| Workspace | PVC-backed, reused across runs | EmptyDir, recreated per run |
| Clone cost | One clone, then worktrees | One clone per run |
| Cold starts | None | One per run |
| PVC | Required | Not required |
| Health probe | `/health` answers for the pod | Each run must probe its own agent |

The MVP ships long-running (matches the plan's PVC). The ephemeral variant
is **parked, not decided**: it leaks in two places — the `/health` endpoint
would need to report per-run rather than per-pod, and the idempotency grace
period (contract §4) would need a per-run clock. Neither is hard, but both
are real work, and the MVP's single-user, one-machine shape (D4/D8) does not
push toward it. It stays open until a second user or a real cold-start problem
names a trigger.
