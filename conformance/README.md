# Kapro Conformance

This directory contains reusable Go test harnesses for external plugin
contracts:

- `actuator`: KAI actuator conformance.
- `gate`: KGI gate conformance.
- `planner`: KPI planner conformance.
- `provider`: KSP spoke provider conformance.

The harnesses are imported by plugin repositories and executed against a live
gRPC plugin server. They intentionally test contract behavior only: idempotency,
determinism, valid result shapes, request immutability, capabilities, and
context cancellation.

Kapro also ships a CLI wrapper for authors who want to test a running plugin
without writing Go tests:

```bash
go run ./cmd/kapro-conformance actuator --endpoint localhost:9090
go run ./cmd/kapro-conformance gate --endpoint localhost:9090
go run ./cmd/kapro-conformance planner --endpoint localhost:9090
```

KSP provider conformance is currently a Go harness because KSP is an
in-process spoke-side SDK contract.

Use repeated `--param key=value` flags to pass plugin-specific scenario
parameters.

Usage examples and per-suite invariants are documented in the Go doc comments of each subpackage (`go doc kapro.io/kapro/conformance/actuator`, `go doc kapro.io/kapro/conformance/gate`, `go doc kapro.io/kapro/conformance/planner`).
