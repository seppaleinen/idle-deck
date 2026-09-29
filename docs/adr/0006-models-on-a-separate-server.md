---
Title:   Models run on a server separate from the workstation
Status:  accepted
Supersedes: —
Related: D20, D5, D7, 0003, 0005
Source:  #3 charting
---

## Decision

Models run on a **server separate from the workstation**. idle-deck orchestrates; it does not host
inference.

## Rationale

Settles the VRAM question from issue #1 — this hardware has no NVIDIA GPU, and local inference on a
Mac was never viable for the models the original design assumed. It also decouples idle-deck's host
machine from the inference server's load.

## Alternatives considered

- Locally-hosted inference (Ollama/LM Studio) — rejected: no NVIDIA GPU, and the model names in #1
  assume a different reality.
- Cloud API as a built-in — rejected for MVP: an API is one form of "remote execution service"; the
  adapter seam (0005) covers it.

## Consequences

- The remote contract (#8) is the primary integration surface.
- The day that a local model becomes viable, it is another harness, not a redesign.
