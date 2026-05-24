# Kapro Conformance

This directory contains reusable Go test harnesses for external plugin
contracts:

- `actuator`: KAI actuator conformance.
- `gate`: KGI gate conformance.
- `planner`: KPI planner conformance.
- `provider`: KSP spoke provider conformance.
- `substrate`: KSI substrate conformance.

The plugin harnesses are imported by plugin repositories and executed against a
live gRPC plugin server. Provider and substrate harnesses are Go-level SDK
contracts. All suites intentionally test contract behavior only: idempotency,
determinism, valid result shapes, request immutability, capabilities, and
context cancellation.

Kapro also ships a CLI wrapper for authors who want to test a running plugin
without writing Go tests, or run the reference suites locally in CI:

```bash
go run ./cmd/kapro-conformance all
go run ./cmd/kapro-conformance all -o json
go run ./cmd/kapro-conformance actuator --endpoint localhost:9090
go run ./cmd/kapro-conformance gate --endpoint localhost:9090
go run ./cmd/kapro-conformance planner --endpoint localhost:9090
go run ./cmd/kapro-conformance provider
go run ./cmd/kapro-conformance substrate
```

KSP provider and KSI substrate conformance are currently Go harnesses because
they are in-process SDK contracts. `kapro-conformance provider` runs the same
provider suite against Kapro's reference provider. `kapro-conformance
substrate` runs reference scenarios for `kubernetes-apply`, `argo-cd`, and
`flux`; custom substrates should still import
`kapro.io/kapro/conformance/substrate` from their own tests until a public
`kapro substrate conformance <class>` CLI is promoted.

The `substrate` suite proves the KSI request/result contract. In `0.6`, the
current in-tree direct, Argo CD, and Flux runtime paths still execute through
legacy actuator/controller adapters, so those paths remain covered by targeted
runtime tests. The reference substrate scenarios are not a substitute for
real-cluster apply tests; authors that can safely mutate an envtest or kind
cluster should provide a non-dry-run `Scenario` from their own repository.

Use repeated `--param key=value` flags to pass plugin-specific scenario
parameters.

Usage examples and per-suite invariants are documented in the Go doc comments
of each subpackage:

```bash
go doc kapro.io/kapro/conformance/actuator
go doc kapro.io/kapro/conformance/gate
go doc kapro.io/kapro/conformance/planner
go doc kapro.io/kapro/conformance/provider
go doc kapro.io/kapro/conformance/substrate
```
