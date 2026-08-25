# Phase 1C Progress

Status date: 2026-08-18

Phase 1C is verified. BTC Backend/Web system orchestration, port planning, service restart, Web control flows, and the final real Windows Gate are complete.

| Package | Status | Result |
| --- | --- | --- |
| P1C-01 DAG orchestration | verified | Stable layers, readiness dependency release, bounded parallel work, reverse stop |
| P1C-02 failure policy | verified | Retain/cleanup policy, cancellation cleanup, aggregated and partial-stop failures |
| P1C-03 port planning | verified | Override/sticky/preferred/fallback, strict modes, SQLite leases, real conflict fallback |
| P1C-04 resolved specification | verified | Immutable expanded runtime snapshot, digest, full port propagation |
| P1C-05 system Operations | verified | Async start/stop/restart, cancellation, idempotency, workspace conflict handling |
| P1C-06 service restart | verified | Leaf restart and dependency downstream closure with manifest protection |
| P1C-07 npm/Node runtime | verified | Trusted npm Runner, Vite loopback listen, logs, readiness, complete tree stop |
| P1C-08 system detail Web | verified | Runtime/ports/progress/actions and fresh cross-page REST snapshots |
| P1C-09 service detail Web | verified | Runtime facts, restart, bounded windowed logs, pause/filter/UTF-8 download |
| P1C-10 operation center | verified | Persisted newest-first list and detailed step timeline/cancellation state |
| P1C-11 full BTC integration | verified | Real Backend -> Web readiness, fallback/sticky ports, restarts, logs, reverse stop |

Phase 1 acceptance evidence P1-01 through P1-05 and P1-07 through P1-10 is present across Phase 1B/1C evidence. P1-06 control-plane restart takeover remains intentionally owned by Phase 1D.

Known limitation: the current targeted Windows `CTRL_BREAK` path causes the Maven/JVM process to emit a thread dump and consume its 15-second graceful timeout before Job Object termination. It does not leave a process or port behind. Changing this signal contract requires design and ADR review during a later Supervisor lifecycle change.

No service or development server uses port `5173`. StackPilot defaults to `32100`, frontend development/preview to `32101`, BTC Web to `32102`, and this isolated verification server remains on `32103`.
