# Development

StackPilot Phase 0 uses the following locked toolchain baseline:

- Go 1.26.6, declared by `go.mod` and its `toolchain` directive.
- Node.js 24.x and npm 11.x, declared by the root `package.json` engines.
- Frontend package versions are locked by `package-lock.json`.

Phase 1B Windows process integration is verified against Maven 3.9.14, npm 11.12.1, and Eclipse Adoptium Java 21.0.10. Runner resolution prefers `JAVA_HOME` for Java, so validation does not depend on an older `java.exe` appearing earlier on `PATH`.

On Windows, scripts prefer `.tools/go/bin/go.exe` when present and otherwise use `go` from `PATH`.
Go and npm caches are kept under the ignored `.cache/` directory so verification does not depend on writable user-profile caches.

## Setup

```powershell
npm install
```

## Development

Run the Vue development server on `http://127.0.0.1:32101`:

```powershell
npm run dev
```

The development server uses a strict port and will fail instead of selecting another port. Requests below `/api` and the exact `/version` path are proxied to the local control plane at `http://127.0.0.1:32100`.

The service-log viewer has an isolated browser fixture at `http://127.0.0.1:32101/test/browser/log-viewer.html`. It mounts the production viewer with 5,000 fictional entries delivered through the same REST-window plus SSE path. Use it for desktop/mobile layout, dynamic-row-height, pause/clear, error navigation, copy, and fullscreen checks; it contains no real service logs.

To run the Go control plane from source, build the frontend first so the latest distribution is embedded:

```powershell
npm run build:web
.\.tools\go\bin\go.exe run ./cmd/stackpilot server --data-dir .\.cache\dev-data
```

## Verification

Run formatting, Go tests, Go vet, frontend type checking, and the frontend production build:

```powershell
npm run check
```

The focused Web state and SSE suite is available separately:

```powershell
npm run test:web
```

Build the frontend and versioned Windows executable:

```powershell
npm run build
```

The repository root `VERSION` file is the only product-version source. It contains a canonical three-part version such as `0.1.0`. Inspect and validate it with:

```powershell
npm run version:show
npm run version:check
```

For each product change set, run `npm run version:bump` once before delivery. It advances PATCH by default and returns `already-bumped` when the same baseline has already advanced. Use `scripts/version.ps1 -Action Bump -Part minor` or `-Part major` only for an explicit compatibility/release decision. Build, check, and test commands never bump the version or create Git commits/tags.

`start-stackpilot.bat` is the repository convenience launcher for the default current-user installation. Its first run terminates any existing `stackpilot.exe` processes, then installs and starts `dist/stackpilot.exe`. Every later run performs an authenticated graceful stop, forcefully terminates every remaining `stackpilot.exe` process, waits until no old process remains, upgrades the registered installation from the current `dist/stackpilot.exe`, starts one fresh control-plane PID, and opens a new browser session unless `--no-open` is supplied. This launcher therefore performs an explicit cold start: terminating an `internal-supervisor` closes its Job Object handles and terminates the business process tree it supervises. Use the public `stackpilot service start` or `service upgrade` lifecycle when Supervisor takeover and managed-process continuity are required.

GoReleaser v2.12.7 is the release source of truth. After `npm ci`, create the same ZIP and SHA-256 manifest used by CI without publishing:

```powershell
$env:STACKPILOT_VERSION = npm run --silent version:show
goreleaser release --snapshot --clean
```

Tagged `v*` builds run the full check script and publish `stackpilot_<version>_windows_amd64.zip` with `checksums.txt`. The workflow rejects any tag other than exact `v<VERSION>`, injects `VERSION` into the binary and archive name, and then verifies the executable identity. Commit and UTC build time remain independent build metadata.

The production binary serves the embedded Web console on the loopback interface:

```powershell
.\dist\stackpilot.exe server --data-dir .\.cache\dev-data
```

The default URL is `http://127.0.0.1:32100`. `--port` can select another loopback port; remote listening is not supported. The Windows single-user data directory defaults to `%LOCALAPPDATA%\StackPilot`; use `--data-dir` for an isolated development database.

For an isolated installation test, always keep installation and data roots separate:

```powershell
.\dist\stackpilot.exe service install --install-dir .\.cache\install --data-dir .\.cache\data --port 32102
.\dist\stackpilot.exe service status --install-dir .\.cache\install --output json
.\dist\stackpilot.exe service uninstall --install-dir .\.cache\install
```

The installed mode is reported as `user-process`. Upgrade stages immutable executables below `versions\<sha256>` and atomically changes the verified marker and HKCU registration. Uninstall preserves the entire data root.

The same binary is an authenticated API client. Commands use the default Server origin and data directory unless `--server` and `--data-dir` select an isolated instance:

```powershell
stackpilot workspace add E:\Projects\BTC
stackpilot up --wait --open
stackpilot status --output json
stackpilot logs btc/backend --follow
stackpilot down --wait
stackpilot wait op_01ARZ3NDEKTSV4RRFFQ69G5FAV
```

`workspace add` probes the fixed manifest first. If `.stackpilot/system.yaml` exists, registration remains immediate. If it is missing, the CLI prints `initialization_required`, returns exit code `3`, and does not claim registration; add explicit `--open` in table mode to open the authenticated BAT import flow. JSON output never opens a browser. The Web flow performs read-only bounded analysis, lets the user correct allowlisted fields, then publishes a validated manifest through a durable Operation. BAT files are never executed.

The import flow also recognizes the narrowly defined BAT -> PowerShell `-File` -> Docker Compose source graph from ADR-0008. Neither script is executed. A detected local build remains blocked until the user confirms Dockerfile execution and separately confirms `running` readiness for every service without a healthcheck. The generated manifest uses `compose.buildPolicy: always`; a later explicit system Start/Restart performs a fixed build followed by `up --no-build`. Service restart never rebuilds. Builds may pull images, access the network, and leave Docker daemon images/cache after cancellation; ordinary Stop intentionally preserves volumes, images, and cache.

Registered workspaces expose a detail drawer with source, services, ports, runtime state, and read-only normalized YAML. Structured edits and path relinking are available only while stopped with no active Operation. Relinking requires the new root to contain a valid manifest with the same System ID and never removes files from the old root.

The trusted `go` Runner is advertised as `workspace.runner.go`. On Windows it resolves
only a server-configured allowed executable or `go.exe` from the service account PATH,
runs the fixed `go version` probe, and never invokes a shell. AgentHub uses this Runner
for API, Worker, and an idempotent database bootstrap while Docker dependencies remain
under the Compose driver. Register `E:\AgentHub` directly; do not select its BAT or
PowerShell launcher. A normal StackPilot stop preserves AgentHub named volumes.
The managed AgentHub profile uses Redis port 6380 so it does not depend on or stop a
Windows Redis service on the conventional 6379 port.

Before an AgentHub integration Gate, verify the owned bootstrap package and Compose
definition from the AgentHub root, then run the real StackPilot Start/Stop workflow:

```powershell
$env:GOCACHE = 'E:\StackPilot\.cache\agenthub-go-build'
go test ./cmd/agenthub-bootstrap
docker compose --file .stackpilot/compose.yaml config
```

`up`, `down`, and `status` discover the current workspace by walking upward to `.stackpilot/system.yaml` and matching its registered canonical root. `--output json` keeps stdout machine-readable and sends wait progress to stderr. `stackpilot open` obtains a 60-second one-time browser bootstrap through Bearer authentication; the long-lived local token remains in OS-protected storage. A ready-to-copy VSCode task set is maintained at `docs/examples/vscode/tasks.json`.

The browser session renews on a 30-minute sliding window and never exceeds eight hours from bootstrap exchange. Renewal is checked five minutes before expiry, when a page becomes visible or focused, and before mutations. Thirty minutes without a successful renewal is a renewal timeout, not keyboard/mouse idle detection. Expiry, logout, Server restart, and token rotation require a new `stackpilot open`; an expired page intentionally has no ordinary retry action. Temporary connection failures expose a bounded re-detection action instead.

Session and CSRF values remain memory-only. Do not add them to localStorage, sessionStorage, IndexedDB, URLs, logs, screenshots, or saved browser state. The repeatable accelerated browser fixture is `test/browser-session-lifecycle-gate.js`; it uses fictional values and a mocked `/api/v1` route against locally served production assets.

Runtime identity reconciliation defaults to 10 seconds and port lease reconciliation to 30 seconds. They can be increased with `--reconcile-interval` and `--lease-reconcile-interval`; values below `10s` and `30s` are rejected.

`scripts/build.ps1` reads the product version from `VERSION`; its explicit `-Version` parameter is reserved for isolated tests. Release automation supplies the validated `STACKPILOT_VERSION` to GoReleaser, while local builds may inject deterministic commit and time through `STACKPILOT_COMMIT` and `STACKPILOT_BUILD_TIME`. Values must contain no whitespace. The binary reports the compiled values with:

```powershell
.\dist\stackpilot.exe version
```

The Web console reads the same running executable identity from `GET /version` and displays it as `系统版本 v<version>`.
