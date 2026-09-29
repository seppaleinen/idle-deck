# Architectural Decision Records — idle-deck

This directory is the **ADR tier** of idle-deck's decision record. It holds the decisions that are
expensive to reverse — the ones whose change would ripple through two or more other decisions.

The full convention is defined by [Decision record #7](https://github.com/seppaleinen/idle-deck/issues/7).
The table of *all* decisions, ADR-tier and table-tier alike, lives in
[`AGENTS.md`](../../AGENTS.md), which is the index. This directory is where the ADR-tier detail lives.

## Which tier does a decision get?

At adoption time (issue #7), existing decisions were split by this rule:

> **Does changing this decision change how two or more other decisions behave?**

- **Yes → ADR tier.** A file in this directory, with full context.
- **No → table tier.** A one-line row in `AGENTS.md`, with rationale in the same row.

From adoption onward, the rule is applied when a decision is made. A decision may **move up** if it
later becomes multi-behavioural (and is then written as a new ADR). It never moves **down**: an ADR
is not demoted to a one-liner, it simply is an ADR.

## File format

```
docs/adr/NNNN-title-with-dashes.md
```

- `NNNN` is zero-padded, sequential, never reused. The first real ADR is `0001`.
- Title is `kebab-case`.

Body:

```markdown
---
Title:
Status:  accepted          ← the ONE mutable field
Supersedes: —              ← filled when this decision replaces an older one
Related: D<n>[, I<n>]      ← ids this decision interplays with
Source:  <ticket/PR>       ← the discussion that resolved it
---

## Decision
## Rationale
## Alternatives considered
## Consequences
```

### Immutability

- The **body is immutable.** Rationale, alternatives, and consequences are frozen history.
- The **single mutable property is `Status:`** in the header. It flips to `superseded` with a
  `Superseded by: NNNN` line when a newer ADR replaces it. That flip is its own commit — recorded in
  git history like everything else.
- Numbering is never reused. A superseded ADR keeps its number forever.

## Statuses

Exactly two:

| Status | Meaning |
|---|---|
| `accepted` | Current. Implementation must follow it (binding, per D7-issue #7 — enforcement weight varies by tier). |
| `superseded` | Replaced by a newer decision, pointed at by `Superseded by: NNNN`. Kept for history, never edited. |

There is no `proposed`, no `deprecated`, no `rejected` state in this record. A decision that turns
out to be wrong is superseded by a new one — that is its own story.

## Superseding

Making a new decision is the act that flips the old one:

1. **New** ADR gets the next number, with `Supersedes: <old NNNN>` in its header.
2. **Old** ADR's `Status:` flips to `superseded`, and a `Superseded by: <new NNNN>` line is added.
   The body is untouched.
3. `AGENTS.md` marks the old row `superseded` with a pointer to the new id.

Example — a pair contrived to demonstrate the mechanics, **not a real decision**:

```markdown
---
Title:   Use YAML for all configuration
Status:  superseded
Superseded by: 0002
Related: D23
---

## Decision
All configuration is written in a YAML file.
```

```markdown
---
Title:   Env-first configuration
Status:  accepted
Supersedes: 0001
Related: D23
---

## Decision
Configuration is env-first: environment variables, optional YAML file, CLI flags override.
```

## The verification check

**Before any push that touches `AGENTS.md` or `docs/adr/`:** every `D<n>` cited anywhere in the repo
must resolve to a definition in `AGENTS.md`. Run:

```sh
grep -oh 'D[0-9]\+' --exclude-dir=.git -r . | sort -u > /tmp/cited
grep -rho '^\| \*\*D[0-9]\+\*\*' --include=AGENTS.md . | grep -o 'D[0-9]\+' | sort -u > /tmp/defined
comm -23 /tmp/cited /tmp/defined   # must be empty
```

(Definitions are the bold `**D<n>**` ids that start an AGENTS.md table row — `| **D<n>**`. The
`--include=AGENTS.md` grep matches row-start bold ids only, not plain citations.)

Fail if any id appears in `/tmp/cited` but not `/tmp/defined`. The D10/D11 defect — ids cited before
they were defined — is exactly what this prevents. When CI lands (#11), this becomes a CI job.

## ADR log

| Number | Title | Status |
|---|---|---|
| [0001](0001-ontology-revisable.md) | Issue #1's ontology is a revisable draft | accepted (from D10/D11) |
| [0002](0002-ontology-standalone-document.md) | The ontology is a standalone document | accepted (from D10/D11) |
| [0003](0003-idle-is-a-probe.md) | Idle means: idle-deck can reach the model | accepted (from D5) |
| [0004](0004-never-name-a-model.md) | idle-deck never names a model | accepted (from D6) |
| [0005](0005-one-harness-adapter.md) | Exactly one harness adapter in the MVP | accepted (from D7) |
| [0006](0006-models-on-a-separate-server.md) | Models run on a server separate from the workstation | accepted (from D20) |
| [0007](0007-multi-repo-event-driven.md) | Multi-repo, event-driven, deny-by-default allowlist | superseded by [0015](0015-github-event-contract.md) |
| [0008](0008-minimal-interfaces-one-implementation.md) | Minimal interfaces, one concrete implementation each | accepted (from D13) |
| [0009](0009-two-retries-then-escalate.md) | Two automated retries, then escalate | accepted (from D14) |
| [0010](0010-go.md) | Go 1.27 | accepted (from D9) |
| [0011](0011-queue-lease.md) | Queue dequeue is a lease, never a pop | accepted (from D24) |
| [0012](0012-tracker-split.md) | Tracker split: inbound parser and outbound writer are separate interfaces | accepted (from D25) |
| [0013](0013-harness-lifecycle.md) | Harness interface is execution lifecycle only; workspace ownership deferred to #8 | accepted (from D26) |
| [0014](0014-remote-execution-service-contract.md) | Remote execution service contract: workspace is remote, contract is typed and pull-based | accepted (from D27) |
| [0015](0015-github-event-contract.md) | GitHub event contract: idle-deck polls, labels trigger tiers, dedupe is a stable resource key | accepted (from D28, D29, D30) |

## The first real supersession

The supersede path is now **exercised**. [GitHub event contract
#9](https://github.com/seppaleinen/idle-deck/issues/9) chose polling, which falsified ADR 0007's
"the repository comes from the webhook's own origin" — so 0007 is `superseded` and 0015 carries the
decision forward, with 0007's body untouched and its deny-by-default intent preserved in D30. Table
tier, D12 → D30, in the same move.

Two earlier candidates passed without becoming supersessions, which is worth recording as evidence the
rule is not trigger-happy: [Architecture boundaries
#6](https://github.com/seppaleinen/idle-deck/issues/6) **confirmed** D18 (Go interface naming,
`Harness` not `BaseHarness`) rather than replacing it, and [Remote execution service
contract #8](https://github.com/seppaleinen/idle-deck/issues/8) decided the workspace question
*under* the harness interface (D27/ADR 0014) instead of changing it — ADR 0013 still stands
unchanged. The ADR-tier decision most likely to be tested next is the [queue
lease](0011-queue-lease.md), when a second queue implementation is considered.