# Service Log Viewer Enhancements - 2026-08-19

## Scope

This evidence closes `plan/plan-20260819-02-service-log-viewer-enhancements.md`. The change is limited to the Web viewer, its bounded client state, tests, and test fixtures. It does not add a deletion API, change the log DTO/SSE protocol, or implement server-side retention.

## Renderer Spike

- The locked `element-plus` version is 2.14.4. Its `ElTableV2` exposes `estimatedRowHeight`, stable `rowKey`, measured dynamic rows, `scrollToRow`, and auto-resize integration.
- The selected renderer is `ElTableV2` with `ElAutoResizer`; no virtual-list or Vue component-test dependency was added.
- The formal viewer uses sequence-keyed rows, fixed metadata/action panes, a measured message pane, and a bounded viewport. Wrap/fullscreen changes rebuild the height cache and restore the current sequence anchor.
- A deterministic 5,000-entry fixture covers all normalized levels, short lines, URLs, unbroken strings, multiline stacks, and long fictional truncated-style records. It contains no real service output or credentials.

At `1440x900`, a filtered multiline stack fixture rendered 14 main-pane rows at 74.0859375px each with `noOverlap=true`. The full 5,000-entry window rendered at most 22 main-pane rows in the tested layouts; fixed left/main/right panes kept total row DOM bounded.

## State And Interaction Results

- Node tests cover the initial REST window, sequence ordering/deduplication, 5,000-entry bounds, pause/resume, pause overflow, clear while paused, merge/replace recovery, monotonic cursor with an empty visible window, scope reset, level fallback, deterministic error blocks, export formatting, and safe Windows filenames.
- The 5,000-entry fixture produced 1,428 `error`/`fatal` entries. Next-error navigation moved from `0/1428` to `1/1428` and located sequence 5 after wrap and fullscreen changes.
- Fullscreen preserved query, wrap, connection, and error-anchor state. The first Escape exited fullscreen while leaving the drawer open.
- While paused, three injected SSE entries produced `已暂停 · 3` and did not enter the visible rows. Clear reset the view and paused buffer at sequence 5003. Two later entries resumed as only sequences 5004 and 5005; pre-floor entries did not reappear.
- The isolated clipboard adapter captured exactly `fictional service log line 1` for the first row copy action, with no sequence, timestamp, level, stream, or action text.

## Layout Results

- Desktop `1440x900`: body scroll width equaled viewport width (1440), normal drawer width was 760, metadata did not shrink, and the log viewport remained bounded.
- Mobile `390x844`: body scroll width equaled viewport width (390), every toolbar button remained within the viewport, 13 main-pane rows were rendered, and the log viewport retained 229.5px height.
- No-wrap rows retain complete message text and an always-on internal horizontal scrollbar; wrap mode uses `pre-wrap` plus `overflow-wrap:anywhere` for URLs and unbroken content.

Sanitized visual evidence:

- `output/playwright/log-viewer-desktop.png`
- `output/playwright/log-viewer-mobile.png`
- `output/playwright/log-viewer-wrapped.png`
- `output/playwright/log-viewer-fullscreen.png`

## Commands

Passed on 2026-08-19:

```powershell
npm run test:web
npm run type-check
npm run build:web
$env:GOCACHE=(Join-Path (Get-Location) '.cache/go-build')
$env:GOMODCACHE=(Join-Path (Get-Location) '.cache/go-mod')
go test ./internal/logs ./internal/api -count=1
./scripts/check.ps1
```

The first sandboxed `npm run build:web` and `./scripts/check.ps1` attempts could not spawn esbuild (`EPERM`); rerunning with process-spawn permission passed. The first targeted Go attempt could not access the user-profile build cache; rerunning with the repository cache passed. The repository has no separate frontend lint script, so no lint result is claimed.

## Remaining Boundary

The native drag selection contract remains limited to currently rendered rows. Cross-screen extraction uses data-driven error excerpts or TXT export. Long-term NDJSON retention, segment metadata cleanup, and `LOG_STORAGE_PRESSURE` remain owned by the separate `03` work package.
