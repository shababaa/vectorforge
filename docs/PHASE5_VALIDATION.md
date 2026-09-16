# Phase 5 validation record

Captured on 2026-09-01 with Go 1.23.12 on Linux x86-64, an Intel Xeon
Platinum 8573C runner, and commit content prepared for the
`phase-2-real-data-pipeline` branch. This is development-machine evidence, not
an internet-scale or cross-machine performance claim.

## Acceptance matrix

| Requirement | Evidence | Result |
| --- | --- | --- |
| Fresh schema migration | Repository and service integration tests create an empty file and assert schema v5 readiness | Pass |
| End-to-end seed | 600 synthetic sessions → features → experiment/model artifacts → forecast → grounded analysis → manifest | Pass in 1.15 s observed startup |
| Repeatable start | A second `SeedServiceDemo` call returns the original experiment/analysis IDs | Pass |
| API contract | `httptest` checks overview, readiness, request IDs, CSP, dashboard HTML, metrics, bearer auth, and job acceptance | Pass |
| Durable queue | Tests cover canonical idempotency, conflict, atomic claim, result, cancellation, and interrupted-running recovery | Pass |
| Static frontend syntax | `node --check market/service/ui/assets/app.js` | Pass |
| Go correctness | `go test -buildvcs=false ./...` | Pass |
| Static analysis | `go vet -buildvcs=false ./...` | Pass |
| Race detector | `go test -buildvcs=false -race ./...` | Pass |
| Windows compatibility | Cross-compiled root/service tests and `cmd/marketserver` for `windows/amd64` | Pass; PE32+ executable produced |
| Docker definition | Non-root multi-stage image, loopback Compose port, read-only root, dropped capabilities, health check | Defined; local runner had no Docker executable, CI build is authoritative |

The Windows build uses the platform-specific no-op directory-sync helper while
retaining file `Sync` at database durability boundaries. Non-Windows builds
continue to sync the parent directory after file creation.

## HTTP load snapshot

The acceptance database was 55,644,160 bytes and contained the complete seeded
workflow. The command exercised full overview construction and JSON
serialization over loopback:

```sh
go run -buildvcs=false ./cmd/marketload \
  -url 'http://127.0.0.1:18082/api/v1/overview?symbol=SYNTH&interval=1d&limit=120' \
  -duration 5s -concurrency 16
```

Observed result:

| Metric | Value |
| --- | ---: |
| Requests | 1,317 |
| Successful | 1,317 |
| Errors/non-2xx | 0 |
| Throughput | 261.62 requests/s |
| p50 | 58.81 ms |
| p95 | 93.23 ms |
| p99 | 115.00 ms |
| Response bytes | 45,300,835 |

All responses were HTTP 200 and the server log contained zero error-level lines.
The client stopped scheduling at five seconds and drained in-flight requests;
elapsed time was 5.034 seconds. These numbers include indexed database reads,
model-card and analyst-artifact reads, DTO construction, and JSON encoding.
They exclude browser rendering, TLS, network distance, and concurrent writes.

## Limits on the evidence

- Synthetic data validates mechanics, not investment value or live-data quality.
- Loopback load testing does not predict public-internet latency.
- The five-second sample is an acceptance check, not capacity planning.
- One process owns the file; no replication or distributed queue was tested.
- The deterministic fixture analyst is not an LLM quality score. Phase 4's live
  Ollama acceptance remains separate.
- Docker files were not locally executed because Docker was absent. The checked-in
  CI container job must pass before treating the image build as independently
  reproduced.

The raw procedure is reproducible from [the service runbook](SERVICE.md). Resume
claims should quote the command, machine context, and limitations alongside any
headline number.
