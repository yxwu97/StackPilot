# StackPilot

StackPilot is a declarative local development system orchestrator. The Windows-first MVP provides a single Go binary for the loopback REST/SSE control plane, CLI, embedded Vue console, durable SQLite state, and supervised BTC Backend/Web orchestration.

The product scope and contracts are defined by:

- `docs/overall-design.md`
- `docs/detailed-design.md`
- `docs/phased-development-plan.md`
- `code_rule.md`

Developer setup and verification commands are documented in `docs/development.md`.

Workspaces can be registered from an existing `.stackpilot/system.yaml` or initialized through the Web import flow from a bounded, read-only BAT analysis. StackPilot generates and validates the manifest, then runs only trusted Runners; it never executes BAT as a lifecycle command. Workspace details, structured manifest edits, and same-System-ID path relinking are available from the workspace management view.

The trusted Go Runner is exposed as `workspace.runner.go` and executes only a resolved
`go.exe` with manifest argument arrays after a fixed version probe. AgentHub uses it
with the Compose and oneshot capabilities to manage its dependencies, database bootstrap,
API, Worker, and Web as one declared system. Register `E:\AgentHub` itself; its historical
BAT/PowerShell launcher remains outside StackPilot's executable trust boundary.

The product version is stored in the root `VERSION` file. `npm run build` compiles it into `stackpilot.exe`; query the resulting executable with `stackpilot.exe version`. The Web console reads the same running-executable identity and shows it as the system version.

The local control plane defaults to `http://127.0.0.1:32100`; Vite development uses `32101`. StackPilot does not use port 5173.

## Windows quick start

After building `dist/stackpilot.exe`, run `start-stackpilot.bat`. On first use, the launcher terminates any existing `stackpilot.exe` processes, then installs and starts the current-user control plane. On every later use, it gracefully stops the installed control-plane PID, forcefully terminates every remaining `stackpilot.exe` process, confirms that all old processes exited, upgrades the installation from `dist/stackpilot.exe`, starts one fresh control-plane PID, and then opens an authenticated Web console. This repository convenience launcher is an explicit cold start: terminating an `internal-supervisor` also terminates the business process tree protected by that Supervisor's Job Objects. The public `stackpilot service start` and `service upgrade` commands retain their normal takeover behavior.

## Windows installation

Run the downloaded candidate as the current user; administrator elevation is neither required nor used:

```powershell
.\stackpilot.exe service install
.\stackpilot.exe service status --output json
```

The installation root defaults to `%LOCALAPPDATA%\Programs\StackPilot`, while durable control-plane data defaults to `%LOCALAPPDATA%\StackPilot`. The process is registered for login startup in HKCU and exposes lifecycle control only through its current-user ACL Named Pipe.

Upgrade by invoking the newly downloaded candidate. Uninstall may be invoked from either a candidate or the installed executable:

```powershell
.\stackpilot-new.exe service upgrade
.\stackpilot-new.exe service uninstall
```

Uninstall removes only the verified installation root and startup registration. It preserves SQLite state, logs, registered workspace references, and all business files. Releases contain a Windows amd64 ZIP plus `checksums.txt`; verify the SHA-256 entry before running the candidate.
