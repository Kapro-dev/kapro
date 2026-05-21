# ADR-0012: Competitive Positioning

## Status
Accepted

## Context
Kapro sits beside mature Kubernetes projects that already own important parts
of delivery: Flux and Argo CD reconcile desired state, Sveltos manages cluster
add-ons, Argo Rollouts and Flagger manage single-cluster progressive delivery,
and notification controllers route events. Without a clear architectural
position, users may evaluate Kapro as another reconciler, add-on manager, or
canary controller instead of the promotion layer above those systems.

Feature matrices are also fragile. They age quickly and make Kapro look like it
is trying to replace tools that it should usually integrate with.

## Decision
Kapro is positioned as the promotion layer for Kubernetes fleets:

> Kapro is the **promotion layer** for Kubernetes. It doesn't reconcile clusters;
> Flux and Argo CD already do that. It doesn't pick add-ons; Sveltos does
> that. It doesn't analyze canaries on a single cluster - Argo Rollouts and
> Flagger do that. Kapro **passes a version across clusters in waves, with
> auditable gates between waves**, against any of those backends as a delivery
> driver.

Public docs should compare Kapro by architectural shape, not by one-off feature
parity. The canonical comparison dimensions are scope, primary CRD, gates,
backend coupling, multi-cluster model, audit model, plugin model, and event API.

### Sveltos

| Dimension | Sveltos | Kapro |
|---|---|---|
| Scope | Cluster add-ons and configuration placement. | Multi-cluster promotion of an artifact version. |
| Primary CRD | `ClusterProfile` and `ClusterPromotion` | `Promotion` |
| Gates | Add-on/config rollout triggers, health checks, and policy-oriented controls. | Stage gates between fleet waves. |
| Backend coupling | Sveltos-native placement and reconciliation. | Flux, Argo CD, OCI, and plugin delivery drivers. |
| Multi-cluster model | Select clusters and promote cluster configuration or add-ons through staged selectors. | Move an application artifact version through selected clusters in ordered waves. |
| Audit model | Profile status and Kubernetes events. | Immutable `PromotionRun` attempts plus child `Target` state. |
| Plugin model | Sveltos extension points. | Actuator, gate, and planner plugin surfaces. |
| Event API | Project-specific Kubernetes status/events. | Documented Preview CloudEvents schema. |

### Argo Rollouts

| Dimension | Argo Rollouts | Kapro |
|---|---|---|
| Scope | Single-cluster progressive delivery. | Multi-cluster promotion. |
| Primary CRD | `Rollout` | `Promotion` |
| Gates | Analysis, pauses, and traffic-shift controls inside one cluster. | Approval, health, verification, soak, and external predicates between waves. |
| Backend coupling | Argo Rollouts controller and traffic providers. | Backend-neutral delivery drivers. |
| Multi-cluster model | Outside the core object model. | First-class fleet, plan, run, and target objects. |
| Audit model | Rollout history and analysis results. | Immutable `PromotionRun` attempts plus child `Target` state. |
| Plugin model | Analysis templates and metric providers. | Actuator, gate, and planner plugin surfaces. |
| Event API | Kubernetes events and controller status. | Documented Preview CloudEvents schema. |

### Flagger

| Dimension | Flagger | Kapro |
|---|---|---|
| Scope | Single-cluster canary and progressive traffic shift. | Multi-cluster promotion. |
| Primary CRD | `Canary` | `Promotion` |
| Gates | Metrics, webhooks, and traffic analysis for one workload. | Wave gates that decide when a version may move to the next cluster set. |
| Backend coupling | Mesh, ingress, and metric provider integrations. | Flux, Argo CD, OCI, and plugin delivery drivers. |
| Multi-cluster model | Operated per cluster. | Hub-owned promotion intent with per-target runtime records. |
| Audit model | Canary status and events. | Immutable `PromotionRun` attempts plus child `Target` state. |
| Plugin model | Provider integrations and webhooks. | Actuator, gate, and planner plugin surfaces. |
| Event API | Kubernetes events and notifications. | Documented Preview CloudEvents schema. |

### GitOps Toolkit Notification Controller

| Dimension | GitOps Toolkit Notification Controller | Kapro |
|---|---|---|
| Scope | Event routing for GitOps objects. | Multi-cluster promotion. |
| Primary CRD | `Alert` / `Receiver` | `Promotion` |
| Gates | None; it routes events rather than deciding rollout progress. | Stage gates between fleet waves. |
| Backend coupling | Flux notification APIs. | Backend-neutral delivery drivers, including Flux. |
| Multi-cluster model | Follows the underlying reconciled objects. | First-class fleet, plan, run, and target objects. |
| Audit model | Event delivery records and receiver state. | Immutable `PromotionRun` attempts plus child `Target` state. |
| Plugin model | Notification providers and receivers. | Actuator, gate, and planner plugin surfaces. Notifications use inline gate policy and lifecycle events, not a plugin contract. |
| Event API | Flux notification API. | Documented Preview CloudEvents schema. |

## Rejected alternatives
- Feature-parity matrix. Feature rows age quickly and force Kapro into a
  reactive story where every competitor feature looks like a missing Kapro
  feature.
- Marketing-only positioning. It may be easier to read, but it does not help
  platform engineers decide where Kapro belongs in an existing delivery stack.
- Replacement narrative. Kapro should not ask users to discard Flux, Argo CD,
  Argo Rollouts, Flagger, Sveltos, or existing notification systems when those
  tools already do their jobs well.

## Consequences
- README, docs, and announcement copy should describe Kapro as a promotion
  layer that composes with existing delivery systems.
- Comparison docs should focus on object model, audit model, and ownership
  boundaries instead of claiming permanent feature superiority.
- New integrations should be evaluated by whether they make Kapro a better
  promotion layer, not whether they turn Kapro into a replacement reconciler.

## References
- [Concepts](../concepts.md)
- [Backends](../backends.md)
- [CloudEvents publisher posture](0003-cloudevents-publisher-posture.md)
- [Sveltos ClusterPromotion](https://projectsveltos.io/v1.2.0/deployment_order/progressive_rollout/)
