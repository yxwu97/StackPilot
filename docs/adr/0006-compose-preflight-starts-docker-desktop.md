# ADR-0006: Compose Preflight May Start Docker Desktop

- Status: Accepted
- Date: 2026-08-20
- Decision gate: P2B-02 / AIWS local startup recovery

## Context

AIWS infrastructure uses the optional Windows Compose driver. A valid local installation can still fail at start or be impossible to clean up when Docker Desktop is installed but its daemon is not running. Requiring a separate manual desktop action makes StackPilot's declared system lifecycle incomplete, while starting Docker for every StackPilot system would add an unrelated side effect to native process workflows.

Docker Desktop startup is a host executable trust boundary. Its path and arguments must not be controllable by a manifest, API request, workspace environment, or arbitrary command field.

## Decision

1. Automatic startup is lazy and occurs only during an explicit Compose start preflight or the Compose stop phase of a user-requested Stop/Restart Operation, after the canonical Docker CLI and Compose v2 plugin pass their version checks but daemon inspection reports unavailable. Periodic read-only reconciliation does not start Docker Desktop.
2. Windows resolves only `%ProgramFiles%\Docker\Docker\Docker Desktop.exe` and `%LOCALAPPDATA%\Programs\Docker\Docker\Docker Desktop.exe`. The selected path must canonicalize to a regular file within the corresponding canonical root.
3. Docker Desktop starts as the current StackPilot user, detached from the control-plane console, with no arguments and the server-owned environment. No manifest or HTTP field can supply its executable, arguments, or working directory.
4. Concurrent preflights on one control-plane process serialize daemon startup. After acquiring the startup lock, a request rechecks daemon readiness so an already successful peer does not launch a second instance.
5. The preflight polls the fixed daemon version command every 500 milliseconds. Docker Desktop cold start shares the complete Compose preflight deadline, whose default is two minutes.
6. A launcher failure remains `DOCKER_DAEMON_UNAVAILABLE`; expiration remains `COMPOSE_PREFLIGHT_TIMEOUT`. Raw process or daemon details are not returned or persisted.
7. Non-Compose systems never start Docker Desktop. StackPilot does not stop Docker Desktop when a Compose project stops, because the desktop daemon is a user-scoped shared dependency rather than an owned child service.

## Consequences

- Starting, stopping, or restarting a Compose-backed system can recover automatically from an installed but stopped Docker Desktop daemon.
- The first Compose lifecycle action can take up to the preflight deadline before returning an asynchronous Operation failure.
- Installation absence, daemon startup failure, insufficient resources, invalid Compose config, port conflicts, and missing Secrets remain separate failures and are not hidden by this behavior.
- Serialization is process-local. Independent StackPilot control-plane processes are already outside the supported single-instance ownership model.

## Rejected Alternatives

- Start Docker Desktop during StackPilot boot: this affects users who only run native process systems and adds avoidable resource use.
- Accept a Docker Desktop path or arguments from the manifest/API: this creates an arbitrary host executable boundary.
- Use a shell, registry command, shortcut, or broad PATH search: these add indirection and weaken executable identity checks.
- Stop Docker Desktop with the managed Compose project: this can interrupt unrelated containers owned by the same user.
