# Phase 1D Progress

Status date: 2026-08-18

Phase 1D is verified. Recovery, local authentication/browser security, audit, CLI, VSCode entry points, installation, upgrade, and release-candidate recovery paths are verified against real BTC.

| Package | Status | Result |
| --- | --- | --- |
| P1D-01 startup recovery | verified | Operation settlement, identity discovery/reconnect, conservative unknown state, aggregate/lease reconciliation, real BTC takeover |
| P1D-02 log resume | verified | Durable spool offsets, sequence continuation, active-tail/index repair, idempotent recovery, real no-replay check |
| P1D-03 periodic reconciliation | verified | 10s identity/30s lease loops, configurable minimums, active-Operation exclusion, no automatic restart |
| P1D-04 local authentication | verified | Argon2id/DPAPI token, Bearer middleware, one-time bootstrap, session/CSRF refresh, crash-safe rotation |
| P1D-05 browser security | verified | Exact loopback Origin, DNS-rebinding rejection, bound CSRF, JSON/no-store, real Playwright Gate |
| P1D-06 audit | verified | Migration 9, safe mutation/security records, cursor query, accepted/failure/denied semantics |
| P1D-07 CLI MVP | verified | Workspace/up/down/status/logs/wait/open, SSE fallback, JSON, real BTC start/log/stop Gate |
| P1D-08 VSCode Tasks | verified | Current-workspace start/stop/status/open process tasks with valid JSON |
| P1D-09 Web error closure | verified | Durable SSE reconnect, persistent errors, stale manifest/snapshot controls, real BTC and mobile-browser Gate |
| P1D-10 install and upgrade | verified | ADR-0003 user-process mode, immutable checksum versions, HKCU startup, ACL control Pipe, rollback upgrade, external/self uninstall, preserved data, GoReleaser ZIP/checksum |
| P1D-11 security/recovery report | verified | Command/path/browser boundaries, exact-process stop wait, crash takeover, cross-version Supervisor trust, Windows race suite, real BTC install/upgrade/recovery/uninstall Gate |

No service or development server uses port `5173`. The Phase 1 verification control planes, BTC, and the UI fixture are stopped; retained validation data remains under the ignored `.local/` root.

## 2026-08-19 maintenance

Browser session lifecycle hardening now provides a 30-minute sliding renewal window, an eight-hour absolute limit, bounded prior-CSRF grace, cross-tab Web Lock/BroadcastChannel coordination, and unified REST/SSE invalidation. Installed user-task lifecycle logs distinguish proven `control_stop`, `upgrade`, `signal`, `serve_error`, `startup_error`, and `normal_exit` outcomes without adding a watchdog or running marker. Verification evidence is in `docs/evidence/browser-session-lifecycle-hardening-20260819.md`.
