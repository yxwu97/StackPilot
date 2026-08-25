# Windows MVP Release Gate

Status date: 2026-08-18

The Phase 1 Windows MVP Gate is verified. No release waiver is required.

| Overall-design 26.1 criterion | Result | Primary evidence |
| --- | --- | --- |
| Web, CLI, and VSCode start/stop BTC without project scripts | verified | P1C-05, P1C-07, P1D-07, P1D-08 |
| Lifecycle request returns a durable Operation with live step progress/logs | verified | P1B-01, P1C-05, P1C-09 |
| Backend readiness gates Web and timeout failures remain explicit | verified | P1B-03, P1B-09, P1C-03 |
| Port conflicts are detected before process creation and policy is applied | verified | P1A-06, P1B-10, P1C-10 |
| Final ports reach Spring Boot, Vite proxy, readiness, CORS, and access URLs | verified | P1A-07, P1A-08, P1C-02, P1C-06 |
| Control-plane restart recovers the same running services | verified | P1D-01, P1D-02, P1D-11 |
| Reverse stop terminates complete Maven/Java/npm/Node trees | verified | P0-08, P1B-03, P1C-11, P1D-11 |
| Logs support live/history/service filters and resumable cursors | verified | P1B-05, P1B-08, P1D-02, P1D-09 |
| API rejects arbitrary commands and every mutation is recorded | verified | P1B-01, P1D-06, P1D-11 |
| `.stackpilot/system.yaml` is the sole definition with correct refresh/cache semantics | verified | P1A-02 through P1A-05, P1C-01, P1D-09 |

## Release-Candidate Run

- `scripts/verify-p1d-11.ps1` installed candidate `0.1.0-p1d11.5` from absent roots, started real BTC, forcibly ended the control plane while service PIDs remained alive, recovered the same instance and PIDs, upgraded in place to `0.1.0-p1d11.6`, stopped BTC in reverse order, and uninstalled while preserving the separate data root.
- The verified control-plane PID sequence was `35036 -> 38356 -> 6312`; backend PID `38088` and Web PID `14296` remained stable through restart and upgrade. Backend log sequence remained `62` across takeover. Ports `8081`, `32102`, and control port `32107` were closed after stop/uninstall.
- Command override, non-registered workspace, canonical path, authentication, exact Origin, CSRF, content-type, audit, and redaction tests passed. Exact-process control-plane shutdown and immutable-version Supervisor trust are covered by focused tests and the real upgrade Gate.

## Engineering Gate

- `scripts/check.ps1`: Web tests 6/6, Vue/TypeScript strict checking, Vite production build, all Go tests, and `go vet ./...` passed.
- `go test -race ./cmd/stackpilot ./internal/... -count=1`: passed on Windows with repository-local MinGW 16.1.0.
- `CGO_ENABLED=0 GOOS=linux|darwin GOARCH=amd64 go build ./...`: both public-core builds passed; this is a compile boundary check, not a runtime-support claim.
- GoReleaser v2.12.7 produced `stackpilot_0.0.0-SNAPSHOT-b47ab22_windows_amd64.zip` containing only `README.md` and `stackpilot.exe`. SHA-256 `31c7a96eacd3d22e74e4e94c03acf9c2cc50061dbe806257825346edd34050f9` matched `checksums.txt`. Embedded metadata reported version `0.0.0-SNAPSHOT-b47ab22`, commit `b47ab22f97c765a06b4b4cee51531abc50a037e5`, and UTC build time `2026-08-18T02:58:14Z`.
- The production-function length check, `git diff --check`, and exact `AGENTS.md`/`CLAUDE.md` SHA-256 match passed. No runtime configuration uses port `5173`.
- Final elevated scan found no StackPilot process and no listener on `32100`, `32101`, `32102`, `32103`, `32105`, `32107`, or `8081`. Preserved validation data was not deleted.

All P0 through P1D work packages and the Windows MVP functional and engineering Gates are verified. Phase 2A may begin.
