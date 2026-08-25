# Browser Session Lifecycle Hardening Evidence

Status date: 2026-08-19

This maintenance package completes AS-00 through AS-06 from `plan/plan-20260819-04-browser-session-lifecycle-hardening.md`.

## Security and contract closure

- Browser sessions use a 30-minute renewal expiry and an eight-hour absolute expiry. Refresh advances to `min(now + 30m, absoluteExpiresAt)` and cannot revive an expired, revoked, restarted, or token-rotation-invalidated session.
- Refresh atomically rotates CSRF and retains one prior digest for at most five minutes and never beyond session expiry. The browser serializes refresh/bootstrap across tabs with a same-origin Web Lock and distributes only ephemeral `csrf/expiresAt` values through BroadcastChannel.
- `SessionResponse` remains exactly `csrf/expiresAt`. No long-lived token, session identifier, absolute expiry, or refresh token was added to the browser contract.
- Exchange, refresh, revoke, and browser token rotation use one Cookie constructor. Active cookies have `HttpOnly`, `SameSite=Strict`, `Path=/`, authoritative UTC `Expires`, and ceiling-rounded positive `Max-Age`; deletion uses the same attributes, a past expiry, and `Max-Age=-1`.
- `AUTH_SESSION_INVALID` remains 401 and `AUTH_BROWSER_REQUEST_REJECTED` remains 403. `docs/error-codes.md` was reviewed and required no mapping or code change.

## Automated verification

| Command | Result |
| --- | --- |
| `go test -race ./internal/security ./internal/api -count=1` with repository MinGW 16.1.0 | Passed; security and API packages had no reported race |
| `powershell -NoProfile -ExecutionPolicy Bypass -File ./scripts/check.ps1` | Passed; 21 Web tests, strict TypeScript, production Web build, all Go tests, and `go vet ./...` |
| `npm run build` | Passed; production Web assets and `dist/stackpilot.exe` built |
| `node --check test/browser-session-lifecycle-gate.js` | Passed |
| `git diff --check` | Passed |

Deterministic Go tests cover initial renewal expiry, refresh at 29 minutes, repeated refresh, absolute-expiry truncation, exact expiry, restart invalidation, revocation, token rotation, concurrent refresh/authenticate/CSRF validation, Cookie rounding, and Cookie deletion. Web tests cover fragment removal, Cookie recovery, timer/focus/visibility/mutation single-flight, mutation ordering, cleanup, REST invalidation, SSE invalidation, and stopped reconnect.

## Browser Gate

The in-app browser backend was not available in this desktop environment, so the required local browser verification used Playwright CLI against an isolated control plane on a non-default port. No real credential was used by the accelerated fixture.

- The unauthenticated real handler performed one `GET /api/v1/auth/session`, received 401, and rendered the explicit `stackpilot open` recovery path with no ordinary retry button.
- Desktop 1440x900 and mobile 390x844 screenshots showed no overlap or overflow.
- The accelerated fixture returned a four-minute initial expiry. The production coordinator immediately coalesced timer/focus/visibility/mutation triggers into one refresh, then rendered the authenticated application.
- The fixture then returned concurrent stable-code REST 401 responses. The coordinator entered `expired`, stopped follow-up requests, and did not expose the unreachable-state retry action.
- The active Cookie was verified as HttpOnly, SameSite Strict, and Path `/`. localStorage, sessionStorage, and IndexedDB were empty.

Screenshots are under `output/playwright/session-active.png`, `session-invalidated.png`, `session-expired-desktop.png`, and `session-expired-mobile.png`. They contain only fictional/empty fixture data.

## Windows lifecycle investigation

An isolated current-user fixture used `<repo>/.local/session-hardening-install`, a separate data root, port 32110, and the unique HKCU Run value `StackPilot-Session-Hardening-Gate`.

- Initial PID 32904 started with a structured `startup` record.
- Candidate upgrade stopped PID 32904 with `reason=upgrade`, `exit_code=0`, then started PID 9464.
- Normal CLI stop recorded `reason=control_stop`, `exit_code=0`.
- The fixture was restarted as PID 3688 and forcibly terminated after its executable path was verified under the isolated install root. `service status` then reported `stopped`, while the last lifecycle record remained `startup` and no exit record existed.

The result explains the observed state without inventing an external terminator: `stopped` means the authenticated control Pipe is unavailable, not that a graceful stop was observed. The generic HTTP layer records only `context_cancelled`; the installed-runtime boundary owns `control_stop`, `upgrade`, or `signal`. Structured logs are sufficient, so no running marker was added.

Uninstall returned `not-installed`; the install root, unique HKCU Run value, fixture process, and port 32110 were absent afterward. The separate ignored data root remains as the lifecycle evidence source. No watchdog, scheduled restart, Windows Service, or control-plane auto-recovery was introduced.

## Limits

The browser Gate accelerates renewal through a mock API route rather than waiting 30 minutes or eight hours. Authoritative time boundaries are covered by injected-clock Go tests. A real `stackpilot open` active-session run was not repeated because the Gate intentionally avoided exposing or persisting a bootstrap or long-lived token; the existing P1D authentication evidence covers that exchange path.
