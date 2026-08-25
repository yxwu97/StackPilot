# ADR-0001: Windows Supervisor Owns Per-Service Job Objects

- Status: Accepted
- Date: 2026-08-17
- Decision gate: D-01 / P0-08

## Context

StackPilot must keep managed services running across an ordinary control-plane exit, reconnect after restart, and still guarantee that a failed runtime owner does not leave an unowned process tree. A Job Object with `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE` provides complete descendant termination only while a trusted process retains its handle. If the HTTP Server owns the last handle, an ordinary Server exit kills services and violates restart/recovery requirements.

The design therefore proposed a detached per-system Supervisor, ACL-protected Named Pipe reconnection, process identity files, and per-service Job Objects. P0-08 had to verify the Windows behavior before P1B Process Driver implementation could rely on it.

## Decision

Retain the detailed-design architecture:

1. A detached instance Supervisor, not the HTTP Server, owns the last handle to each per-service Job Object.
2. The Supervisor creates each root process suspended, assigns it to the Job, atomically persists identity, and only then resumes the primary thread.
3. Jobs use `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE`; unexpected Supervisor termination therefore terminates the assigned root and all inherited descendants.
4. Server/Supervisor communication uses a random local Named Pipe with a protected allow-only DACL containing exactly the runtime account SID and Local System (`S-1-5-18`).
5. Reconnection first validates Supervisor PID, creation time, canonical executable path, account SID, and protocol version from `supervisor.json`, then validates each service identity the same way.
6. The protocol remains length-prefixed JSON with a 1 MiB limit and a fixed message registry. It does not accept a shell command string.
7. `identity.json` is written through create, flush, close, and atomic rename before thread resume. Database reconciliation treats the live OS identity as final fact when file/database state differs.

P1B-03 will implement the production protocol and lifecycle. The P0-08 executable remains an isolated validation artifact and is not shipped as a public StackPilot command.

## Verified Evidence

The Windows-only Spike uses a short-lived launcher to model the Server, a detached Supervisor process, a protected reconnectable Named Pipe, a kill-on-close Job, and suspended process creation. Three profiles passed on Windows:

| Profile | Observed tree before Supervisor failure | Result after Supervisor termination |
| --- | --- | --- |
| Generic | Root plus two generations of Spike worker descendants | Root and every captured descendant exited |
| npm | `cmd.exe` chain plus at least two `node.exe` descendants | Root and every captured descendant exited |
| Maven | `cmd.exe` parent plus `java.exe` Maven process | Root and every captured descendant exited |

For every profile, the launcher had already exited when a new client validated identity and connected. The client disconnected and connected again successfully. The live Pipe DACL enumerated exactly the current account SID and `S-1-5-18`. The identity write timestamp preceded the resume record.

The restricted development sandbox denies ordinary Named Pipe connection creation; `go-winio`'s own positive pipe tests fail there with the same `Access is denied`. The real Spike was therefore run outside the sandbox. This environmental restriction does not change the Windows result.

## Consequences

- P1B-03 and dependent Process Driver work may proceed using the Supervisor architecture; D-01 is closed.
- The Server must never become the sole owner of managed-service Job handles.
- Supervisor protocol compatibility must cover the current stable release to next-release takeover window, or upgrades must explicitly require services to stop first.
- Identity mismatch or insufficient inspection permission produces `unknown`; it never authorizes PID-only termination.
- Production implementation still requires protocol fuzzing, ACL negative tests under a genuinely different account, spool ownership, graceful stop, and crash/recovery integration with SQLite.

## Rejected Alternatives

- Server-owned kill-on-close Job: rejects ordinary Server restart continuity.
- PID-only recovery/termination: cannot prove ownership and is vulnerable to PID reuse.
- Unprotected/default Named Pipe ACL: expands the local control boundary beyond the runtime account and SYSTEM.
- Anonymous stdout/stderr pipes: cannot be reconnected after Server restart; persistent spool files remain the preferred design.
