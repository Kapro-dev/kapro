# ADR-006: Multi-Cloud Provider Onboarding

**Status:** Superseded.
**Original date:** 2026-04-19
**Superseded date:** 2026-04-24

## Summary (current state)

The generic cluster-provider abstraction proposed in this ADR — `pkg/provider`,
`internal/provider/{crd,gke}`, the `ProviderSpec` field on `MemberCluster`,
and the pluggable KCI (Kapro Cluster Interface) — has been **removed** from
the Kapro runtime. Multi-cluster onboarding and inventory are now handled by
two concrete, non-pluggable mechanisms:

- **`MemberCluster` CRD** — the single cluster-inventory object. Platform
  engineers register each workload cluster by creating a `MemberCluster` with
  an `ActuatorSpec` (how to deliver) and optional `HealthCheckSpec` / `Topology`.
- **`internal/bootstrap`** — the spoke-side bootstrap path used by
  `kapro-cluster-controller` to register with the hub. It uses Kubernetes
  CertificateSigningRequests and optionally GCP Workload Identity Federation
  for hub authentication. This is a fixed, code-level path — not a runtime
  extension interface.

There is no runtime dispatch by `spec.provider.type` and no generic provider
registry. Cloud-specific onboarding details (e.g. GKE, EKS, AKS) are not a
Kapro extension surface.

## Why this was superseded

- The abstraction was broader than runtime truth: only the CRD-based path was
  ever exercised in live reconcile; the direct-connect path was half-wired.
- The extra concepts (KCI-Connect vs KCI-Register, `ProviderSpec`,
  `ProviderRegistry`) increased conceptual surface without increasing capability.
- A Flux-style operator benefits more from a narrow, truthful CRD (`MemberCluster`)
  than from a pluggable provider interface.

## See also

- `docs/SPEC.md` — current architecture.
- `docs/adr/ADR-007-kxi-interface-family.md` — sibling ADR, also superseded.
- `internal/bootstrap/bootstrap.go` — current spoke bootstrap path.
- `api/v1alpha1/types.go` (`MemberCluster`) — current cluster inventory.

## Historical context

This ADR originally proposed onboarding patterns for cloud-specific variants
(GKE, EKS, AKS, on-prem). The intent survives — Kapro can still be extended
to those environments — but the mechanism has changed. Extension now happens
at the **actuator** layer (`pkg/actuator`) and via `MemberCluster` bootstrap
configuration, not through a generic cluster-provider interface.
