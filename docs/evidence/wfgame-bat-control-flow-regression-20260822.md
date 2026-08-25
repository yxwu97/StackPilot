# WFGame BAT Control-flow Regression Evidence

Date: 2026-08-22  
Scope: read-only workspace import analysis  
Related decision: ADR-0007  

## Incident

Analyzing `E:\WFGame\run.bat` returned `WORKSPACE_SCRIPT_SYNTAX_UNSUPPORTED`. The real read-only Gate reproduced the internal cause as `unmatched closing parenthesis`.

The parser recognized only `if errorlevel` blocks. It did not enter a block for the already specified safe subset `if not exist <literal> (` and therefore treated that block's closing `)` as unmatched. Fixed MODE comparisons and label/goto flow in the same script were also not modeled.

## Correction

- Added closed parsing for `if exist/not exist`, numeric errorlevel conditions, and controlled string equality conditions.
- Added fixed label/goto collection with label existence, duplicate-label, jump-count, and backward-jump checks. Dynamic and backward jumps remain rejected.
- Allowed only diagnostic/control statements (`echo`, `pause`, `exit /b`, fixed `goto`, and bounded `set`) as single-line conditional bodies.
- Conditional service/interpreter execution remains a blocking syntax finding.
- Dangerous command syntax is classified before the generic conditional blocker, including inside parenthesized blocks.
- Added a repository-local, machine-neutral WFGame-shaped fixture; no project source, absolute Creator path, Secret, or user data was copied.

This is an implementation correction to the existing ADR-0007 and workspace-import plan contract. It does not add a shell Runner, execute BAT, enable the Cocos capability, or change any API/Manifest contract.

## Verification

| Command / gate | Result |
| --- | --- |
| Importer BAT/PS1, controlled Compose source graph, and minimized WFGame control-flow tests | Passed |
| `go test -timeout 120s ./internal/importer ./internal/api` | Passed |
| `STACKPILOT_WFGAME_PATH=E:\WFGame go test -timeout 60s -v ./internal/importer -run '^TestWFGameReadOnlyGate$'` | Passed; exactly two candidates were produced and both remained blocked as designed |
| `go test -timeout 180s ./...` | Passed |
| `go vet ./...` | Passed |
| `npm run build` | Passed; built Web assets and `dist/stackpilot.exe` 0.1.0 |

The current-user installation was upgraded through the authenticated repository launcher after confirming that both runtime and import active-Operation counts were zero. It restarted successfully as PID 38188. The installed immutable version directory is `1846a1ff2ddfb17f7abb5966d868ce5667b4da2b16291ef9c75cca76a9eeea6c`, exactly matching the SHA-256 of the built `dist/stackpilot.exe`.

The real Gate was read-only. It did not create `.stackpilot/system.yaml`, execute the BAT file, start Node or Cocos Creator, or write any file under `E:\WFGame`.

The remaining candidate blockers are intentional and unchanged: the current Node static server exposure is not proven loopback-only, and `workspace.runner.cocos` remains disabled for build-and-serve.

## 2026-08-23 Standalone REM Follow-up

The installed UI exposed a narrower false positive at `run.bat:7`: the source line is
a standalone `REM`, while the parser only ignored `REM` followed by a space and
comment text. The parser now accepts the exact `REM` token and `REM` followed by a
space or tab, without accepting lookalike commands such as `REMARK`. The minimized
WFGame fixture includes the standalone form, and the real read-only Gate asserts
both that the syntax blocker is absent and that the genuine non-loopback exposure
blocker remains present.

Verification passed with `go test -timeout 120s ./internal/importer`, the real
`TestWFGameReadOnlyGate`, host-permission `go test -timeout 180s ./...`,
`go vet ./...`, `npm run test:web`, `npm run type-check`, and `npm run build`.
The current-user installation was upgraded through `scripts/start-stackpilot.ps1`
after confirming zero active runtime/import Operations and zero active instances.
The installed executable SHA-256 is
`0abd5d940c78c9ddb98d714fa3248f687c8bd06039c9a504650945e4a6556e36`, exactly
matching `dist/stackpilot.exe`; the host-permission control status reports PID
`28336` running on the registered loopback port. An authenticated CLI probe of
`E:\WFGame` returned `initialization_required` with `run.bat` and did not apply a
draft or write project files.
