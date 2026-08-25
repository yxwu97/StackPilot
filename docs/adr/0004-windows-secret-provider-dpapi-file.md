# ADR-0004: Windows Secrets Use Current-User DPAPI Files

- Status: Accepted
- Date: 2026-08-18
- Decision gate: D-12 / P2A-01

## Context

Phase 2 needs system-scoped Secret values that can be set before process launch, versioned without storing plaintext in SQLite, resolved only in memory, and retained across control-plane restart and upgrade. The Phase 1 installation already runs the Server, CLI, Supervisor, and managed processes under one Windows user and has real integration coverage for current-user DPAPI, protected DACLs, and atomic file replacement.

Windows Credential Manager generic credentials were considered first. Its credential blob limit is too small for StackPilot's bounded 64 KiB Secret contract, its global target-name namespace is less naturally tied to one `DATA_DIR`, and isolated upgrade/backup/test behavior would require a second lifecycle boundary. DPAPI files preserve the current single-user threat model while supporting bounded larger values and explicit atomic replacement.

## Decision

1. The Windows provider is named `dpapi-file`. Protected records live below canonical `DATA_DIR/secrets`; the directory and each file have a protected DACL granting full control only to the current user and SYSTEM.
2. `CryptProtectData` uses current-user scope, UI-disabled operation, and fixed application entropy `StackPilot/secret/v1`. The entropy separates Secret blobs from local-auth token blobs; it is context, not an encryption key.
3. A key is `(system_id, name)`. Both components use `^[a-z][a-z0-9-]{0,62}$`. The on-disk filename is SHA-256 over the length-unambiguous `system_id NUL name` key, so user input never becomes a path segment.
4. The encrypted strict-JSON record contains schema version, key, monotonically increasing version, UTC update time, and a value of 1 through 65536 bytes. Identity and metadata are encrypted together. Unknown fields, oversized files, malformed timestamps, key mismatches, non-regular files, and canonical path escapes are rejected.
5. The protected file is the value fact; SQLite `secret_metadata` is a non-sensitive projection containing only system ID, name, provider, version, and update time. A successful write atomically replaces the DPAPI file before advancing the projection. Resolve/metadata reads reconcile a missing or older projection from the protected record. A definite missing file removes stale metadata. Metadata version rollback is rejected.
6. The provider surface is limited to `Set`, `Resolve`, `Metadata`, and `Delete`. `Resolve` returns an owned temporary byte buffer with an explicit `Clear` operation. Later process injection must clear that buffer immediately after constructing the child environment.
7. Delete recalculates the registered hash path, requires a canonical regular file directly below the protected directory, deletes the DPAPI file first, and then removes its metadata projection. Repeating delete is safe.
8. The same-Windows-user boundary is trusted, consistent with ADR-0003 and the Supervisor Pipe model. The provider protects against offline access by other accounts and accidental persistence, not arbitrary code already executing as the owning user.

## Consequences

- Secret values do not enter SQLite, DTOs, events, SSE, logs, Operation snapshots, resolved specifications, or audit records.
- Copying only `stackpilot.db` does not copy usable Secret values. Copying DPAPI files to another Windows user does not make them decryptable.
- A crash between protected-file replacement and SQLite projection update is recoverable without retaining plaintext or introducing a second Secret journal.
- Updating a Secret creates a new version but does not restart existing services. Runtime version recording and stale-instance UI belong to P2A-03.
- macOS Keychain and Linux Secret Service remain Phase 3 decisions behind the same provider interface.

## Rejected Alternatives

- Windows Credential Manager: insufficient value capacity for the accepted bound and a machine-global lifecycle that is harder to isolate by data directory.
- Plaintext or application-key-encrypted SQLite columns: violates the explicit database boundary and makes key storage circular.
- Machine-scope DPAPI: allows unrelated users on the same host to decrypt through machine context and does not match the current-user installation identity.
- Environment variables or manifest literals as storage: leak through process inspection, snapshots, shell history, logs, or source control.
