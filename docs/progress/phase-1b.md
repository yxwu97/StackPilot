# Phase 1B Progress

Status date: 2026-08-18

Phase 1B is verified. The internal packages and the final real BTC Backend lifecycle Gate are complete.

| Package | Status | Result |
| --- | --- | --- |
| P1B-01 Operation foundation | verified | Persisted lifecycle/steps, database workspace lock, 24-hour idempotency, cancellation, concurrent/reopen tests |
| P1B-02 Runner resolution | verified | Maven Wrapper/npm/Java resolution, trusted paths, bounded version preflight, real `.cmd` argument tests |
| P1B-03 Supervisor implementation | verified | Restricted Pipe, strict protocol, identity files, detached reconnect, per-service Job ownership, crash kill-tree tests |
| P1B-04 Process Driver | verified | Canonical resolved specs, executable rehash, hidden suspended launch, double identity proof, graceful/forced full-Job stop |
| P1B-05 Log capture | verified | Bounded spool tail, fail-closed redaction, NDJSON rotation/index, sequence history/cursor, migration 3 |
| P1B-06 readiness engine | verified | Process/TCP/HTTP checks, immediate sequential scheduler, thresholds/timeout/cancel, hardened local HTTP, migration 4 |
| P1B-07 persisted events/SSE | verified | Atomic Operation/Step events, migration 5, non-blocking Broker, catch-up/heartbeat/expired-cursor SSE |
| P1B-08 log API/SSE | verified | Latest/older REST windows, active-ring merge, sequence SSE catch-up, bounded slow-client disconnect, cursor gaps |
| P1B-09 single-service use case | verified | Persisted instance state/events, async root-service start/stop, failure retention/cleanup, cancellation, status and Operation API |
| P1B-10 process fixtures | verified | Standalone slow-ready, immediate-exit, child-tree, ignore-terminate, large-log and port-competition modes |
| P1B-11 BTC Backend integration | verified | Real Maven start, Actuator readiness, logs, exact CORS, forced Job-tree stop, Supervisor exit, and port release passed |

No service or development server uses port 5173. StackPilot uses `32100`, Vite development uses `32101`, and the BTC manifest draft uses `32102` for Web.
