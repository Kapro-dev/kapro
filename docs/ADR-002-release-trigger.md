# ADR-002: ReleaseTrigger Must Be Safe by Default

**Status:** Proposed  
**Date:** 2026-05-14  
**Deciders:** Vinayaka Krishnamurthy (@Kapro-dev)  

---

## Context

Kapro can eventually close the gap between CI and fleet rollout by watching
external artifact sources and creating `Release` objects automatically. This is
similar to Kargo's Warehouse pattern: an external source detects new freight and
feeds the promotion engine.

The same feature is dangerous if it turns every pushed tag into a fleet-wide
production rollout. A broken CI loop, mutable tag, or compromised registry could
create repeated releases faster than humans or gates can reason about them.

---

## Decision

Add a `ReleaseTrigger` CRD after v1 only if it is safe by default.

`ReleaseTrigger` creates `Release` objects. It does not apply manifests, bypass
pipeline gates, mutate active releases, or promote directly to production.

The first implementation should support OCI registry sources only. Additional
sources such as GitHub releases, MLflow model registry events, Prometheus
alerts, and external webhooks can be added after the OCI path is proven.

---

## Required Safeguards

Every implementation must include these controls:

| Safeguard | Requirement |
|---|---|
| Suspended creation | Created `Release` objects default to `spec.suspended: true` unless explicitly disabled. |
| Tag filtering | `spec.tagPattern` is required. Do not trigger on every tag by default. |
| Digest pinning | Created releases must reference an immutable OCI digest, not only a mutable tag. |
| Signature policy | `spec.requireSignature` verifies artifacts before creating releases. |
| Cooldown | `spec.cooldown` prevents rapid-fire release creation. |
| Max active | `spec.maxActive` limits concurrent releases created by one trigger. |
| Scope | `spec.scope` can restrict created releases to canary stages or selected clusters. |
| Dry run | `spec.dryRun` records what would be created without creating it. |
| Idempotency | Status records observed tag/digest pairs so repeated polls do not create duplicates. |
| Conditions | Status exposes `Ready`, `Suspended`, `ArtifactVerified`, and `ReleaseCreated` conditions. |

---

## Proposed Shape

```yaml
apiVersion: kapro.io/v1alpha1
kind: ReleaseTrigger
metadata:
  name: checkout-oci
spec:
  suspended: true
  source:
    type: oci
    oci:
      repository: oci://registry.example.com/checkout
      tagPattern: "^v[0-9]+\\.[0-9]+\\.[0-9]+$"
      requireSignature: true
  releaseTemplate:
    kaproAppRef:
      name: checkout
    pipelineRef:
      name: production
    suspended: true
    scope:
      stages:
        - canary
  cooldown: 30m
  maxActive: 1
  dryRun: false
```

---

## Non-Goals

- No direct hub-to-spoke apply path.
- No replacement for CI build and signing.
- No automatic production promotion by default.
- No mutable update of an existing `Release`.
- No source-specific controller sprawl before the OCI trigger proves useful.

---

## Consequences

This keeps Kapro Kubernetes-native and extensible without making autonomous
delivery the default behavior. Platform teams can start with detection-only
automation, review the created release, then progressively relax the safeguards
when they trust the pipeline and signature policy.

