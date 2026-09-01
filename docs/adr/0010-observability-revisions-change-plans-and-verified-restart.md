# ADR-0010: Observability, Revision Planning, and Verified Restart

- Status: Accepted
- Date: 2026-08-31
- Decision gate: RO-00 / Phase 3C observability and change planning

## Context

StackPilot already persists launch-time Manifest and ResolvedSystemSpec digests,
runtime identities, readiness/liveness results, restart attempts, Incidents, and
Secret metadata versions. It does not yet persist full-tree resource samples,
workspace revision identities, ChangePlans, or verified-restart results. The Windows
Supervisor owns the Job Object but protocol v1 exposes only lifecycle inspection.

The registered control-plane database was inspected read-only on 2026-08-31. It
contained five valid workspaces and 19 services:

| System | Services | Process | Compose | Oneshot |
| --- | ---: | ---: | ---: | ---: |
| AgentHub | 6 | 5 | 1 | 2 |
| AIWS | 5 | 4 | 1 | 1 |
| BTC | 2 | 2 | 0 | 0 |
| GNMarket | 3 | 3 | 0 | 0 |
| PMS | 3 | 3 | 0 | 0 |

All 19 services currently omit explicit liveness and all restart policies remain
"never". This is a hard product fact: Phase 2E has implemented the engine, but the
five-system observation and verified-restart coverage Gate has not passed.

The database contained 3,546 health detail rows, no hourly aggregate rows, and about
803 KiB of health table/index pages. Existing defaults remain the comparison point:
24-hour detail retention, 1,000 recent rows per service instance, 500-row deletion
batches, and hourly retention execution.

An isolated SQLite WAL benchmark used the current 19-service scale and a proposed
resource-sample row/index shape. It did not represent concurrent production load:

| Interval | Rows/day | Database bytes/day | Bulk insert | One-hour query | Hourly aggregate |
| --- | ---: | ---: | ---: | ---: | ---: |
| 10 s | 164,160 | 22,233,088 | 557 ms | 54 ms | 193 ms |
| 30 s | 54,720 | 7,438,336 | 206 ms | 28 ms | 92 ms |
| 60 s | 27,360 | 3,784,704 | 139 ms | 27 ms | 66 ms |

These figures justify a conservative 30-second default, but do not by themselves
authorize publishing resource monitoring. Concurrent lifecycle/health load, real
Job accounting, and Docker stats still require RO-04 Gates.

After the repositories were implemented, a second isolated WAL contention Gate used
the migrated schema, 19 service instances, 2,280 preloaded metric rows, 60 concurrent
19-service metric batches, four bounded compactions, 60 liveness-result writes, and 60
runtime reconciliation writes. The accepted isolated limits are 250 ms p95 and 1 s
maximum for control writes, and 2 s maximum for metric batches/compaction. The
2026-08-31 run passed: health and reconciliation p95 were 0.684 ms and below the
timer resolution, their maxima were 86.956 ms and 41.504 ms, metric-batch p95 was
5.501 ms, and the compaction maximum was 13.189 ms. Evidence is stored in
`docs/evidence/ro04-sqlite-contention.json`. This closes the isolated SQLite contention
check only; it is not a production workload or permission to publish the capability.

## Decision

### Delivery and capabilities

1. Deliver three independently gated milestones:
   - Milestone A: phase3.resource-monitoring after RO-02, RO-03, and RO-04.
   - Milestone B: phase3.change-planning after RO-02, RO-03, and RO-05.
   - Milestone C: phase3.verified-restart after RO-03, RO-05, and RO-06.
2. RO-00 and RO-01 are shared prerequisites. A capability is advertised only after
   its production implementation and real Gate pass. Before then, every new route is
   absent or returns FEATURE_NOT_ENABLED.
3. Capability constants have one Go source of truth. /version, Manifest internal gate
   mapping, API routing, and tests consume that source. Web consumes /version and does
   not duplicate the complete registry.

### Resource metrics

4. RuntimeMetricSample has source process-job or compose, status available,
   unavailable, or unsupported, an observed UTC timestamp, interval, and
   source-specific numeric facts. Missing values remain null and carry a stable safe
   reason; zero is never used as an unavailable sentinel.
5. Process CPU is computed from consecutive Job Object total kernel plus user CPU
   deltas: deltaCPU / deltaWall / logicalProcessorCount * 100. The result is
   machine-normalized and bounded to 0..100. Counter rollback, identity change,
   nonpositive elapsed time, or missing prior sample discards the interval. The first
   sample records cumulative CPU but no percentage.
6. Process memory is the current full-Job committed byte count returned as `JobMemory`
   by `JobObjectMemoryUsageInformation` (information class 28). Active process count and
   cumulative CPU come from `JobObjectBasicAccountingInformation`. If class 28 is not
   supported, the sample is unavailable; StackPilot does not fall back to peak memory or
   root-PID working set and never presents either as the managed tree.
7. Compose sampling uses exact container IDs obtained from persisted project identity
   and StackPilot labels. It performs one bounded non-streaming stats call. Container
   detail is aggregated to the Manifest service by summing CPU and memory and counting
   exact containers. Name-only lookup is forbidden.
8. Defaults and hard limits:
   - sample interval: 30 seconds; configuration range 10 seconds to 5 minutes;
   - detail retention: 24 hours;
   - hourly aggregate retention: 30 days;
   - retention cadence: 1 hour; delete batch: at most 1,000 rows;
   - global workers: 4; one in-flight sample per service instance;
   - queue capacity: 128; sample timeout: 5 seconds;
   - Docker stdout and stderr: at most 1 MiB each;
   - metric query: at most 100 services, 31 days, and 2,000 returned points.
9. The sampler batches one observation cycle into one short transaction. SQLite busy,
   storage pressure, old Supervisor protocol, or Docker failure skips the metric
   sample with a bounded status/log and never blocks start, stop, logs, liveness, or
   reconciliation. There is no unbounded retry queue.
10. High-frequency metric points do not enter domain SSE. Clients use bounded REST
    windows and a minimum 15-second refresh interval.

### Supervisor compatibility

11. Supervisor protocol v2 adds a closed observe-service request and safe resource
    response. Existing v1 lifecycle messages retain their shapes and semantics.
12. The control plane first performs the existing authenticated hello. An attached v1
    Supervisor remains valid for inspect/stop/recover and returns metric status
    unsupported; it is not replaced while it owns services solely to obtain metrics.
13. A newly launched empty Supervisor uses the current protocol. Upgrade takeover
    continues to require account, canonical executable, installation marker, SHA-256,
    PID/creation time, command digest, and protocol checks. Metrics never weaken
    lifecycle identity verification.

### Health coverage

14. RO-03 reuses the completed Phase 2E engine and does not reopen its historical
    Gate. It adds a coverage projection with levels business, container, process-only,
    and unavailable plus the latest safe result.
15. Liveness is always explicit in the Manifest. It is never copied from readiness.
    Process checks prove identity only. Any required daemon with process-only or
    unavailable liveness blocks full verified restart. Oneshot services satisfy
    coverage only by reaching Completed.
16. The current five manifests remain blockers until their service-specific liveness
    declarations and endpoints pass separate real Gates. This ADR does not authorize
    editing external business repositories or enabling automatic restart.
    Health rows persist an explicit readiness or liveness purpose. Migration 000019
    classifies all historical rows and aggregates as readiness because their original
    contract cannot prove recurring liveness. A runtime without a persisted resolved
    health contract reports coverage unavailable rather than inferring it.

### Revision snapshots

17. A SystemRevisionSnapshot is immutable, versioned canonical JSON plus SHA-256.
    Kinds are running and workspace. Equal canonical inputs reuse one row.
18. Running snapshots reference launch-time Manifest/ResolvedSpec and runtime/Secret
    metadata. They never refresh those facts from the current workspace.
19. Workspace collection is read-only and bound to the registered canonical root. It
    may use:
    - fixed Manifest load/strict validation and trusted Runner resolution;
    - fixed git.exe commands "rev-parse --verify HEAD",
      "symbolic-ref --short -q HEAD", and "status --porcelain=v1 -uno";
    - registered Compose identity/version inspection without build, pull, up, or
      Docker Desktop startup;
    - SHA-256 for an allowlist of pom.xml, package.json, supported lockfiles,
      requirements text/lock files, pyproject.toml, go.mod, go.sum, registered Compose
      files, and registered migration file sets.
20. Git is resolved as a trusted regular git.exe, executed without shell, with fixed
    argv, the registered canonical working directory, a 3-second timeout, 256 KiB per
    output stream, and a minimal inherited environment. No HTTP, Manifest, or template
    value can affect executable, argv, or directory. Missing/non-repository/unsafe
    sources are structured unavailable states, never fake revisions.
21. File collection limits are 256 files, 1 MiB per file, and 32 MiB total. Every file
    must be a canonical regular file within the registered workspace. Directories,
    symlink/junction escapes, .env, arbitrary recursive hashing, and file contents are
    excluded. DTOs expose relative allowlist identity and digest, not absolute paths.
22. Canonical JSON uses stable field order, sorted collections, explicit schema
    version, and explicit unavailable states. It contains no Secret values, ordinary
    environment values, complete argv, raw Git/Docker output, or executable paths.

### ChangePlan

23. change-plan is a persisted asynchronous Operation with steps collect-running,
    collect-workspace, compare, classify-risk, and persist-plan. It has no PortLease,
    Secret resolution, process, build, pull, oneshot, or workspace write side effect.
24. Plans compare only two versioned snapshots. Items are stable sorted typed
    differences. Risk levels are info, low, medium, high, and blocked; the aggregate
    is the maximum item risk. Rule version starts at change-risk/v1.
25. change-risk/v1 treats removed required services, missing required business or
    container liveness, invalid Manifest, unsafe source, and unknown running identity
    as blocked. Driver/mode/DAG changes, dirty Git, Runner executable changes,
    migration set changes, Compose image changes, and unavailable identity are at
    least high. Port, restart, Secret-version, dependency-lock, and argument-digest
    changes are at least medium. Unknown never becomes low.
26. Plans are immutable after terminal success. A source change during collection
    fails the Operation and persists no mixed plan. Reuse may occur only for identical
    from/to/rule digests while preserving Operation audit and idempotency semantics.

### Verified restart

27. verified-restart is a distinct persisted Operation. Input is limited to workspace
    and system ownership, ChangePlan ID, and normal idempotency/auth metadata.
28. Before any lifecycle side effect it loads the terminal plan, recollects workspace
    revision, requires exact toDigest, re-evaluates blockers/capability/active
    Operation, and returns CHANGE_PLAN_STALE or CHANGE_PLAN_BLOCKED when applicable.
29. After validation it composes the existing reverse-topology stop and fresh
    start/port-plan/readiness paths. Stop uses the old instance ResolvedSpec; start
    uses the newly resolved candidate. It does not copy either state machine.
30. Success requires every required daemon to stay Ready with successful qualifying
    liveness for a 30-second stability window. Every required oneshot must be
    Completed. Identity replacement, Degraded/Failed/Unknown, or liveness owner loss
    fails verification.
31. Initial release adds no post-start verification Manifest field. Readiness plus
    explicit liveness is the only production verification contract. Browser smoke,
    authenticated flows, external URLs, custom headers, request bodies, and scripts
    remain real Gate activities.
32. Cancellation before stop has no lifecycle side effect. After stop begins, existing
    stop/start cancellation and recovery ownership applies. Failure never performs Git
    changes, database restore, source restore, volume restore, or automatic rollback.
    It preserves truthful runtime state and bounded Operation/Incident evidence.

### Storage and API

33. Migration 000017 introduces revision, plan, service metric detail, and hourly
    aggregate tables and expands the closed Operation type constraint. Migration
    000018 adds constrained Compose container details, and 000019 adds explicit health
    purpose. A verification table is added only if RO-06 proves
    Operation/health/Incident cannot represent required facts without loss.
34. JSON columns require json_valid, schema version, byte limit, and ownership/digest
    uniqueness. Metrics use typed numeric columns rather than an unconstrained JSON
    time-series blob.
35. Error families are METRIC_*, REVISION_*, CHANGE_PLAN_*, and VERIFICATION_*.
    Existing FEATURE_NOT_ENABLED remains the only capability-off response.

## Consequences

- Running facts, candidate facts, planning, and execution remain distinct and
  auditable.
- Old Supervisors continue lifecycle management but cannot produce resource metrics.
- The initial resource policy adds roughly 7.4 MB/day at the current 19-service scale
  before retention/checkpoint effects, then reduces history to hourly aggregates.
- The five registered systems do not yet qualify for verified restart because their
  Manifests have no explicit liveness.
- Change planning and verified restart cannot be marketed as source upgrade, artifact
  deployment, database migration, backup, or rollback.

## Rejected Alternatives

- Sample the root PID: it misstates Maven/npm/Java/Node process-tree resources.
- Replace an active old Supervisor to gain metrics: it risks breaking lifecycle
  ownership.
- Hash the whole workspace or read .env: it is unbounded and expands the Secret
  boundary.
- Let users provide Git refs, commands, paths, or Docker flags: it creates a general
  execution surface.
- Reuse analyze, port-plan, or ordinary restart: their persistence and recovery
  semantics do not represent planning or verified execution.
- Infer risk with a model: execution safety requires deterministic versioned rules.
- Add a general post-start HTTP/script hook: it creates SSRF, Secret, and command
  surfaces before a real need and contract exist.
- Automatically restore source, databases, images, or volumes: StackPilot cannot prove
  those actions safe or complete.
