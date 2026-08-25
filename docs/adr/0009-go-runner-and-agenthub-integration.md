# ADR-0009: Trusted Go Runner and AgentHub Integration

- Status: Accepted
- Date: 2026-08-24
- Decision gate: AgentHub declarative integration

## Context

AgentHub's historical Windows launcher combines Docker Compose, database bootstrap,
Go API/Worker processes, and a Vite Web process. Workspace import correctly rejects
the launcher's general PowerShell syntax as `WORKSPACE_SCRIPT_DANGEROUS`. Supporting
the complete system must preserve that boundary while giving StackPilot ownership of
dependency ordering, readiness, logs, recovery, and non-destructive stop.

StackPilot already has trusted Maven, npm, Java, Node, Python venv, and Compose paths.
AgentHub needs the installed Go toolchain, but does not need a shell Runner, an
HTTP-provided executable, container exec, or script whitelisting.

## Decision

1. Add process Runner `go`, internally gated by `go` and publicly advertised as
   `workspace.runner.go` after its automated gates pass.
2. On Windows, resolve only a server-configured explicit `go.exe` inside an allowed
   tool root or `go.exe` from the service account PATH. Canonicalize a regular file,
   run the fixed `go version` probe with bounded output/deadline, parse the Windows
   toolchain result, and record the executable SHA-256.
3. The manifest supplies only a fixed argument array and workspace-contained working
   directory. No API or import correction can supply an executable path, shell,
   command string, or environment-driven command substitution.
4. Keep `WORKSPACE_SCRIPT_DANGEROUS` unchanged. AgentHub registers its repository root
   and checked `.stackpilot/system.yaml`; StackPilot never executes its BAT or
   PowerShell launchers.
5. AgentHub declares one Compose infrastructure daemon, one Go bootstrap oneshot,
   npm install oneshot, Go API/Worker daemons, and npm Web daemon. DAG conditions use
   `ready` and `completed` explicitly.
6. The AgentHub-owned bootstrap connects directly to the ready loopback PostgreSQL
   port, verifies registered migration checksums, applies migrations/fixtures
   idempotently, reconciles the synthetic application role, and creates or validates
   the local content key. It invokes no Docker or shell command.
7. AgentHub's checked Compose include and synthetic credentials are public local
   development inputs. They must not be reused outside that profile. Ordinary Stop
   preserves named volumes.

## Consequences

- Registered Go workspaces gain the same Supervisor, Job Object, log, readiness, and
  recovery behavior as other process Runners.
- `go run` executes registered workspace source and can download modules on first use;
  explicit workspace registration and Start are the authorization boundary.
- AgentHub start can also pull pinned container images and run `npm ci`; network or
  registry failure remains a visible Operation failure.
- The Go capability is independently disableable and manifests using it return
  `FEATURE_NOT_ENABLED` when absent.

## Rejected Alternatives

- Whitelist AgentHub's PowerShell launcher: this would add a general host command
  surface and duplicate lifecycle ownership.
- Manage only Compose dependencies: API, Worker, bootstrap, and Web would remain
  outside the declared Operation and stop/recovery model.
- Run migrations through container exec or manifest SQL: this would move AgentHub
  schema ownership into StackPilot and add a generic execution surface.
- Delete volumes on ordinary Stop: this would violate the Compose persistence contract
  and make routine lifecycle operations destructive.
