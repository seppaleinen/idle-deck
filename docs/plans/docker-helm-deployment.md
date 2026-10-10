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

FROM golang:1.27 AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# CGO_ENABLED=0 asserted, not inherited (ADR 0017 / D32)
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /out/idle-deck . && \
    CGO_ENABLED=0 go build -ldflags="-s -w" -o /out/idle-deck-remote ./cmd/remote

FROM ubuntu:24.04
# git required by the remote server (D27: it owns checkout and PR production).
# Ubuntu (not alpine/slim): the remote installs agent tooling and configs,
# and a full base image is easier to extend than a minimal one.
RUN apt-get update && apt-get install -y --no-install-recommends \
        git ca-certificates tzdata curl \
    && rm -rf /var/lib/apt/lists/* \
    && adduser --disabled-password --gecos "" --uid 1000 idle-deck
COPY --from=builder /out/idle-deck /usr/local/bin/idle-deck
COPY --from=builder /out/idle-deck-remote /usr/local/bin/idle-deck-remote
USER idle-deck
ENTRYPOINT ["idle-deck"]
```

**The big open question: the agent CLI.** The remote server shells out to
`opencode` (an ACP runner, `cmd/remote/main.go`). Options:

Ubuntu base + `npm i -g opencode` in the image — pin the version in the
Dockerfile. Image grows to ~600–800 MB (Ubuntu + node + opencode). Acceptable
for a single-user tool; revisit if that becomes a problem.

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
- [x] Pin the opencode version; verify `npm i -g` works on Ubuntu 24.04
      the base image. Pinned to **1.18.35** (the release the ACP runner
      was verified against; 2.0.x rejects the `--cwd` flag the runner
      uses). Installed in the builder and only the standalone binary
      ships to runtime — the image needs no Node.

### Phase 1 — Docker image + CI (≈1 day)
- [x] `Dockerfile` (multi-stage, non-root, both binaries, git + opencode)
- [x] CI `docker` job: buildx multi-arch, push on push + tag
- [x] Local smoke: `docker build`, `docker run ... idle-deck check` fails
      without config, succeeds with env; `docker run ... idle-deck run`
      with the stubs reachable
- [x] Tag `v0.1.0`, verify image appears in GHCR

### Phase 2 — Helm chart core (≈1–2 days)
- [ ] Scaffold + templates (namespace, SA, Secret, PVC, 2 Deployments,
      remote Service)
- [ ] `values.schema.json` (repos non-empty, valid JSON roleToModel)
- [ ] Test against k3d/k3s locally: install, watch pods ready, trigger a P1
      (new issue) end-to-end through the cluster, verify Draft PR lands
- [ ] PVC persistence test: `kubectl delete pod`, verify watermark + queue
      survive

### Phase 3 — Dashboard exposure (≈half a day, optional)
- [ ] Sidecar `serve` + Service + optional NodePort/Ingress
- [ ] Auth front (Traefik basic-auth or oauth2-proxy) for anything non-local
- [ ] Port-forward docs in NOTES.txt

### Phase 4 — GitOps wiring (≈half a day, if Flux)
- [ ] Publish chart (GHCR OCI or git repo)
- [ ] HelmRelease + ImageAutoUpdater in infra repo
- [ ] Test a forced image bump end-to-end

## 7. Risks / open questions

1. **opencode on Ubuntu 24.04** — Node on Ubuntu is well-trodden, but pin and
   verify. Fallback: `node:20-slim` base (Debian) if npm misbehaves.
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

## 8. What this plan deliberately does NOT do

- No operator (K8s custom controller) — a Helm chart is the right weight for
  a single-user, two-pod deployment.
- No multi-tenancy/multi-replica — D4/D8/D24.
- No separate image for the remote server — one image, two entrypoints is
  simpler to maintain and the size delta is one static Go binary (~15 MB).
