# GA Readiness

Kapro is not GA while it uses `kapro.io/v1alpha1` APIs and lacks release,
upgrade, soak, and audit evidence. This document is the GA evidence matrix.

## Current State

| Area | Status | Evidence |
|---|---|---|
| Install path | Alpha production-capable | Helm/Kustomize render checks, Kind demo, Promotion smoke, and install docs. |
| Argo brownfield onboarding | Alpha production-capable | Live Argo E2E covers plain Applications, multi-source Applications, app-of-apps child Applications, and ApplicationSet Git generator inputs. |
| Flux brownfield onboarding | Alpha production-capable | Flux Git-native E2E and live Flux controller E2E cover source and workload version fields. |
| Promotion policies | Alpha production-capable | `PromotionPolicy` CEL rules and freeze windows are enforced before `PromotionRun` creation; artifact verification policy remains on `PromotionTrigger`. |
| Plugin runtime | Preview | `PluginRegistration` readiness probes hot-load actuator, gate, and planner adapters when `KAPRO_ENABLE_PLUGIN_GATEWAY=true`. |
| Planner runtime dispatch | Preview | KPI plugins can filter, defer, and score targets through the PromotionRun planner while Kapro owns binding and state. |
| API version | Not GA | Public Kubernetes APIs are still `kapro.io/v1alpha1`. |
| Upgrade history | Not GA | No tagged release-to-release upgrade history exists yet. |
| Production soak | Not GA | Local and synthetic evidence exists, but broad customer/operator soak is not yet published. |
| Security audit | Not GA | Threat model and security docs exist, but no independent audit has been published. |

## GA Exit Gates

Kapro can be proposed for GA only after all of these gates are satisfied:

- A stable Kubernetes API version is published with conversion and migration
  guidance from the previous served version.
- At least one real tagged upgrade path has been validated and documented in
  release notes.
- Argo and Flux onboarding paths have passing live E2E evidence for the tagged
  release.
- Large-fleet limits are documented with benchmark evidence for repository
  size, backend object count, target count, and PromotionRun fanout.
- Plugin compatibility, hot reload, and KPI planner dispatch have conformance
  and operational evidence.
- Security boundaries, hub gateway exposure, plugin trust, RBAC, and secret
  handling have had an independent review or audit.
- At least two non-maintainer operators have completed installation,
  brownfield or greenfield onboarding, upgrade, and rollback validation.

## What Cannot Be Claimed Yet

Do not claim:

- GA production-ready.
- Stable API compatibility.
- Independently audited security.
- Broad real-world soak.
- Published upgrade compatibility across historical releases.

The correct public claim remains:

> Kapro is alpha production-capable for controlled adopters that can run the
> documented verification and accept `v1alpha1` API movement.
