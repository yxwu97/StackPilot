# ADR-0003: Phase 1 Uses a Per-User Windows Background Process

- Status: Accepted
- Date: 2026-08-18
- Decision gate: D-04 / P1D-10

## Context

Phase 1 is a Windows-first, local, single-user product. StackPilot must start automatically for that user, retain managed processes across a control-plane upgrade, and preserve all control-plane and business data during uninstall. The runtime identity also owns workspace access, the DPAPI-protected local token, Supervisor Named Pipe ACLs, and the managed Maven/npm/Java processes.

Running the control plane as LocalSystem would change all of those boundaries. It would require machine-level `%ProgramData%` storage, a different Secret protection model, explicit workspace ACL grants, and a reviewed rule for which user identity launches business processes. Those capabilities are outside Phase 1 and would make the current single-user authentication contract misleading.

## Decision

1. Phase 1 installs StackPilot as a current-user background process. A fixed `HKCU\Software\Microsoft\Windows\CurrentVersion\Run` value starts it after that user logs on; it is not a Windows Service and requires no elevation.
2. The default installation root is `%LOCALAPPDATA%\Programs\StackPilot`; immutable executables are staged at `versions\<sha256>\stackpilot.exe` and selected by the verified installation marker. The default data directory remains `%LOCALAPPDATA%\StackPilot`; installation files and durable data are separate roots.
3. The public management surface remains `stackpilot service install|upgrade|start|stop|status|uninstall`. The word `service` names the background-control role, while status output reports the concrete `user-process` mode.
4. The login-start command is fixed by StackPilot and starts the loopback-only Server with the registered absolute data directory. HTTP command, arguments, working directory, and environment overrides are never accepted.
5. Start launches the registered binary directly as a hidden detached process. Status and stop use a current-user ACL-protected local control channel. Stop cancels the Server context and waits for its normal database and HTTP shutdown path; if identity/control cannot be proven, StackPilot refuses unverified termination. Install, start, and upgrade report `running` only after both the control channel and loopback HTTP readiness succeed.
6. Upgrade is launched from a downloaded candidate binary. It verifies the existing installation record, gracefully stops the old control plane, stages the immutable candidate, atomically switches the marker and HKCU registration, starts the new version, and rolls back the old marker/registration/running state on failure. An idempotent upgrade with an unchanged candidate reasserts the marker-selected HKCU registration before returning status, repairing a stale login command left by an interrupted or isolated validation run. Managed service Supervisors remain independent and are reconciled by the new Server. A Supervisor accepts a different-version control executable only when it has the same Windows account, is the strict marker-selected executable under the same `versions` root, and its actual SHA-256 matches both the marker and version directory; development/non-installed processes retain exact executable-path trust.
7. Uninstall stops the process, removes its HKCU login registration, then removes only the verified installation root. Self-uninstall delegates final image removal to a short-lived current-user cleanup process and requires a startup handshake before reporting success. It never deletes the data directory, registered workspaces, business files, managed data volumes, logs, or SQLite state.
8. Installation metadata records the mode, canonical install/data roots, task identity, installed version, and executable checksum. Every destructive file action must revalidate the marker and canonical boundary.
9. The installed runtime emits structured lifecycle records for `startup`, `normal_exit`, `control_stop`, `signal`, `upgrade`, `serve_error`, and `startup_error` where the responsible boundary can prove that classification. The control protocol distinguishes an upgrade stop from an ordinary stop. A missing exit record means only that normal closure was not observed; it does not identify an external terminator. No watchdog or automatic control-plane restart is introduced by this diagnostic contract.
10. The repository-only `start-stackpilot.bat` convenience launcher is intentionally a cold restart-and-update path, while the public `service start` command remains idempotent and the public `service upgrade` path preserves Supervisor takeover. After the first installation, each launcher invocation runs the authenticated `service stop`, forcefully terminates every remaining process whose exact image name is `stackpilot.exe`, waits until none remain, updates the registered immutable executable through `service upgrade` using the repository's current `dist/stackpilot.exe`, and then runs `service start`. First installation performs the same process-name cleanup before install. This is a user-requested local recovery boundary: terminating an `internal-supervisor` closes its Job Object handles and terminates its managed business process trees. Failure to terminate any matching process aborts the launcher before a new control plane is started.

The generic HTTP server layer records only `context_cancelled` because it cannot distinguish a control-Pipe cancellation from an OS signal. The installed-runtime boundary owns the authoritative `control_stop`, `upgrade`, or `signal` classification. This prevents a normal upgrade from being misreported as a signal.

## Consequences

- StackPilot starts after the owning user logs on, not during machine boot before logon.
- The Server, CLI, Supervisor, and managed processes retain one Windows identity and one DPAPI/ACL boundary.
- Installation and updates require no administrator privilege and cannot claim machine-wide availability.
- A future Windows Service mode needs a separate security and data-migration decision; it cannot silently reuse the Phase 1 user token or launch projects as LocalSystem.
- Release and recovery tests must cover fresh install, idempotent install, candidate upgrade, running-instance takeover, preserved data, and uninstall from both an external candidate and the installed command.

## Rejected Alternatives

- LocalSystem Windows Service in Phase 1: changes the identity, storage, DPAPI, workspace ACL, and child-process security model beyond the accepted single-user scope.
- HKCU `Run` entry as the only lifecycle mechanism: provides weak status and stop control. The accepted design uses it only for login activation and retains StackPilot's authenticated local control channel as the lifecycle authority.
- Current-user Task Scheduler task: clean lifecycle registration, but this Windows host requires elevation even for a minimal current-user task, violating the no-administrator Phase 1 requirement.
- Startup-folder shortcut: has the same control limitations and a less explicit registration record.
- Installer-only lifecycle scripts: create a second implementation outside the single executable and do not provide a stable CLI management surface.
