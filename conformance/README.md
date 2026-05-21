# Kapro Conformance

This directory contains reusable Go test harnesses for external plugin
contracts:

- `actuator`: KAI actuator conformance.
- `gate`: KGI gate conformance.
- `planner`: KPI planner conformance.

The harnesses are imported by plugin repositories and executed against a live
gRPC plugin server. They intentionally test contract behavior only: idempotency,
determinism, valid result shapes, request immutability, capabilities, and
context cancellation.

Usage examples and per-suite invariants are documented in the Go doc comments of each subpackage (`go doc kapro.io/kapro/conformance/actuator`, `go doc kapro.io/kapro/conformance/gate`, `go doc kapro.io/kapro/conformance/planner`).
