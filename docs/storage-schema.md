# Storage Schema

SQLite migration files under `internal/storage/migrations` are the machine-readable source of truth for the current database schema. Applied files are immutable; every startup compares their SHA-256 checksums with `schema_migrations` and refuses inconsistent history.

## Connection Contract

- Driver: `modernc.org/sqlite` without CGO.
- Journal: WAL.
- Foreign keys: enabled on every pooled connection.
- Busy timeout: 5000 ms.
- Synchronous mode: NORMAL.
- Pool: at most four open and four idle connections.
- Persistent times: UTC RFC 3339 nanosecond strings.

The `server --data-dir` path is converted to an absolute path by the command boundary. The storage layer creates and canonicalizes that directory, then opens `stackpilot.db` within it.

## Migration Metadata

`schema_migrations` is owned by the migration runner:

| Column | Meaning |
| --- | --- |
| `version` | Positive, monotonically increasing migration number |
| `name` | Stable filename suffix |
| `checksum` | Lowercase SHA-256 of the exact migration bytes |
| `applied_at` | UTC RFC 3339 nanosecond timestamp |

The applied rows must be an exact prefix of the embedded migrations. A newer, missing, renamed, reordered, or checksum-mismatched history prevents startup. Each pending migration and its metadata row commit in one transaction.

## Version 1

`000001_system_catalog.sql` creates only the registration and read-only catalog needed by Phase 1A:

| Table | Purpose | Key constraints |
| --- | --- | --- |
| `systems` | Stable system identity and current manifest digest | Primary key `id` |
| `manifest_snapshots` | Immutable normalized manifest snapshots | Primary key `digest`; valid JSON; system foreign key |
| `workspaces` | Registered workspace and last manifest outcome | Primary key `id`; unique canonical path; system/snapshot foreign keys |
| `services` | Service definition summaries for a workspace | Composite primary key; workspace cascade foreign key |

Port leases and local authentication are added by Versions 7 and 8. Phase 2 Secret metadata, per-instance launch versions, and runtime process mode are introduced by Versions 10 through 12; incident storage is introduced only by Version 15.

## Version 2

`000002_operations.sql` adds the Phase 1B Operation lifecycle foundation:

| Table | Purpose | Key constraints |
| --- | --- | --- |
| `operations` | Persisted asynchronous mutations, cancellation metadata, and request idempotency | Primary key `id`; one active Operation per workspace; scoped idempotency key uniqueness |
| `operation_steps` | Stable ordered progress steps and retry attempts | Primary key `(operation_id, step_no)`; unique step key per Operation; Operation cascade foreign key |

Active workspace ownership is represented by the partial unique index covering `queued`, `running`, and `cancelling`; reaching a terminal state releases that constraint. Idempotency keys are scoped by caller subject, route semantics, and workspace. Their expiry is stored as a UTC timestamp and the key is cleared after the 24-hour retention window without deleting Operation history.

## Version 3

`000003_runtime_logs.sql` adds the runtime identity and closed-log-segment foundation used by the Phase 1B Process Driver and Log Manager:

| Table | Purpose | Key constraints |
| --- | --- | --- |
| `system_instances` | Immutable manifest/resolved-spec runtime scope and aggregate state | One non-stopped instance per workspace |
| `service_instances` | Per-start service state and verified process identity | Unique service per system instance; optimistic `state_version` |
| `log_segments` | Closed NDJSON segment locator and sequence/time bounds | Unique registered path; indexed service-instance sequence range |

ServiceInstance identifiers use the documented `svi_<ULID>` form. Raw stdout/stderr and final log message bodies remain outside SQLite. The `platform_token` is an internal recovery value and must never be included in API DTOs, events, logs, or diagnostics.

## Version 4

`000004_health_results.sql` adds the bounded readiness evidence used by the Phase 1B Health Engine:

| Table | Purpose | Key constraints |
| --- | --- | --- |
| `health_results` | Completed process/TCP/HTTP check outcomes | Service-instance cascade foreign key; success/error consistency; indexed newest-first by instance/time |

Only redacted summaries of at most 2 KiB are persisted. Raw HTTP response bodies and the optional in-memory HTTP status field are not stored. Query limits are bounded by the repository; Phase 2 adds retention and aggregation when liveness enables continuous checks.

## Version 5

`000005_events.sql` adds the durable low-frequency domain event stream used by Phase 1B SSE:

| Table | Purpose | Key constraints |
| --- | --- | --- |
| `events` | Operation, service state, health-state, and audit event history | Monotonic integer cursor; structured object JSON; workspace/system scope; optional runtime/Operation scope; indexed catch-up paths |

Operation and OperationStep updates insert their corresponding event through the same SQLite connection and transaction. The in-memory broker receives only the committed event ID after commit and is never the source of truth. Event payloads are bounded structured JSON and do not contain request bodies, commands, environments, platform tokens, or raw logs.

SystemInstance and ServiceInstance creation and every service/system state transition use the same transaction pattern. Service updates include `WHERE id = ? AND state_version = ?` and increment the version exactly once; a stale writer receives a state conflict instead of overwriting a newer health, stop, or reconciliation result.

## Version 6

`000006_service_stop_policy.sql` adds the bounded `service_instances.graceful_timeout_ms` runtime snapshot. Stop and cancellation use the value resolved at start time rather than a refreshed manifest or a hard-coded timeout. Existing instances upgrade to the documented 15-second default; new instances persist their validated service policy.

## Version 7

`000007_port_plans.sql` adds the Phase 1C whole-system port plan and resolved-spec persistence foundation:

| Table | Purpose | Key constraints |
| --- | --- | --- |
| `resolved_system_specs` | Immutable, non-secret expanded runtime specifications | SHA-256 primary key; valid object JSON; workspace and manifest foreign keys |
| `workspace_port_overrides` | Explicit per-workspace TCP preferences | One logical name per workspace; loopback-only; bounded port |
| `sticky_port_history` | Last successful bound endpoint per logical port | One logical name per workspace; manifest provenance |
| `port_leases` | Reserved and bound endpoint ownership | Unique logical name per plan; partial unique active endpoint index; constrained lifecycle state |

The active endpoint index covers only `reserved` and `bound`, so released and expired history remains queryable without blocking reuse. Reserved rows use a bounded expiration timestamp; only stale reserved rows are expired automatically. Bound rows require explicit OS ownership verification before release. A successful system start copies bound endpoints into sticky history.

## Version 8

`000008_local_auth.sql` adds the Phase 1D local-token hash registry:

| Table | Purpose | Key constraints |
| --- | --- | --- |
| `auth_tokens` | Argon2id hashes and safe use/revocation metadata for local CLI authentication | Primary key `id`; one active token; UTC timestamps |

SQLite never stores the plaintext local token. On Windows the token is encrypted for the current user with DPAPI and written below `DATA_DIR/auth` with a protected DACL granting access only to that user and SYSTEM. Startup refuses to serve when an active database hash exists but the protected token is missing or does not match. Browser bootstrap codes, sessions, and CSRF values are bounded in-memory credentials and are intentionally invalidated by Server restart.

## Version 9

`000009_security_audit.sql` adds crash-safe token rotation and immutable Phase 1D audit records:

| Table | Purpose | Key constraints |
| --- | --- | --- |
| `auth_token_rotation` | Single pending hash journal bridging SQLite and atomic DPAPI-file replacement | Fixed singleton key; no plaintext token |
| `audit_events` | Authentication and control mutation audit trail | Monotonic cursor; bounded vocabulary; indexed time, action, and Operation lookup |

Token rotation first commits the pending hash, then atomically replaces the DPAPI file, then revokes the prior hash and activates the pending hash in one SQLite transaction. Startup either completes the pending rotation when secure storage matches it or clears an unstarted journal when secure storage still matches the active hash. Audit rows contain subject/client type, safe action and target identifiers, request result, trace/Operation correlation, error code, and UTC time; request bodies, paths supplied in bodies, headers, cookies, tokens, commands, and environments are never stored.

## Version 10

`000010_secret_metadata.sql` adds the P2A-01 non-sensitive Secret projection:

| Table | Purpose | Key constraints |
| --- | --- | --- |
| `secret_metadata` | System-scoped DPAPI provider identity, monotonic version, and UTC update time | Primary key `(system_id, name)`; provider fixed to `dpapi-file`; version at least 1; no value column |

The current-user DPAPI file below canonical `DATA_DIR/secrets` is the value fact. SQLite never stores plaintext, ciphertext, a decryptable key, or a value digest. Provider reads reconcile a missing/older projection from the protected record and reject version rollback. The table intentionally has no `systems` foreign key so a Secret may be provisioned before workspace registration and remain through workspace removal; explicit Secret deletion owns its lifecycle.

## Version 11

`000011_service_secret_versions.sql` adds the P2A-03 non-sensitive launch projection:

| Table | Purpose | Key constraints |
| --- | --- | --- |
| `service_instance_secret_versions` | Environment name, Secret key, provider/version, and resolution time used by a service launch | Primary key `(service_instance_id, environment_name)`; service-instance cascade foreign key; version at least 1; monotonic repository update |

The table contains neither plaintext, ciphertext, nor a value digest. A restart of the same service instance may advance a recorded version but cannot roll it back. Reconciliation compares the current protected value's version with this projection before resuming exact-value log capture; a mismatch fails closed.

## Version 12

`000012_service_instance_process_mode.sql` adds the required `service_instances.process_mode` runtime snapshot constrained to `daemon` or `oneshot`. Existing rows upgrade to `daemon`; new rows persist the validated launch mode. Reconciliation uses this immutable value to interpret an observed process exit without consulting a refreshed manifest, allowing exit code `0` to restore a oneshot as Completed while an exited daemon remains a failure.

`000013_compose_health_results.sql` rebuilds `health_results` with the same columns, foreign keys, outcome invariant, and index while extending the closed `kind` set with `compose`. Existing process/TCP/HTTP rows and IDs are copied unchanged before the legacy table is dropped.

## Version 14

`000014_service_instance_driver.sql` adds the immutable `service_instances.driver` runtime snapshot and the nullable `compose_project_token`. Historical rows upgrade to `driver='process'`. The opaque Compose token is bounded to 64 KiB and is valid only for Compose instances; process PID identity remains stored in its existing dedicated columns and is never synthesized for containers.

## Version 15

`000015_incidents_and_health_aggregation.sql` adds `incidents`, `incident_analyses`, `health_hourly_aggregates`, and `service_restart_attempts`. One partial unique index permits only one open incident per deterministic fingerprint. Incident evidence references existing event and health cursors, analysis payloads remain versioned JSON objects, and restart attempts are persisted per service instance. Continuous health details retain a bounded recent window while older rows are compacted into per-service, per-kind hourly counts and duration aggregates.
## Workspace Import And Editing

Migration `000016_workspace_imports.sql` adds four bounded control-plane tables:

- `workspace_drafts` stores 24-hour structured import/edit/relink drafts, canonical target hashes, relative source evidence, and base manifest digests. It never stores BAT source text or Secret values.
- `workspace_import_operations` and `workspace_import_operation_steps` provide durable pre-registration `op_` lifecycle state and a unique active canonical-target lock without creating placeholder workspace/system rows.
- `workspace_sources` records existing-manifest, BAT import, structured-edit, or relink provenance and an optional relative BAT entry/digest.

Import and edit publication is recoverable across the file/SQLite boundary: the Operation is queued first, a same-directory file is flushed and atomically published, then the workspace snapshot/source projection is committed. Relink uses the same path-scoped Operation and atomically updates only the catalog path/snapshot after revalidating the same System ID; old workspace files are never deleted. A restarted worker reconciles the fixed manifest and catalog before completing the remaining steps.

## Version 17

`000017_observability_change_planning.sql` extends the closed `operations.type` constraint with
`change-plan` and `verified-restart` while preserving all historical Operation rows and their step,
event, and port-lease foreign keys. The migration uses the migration runner's explicit
`stackpilot:foreign-keys-off` directive on one dedicated connection, performs the parent-table rebuild
inside one transaction, runs `foreign_key_check` before commit, and restores foreign-key enforcement
before returning the connection to the pool. Ordinary migrations continue with foreign keys enabled.

The migration adds four focused tables:

| Table | Purpose | Key constraints |
| --- | --- | --- |
| `system_revision_snapshots` | Immutable running or workspace canonical revision JSON | `rev_<ULID>` ID; unique SHA-256; running snapshots require a system instance; valid object JSON no larger than 4 MiB |
| `change_plans` | Immutable deterministic comparison of two revision snapshots | `plan_<ULID>` ID; unique creator Operation; `change-risk/v1`-style bounded version; blocked state/risk/count consistency; valid result JSON no larger than 4 MiB |
| `runtime_metric_samples` | Bounded process-job or Compose detail samples | Unique service/source/time; 10 seconds to 5 minutes interval; explicit available/unavailable/unsupported state; typed nonnegative counters; CPU bounded to 0-100 |
| `runtime_metric_hourly_aggregates` | Hourly count, CPU, and memory aggregates | Primary key by service/source/hour; available/sample count invariants; paired min/max/total fields |

Metric absence is never stored as a zero measurement: unavailable and unsupported samples require a
safe reason code and null numeric fields. The new tables contain no command, environment, Secret value,
absolute workspace path, raw Git/Docker output, or source file content. No verification table is created;
RO-06 must first prove that Operation, health, and Incident evidence cannot represent the required result.

## Version 18

`000018_container_metric_details.sql` adds `runtime_container_metric_samples` for bounded
per-container Compose observations. Rows are children of one service-level metric sample and
are deleted with that sample. A database trigger rejects details whose parent is not an
`available` `compose` sample, so process metrics cannot be mislabeled as container facts.

## Version 19

`000019_health_result_purpose.sql` adds the closed `readiness|liveness` purpose to
`health_results` and to the hourly aggregate key. Historical rows and aggregates are
conservatively classified as readiness because their original schema cannot prove which
recurring checks were liveness; no historical liveness evidence is inferred.
