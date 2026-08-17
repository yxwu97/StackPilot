# Development

StackPilot Phase 0 uses the following locked toolchain baseline:

- Go 1.26.6, declared by `go.mod` and its `toolchain` directive.
- Node.js 24.x and npm 11.x, declared by the root `package.json` engines.
- Frontend package versions are locked by `package-lock.json`.

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

The development server uses a strict port and will fail instead of selecting another port. Requests below `/api` are proxied to the local control plane at `http://127.0.0.1:32100`.

To run the Go control plane from source, build the frontend first so the latest distribution is embedded:

```powershell
npm run build:web
.\.tools\go\bin\go.exe run ./cmd/stackpilot server
```

## Verification

Run formatting, Go tests, Go vet, frontend type checking, and the frontend production build:

```powershell
npm run check
```

Build the frontend and versioned Windows executable:

```powershell
npm run build
```

The production binary serves the embedded Web console on the loopback interface:

```powershell
.\dist\stackpilot.exe server
```

The default URL is `http://127.0.0.1:32100`. `--port` can select another loopback port; remote listening is not supported.

Release automation can inject deterministic metadata through `STACKPILOT_VERSION`, `STACKPILOT_COMMIT`, and `STACKPILOT_BUILD_TIME`. Values must contain no whitespace. The binary reports the compiled values with:

```powershell
.\dist\stackpilot.exe version
```
