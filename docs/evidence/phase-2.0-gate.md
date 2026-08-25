# Phase 2.0 AIWS Release Gate

Status date: 2026-08-18

Phase 2.0 is verified. No release waiver is required.

| Gate criterion | Result | Evidence |
| --- | --- | --- |
| AIWS Compose lifecycle, recovery, logs, and volume-preserving ordinary stop | verified | P2B-03 through P2B-07, P2C-02, P2C-06, P2C-07 |
| Keycloak Configure releases downstream only after exit 0 | verified | P2A-04, P2A-05, P2C-03, P2C-07 |
| OIDC, Web/API, callback/logout, origins, and proxy share one port plan | verified | P2C-04, P2C-05, P2C-07 |
| Secret values stay out of SQLite, logs, SSE, snapshots, errors, and repository files | verified | P2A-01 through P2A-03, real AIWS Gate, non-printing value scan |
| Current BTC regression passes and non-Compose assembly does not require Docker | verified | current `verify-p1d-11.ps1` run; `TestOrchestrationAssemblyDoesNotRequireDocker` |
| Windows MVP databases upgrade through Phase 2 migrations | verified | production migration v1-v13 matrix, empty/repeat/checksum tests |

## Real AIWS Gate

The rebuilt candidate ran the complete real `E:\AIWorkflowStudio` DAG twice during final UI verification. The accepted final run produced:

```json
{
  "workspaceId": "ws_01M0AA546FYGA7JEAAFNVGJWXF",
  "instanceId": "si_01M0AA54C7H80C9HEHQQ8F0RPY",
  "startOperationId": "op_01M0AA54C4V740YQVYMT2XE9X4",
  "repeatOperationId": "op_01M0AA755PN91RN5A4057R5EV8",
  "stopOperationId": "op_01M0AABT5DHSQ7CXZM1SGXWS4S",
  "services": 5,
  "ports": 13,
  "recovery": "same instance",
  "endpoints": "server/runtime/web/oidc ready"
}
```

The authenticated production Web build showed `5 / 5`, the oneshot `已完成` state, six healthy containers, thirteen ports, live Compose logs, and the `6173` Web entry. Desktop and mobile browser checks passed without document overflow.

## Current BTC Regression

Candidates `0.2.0-gate.1` and `0.2.0-gate.2` ran the real `E:\BidTravelCloud` recovery and upgrade Gate:

```text
instance=si_01M0AAZ54B5EY1TJF3NSY34RS7
backend PID=32116 port=8081
web PID=20300 port=32102
control PID=28872 -> 18440 -> 35228
log sequence=62 -> 62
start=op_01M0AAZ54713632YNZM6Z244T7
stop=op_01M0AAZXHBE1TFS0AJF96HN9V7
```

The same instance and business PIDs survived control-plane termination and cross-version takeover. Reverse stop removed both process trees and ports; uninstall removed the user task, installation, and startup registration. The isolated preserved test database and candidate binaries were then removed after canonical path validation.

## Engineering Gate

- `scripts/check.ps1`: Web tests 6/6, strict Vue/TypeScript check, Vite build, all Go tests, and `go vet ./...` passed.
- `go test -race ./cmd/stackpilot ./internal/... -count=1`: passed with repository-local MinGW 16.1.0.
- Production migration upgrades from versions 1-13, empty database, repeat open, and checksum mismatch passed.
- `CGO_ENABLED=0 GOOS=linux|darwin GOARCH=amd64 go build ./...`: both compile-boundary checks passed; no runtime-support claim is made.
- The exact AIWS Secret values were compared in-process against all non-ignored repository files; no match was found and values were not printed.
- `git diff --check` passed; `AGENTS.md` and `CLAUDE.md` SHA-256 values match.
- Port `5173` appears only in explicit non-use or rejection documentation/tests. Runtime ports use 32100/32101/32102, AIWS 6173, and isolated Gate ports 32140/32141.
- Final cleanup found no related listener, process, user startup entry, AIWS container/network/volume, isolated data root, or UI marker.

All Phase 2A, 2B, and 2C work packages and the Phase 2.0 release Gate are verified. Phase 2D may begin.

