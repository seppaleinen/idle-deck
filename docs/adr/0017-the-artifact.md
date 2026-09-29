---
Title:   The artifact: one static cgo-free binary, installed with go install
Status:  accepted
Supersedes: —
Related: D32, D2, D4, D9, D13, D19, D23, 0010, 0012
Source:   #11 Install, run, configure: the operator and developer surface
---

## Decision

**idle-deck ships as one static, cgo-free Go binary with its main package at the repository root,
installed with `go install`.** Release binaries, a Homebrew formula, and a container image are all
deferred.

| Property | Value |
|---|---|
| Module path | `github.com/seppaleinen/idle-deck` |
| Main package | repository root, so the binary is `idle-deck` |
| Go | 1.27 (D9) |
| `CGO_ENABLED` | `0` — static, no C toolchain anywhere |
| Queue driver | `modernc.org/sqlite` (pure Go) |
| Install | `go install github.com/seppaleinen/idle-deck@<tag>` |
| CI gates | `go build`, `go vet`, `go test`, the D-id check, a config-less smoke test |

The **SQLite driver** is the decision that carries the rest. D13 says "SQLite queue" without naming
one, and the driver is the only thing that decides whether cgo is in the build at all.

## Rationale

`mattn/go-sqlite3` is the faster, more battle-tested driver, and it requires cgo. cgo is a tax paid
on every machine forever: a C toolchain for every developer, `CGO_ENABLED=0` becomes impossible, so
the binary cannot be static; cross-compiling needs a C cross-compiler per target; and `go install`
breaks on any machine without Xcode command-line tools.

`modernc.org/sqlite` (v1.60.0, declaring `go 1.26.0`, dependency chain entirely pure Go) trades raw
SQLite throughput for a binary that installs anywhere Go does. **That trade is free at this
project's scale, and the scale is the point**: `max_concurrent_jobs` is 1 (D8) and the queue is one
small SQLite file holding a handful of tasks. Throughput is not a constraint idle-deck has. A
faster database would be optimising a bottleneck that does not exist, at the cost of a build
requirement on every machine that will ever build the project.

Main package at the **root** rather than in `cmd/idle-deck/`: with one binary, a root main package
makes the install command name the binary directly and keeps `go build ./...` producing the
artifact. `cmd/` earns its keep when a repository ships several binaries; this one ships one.

`go install` as the primary path, with a real `v0.x.y` tag cut when the first working binary exists
(#10), and the **tagged** form documented as the reproducible install. The repository had no tags
when this was decided, so `@latest` resolves to a pseudo-version of `main` — a moving target, which
is fine as a convenience and wrong as an install record.

**CI gates are in this ADR rather than a table row** because they are the mechanism that keeps the
artifact honest, and because the D-id check is already *mandated* by `AGENTS.md` before any push
touching `AGENTS.md` or `docs/adr/` — it has been running by hand, and CI is where a manual gate
stops being a gate. `golangci-lint` is deliberately absent: a versioned external toolchain that will
churn, covering ground `go vet` already covers at this size.

## Alternatives considered

- **`mattn/go-sqlite3` (cgo)** — rejected: see above. It remains the right answer if idle-deck ever
  runs many workers against a large queue, which D8 currently forbids.
- **A container image as the primary artifact** — rejected: it buys dependency isolation from a
  dependency set idle-deck barely has. No inbound surface (D28), one static binary, one SQLite file.
  It also adds an image to rebuild on every Go upgrade, and it would put SQLite state in a volume,
  which is a persistence story to design rather than a file to open.
- **Release binaries per platform as primary** — rejected for the MVP, and the reason is scope, not
  merit: they buy a non-Go user an install at the cost of a release pipeline, a platform matrix, and
  (on macOS) signing and notarization. D4 scopes the MVP to a single user on one machine — the owner
  of this repository, who has Go.
- **A Homebrew formula** — strictly downstream of having something to package, and cheaper than raw
  binaries when it happens, so it is the first thing to add rather than the first.
- **A `cmd/` layout with multiple binaries** — rejected: one binary, one main package.
- **A generated `launchd` plist** — rejected, and noted here because it is the same class of mistake:
  a generator is a second surface to keep in sync with the configuration, bought with a one-time
  copy-paste. The plist is documented instead.
- **`golangci-lint` in CI from day one** — rejected: churn without coverage at this size.

## Consequences

- **Any machine with Go can install and run idle-deck.** No Xcode CLT, no C compiler, no container
  runtime, no package manager.
- **The build is `CGO_ENABLED=0` and must stay that way.** A dependency that reintroduces cgo would
  silently break `go install` for users, so CI's `go build` should assert the flag rather than
  inherit it from the environment.
- **Release engineering is deferred, with the trigger recorded**: the first user who does not have
  Go reopens this ADR. That is a change in D4's scope, so it is a new decision, not a task.
- **The binary owns a directory, not just a file** — the SQLite database and, by default, nothing
  else. Logs go wherever the supervisor sends them.
- **A `v0.x.y` tag is part of the first working release**, not an afterthought: it is what makes the
  install reproducible, and `#10` cannot cut a meaningful tag until there is a binary to tag.
