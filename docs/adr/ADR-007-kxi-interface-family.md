# ADR-007: KXI — The Kapro Extension Interface Family

**Status:** Superseded.
**Original date:** 2026-04-19
**Superseded date:** 2026-04-24

## Summary (current state)

The original KXI proposal grouped seven interfaces (KAI, KGI, KHI, KVI, KRI,
KNI, KCI) under a unifying naming and registration story. Since then, the
project has narrowed its pluggable runtime extension surface to **two**
interfaces:

| Name | Go package     | Role                                              |
|------|----------------|---------------------------------------------------|
| KAI  | `pkg/actuator` | How a version is applied to a target cluster     |
| KGI  | `pkg/gate`     | Whether a rollout may advance to the next phase  |

Both are backed by the generic `pkg/registry` type, register implementations
by name at operator startup, and are validated by a conformance suite in
`conformance/gate` and `conformance/actuator`.

## What changed

- **KCI removed.** `pkg/provider`, `internal/provider/*`,
  `MemberClusterSpec.Provider`, and `conformance/provider` are all gone.
  See `ADR-006-multi-cloud-provider-onboarding.md`.
- **KHI / KVI / KRI / KNI are not runtime extension points.** Health,
  verification, OCI fetch, and notification live as internal packages
  (`internal/health`, `internal/verification/cosign`, `internal/oci/oras`,
  `internal/notification`) with fixed implementations. They may grow an
  interface later, but they are not advertised as pluggable today.
- **Plugin-over-gRPC CRD (`PluginRegistration`) is not implemented.**
  Extension happens by compiling an implementation into a Kapro binary and
  registering it at startup. Out-of-process plugins are a future option,
  not a current contract.

## Why this was superseded

- Advertising seven extension interfaces when only two are truly pluggable
  misled contributors and inflated the conceptual surface.
- Cluster onboarding is better served by a concrete `MemberCluster` CRD than
  by a generic provider abstraction (see ADR-006).
- A Flux-style operator is judged by the **truthfulness** of its extension
  contract, not its breadth.

## See also

- `docs/SPEC.md` — current architecture and extension points.
- `docs/adr/ADR-006-multi-cloud-provider-onboarding.md` — sibling ADR.
- `pkg/actuator/` and `pkg/gate/` — the two current extension interfaces.
- `conformance/gate/suite.go` and `conformance/actuator/suite.go` — the
  contracts any external implementation must satisfy.

## Historical context

This ADR originally described a systematic, gRPC-backed family of seven
interfaces. The naming convention (KAI / KGI / …) and the
"one interface, one question" design principle live on, but only where
runtime actually uses them. The rest has been removed to keep the
advertised architecture truthful.
