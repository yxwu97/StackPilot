# ADR-0002: Web Authentication Uses One-Time Fragment Bootstrap

- Status: Accepted
- Date: 2026-08-18
- Decision gate: D-05 / P1D-04 / P1D-05

## Context

StackPilot listens only on loopback in Phase 1, but a malicious browser origin can still send requests to local HTTP services. The Web console therefore needs authentication without placing the long-lived local access token in browser storage, URL queries, referrers, history, server logs, or frontend bundles. Installation-time browser session creation would couple authentication to one installer and would not cover later `stackpilot open` invocations.

## Decision

1. `stackpilot open` reads the long-lived local token from OS-protected storage and requests a one-time bootstrap code with Bearer authentication.
2. Bootstrap codes contain 256 random bits, live for 60 seconds, are stored only in Server memory as digests, and are consumed exactly once.
3. The CLI opens `http://127.0.0.1:<port>/#bootstrap=<code>`. The code is in the URL fragment, so browsers do not send it in the HTTP request or Referer header.
4. The Web app reads the fragment, immediately removes it with `history.replaceState`, and exchanges the code at `POST /api/v1/auth/session` using an exact same-origin `Origin` and JSON body.
5. A successful exchange sets an opaque `HttpOnly`, `SameSite=Strict`, `Path=/` session cookie. Browser sessions use a 30-minute renewal window and an eight-hour absolute lifetime. A successful refresh advances the renewal expiry to `min(now + 30m, absoluteExpiresAt)` and resets the cookie to the same effective expiry. Thirty minutes without a successful refresh expires the session; this is not keyboard or pointer idle detection.
6. `GET /api/v1/auth/session` restores the in-memory CSRF value after a page reload and renews an active session. The response is `Cache-Control: no-store`; server time is authoritative. It never revives a renewal-expired, absolute-expired, revoked, restart-invalidated, or rotation-invalidated session.
7. Each successful refresh rotates CSRF. The server accepts only the current CSRF and one immediately previous CSRF for a bounded five-minute grace period, never beyond the session expiry. This closes the refresh-versus-in-flight-mutation race without retaining unbounded token history. Tabs publish the newest CSRF and expiry through an ephemeral `BroadcastChannel`; no authentication material is written to browser storage.
8. Within a tab, one session coordinator owns a single refresh promise. Across tabs, the same-origin Web Locks API serializes bootstrap and refresh requests; a waiting tab skips its refresh when BroadcastChannel has already delivered newer credentials. New mutations wait for the refresh promise before capturing the CSRF header. Mutations already sent may finish with the bounded prior-CSRF grace; failed mutations are never automatically replayed.
9. Renewal is scheduled five minutes before the known expiry and is also checked on page visibility, focus, and before mutation. Transient failures retry with bounded exponential delays only while a retry can finish before the known expiry. A `401 AUTH_SESSION_INVALID` immediately enters the global expired state and stops REST mutations, renewal timers, and SSE reconnects.
10. Cookie `Expires` and `Max-Age` are derived from the same injected server clock used by the authentication manager. Positive sub-second remainders round up to one second without making `Expires` exceed the authoritative expiry; zero or negative values are used only for deletion. Session exchange, refresh, revoke, and browser token rotation use one cookie constructor.
11. Browser mutation requests require the session cookie, exact Origin, JSON Content-Type, and matching `X-StackPilot-CSRF`. Bearer-authenticated CLI requests do not use cookies or CSRF.
12. Ordinary API routes never accept access tokens or bootstrap codes from query parameters. Static SPA assets and health/version endpoints may remain anonymous; business routes below `/api/v1` require Bearer or session authentication except the session exchange endpoint.
13. Server restart, logout, and local-token rotation invalidate bootstrap codes and browser sessions. The user re-runs `stackpilot open`; managed systems are unaffected.

## Consequences

- The browser never receives the long-lived token and cannot read the session cookie.
- A copied bootstrap URL has a short replay window and becomes useless after first exchange.
- Active pages can remain authenticated for at most eight hours. Reloading within the renewal lifetime works; expiration or restarting StackPilot requires a new bootstrap.
- The CLI and Web authentication paths share authorization semantics but retain distinct CSRF requirements.
- HTTPS is not required for the Phase 1 loopback-only listener. The cookie cannot use `Secure` over local HTTP; remote listening remains disabled.
- The prior-CSRF grace slightly increases the usable lifetime of one superseded per-session value. It is bounded to one digest and five minutes, remains session-bound, and still requires the exact Origin and JSON request constraints.

## Contract and lifecycle closure

| Surface | Before | Accepted behavior |
| --- | --- | --- |
| Go authentication config | One 15-minute `SessionTTL` | 30-minute renewal TTL, eight-hour absolute TTL, injected clock |
| Session state | CSRF digest and one expiry | current/prior CSRF digests, prior grace, renewal expiry, absolute expiry |
| `GET /auth/session` | CSRF rotation only | atomic CSRF rotation and renewal, refreshed cookie, no-store response |
| Session response | `csrf`, `expiresAt` | unchanged; no session identifier or absolute expiry exposure |
| REST/SSE failure | local request/store errors | stable-code global invalidation; `403`, `404`, `409`, and `500` remain ordinary errors |
| Browser persistence | none | unchanged; cross-tab coordination is ephemeral only |

The browser coordinator serializes refresh and new mutations within a tab and uses a named same-origin Web Lock across tabs. At most the newest and immediately preceding CSRF digests validate. Broadcast messages contain only the same ephemeral `csrf/expiresAt` response already held in memory; a bootstrap message resets other tabs after the shared Cookie changes. Page unload needs no special write because all browser state remains ephemeral. Browsers without Web Locks retain the bounded prior-CSRF fallback but may require one tab to recover after truly simultaneous refreshes.

Transient refresh retries use delays of 1, 2, 4, 8, and at most 15 seconds. A retry is not scheduled when the known expiry would be reached before that delay plus a one-second request budget. Visibility and focus triggers coalesce into the current promise and do not reset retry history.

## Rejected Alternatives

- Query-string token: leaks through history, logs, screenshots, and referrers.
- Long-lived token in localStorage/sessionStorage: exposes the master token to any frontend script compromise.
- Installer-created session only: does not support later browser launches or portable/no-installer use.
- Relying on loopback and SameSite alone: does not meet the explicit local malicious-origin and CSRF threat model.
