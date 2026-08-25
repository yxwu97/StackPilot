# Controlled Compose Build And Import Verification Evidence

Date: 2026-08-22  
Plan: `plan/plan-20260822-01-controlled-compose-build-and-import.md`  
Decision: ADR-0008, with narrow ADR-0005 and ADR-0007 revisions  
Status: repository implementation, isolated Gates, and the separately authorized GNMarket read-only probe/analyze Gate passed; write/runtime Gates remain pending

## Delivered Contract

- `phase2.compose-build` is a separate public capability. Compose build remains disabled unless the Manifest explicitly selects `compose.buildPolicy: always`; the closed default is `never`.
- BAT and PowerShell files remain read-only analysis inputs. The importer follows only bounded, literal BAT -> PS1 -> Compose references and never executes either script.
- The importer produces a structured Compose candidate with sorted managed/build services, ports, evidence, and per-service `healthy|running` readiness. Build and every `running` downgrade require independent confirmation before apply.
- Build contexts and Dockerfiles must be regular local files inside the canonical workspace. Remote contexts and advanced build fields such as args, Secret, SSH, cache, entitlements, network, and additional contexts are rejected.
- Explicit system Start and Restart run fixed `docker compose ... build <services>` followed by fixed `up ... --no-build`. User and automatic service restart call `StartWithoutBuild` and never build.
- Build failure, timeout, and cancellation do not proceed to `up`. Ordinary Stop preserves volumes, images, and BuildKit cache.
- Compose identity persists build policy, sorted build services, per-service readiness, and start timeout. Tokens written before the timeout field existed retain the historical default.

## Automated Gates

| Command / gate | Result |
| --- | --- |
| `go test -timeout 180s ./...` | Passed; includes Manifest Schema, OpenAPI contract, importer, API, driver, recovery, SQLite, and orchestration regressions |
| `go vet ./...` | Passed |
| `go test -timeout 90s ./internal/driver/compose ./internal/api` | Passed |
| `go test -timeout 120s ./internal/orchestrator` | Passed, including real SQLite build/restart/failure semantics |
| `npm run test:web` | Passed, 24 tests |
| `npm run type-check` | Passed |
| `npm run build` | Passed; built Web assets and `dist/stackpilot.exe` version 0.1.0. The earlier sandboxed Web build hit Windows `spawn EPERM`, and the required host-permission reruns passed |

The Compose tests assert exact, sorted build/up argument arrays. Build failure, timeout, and cancellation all assert that no up command ran. API regression coverage uses a five-service Compose import candidate and requires separate build, `job running`, and `gateway running` confirmations. Existing no-build Compose, Maven/npm/Java/Node import, and service restart behavior remain covered.

## Windows Docker Gates

Environment:

- Docker client/engine: 29.5.3, API 1.54
- Docker Desktop: 4.77.0 (228796)
- Docker Compose: v5.1.4
- Engine OS/architecture: linux/amd64

Actual isolated Gate commands and results:

| Command / gate | Result |
| --- | --- |
| `STACKPILOT_COMPOSE_BUILD_INTEGRATION=1 go test -timeout 180s -v ./internal/driver/compose -run '^TestInstalledControlledComposeBuildLifecycle$'` | Passed in 19.73s |
| Installed Compose preflight, override, lifecycle, and log/health Gates | All passed in 18.19s |
| Installed Compose orchestration/control-plane recovery Gate | Passed in 8.59s |

The controlled build Gate compiled the repository's static Linux process fixture, built a unique local `FROM scratch` image through the production Compose lifecycle, started it with `up --no-build`, reached healthy, and stopped it. It then replaced the Dockerfile with invalid content: `StartWithoutBuild` still started the existing image, proving the service-restart path skipped build; a subsequent explicit Start returned `COMPOSE_BUILD_FAILED`, and inspection proved the project remained stopped.

Cleanup used only the exact generated Compose project, volume, network, and uniquely named image. Follow-up Docker queries found none of those resources. Existing unrelated AIWS Compose containers were observed and left untouched. No global image, volume, or BuildKit cache prune was run; fixture build cache may remain by ADR policy.

## Browser Gate

Playwright CLI exercised the production Vue import dialog against the fictional repository-local five-service fixture at desktop and 390 x 844 mobile viewports:

- probe -> analyze, disabled apply with initial blockers, independent build and two running-readiness confirmations, correction, and successful registration;
- confirmation state remained checked after the corrected candidate response;
- long stable error codes wrapped on mobile, dialog/steps/table content had no horizontal overflow;
- final mobile width metrics were viewport/document `390/390`, overlay `375/375`, and dialog `358/358`.

Screenshots:

- `output/playwright/controlled-compose-import-desktop.png`
- `output/playwright/controlled-compose-import-mobile.png`

The Playwright session and local UI fixture listener on port 32144 were closed after verification.

## Storage, Compatibility, And Security

No SQLite migration was required. Manifest snapshots, resolved specs, OperationStep records, and the existing Compose identity token carry the new data. Legacy identity-token coverage verifies the historical timeout default; normalized Manifest tests cover the new closed defaults and digest behavior.

The source tree, DTOs, operation errors, and test evidence do not contain Dockerfile bodies from a real project, complete Compose config output, credentials, tokens, or unredacted child-process environments. The repository fixture is fictional and does not access or copy content from `E:\GNMarket`.

`AGENTS.md` and `CLAUDE.md` were not changed and both had SHA-256 `4F4400A0BBCC542096D6DC7A31B54EFEF351D3E3C932D7126A17F5CAA69456B1` at finalization.

## Real GNMarket Gate Status

On 2026-08-23, the separately authorized read-only Gate ran against `E:\GNMarket`.
The production probe found `start-gnmarket.bat`, and the importer followed the fixed
BAT -> `scripts/dev-up.ps1` -> `compose.yaml` source graph without executing BAT,
PowerShell, Docker, or any business command. The analysis passed after closing three
fixture gaps: shallow BAT candidates must not be starved by deep cache directories,
a recognized Compose command may have one exact `$LASTEXITCODE` failure guard, and a
BAT wrapper may compare a saved `%ERRORLEVEL%` through the fixed negated equality
guard. Malicious guard variants remained blocked by the importer regression suite.

Manifest write, registration, Docker build/up, readiness, logs, and stop Gates remain
pending because they have side effects and require separate authorization under the
plan. The read-only Gate did not modify `E:\GNMarket`, create its Manifest, or access
the Docker daemon.
