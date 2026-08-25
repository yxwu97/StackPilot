# Phase 2.1 Three-System and Diagnostics Gate

Status date: 2026-08-19

Phase 2.1 is verified. No release waiver is required.

| Gate criterion | Result | Evidence |
| --- | --- | --- |
| PMS/BTC preferred Web port conflict resolves and remains sticky | verified | BTC 32102, PMS 32400, sticky PMS restart |
| PMS Backend/RAG/Web use real readiness | verified | Maven, Python venv, npm endpoints all ready; no fixed-delay acceptance |
| Rule diagnostics cover required failures with traceable evidence | verified | controlled port/exit/timeout plus Java/Node/Python fault matrix |
| Incident context excludes Secret and suggestions cannot execute high-risk actions | verified | redaction/budget tests, plaintext scan, `automatic=false`, analyze-only browser request |
| BTC, AIWS, and PMS regressions pass | verified | current candidate passed all three real workspace scripts |
| Phase 2.0 database upgrades directly to Phase 2E | verified | production migration 1-14 -> 15 test preserves historical health data |

## Accepted Real Gate Results

BTC installation/recovery/upgrade:

```text
candidate 0.2.1-gate.1 -> 0.2.1-gate.2
instance si_01M0BV1PBPYQ5PGY0PTV8ZV5CY
backend PID 24480, web PID 32820
control PID 33320 -> 20332 -> 39084
log sequence 82 -> 82
backend 8081, web 32102
```

AIWS:

```text
instance si_01M0BVADC10J4CTEAB0GCJ8C4J
start op_01M0BVADBXRGEA46P98GE4BBFB
repeat op_01M0BVBNM0D7GSNES92SAAZAKH
stop op_01M0BVC2JPYB2766AH7D59TGW5
5 services, 13 ports, same-instance recovery
```

PMS/BTC:

```text
BTC instance si_01M0BVEDVZDZZYNHBH5A05AHS3, web 32102
PMS instances si_01M0BVEX9E0SKD1GDWAXD4P3HF -> si_01M0BVHJCSD92WFMKC1XNNDE6X, web 32400
PMS start op_01M0BVEX9B6G7T4FR1CD6MV12S
PMS repeat op_01M0BVGK2GM228Y4CCRQHCT4CY
PMS sticky restart op_01M0BVHJCK1BGH8C3Y1AYK15P5
```

The first AIWS attempt correctly failed closed with `DOCKER_DAEMON_UNAVAILABLE` while Docker Desktop was stopped; it was not accepted as a Gate result. After restoring the daemon, the complete Gate passed. Final verification includes full Go/Web regression, static checks, contract/schema checks, Secret scanning, and resource cleanup.
