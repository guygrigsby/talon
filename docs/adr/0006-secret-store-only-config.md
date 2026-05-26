# ADR 0006: Secret-Store References Only In Config

## Status

Accepted

## Context

The native Talon config is now the source of truth for local runtime behavior.
Keeping literal provider keys, bot tokens, gateway tokens, or channel passwords
in that file makes config edits, backups, diagnostics, and logs harder to keep
safe. This repo no longer needs a compatibility migration path for plaintext
secrets because Talon is not yet used by other installations.

## Decision

Persisted config may store only secret references for credentials:

- `op://...` for 1Password items.
- `keychain://...` for OS-specific secret storage on macOS.

`talon config set` and RPC-backed config writes reject plaintext strings at
sensitive leaf keys such as `token`, `password`, `apiKey`, and `botToken`.
Setup flows may accept raw secrets transiently for verification, but they must
store the secret in an OS secret store and write only the resulting reference to
config.

The TOML migration preview never reveals plaintext secrets. Literal secrets
from old config are omitted from the generated TOML with comments telling the
operator to move them to 1Password or the OS keychain.

## Consequences

Users configure providers and channels by first creating a secret-store entry
and then writing the reference into Talon config. Existing plaintext local
config must be cleaned up now rather than carried forward.

Runtime resolution still accepts literal values from CLI flags or environment
variables because those are transient process inputs, not persisted config.
