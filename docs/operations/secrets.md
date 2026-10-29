# Secrets — macOS Keychain

This document describes how idle-deck stores and retrieves secrets using the macOS Keychain (#68).

## Overview

idle-deck treats `IDLE_DECK_GITHUB_TOKEN` and `IDLE_DECK_HARNESS_TOKEN` as secrets. On macOS, these can be stored in the macOS Keychain under service `idle-deck.secrets` and account names `github.token` and `harness.token`.

Secrets are resolved with the following precedence for the two secret fields only:

```
CLI flags > environment variables > Keychain > YAML > defaults
```

Non-secret configuration fields are not consulted in the Keychain.

The Keychain is a fallback, not a requirement. If a secret is not found in the environment and not present in the Keychain, loading fails with `missing required configuration variables`.

## Closed namespace

Only two keys are valid:

| Key               | Environment variable         | Purpose                          |
|-------------------|------------------------------|----------------------------------|
| `github.token`    | `IDLE_DECK_GITHUB_TOKEN`     | GitHub API access                |
| `harness.token`   | `IDLE_DECK_HARNESS_TOKEN`    | Harness bearer token             |

Attempting to use any other key returns `ErrSecretNotFound` and is rejected.

## CLI commands

```
idle-deck secrets <command> [args]
```

### `idle-deck secrets set <key> [value]`

Store a secret in the Keychain.

- `<key>` must be `github.token` or `harness.token`.
- If `[value]` is omitted, the value is read from stdin (useful for pipelines).
- On macOS, the secret is stored in the Keychain under service `idle-deck.secrets`.
- On non-macOS, the command prints a visible stderr notice and succeeds with a no-op. Secrets are environment variables only.

Example:

```bash
idle-deck secrets set github.token ghp_xxx
# or
cat token.txt | idle-deck secrets set github.token
```

### `idle-deck secrets get <key>`

Retrieve a secret and print its raw value to stdout (no trailing newline added by the command). Exit code 0 on success, 1 if the secret is not found or on error.

On non-macOS, the command prints `secret not found` to stderr and exits 1.

Example:

```bash
idle-deck secrets get github.token
```

### `idle-deck secrets delete <key>`

Remove a secret from the Keychain. On non-macOS this is a silent no-op that succeeds.

Example:

```bash
idle-deck secrets delete harness.token
```

### `idle-deck secrets list`

List stored keys (names only, no values). Keys are printed one per line, sorted alphabetically. On non-macOS this prints nothing and succeeds.

Example:

```bash
$ idle-deck secrets list
github.token
harness.token
```

## Configuration precedence

The Keychain is consulted only for the two secret fields, and only when the environment variable is empty. This means:

1. `IDLE_DECK_GITHUB_TOKEN` set → used, Keychain ignored
2. Env var unset, Keychain has `github.token` → used from Keychain
3. Both unset → loading fails with missing-var error

Secrets are never logged, printed in diagnostics, or written to SQLite (D36).

## Platform differences

| Platform | Behavior |
|----------|----------|
| macOS    | Full Keychain integration via `security` CLI. Secrets stored in `idle-deck.secrets` generic password items. |
| Linux / others | `Keychain` is a noop stub. `set` prints a visible notice and does not store anything. `get` always reports not found. `delete` and `list` are no-ops. All commands succeed without crashing. |

This allows the same binary and tests to run on Linux CI: darwin-specific tests use a fake runner and never invoke `security`.

## Testing

- **High-level tests** (`config/keychain_test.go`): use `fakeKeychain` and run everywhere, verifying `resolveSecrets` precedence.
- **Darwin arg tests** (`config/keychain_darwin_test.go`): run with `//go:build darwin`, use a fake runner to verify that the `security` command is invoked with the correct arguments.
- **Config precedence** (`config/config_test.go`): tests that `Load` falls back to Keychain when env vars are empty, and that env vars win over Keychain.

## Security notes

- The `security` CLI is invoked with secret values passed as `-w <value>` arguments. The value briefly appears in the process command line. This is the design contract for the MVP: using `security` CLI rather than the Keychain C API (CGO-free per D32).
- Secrets are never logged. Redaction in `check` and `status` replaces secret values with `<redacted>`.
- No secrets are read from `.env` files (D36). Secrets live in the environment or the Keychain.
