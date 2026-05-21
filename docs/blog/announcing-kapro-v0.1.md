# Announcing Kapro v0.1

Kapro is a promotion control plane for Kubernetes fleets.

It answers one operational question:

```text
Which clusters are allowed to receive this artifact version now, and why?
```

Most platform teams already have good tools for local delivery. Flux and Argo
CD reconcile desired state. Argo Rollouts and Flagger handle single-cluster
progressive delivery. Sveltos is strong at cluster add-on placement. Those
systems are not the problem Kapro is trying to replace.

Kapro sits one layer above them. It passes a version across clusters in waves,
with auditable gates between waves, while the local backend keeps owning the
actual rollout.

## Why another layer?

CI can stamp a tag into many repositories, but CI is a poor long-lived source of
rollout truth. It forgets cluster state as soon as the job exits. It is hard to
ask a completed pipeline why one region is paused, why another moved forward,
or which human approved a gate.

Kapro moves that intent into Kubernetes:

- `Promotion` is the user-authored intent: promote this version through this
  fleet.
- `PromotionRun` is the controller-authored execution attempt and audit record.
- `Target` is the per-cluster, per-stage runtime record.
- `Plan` defines stage order and gates.
- `Fleet` defines the clusters and delivery defaults.

That split is deliberate. It is the difference between desired rollout intent
and immutable execution history.

## What Kapro does not do

Kapro does not build artifacts, render manifests, replace GitOps controllers, or
shift traffic inside a single cluster. Those jobs stay with tools that already
do them well.

Kapro coordinates promotion decisions across the fleet:

- Which version is active?
- Which clusters are in the next wave?
- Which gates passed?
- Which gates blocked?
- Which execution attempt is the current one?
- Which target failed or converged?

## Working with existing stacks

Kapro is backend-neutral. A fleet can use Flux, Argo CD, OCI pull delivery, or a
custom plugin. That lets a platform team adopt Kapro without forcing every
application team to change its local deployment controller first.

For brownfield environments, Kapro should be introduced as the promotion layer
around what already works. Keep existing GitOps reconciliation where it is.
Start by modeling the fleet, plan, and promotion intent. Then connect delivery
drivers where they provide value.

## What v0.1 includes

The public preview focuses on the core promotion model:

- `kapro.io/v1alpha2` Kubernetes APIs.
- Durable `Promotion` intent and immutable `PromotionRun` attempts.
- Per-target runtime state through `Target`.
- Fleet and plan modeling.
- Backend-neutral delivery shape for Flux, Argo CD, OCI, and plugins.
- Preview CloudEvents emission for promotion lifecycle events.
- Helm install, quickstarts, drift canaries, and Kind smoke coverage.

This is pre-stable software. Core CRDs are still Alpha and several extension
surfaces are Preview, so v0.1 is for serious evaluation rather than a GA
compatibility promise. The purpose is to validate the object model and adoption
path with real platform feedback before freezing higher-level UX.

## The design bet

Kapro's design bet is that promotion deserves its own API object.

Not a CI script. Not a hidden controller queue. Not a field buried in a backend
object. A promotion is operational intent that should be inspectable, retryable,
auditable, and observable.

That is the foundation Kapro v0.1 puts in front of users.
