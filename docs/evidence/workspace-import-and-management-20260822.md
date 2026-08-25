# Workspace Import And Management Verification Evidence

Date: 2026-08-22  
Plan: `plan/plan-20260819-01-workspace-import-and-management.md`  
Decision: ADR-0007

## Delivered Contract

- Missing fixed manifests now enter the normal `initialization_required` probe flow.
- BAT analysis is read-only and bounded, detects nested references and cycles, recognizes Maven/npm/Java/Node commands, and blocks dangerous, unsupported, or unsafe-exposure evidence.
- Structured corrections create a new validated draft; API DTOs omit internal Manifest objects and service environments.
- Import, edit, and same-System-ID relink apply through durable path-scoped Operations with idempotency and a SQLite active-target lock.
- Workspace details, structured edits, relink previews, CLI action-required handoff, and Web flows are implemented.
- Node runs directly through the trusted resolver and Process Driver. Cocos remains disabled as the separate `workspace.runner.cocos` capability.

## Automated Gates

| Command / gate | Result |
| --- | --- |
| `go test ./...` with host Windows permissions | Passed, including API contract, migrations, SQLite repositories, Supervisor, DPAPI, Process Driver, importer, and Web embed tests |
| `go vet ./...` | Passed |
| `npm run test:web` | Passed, 24 tests |
| `npm run type-check` | Passed |
| `npm run build` | Passed; `dist/stackpilot.exe` version 0.1.0 |
| `TestWorkspaceImportAndStructuredEditWithRealSQLite` | Passed import, idempotency, atomic publication, registration, edit, relink, and old-file preservation |
| `TestWorkspaceImportCanonicalTargetLockIsDatabaseEnforced` | Passed; one concurrent create succeeded and one received the database lock conflict |
| `TestNodeRunnerStartReadyLogsAndStopsProcessTree` | Passed with real `node.exe`, loopback TCP readiness, stdout spool, and child-process exit |
| `TestWFGameReadOnlyGate` with `STACKPILOT_WFGAME_PATH=E:\WFGame` | Passed; exactly two candidates and both correctly blocked |

The first sandboxed full Go run failed Windows ACL, Named Pipe, and DPAPI tests because the sandbox denied host security operations. The required final run used host Windows permissions and passed the same suites; there is no test waiver.

## Browser Gate

Playwright CLI exercised the production Vue assets against the local UI fixture at desktop and 390 x 844 mobile viewports:

- path probe, BAT selection, analysis, structured correction controls, YAML preview, and apply progress;
- workspace detail, source/services/ports/YAML views;
- structured edit preview/apply;
- relink path validation and preview;
- responsive correction of mobile table and number-input clipping.

Screenshots are stored in `output/playwright/workspace-import-desktop.png` and `output/playwright/workspace-import-mobile-final.png`. The fixture intentionally returns no SSE stream, so the visible reconnect warning is expected and is not used as a product success signal.

## WFGame Gate

The authorized check was read-only. It did not create `.stackpilot/system.yaml` or modify project files. Analysis found `serve-existing` and `build-and-serve`. `tools/serve.js` does not prove loopback-only binding, so unsafe exposure blocks apply; the build candidate is additionally blocked by the disabled Cocos capability. This is the required safe outcome, not a failed import implementation.
