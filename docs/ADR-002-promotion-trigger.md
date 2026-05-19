# ADR-002: PromotionTrigger Must Be Safe by Default

**Status:** Proposed  
**Date:** 2026-05-14  
**Deciders:** Vinayaka Krishnamurthy (@Kapro-dev)  

---

## Context

Kapro can eventually close the gap between CI and fleet rollout by watching
external artifact sources and creating `PromotionRun` objects automatically. The
trigger watches for verified artifact changes and feeds the normal Kapro
promotion promotionplan.

The same feature is dangerous if it turns every pushed tag into a fleet-wide
production rollout. A broken CI loop, mutable tag, or compromised registry could
create repeated promotionruns faster than humans or gates can reason about them.

---

## Decision

Introduce a `PromotionTrigger` API only if it is safe by default. The first
controller implementation observes OCI sources and creates PromotionRuns only after
the configured safeguards pass.

`PromotionTrigger` creates `PromotionRun` objects. It does not apply manifests, bypass
promotionplan gates, mutate active promotionruns, or promote directly to production.

The first implementation should support OCI registry sources only. Additional
sources such as GitHub releases, MLflow model registry events, Prometheus
alerts, and external webhooks can be added after the OCI path is proven.

The controller must reject unsafe or malformed trigger configuration before it
contacts an artifact source. Invalid source settings, tag patterns, poll
intervals, cooldowns, and negative concurrency limits stall the trigger instead
of falling through to promotionrun creation.

---

## Required Safeguards

Every implementation must include these controls:

| Safeguard | Requirement |
|---|---|
| Suspended creation | Created `PromotionRun` objects default to `spec.suspended: true` unless explicitly disabled. |
| Tag filtering | `spec.tagPattern` is required. Do not trigger on every tag by default. |
| Tag ordering | Matching OCI tags are selected by semantic-version ordering when they are semver-like, including `v1.10.0` over `v1.2.0`; non-semver tags keep deterministic lexical ordering. |
| Digest pinning | Created promotionruns must reference an immutable OCI digest, not only a mutable tag. |
| Signature policy | `spec.requireSignature` verifies artifacts before creating promotionruns. |
| Cooldown | `spec.cooldown` prevents rapid-fire promotionrun creation and also considers recent trigger-owned PromotionRuns so status drift cannot bypass the delay. |
| Max active | `spec.maxActive` limits concurrent promotionruns created by one trigger. |
| Scope | `spec.scope` can restrict created promotionruns to canary stages or selected clusters. |
| Dry run | `spec.dryRun` records what would be created without creating it. |
| Idempotency | Status records observed tag/digest pairs so repeated polls do not create duplicates. |
| Conditions | Status exposes `Ready`, `Suspended`, `ArtifactVerified`, and `PromotionRunCreated` conditions. |

---

## Proposed Shape

```yaml
apiVersion: kapro.io/v1alpha1
kind: PromotionTrigger
metadata:
  name: checkout-oci
spec:
  suspended: true
  source:
    type: oci
    oci:
      repository: oci://registry.example.com/checkout
      tagPattern: "^v[0-9]+\\.[0-9]+\\.[0-9]+$"
      # Set true only after configuring a trigger verifier.
      requireSignature: false
  promotionrunTemplate:
    promotionPlans:
      - name: production
        promotionPlan: checkout-production
    suspended: true
    scope:
      targets:
        - checkout-canary-eu
  cooldown: 30m
  maxActive: 1
  dryRun: false
```

The API and first OCI controller are available as a preview. The controller
creates digest-pinned PromotionRuns only after the configured safeguards pass.

---

## Non-Goals

- No direct hub-to-spoke apply path.
- No replacement for CI build and signing.
- No automatic production promotion by default.
- No mutable update of an existing `PromotionRun`.
- No source-specific controller sprawl before the OCI trigger proves useful.

---

## Consequences

This keeps Kapro Kubernetes-native and extensible without making autonomous
delivery the default behavior. Platform teams can start with detection-only
automation, review the created promotionrun, then progressively relax the safeguards
when they trust the promotionplan and signature policy.
