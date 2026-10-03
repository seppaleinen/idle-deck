# Implementation Plan: Issue #14 – Configuration Layer: Env Vars + Flags, No YAML

## 1. Objective & Scope

Implement the configuration layer for idle-deck per **D34** (env vars + CLI flags, precedence flags > env > defaults) and **D36** (single redaction function, secrets only in environment). Build the `check` command implementing operator-surface §5 steps **1, 4, 6** (required-variable presence, SQLite path writable + schema current, resolved config redacted). This is the first thing the daemon touches at startup.

**Depends on:** #15 (repo skeleton, CI, go module) – already complete.

**Scope:** 
- `config` package: loading, validation, redaction, required-variable tracking
- CLI: `check`, `run`, `status`, `--version` commands (D33)
- `check` command implements operator-surface §5 steps 1, 4, 6 only (steps 2,3,5 deferred – need tracker/harness adapters)
- **No** YAML/TOML/JSON file parser, no `.env` reader, no secrets file (D34, D36)

**D-refs cited:** D23, D34, D36, D37, D33, D30, D43, D44, D45, D14, D17, D28, D29, D15, D21, D6, D8, D9, D13, D18, D32, D19, D22, D4, D10, D11, D1, D2, D7, D20, D24, D25, D26, D27, D40, D41, D42, D5, D31, D16, D12, D35, D38, D39.

---

## 2. Grounding & Key Decisions

| Concern | Decision | Rationale |
|---------|----------|-----------|
| **Config seam** | No `Config` interface in `boundaries.md` (only Tracker/Queue/Harness/Worker/IdlePolicy). Config is a cross-cutting data package, not an adapter seam. `Load()` returns `*Config` directly; no interface needed. | Avoids fake abstraction; YAGNI for interface. |
| **Secrets** | Only `GitHubToken`, `HarnessToken` are secrets (D36). No `--github-token`/`--harness-token` flags (would leak in `ps`). Tokens env-only (D36). |
| **Precedence** | Flags > Env > Defaults (D34). Implemented via `flag.Visit()`: only explicitly set flags override env. |
| **Required vars** | `IDLE_DECK_GITHUB_TOKEN`, `IDLE_DECK_REPOS`, `IDLE_DECK_HARNESS_URL`, `IDLE_DECK_HARNESS_TOKEN` – missing = error listing names (never values). |
| **Repos parse** | Comma-separated `owner/repo`, trimmed, empties dropped. Format validated (one slash, no spaces). **No allowlist gate** – I1/D30 enforced by TrackerSource, not here. |
| **Sweep vars** | `SWEEP_REPOS` ⊆ `REPOS` validated at config load (subset check). `PERIOD=0` disables sweep. Prompt default from §4. |
| **Tiers & budgets** | Stored as `time.Duration` (seconds) and `int` (tokens). Env vars are integer seconds (`900`), not duration strings. Parsed as `int` × `time.Second`. |
| **Redaction** | Single `Redact(cfg Config) Config` → copies config, replaces `GitHubToken`/`HarnessToken` with literal `<redacted>`. Only two secret fields. `check` prints `cfg.Redacted().String()`. |
| **Check command** | Steps 1, 4, 6 only. Steps 2,3,5 deferred (need tracker/harness adapters). Step 4 = path writable + schema current (latter deferred – see Risks). |
| **CLI** | Root `main.go` dispatches `check`/`run`/`status`/`--version` via `flag` package. `--version` prints `idle-deck 0.0.0-dev`. |
| **CI smoke test** | Updated to assert `check` exits non-zero with no config (done-when #1). Current CI expects exit 0 – **must be updated**. |

---

## 3. Work Items (Execution Order)

### W1. `config/config.go` – Rewrite stub to full Config package
**Replace** existing stub. Contains:
- `Config` struct (all fields below)
- `Defaults()` – returns all defaults (exact values from operator-surface §4)
- `FromEnv() (Config, error)` – reads env, applies defaults, validates types, returns missing required names
- `RegisterFlags(fs *flag.FlagSet) *FlagOverrides` – registers all CLI flags (only non-secrets)
- `Load(flags *FlagOverrides) (Config, error)` – precedence: defaults → env → flags (via `flag.Visit`)
- `Validate(cfg Config) error` – missing required vars, type checks (durations, ints, URLs, log level/format, repos format)
- `MissingRequired() []string` – returns names of empty required vars
- `Redact(c Config) Config` – single redaction function (D36), returns copy with `GitHubToken`/`HarnessToken` = `"<redacted>"`
- `Format(c Config) string` – line-oriented `NAME=value` output (secrets already redacted by caller)
- `CheckDB(path string) error` – step 4a: path writable (mkdir + temp file probe); schema check via `queue.SchemaCurrent()` (stub returning nil → "deferred" log line)
- `requiredVars` and `secretFields` package-level constants
- `defaultSweepPrompt` const (exact text from operator-surface §4)
- `defaultDBPath()` expands `~` via `os.UserHomeDir()`

**Flags** (only non-secret, per-run tunables – tokens env-only per D36):
```
--db, --repos, --github-api, --harness-url,
--poll-interval, --max-concurrent-jobs,
--log-level, --log-format,
--tier-timeout-plan/do/sweep/hotfix,
--budget-plan/do/sweep/hotfix,
--github-api,
--retry-backoff-initial, --retry-backoff-max,
--sweep-period, --sweep-repos, --sweep-prompt
```
Flag names = kebab-case of env var suffix (e.g., `IDLE_DECK_TIER_TIMEOUT_PLAN` → `--tier-timeout-plan`).

---

### W2. `config/flags.go`
```go
type FlagOverrides struct {
    DB              *string
    PollInterval    *time.Duration
    MaxConcurrent   *int
    LogLevel        *string
    LogFormat       *string
    GitHubAPI       *string
    HarnessURL      *string
    // Tier timeouts
    TierTimeoutPlan   *time.Duration
    TierTimeoutDo     *time.Duration
    TierTimeoutSweep  *time.Duration
    TierTimeoutHotfix *time.Duration
    // Budgets
    BudgetPlan    *int
    BudgetDo      *int
    BudgetSweep   *int
    BudgetHotfix  *int
    GitHubAPI     *string
    RetryBackoffInitial *time.Duration
    RetryBackoffMax     *time.Duration
    SweepPeriod   *time.Duration
    SweepRepos    *string
    SweepPrompt   *string
}

func RegisterFlags(fs *flag.FlagSet) *FlagOverrides { ... }
```

`RegisterFlags` creates `FlagOverrides` struct, binds pointers to `flag.String/Int/DurationVar` with **zero defaults** (empty = not set). `ApplyFlags(cfg *Config, fo *FlagOverrides) error` – copies only non-nil pointers.

---

### W3. Root `main.go` – CLI Dispatcher (replaces stub)
```go
package main

import (
    "flag"
    "fmt"
    "os"
    "github.com/seppaleinen/idle-deck/config"
)

const version = "0.0.0-dev"

func main() {
    if len(os.Args) < 2 { usage(); os.Exit(2) }
    switch os.Args[1] {
    case "--version", "-version", "version":
        fmt.Printf("idle-deck %s\n", version); return
    case "check": os.Exit(runCheck(os.Args[2:]))
    case "run": os.Exit(runRun(os.Args[2:]))
    case "status": os.Exit(runStatus(os.Args[2:]))
    default: usage(); os.Exit(2)
    }
}

func runCheck(args []string) int {
    fs := flag.NewFlagSet("idle-deck check", flag.ContinueOnError)
    flags := config.RegisterFlags(fs)
    if err := fs.Parse(args); err != nil { return 2 }
    cfg, err := config.Load(flags)
    if err != nil {
        fmt.Fprintln(os.Stderr, err) // prints missing var names or parse errors
        return 1
    }
    // Step 4: SQLite path writable + schema current
    if err := checkDB(cfg.DB); err != nil {
        fmt.Fprintln(os.Stderr, err); return 1
    }
    fmt.Println(cfg.Redacted().String()) // step 6: redacted config
    return 0
}
```

**`checkDB` (step 4):**
```go
func checkDB(path string) error {
    dir := filepath.Dir(expandHome(path))
    if err := os.MkdirAll(dir, 0o755); err != nil { return fmt.Errorf("cannot create dir: %w", err) }
    f, err := os.CreateTemp(dir, ".idle-deck-write-test-*")
    if err != nil { return fmt.Errorf("dir %q not writable: %w", dir, err) }
    f.Close(); os.Remove(f.Name())
    // Schema: defer to storage issue (#10). If DB exists, open and check PRAGMA user_version.
    if _, err := os.Stat(path); err == nil {
        db, _ := sql.Open("sqlite", path)
        defer db.Close()
        var ver int
        _ = db.QueryRow("PRAGMA user_version").Scan(&v)
        fmt.Printf("schema: version %d (expected %d – TODO #10)\n", v, expectedSchemaVersion)
    }
    fmt.Printf("SQLite: path %q writable\n", path)
    return nil
}
```

`runRun(args)` – loads config, validates, exits 1 with missing-vars message (per §10 smoke test). Prints "daemon not implemented (issue #10)" on success.

`status` – stub: prints "status: no state yet (issue #10)", exits 0.

---

### W4. Tests – `config/config_test.go` + `main_test.go`

| Test | Done-when | Description |
|------|-----------|-------------|
| `TestLoad_MissingRequired` | 1 | No env, no flags → `Load` returns `*MissingVarError` with all 4 names |
| `TestLoad_Defaults` | 2 | All four required set → `Load` ok, `Redacted()` has `<redacted>` for tokens, non-secrets visible |
| `TestPollIntervalBad` | 3 | `IDLE_DECK_POLL_INTERVAL=bad` → error names `IDLE_DECK_POLL_INTERVAL` + "duration" |
| `TestFlagOverridesEnv` | 4 | `IDLE_DECK_DB=/env` + `--db=/flag` → `DB == /flag` |
| `TestRedaction` | 2 | `Redact()` replaces both tokens with `<redacted>`, others unchanged |
| `TestNoEnvFile` | 5 | `grep -rn '\.env\|ReadFile\|os.Open' config/` → no matches |

`main_test.go`: `TestCheckNoConfig` (exit 1, names printed), `TestCheckAllSet` (exit 0, redacted output), `TestRunRefusesMissing`, `TestVersion`.

---

### W4. `cmd/idle-deck/main.go` – Retire Placeholder
Replace stub with documentation-only file (no `func main`):
```go
// Package main is retained for layout reference only.
// The idle-deck binary's entry point is the package at the repository root
// (D32). This directory is NOT built as a binary; it exists so the CLI's
// home is discoverable alongside the other packages.
package main
```

---

### W5. CI Smoke Test Update – `.github/workflows/ci.yml`
Change line 29 (`go run . check`) to assert non-zero exit with no config:
```yaml
- name: Smoke test
  run: |
    go run . --version
    go run . check && { echo "FAIL: check should fail without config"; exit 1; } || echo "check refused without config (expected)"
    go run . run 2>/dev/null || echo "run refused as expected"
```

---

## 4. Verification Steps (Mapping to Done-When)

| # | Done-When | Verification Command / Test |
|---|-----------|------------------------------|
| 1 | `check` no env → non-zero, prints 4 names | `env -i idle-deck check; echo $?` → `1`; stdout has 4 names, no values |
| 2 | All 4 set → exit 0, tokens `<redacted>` | `IDLE_DECK_GITHUB_TOKEN=t IDLE_DECK_REPOS=acme/widgets IDLE_DECK_HARNESS_URL=https://h IDLE_DECK_HARNESS_TOKEN=t go run . check; echo $?` → 0; output has `<redacted>` twice, no raw tokens |
| 3 | `IDLE_DECK_POLL_INTERVAL=bad` rejected | `IDLE_DECK_POLL_INTERVAL=bad ... go run . check` → exit 1, stderr has `IDLE_DECK_POLL_INTERVAL` + `duration` |
| 4 | `--db` overrides `IDLE_DECK_DB` | `IDLE_DECK_DB=/env go run . check --db=/flag` → output shows `/flag` value (with required vars set) |
| 5 | No `.env` reader | `grep -rn '\.env\|os\.ReadFile\|os\.Open' config/ cli/` → empty |

Plus standard gates: `go build ./...`, `go vet ./...`, `go test ./...`, D-id check.

---

## 5. Risks & Open Questions

| # | Risk / Question | Mitigation / Decision |
|---|-----------------|------------------------|
| R1 | **CI smoke test conflict** – #15 postmortem says `check` exits 0; #14 requires non-zero. | Update CI smoke test as in W5. Flag as required companion change. |
| R2 | **`Config` seam missing in `boundaries.md`** – issue says "grounded in `boundaries.md` (the `Config` seam)" but no Config seam exists. | Config is cross-cutting, not an adapter seam. Keep as package; if seam needed, create ADR. |
| R3 | **Step 4 "schema current" deferred** – no storage/schema yet. | Implemented path-writable check; schema check deferred with `// TODO(#10)` + print line. Record deferral in issue, ADR, and operator-surface. |
| R4 | **Tokens as flags?** – D36 says secrets are environment only. **Decision:** No flags for `GITHUB_TOKEN`/`HARNESS_TOKEN` (leak in `ps`). |
| R5 | **Sweep repos subset validation** – `IDLE_DECK_SWEEP_REPOS` must be ⊆ `REPOS`. Config validates shape; subset check deferred to sweep scheduler (allowlist gate is TrackerSource). | Document as deferral. |
| R6 | **`cmd/idle-deck/main.go` duplicate main** – Root `main.go` is entry (D32). | Keep stub as reference; update comment to say "real CLI in root". Defer deletion to separate cleanup. |
| R7 | **DB default path on non-macOS** – `~/Library/Application Support/...` is macOS-specific. CI is Ubuntu. `check` fails at step 1 (missing vars) before DB check; dev machines need `IDLE_DECK_DB` override on Linux. Document in dev guide. |
| R8 | **Schema current check** – no schema yet. `check` prints `schema: pending (storage slice #10)`; real check in storage issue. | Document deferral in 3 places (code TODO, issue #14 deferral line, operator-surface §11). |
| R9 | **DB default path on non-macOS** – `~/Library/Application Support/...` is macOS-specific. CI runs on Ubuntu. | `check` step 4 only runs if required vars present (CI sets none → fails early). For local Linux dev, require `IDLE_DECK_DB` override. Document in dev guide. |
| R10 | **`run` / `status` stubs** – not in scope. `run` validates config, prints "daemon not implemented (issue #10)" and exits 1. `status` prints placeholder, exits 0. | Document as TODO #10 / #11. |
| R11 | **`go mod tidy` forbidden** – Knowledge page says strictly forbidden. `go mod` only; `go mod download` before build if needed. | Document in plan. |
| R12 | **D-id verification** – all cited D-ids (D1–D45) exist in AGENTS.md. Verified. | No action. |

---

## File Path Confirmation

Plan written to: **`docs/plans/issue-14-plan.md`**

---

## Trace
```
TRACE: team-lead → dev-team-lead (planning) → [SUCCESS: plan written to docs/plans/issue-14-plan.md]
```