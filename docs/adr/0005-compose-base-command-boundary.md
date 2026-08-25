# ADR-0005: Trust Fixed Commands in Registered Compose Definitions

- Status: Accepted
- Date: 2026-08-18
- Decision gate: P2C-02

## Context

P2B initially rejected any `command` in a managed service after resolving the registered Compose file. That rule is stricter than the documented runtime-override boundary and prevents real infrastructure images whose supported startup requires fixed container arguments. AIWS Keycloak, MinIO, and OpenTelemetry Collector all need such image-specific arguments. Removing them or replacing the images would make the real integration less reproducible without reducing the host trust boundary.

The registered canonical Compose file is already executable workspace configuration. StackPilot validates it from `.stackpilot/system.yaml`, never accepts it or command values from HTTP, and invokes Docker with a fixed argument array. A Compose container command does not become a host shell command. The generated runtime override remains the untrusted expansion boundary and cannot add or alter commands.

## Decision

1. A fixed `command` resolved from the registered canonical base Compose file is permitted.
2. Runtime overrides remain structurally limited to loopback TCP port mappings, non-Secret environment values, and StackPilot identity labels. They cannot contain `command` or `entrypoint`.
3. Base `entrypoint` overrides remain rejected because they replace the image's executable trust anchor and are not required by the accepted AIWS integration.
4. `build` remains rejected by default. ADR-0008 narrowly permits explicitly opted-in,
   workspace-contained local Dockerfile builds behind `phase2.compose-build`;
   privileged containers, host filesystem root bind mounts, and dependencies on
   undeclared services remain rejected.
5. StackPilot continues to execute only fixed Docker/Compose CLI argument arrays without a host shell. No API accepts a Compose file, container command, or arbitrary argument override.

## Consequences

- AIWS can retain the documented commands of pinned official images while receiving dynamic host ports only from StackPilot's immutable override.
- The effective container command remains reviewable in the business workspace and covered by Compose config preflight.
- A malicious registered Compose workspace can already execute container workloads; this decision does not expand the remote API or host command surface.
- Future requests to allow a base `entrypoint`, runtime command mutation, or arbitrary Compose files require another explicit design decision.

## Rejected Alternatives

- Build derived images without a separate opt-in: build adds supply-chain, network,
  disk, and cache behavior. ADR-0008 defines the only accepted local build subset.
- Add commands through the generated override: this would put executable behavior in the runtime expansion boundary and violate the immutable allowlist.
- Invoke the legacy PowerShell launcher: this bypasses Compose identity, health, log, stop, and recovery semantics.
