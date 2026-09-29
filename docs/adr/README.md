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
- The **header is not uniformly frozen**, and the fields divide by what they are *for* (D42):

| Field | Rule |
|---|---|
| `Status:` | **Mutable by design.** Flips to `superseded` with a `Superseded by: NNNN` line when a newer ADR replaces it. That flip is its own commit. |
| `Superseded by:` | **Added** by that same flip. It is the mechanism, not an exception to the rule. |
| `Related:` | **Correctable.** It is a navigational index, not a claim — a wrong pointer sends a reader to an irrelevant decision and asserts nothing false. Corrected in its own commit, so the diff is the record. |
| `Title:`, `Supersedes:`, `Source:` | **Immutable.** Title changes what the record is called, `Supersedes:` is the supersession's own assertion, and `Source:` is provenance. |
| `## Decision` onward | **Immutable.** The whole of the argument below the header. |

- **Why this split exists.** The convention used to say "the single mutable property is `Status:`"
  while *the same document* instructed adding a `Superseded by:` line — so the header was already
  gaining fields, and the absolute claim was under-specified rather than merely strict. The gap was
  found the hard way: ADR 0017's `Related:` listed **D31** (the idle-probe decision, unrelated to the
  artifact) and omitted **D32**, its own decision, which ADR 0004 and 0005 both list. Nobody could tell
  whether the header was fair game. See [D42](../../AGENTS.md).
- Numbering is never reused. A superseded ADR keeps its number forever.
- A **navigational correction is not a supersession and not a rewrite.** Nothing about the decision
  changed, so no new ADR is minted and no status flips — which is what distinguishes it from the 0003
  → 0016 case, where a *sentence someone would quote* had become untrue.

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
| [0003](0003-idle-is-a-probe.md) | Idle means: idle-deck can reach the model | superseded by [0016](0016-idle-probed-at-the-harness.md) |
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
| [0016](0016-idle-probed-at-the-harness.md) | Idle is probed at the harness's `/health`, and `/health` must report model reachability | accepted (from D31) |
| [0017](0017-the-artifact.md) | One static cgo-free binary, main package at the repo root, installed with `go install` | accepted (from D32) |
| [0018](0018-framework-agnostic-means-never-naming-a-vendor.md) | "Framework-agnostic" means never naming a model, provider, or vendor; the seam is proved by a conformance suite, not a second adapter | accepted (from D40, D41) |

## Supersessions, and what they cost

The path is now **exercised twice**, and the two cases are not the same shape.

**0007 → 0015** ([GitHub event contract #9](https://github.com/seppaleinen/idle-deck/issues/9)):
polling falsified "the repository comes from the webhook's own origin" outright, so the old ADR's
*mechanism* was simply untrue. Its deny-by-default intent survived as D30. Table tier, D12 → D30, in
the same move.

**0003 → 0016** ([Install, run, configure #11](https://github.com/seppaleinen/idle-deck/issues/11)):
the harder case. "Idle means idle-deck can reach the model" is *still true* — what moved was the
probe, from the inference server to the harness's `/health`. A supersession is not only for when a
decision is falsified; it is for when the part of a decision someone will quote has changed. Had
0016 been written as a table-tier note, ADR 0003 would have remained `accepted` while its own body
said "it probes the inference server" — and the remote contract already said otherwise, so the
project would have held two documents that each looked authoritative and disagreed.

That case also produced a **cross-ticket leak** worth naming as a category: #8 had already routed
the idle probe to `/health` without amending 0003, and it took #11 — a ticket about the *operator
surface* — to notice. A convention that is never exercised has no evidence it works, and a decision
made as a side effect of another ticket can sit inconsistent for a long time.

**Two candidates passed without becoming supersessions**, which is evidence the rule is not
trigger-happy: [Architecture boundaries #6](https://github.com/seppaleinen/idle-deck/issues/6)
**confirmed** D18 (Go interface naming, `Harness` not `BaseHarness`) rather than replacing it, and
[Remote execution service contract #8](https://github.com/seppaleinen/idle-deck/issues/8) decided
the workspace question *under* the harness interface (D27/ADR 0014) instead of changing it — ADR
0013 still stands unchanged. Note the asymmetry with the 0003 case: 0013 was left alone because
nothing in it became untrue, whereas 0003 was superseded because a sentence in it became untrue.
That is the line.

**A third case, and the first *additive* one:** [#12](https://github.com/seppaleinen/idle-deck/issues/12)
answered a question **ADR 0005** had deferred, and the answer was a *narrowing*, not a reversal.
"Framework-agnostic" was carrying two claims; only one was provable, so D40 keeps the provable one and
declines the other. That is what a supersession is **not** for: nothing in 0005 became untrue. 0005
still stands, 0013 still stands, 0014 still stands, and their bodies still say "#12 decides" — which is
correct, because an ADR body is frozen history and 0018 is the answer. Editing 0005 to remove the
pointer would have destroyed the record of *why* the question was open. The claim that needed fixing
lives in the mutable documents, and those are what #12 amended.

ADR 0018 is also the first decision here that **adds a mechanism rather than removing a claim** — a
conformance suite (D41) specified in `docs/architecture/remote-contract.md` §9a, which is what an
adapter must pass before a second adapter is worth writing. The deferral carries a trigger list,
because a deferral without one is an unowned promise.

The ADR-tier decision most likely to be tested next is the [queue
lease](0011-queue-lease.md), when a second queue implementation is considered — and the harness seam's
version of that question is now answered by the same instrument, one ticket earlier than expected.
