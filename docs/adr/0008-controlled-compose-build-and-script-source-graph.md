# ADR-0008: Controlled Compose Build and Script Source Graph

- Status: Accepted
- Date: 2026-08-22
- Decision gate: CB-00 / controlled Compose build and import

## Context

Workspace import currently treats BAT files as bounded, read-only migration input and
the Compose driver rejects every resolved `build` definition. Some registered local
workspaces use a fixed BAT -> PowerShell -> Compose chain and build images from local
Dockerfiles. Supporting those workspaces must not add a shell Runner, accept runtime
arguments from HTTP, weaken path containment, or cause automatic service recovery to
rebuild images repeatedly.

A registered Compose file and its fixed container commands are already executable
workspace configuration. A local Dockerfile adds a broader build-time trust surface:
it can run build steps, use the daemon network, populate shared BuildKit cache, consume
disk, and leave images or cache after cancellation. That surface therefore requires a
separate, explicit opt-in rather than inheriting `phase2.compose`.

## Decision

1. The public capability is `phase2.compose-build`; the manifest validator uses the
   corresponding internal gate `compose-build`. Production advertises the public
   capability only after all CB automated gates pass.
2. `compose.buildPolicy` is a closed `never|always` value and defaults semantically to
   `never`. `always` requires `driver: compose`, `mode: daemon`,
   `readiness.type: compose`, `phase2.compose`, and `phase2.compose-build`.
3. `compose.readiness` is a closed map keyed by every managed Compose service. Values
   are `healthy|running`. An omitted map preserves the existing all-`healthy`
   behavior. `running` is valid only when resolved Compose config has no healthcheck
   for that service and must originate from explicit import confirmation.
4. Existing manifests omit the new default fields from normalized JSON. This preserves
   their definition digest and recovery snapshots. An explicit new field participates
   in manifest, resolved-spec, and Compose identity digests.
5. A build definition is allowed only for a managed service and only as a local string
   context or an object containing `context` and optional `dockerfile`. Context and
   Dockerfile must canonicalize to regular workspace-contained paths at import and
   again at start. Remote/Git contexts, absolute or escaping paths, `args`, additional
   contexts, Secret/SSH mounts, target, network, privileged/entitlements, cache,
   extra hosts, platforms, provenance, and SBOM controls are rejected.
6. Explicit system Start and system Restart apply `always`. User and automatic
   `service-restart` operations never build. All Compose starts use a separate build
   followed by `up --no-build`; build failure, timeout, or cancellation cannot enter
   the up step.
7. The fixed commands are:
   `docker compose --project-name <project> --file <base> --file <override> build
   <sorted-build-services...>` and
   `docker compose --project-name <project> --file <base> --file <override> up -d
   --wait --no-deps --no-build --wait-timeout <seconds> <sorted-services...>`.
   No request-provided flags, shell, pull flag, or alternate working directory is
   accepted. Build has the system start deadline and the same bounded 4 MiB output per
   stream as other Compose commands.
8. A cancelled or interrupted build can leave daemon cache or images. StackPilot does
   not claim rollback and does not prune production resources. Stop never rebuilds and
   never removes volumes, images, or cache. Tests may remove only exact resources that
   they created and labeled; global prune is forbidden.
9. Compose project naming remains instance-scoped. Repeated explicit starts can reuse
   content-addressed daemon cache, while different instances can leave distinct image
   names. Project identity includes build policy, sorted build services, and per-service
   readiness requirements.
10. Recovery treats build as replayable only while no matching managed project exists.
    If matching containers exist, recovery observes and resumes readiness/log ownership
    without rebuilding. Build completion itself is an external side effect, not a
    transactional database fact.
11. BAT and PowerShell files are never executed. BAT analysis uses one structural pass,
    with at most 4,096 logical lines, 256 conditional blocks, nesting depth 16, 32
    referenced files, depth 8, and 1 MiB total source bytes. It validates parentheses
    and `if/else` pairing without branch expansion. Service starts or dynamic
    interpreters inside conditional branches block import.
12. BAT may reference one workspace-relative literal `.ps1` through fixed
    `powershell.exe|pwsh.exe` plus safe non-interactive switches and `-File`.
    `-Command`, encoded commands, stdin, variables in the path, and additional dynamic
    arguments are rejected. The PS1 subset accepts only fixed preference assignments,
    `Write-Host`, and literal `docker compose` commands needed to identify `--file`,
    `up --build -d`, and `ps`. An identified Compose command may be followed immediately
    by one exact three-line `if ($LASTEXITCODE -ne 0) {` guard whose body is one quoted
    `throw` message and whose only interpolation is `$LASTEXITCODE`; the guard is
    structural error handling and contributes no command fact. All other variables,
    functions, script blocks, pipelines, redirection, invocation/dot-source operators,
    downloads, registry access, `Start-Process`, and non-Docker commands block import.
13. The source graph digest covers sorted relative paths and content digests for BAT,
    supported PS1, Compose, and directly inspected files that affect the candidate.
    Any change between analyze and apply returns `WORKSPACE_IMPORT_SOURCE_CHANGED`.
    Dangerous findings take precedence over generic unsupported syntax and findings
    have deterministic ordering.
14. The existing import draft/operation tables, manifest snapshot, resolved spec, and
    operation steps carry all required persistent facts. No database migration is
    introduced.

## Consequences

- A user who registers and explicitly starts an opted-in workspace authorizes local
  Dockerfile execution within the documented limits.
- Ordinary Compose systems and existing normalized digests retain their current
  behavior; the new capability remains independently disableable.
- Cancellation cannot guarantee daemon-side build cleanup, so diagnostics must report
  possible owned residue without exposing Dockerfile contents or full Compose config.
- Import candidates expose structured Compose/build/readiness evidence and require
  confirmation for each no-healthcheck service before apply.

## Rejected Alternatives

- Run BAT or PowerShell to discover behavior: this creates an arbitrary host command
  surface during import.
- Let `docker compose up` build implicitly: this loses a persistent cancellation and
  failure boundary and can rebuild during recovery.
- Allow all Compose build fields: build secrets, SSH, remote contexts, and entitlements
  materially enlarge the trust boundary.
- Build during service restart or automatic recovery: repeated failures could create an
  unbounded build loop and change restart-attempt semantics.
- Prune BuildKit cache on stop: cache is daemon-global and cannot be safely attributed
  to one StackPilot operation.
