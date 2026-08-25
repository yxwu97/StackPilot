# ADR-0007: Workspace Import, Editing, and Pre-registration Operations

- Status: Accepted
- Date: 2026-08-22
- Decision gate: WI-00 / workspace import and management

## Context

The existing registration endpoint accepts only a workspace that already contains
`.stackpilot/system.yaml`. A first-time import must inspect a bounded subset of BAT
syntax without executing it, let the user confirm a structured draft, atomically
publish a validated manifest, and only then create the workspace catalog record.

Existing runtime Operations require workspace and system foreign keys. Creating a
fake catalog record before a valid manifest exists would weaken those invariants.
Making the existing columns nullable would also force a high-risk rebuild of every
event and port-lease relationship used by the runtime control plane.

## Decision

1. BAT and referenced PowerShell files are read-only migration input. StackPilot never
   runs them and never adds a shell, BAT, PowerShell, or arbitrary executable Runner.
   ADR-0008 defines the only accepted fixed BAT -> PS1 -> Compose source subset.
2. A probe returns `ready_to_register` when the fixed manifest exists and
   `initialization_required` when it does not. The latter is a normal DTO state,
   not an error code. The legacy `POST /api/v1/workspaces` contract remains strict.
3. Pre-registration mutations use dedicated `workspace_import_operations` and
   `workspace_import_operation_steps` tables. They use the same `op_` identifier,
   lifecycle states, idempotency retention, step model, and terminal semantics as
   runtime Operations, but are locked by a SHA-256 canonical target key instead of
   a workspace foreign key. This is the equivalent path scope chosen for imports;
   no fake workspace or system row is created.
4. Import drafts are bounded, expire after 24 hours, and store structured findings,
   relative evidence paths, source digests, and generated previews. BAT contents,
   complete environments, Secrets, and control-plane paths are not persisted.
5. Apply is asynchronous. The operation is durably queued before file mutation.
   Its ordered steps are `verify-source`, `validate-draft`, `stage-manifest`,
   `publish-manifest`, `register-workspace`, and `record-source`. On restart, a
   queued operation is resumed; a running operation is reconciled against the
   target file and catalog before retrying or failing with a stable code.
6. Initial publication requires the target manifest not to exist. Editing requires
   an exact base snapshot digest and an exact current disk digest. Both use a
   same-directory temporary regular file, flush, close, canonical boundary check,
   and atomic rename. A database failure after publication is recoverable because
   the validated manifest is the source of truth and the operation remains
   non-terminal until registration or refresh succeeds.
7. Registered workspace source metadata stores only source type, a workspace-
   relative entry script, its digest, and analysis time. Re-linking never deletes
   the old workspace files and requires a stopped runtime, no active runtime
   Operation, a unique canonical target, and the same system ID.
8. `node` is a process Runner. Windows resolves only a server-configured trusted
   executable or `node.exe` on the service account PATH, probes fixed `--version`,
   and invokes it directly with an argument array. HTTP and manifests cannot
   provide an executable path. The public capability is `workspace.runner.node`.
9. Cocos Creator build remains a separate disabled capability named
   `workspace.runner.cocos`. Import analysis may show a blocked candidate, but it
   cannot be applied until a later accepted Runner design and implementation.
10. The existing synchronous workspace refresh endpoint remains a versioned
    compatibility exception. New import, edit, and relink mutations return a
    persisted Operation reference. A future API version may replace refresh with
    an Operation after existing clients migrate.
11. Use cases remain in `internal/workspace`; pure bounded analysis lives in
    `internal/importer`. API and CLI layers only validate, map, and call these
    use cases.

## Threat Controls

- Every root, script, and referenced file is canonicalized and checked after link
  resolution. Only regular files inside the canonical workspace are read.
- Analysis limits file size, total bytes, file count, reference depth, diagnostics,
  and control-flow structure. It does not expand branch combinations. Unsupported or
  dangerous syntax blocks apply.
- Only ASCII and UTF-8 with optional BOM are accepted. Host environment expansion,
  command substitution, pipes, downloads, registry access, and nested command
  interpreters are rejected.
- Evidence uses relative paths and line numbers. Error details use an allowlist.
- The canonical target lock and idempotency index are database constraints, not
  process-local flags.

## Consequences

- Runtime Operation and event schemas retain their existing non-null workspace
  invariants while pre-registration still has a durable, recoverable operation.
- Operation queries expose a common DTO but dispatch to the runtime or import
  repository by identifier.
- Import workers require bounded ownership and shutdown. A server restart can
  safely resume from the manifest and catalog facts without executing user code.
- Cocos build-and-serve drafts remain visible but blocked; serve-only Node drafts
  can complete when their evidence and loopback exposure are confirmed.

## Rejected Alternatives

- Insert a placeholder workspace/system: it creates catalog state without a valid
  source-of-truth manifest and violates foreign-key meaning.
- Make all runtime Operation and Event scopes nullable: this expands the migration
  and recovery blast radius without benefiting runtime lifecycle operations.
- Run BAT to discover behavior: this is arbitrary code execution during import.
- Generate YAML from unconfirmed inference: this can silently expose ports or start
  the wrong process.
- Accept raw YAML, command strings, or executable paths from Web/API: these bypass
  the structured trust boundary.
