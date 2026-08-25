# Executable and system version evidence

Date: 2026-08-19

## Decision record

- `VERSION` is the only source-controlled StackPilot product version source. The established baseline is `0.1.0` and uses canonical `MAJOR.MINOR.PATCH` without zero padding.
- A product change set advances the version through `scripts/version.ps1 -Action Bump`; PATCH is the default. Build, check, test, snapshot, and repeated CI consume the version without incrementing it.
- The private root and Web npm package versions remain `0.0.0`. They describe unpublished npm workspaces and are intentionally not product-version sources, avoiding lockfile churn.
- `internal/buildinfo` is the runtime identity source for both CLI and HTTP. The Web console reads the running executable through root `GET /version`; it does not embed a Vite/package version.
- Release tags must exactly equal `v<VERSION>`. GoReleaser uses validated `STACKPILOT_VERSION` for both the executable linker value and archive name.

## Implemented result

- Added strict UTF-8/LF version parsing, numeric comparison, patch/minor/major calculation, exclusive update locking, atomic `VERSION` replacement, change-path classification, base-ref validation, tag validation, and an `already-bumped` idempotent result.
- Added isolated PowerShell coverage for version arithmetic, invalid formats, BOM/CRLF rejection, atomic replacement, path policy, and exclusive locking.
- `scripts/build.ps1` now reads `VERSION`, rejects invalid explicit overrides, injects the value with Go linker flags, and executes the resulting exe to verify its first version line.
- CI validates version advancement against the event base when available. Release validates the tag; Windows snapshot verification executes the archive's `stackpilot.exe`.
- OpenAPI constrains `VersionResponse.version` to canonical three-part product versions. CLI/API contract tests assert the injected build identity.
- Web `getVersion()` requests exact `/version`, validates the DTO, and displays `系统版本 v0.1.0` in the authenticated sidebar and authentication state. Version failure produces `--` without blocking authentication or catalog state.
- Vite proxies exact `/version` in addition to `/api`.

## Verification

| Command or Gate | Result |
| --- | --- |
| `npm run version:show` | Passed; `0.1.0` |
| `npm run version:check` | Passed; `version=0.1.0 status=valid baseline=none` |
| `scripts/test-version.ps1` | Passed |
| `scripts/version.ps1 -Action Bump` on the initial baseline | Passed; returned `already-bumped`, `VERSION` SHA-256 unchanged |
| `scripts/version.ps1 -Action Check -ExpectedTag v0.1.1` | Expected failure; rejected mismatch with `VERSION v0.1.0` |
| Focused Go tests | Passed for `internal/buildinfo`, `internal/api`, and `cmd/stackpilot` |
| `npm run check` | Passed outside the restricted process sandbox: 24 Web tests, TypeScript strict check, Vite production build, all Go tests, and `go vet ./...` |
| `npm run build` | Passed; `dist/stackpilot.exe` reported `StackPilot 0.1.0`; `VERSION` hash unchanged |
| GoReleaser 2.12.7 `check` | Passed |
| GoReleaser 2.12.7 snapshot | Passed using repository-local `GOMODCACHE`/`GOCACHE` after the first attempt tried unavailable network downloads |
| Snapshot archive | `stackpilot_0.1.0_windows_amd64.zip`; SHA-256 `0b040b04cff4d8aef18570fb5cfcd5ffc33814cf2a4a0d1356e4066ee492162d`; checksum matched |
| Archive contents | Exactly `README.md` and `stackpilot.exe` |
| Archived executable identity | Version `0.1.0`; commit `b47ab22f97c765a06b4b4cee51531abc50a037e5`; built `2026-08-19T14:00:16Z` |
| Running executable HTTP identity | `GET http://127.0.0.1:32103/version` returned version `0.1.0` |
| Browser desktop snapshot | Real local page showed `系统版本 v0.1.0` |
| Browser mobile emulation snapshot | Real local page showed `系统版本 v0.1.0` |
| Agent instruction mirror | `AGENTS.md` and `CLAUDE.md` SHA-256 both `4F4400A0BBCC542096D6DC7A31B54EFEF351D3E3C932D7126A17F5CAA69456B1` |

The first sandboxed `npm run check` and GoReleaser attempts encountered Windows `spawn EPERM` for the Vite/esbuild subprocess. Re-running the same checks outside that restricted process sandbox passed. GoReleaser additionally needed the already populated repository Go caches because network dependency downloads were unavailable.

## Browser limitation

The Codex in-app Browser backend was unavailable in this session. The Playwright CLI successfully opened the real desktop and mobile-emulated pages and produced DOM snapshots, but this Windows environment did not retain its browser session across CLI commands, so a follow-up screenshot command could not attach. The DOM snapshots and live HTTP response were verified; no screenshot is claimed as evidence.

The validated local server remains available at `http://127.0.0.1:32103` with isolated data under `.cache/version-ui-data` for manual inspection.
