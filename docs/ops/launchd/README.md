# Supervision: launchd LaunchAgent (D35)

idle-deck is a personal tool acting as your GitHub identity, so it runs as a
**LaunchAgent** — per-user, in `~/Library/LaunchAgents/`, starts at login, no
root — rather than a LaunchDaemon (system-wide, starts at boot, needs root).

**No plist generator subcommand.** A generator is a second surface to keep in
sync with the config surface, bought with a one-time copy-paste. This file is
the plist; the operator copies and adjusts it. That is the design.

## The plist

`com.seppaleinen.idle-deck.plist` (in this directory) is the canonical copy.
It is also rendered inline in
[`docs/operations/operator-surface.md`](../operations/operator-surface.md) §3.

## Install

```bash
# 1. Install the binary (D32: go install, static cgo-free).
go install github.com/seppaleinen/idle-deck@latest

# 2. Copy the plist and adjust the two absolute paths:
#      - ProgramArguments[0]: path to the idle-deck binary
#      - StandardOutPath / StandardErrorPath: log directory
#    launchd does NOT expand ~. Every path is absolute.
cp com.seppaleinen.idle-deck.plist ~/Library/LaunchAgents/com.seppaleinen.idle-deck.plist
chmod 600 ~/Library/LaunchAgents/com.seppaleinen.idle-deck.plist

# 3. Create the log directory. launchd does not create it, and the job
#    refuses to start without it.
mkdir -p ~/Library/Logs/idle-deck

# 4. Bootstrap the job into the user session.
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/com.seppaleinen.idle-deck.plist
```

The daemon starts at login and runs `idle-deck run` in the foreground. It
restarts on crash and stops cleanly on `SIGTERM` (which idle-deck turns into
context cancellation).

## Uninstall

```bash
launchctl bootout gui/$(id -u)/com.seppaleinen.idle-deck
```

`bootout` is the stop path. There is no `idle-deck stop` command (**D33**):
`launchctl bootout` is the stop, and it is the only one.

## Four mechanical details that bite

1. **`launchd` does not expand `~`.** Every path in the plist is absolute —
   the binary, the log files, everything. A `~/` anywhere silently fails.

2. **The log directory must exist** or the job refuses to start. `mkdir -p`
   it before `launchctl bootstrap`.

3. **`KeepAlive` is `SuccessfulExit: false`, not `true`.** Plain `true` would
   respawn the daemon even after a deliberate clean stop. This is the
   "restart on crash, respect the exit" form.

4. **`ThrottleInterval`** prevents a crash loop from becoming a spin. Without
   it, a crashing daemon costs unbounded launchd CPU; with it, a crash costs
   one retry per interval.

## Secrets (D36)

The two tokens live in `EnvironmentVariables`. The plist itself is
`chmod 600` and is read by `launchd`, never by idle-deck. Rotation is
rotate-and-restart: edit the plist, `bootout`, `bootstrap`. A token that
expires mid-run is an ordinary retryable failure (**D14**).

## Related

- [`docs/operations/operator-surface.md`](../operations/operator-surface.md) §3
  — the operator surface, with the plist rendered inline.
- [`README.md`](../../README.md) — the user-facing summary.
- **D35** in `AGENTS.md`.