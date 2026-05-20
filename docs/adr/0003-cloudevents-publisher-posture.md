# ADR-0003: CloudEvents publisher posture — emit, don't route

## Status
Accepted

## Context

Kapro emits events on every fleet-promotion transition. The
question: which slice of the notification problem does Kapro own?

The Kubernetes delivery ecosystem already has mature notification routers:

- **Argo CD Notifications** (graduated): Annotation-driven subscriptions
  on `Application` state → Slack/Teams/PagerDuty/OpsGenie/email/webhook,
  ~15 backends, templating, conditional routing.
- **Flux Notification Controller** (graduated): `Provider` + `Alert`
  CRDs subscribe to Flux objects → same backends.
- **kube-event-exporter** (sandbox): Routes any Kubernetes Event to
  backends generically.
- **Argo Events** (incubating): EventSource → Sensor → Trigger pattern
  over any K8s CR or external signal.
- **CloudEvents** (graduated): Standard envelope.

A naive path: ship `webhook`, `slack`, `teams`, `pagerduty`, `opsgenie`,
`email`, ... handler kinds in Kapro itself. That's tempting because it
gives users "install Kapro, get Slack notifications out of the box."
It's also the road to becoming Flux Notification Controller, badly.

## Decision

**Kapro is the canonical source of fleet-promotion CloudEvents. Kapro
does not ship a notification routing layer.**

Concretely:

1. Publish CloudEvents v1.0 envelopes for every fleet-promotion
   transition (whole-Promotion, attempt, wave, stage, gate).
2. Define a stable, versioned `EventType` vocabulary in `pkg/events`
   that third-party subscribers depend on.
3. Provide TWO subscription paths:
   - Operator-level sink (`KAPRO_EVENTS_SINK_URL`) for fleet-wide
     fan-out — the canonical path.
   - Per-Promotion `spec.lifecycle.handlers[]` for one-off team-level
     integrations.
4. Ship `webhook` and `event` handler kinds only. Do NOT ship `slack`,
   `teams`, `pagerduty`, `opsgenie`, `email`, etc.
5. Document integration recipes in `docs/events.md` that show users how to
   subscribe via Argo Events, Flux Notification Controller,
   kube-event-exporter, etc. — those projects already speak every backend
   natively.

This is the same posture Tekton holds for CI: own the primitive
(`TaskRun`/`PipelineRun`), define the contract, let others build on top.
Tekton does not ship Slack triggers.

## Rejected alternatives

### A. Ship Slack/Teams/PagerDuty/OpsGenie/email backends in-tree
Every backend we ship is one we maintain forever. Slack changes their
attachment schema → we break. PagerDuty revs Events v3 → we lag.
Direct duplication of Flux Notification Controller / Argo CD Notifications.

### B. Mutate Flux NC or Argo CD Notifications CRDs from the Kapro controller
Couples our release cadence to theirs. Demands RBAC into
`flux-system`/`argocd` namespaces. Creates ownership conflicts. No
Upstream delivery projects should not mutate each other's notification APIs.

### C. Subscribe to Flux/Argo events and re-emit them in our vocabulary
Wrong layer. Flux/Argo emit per-cluster reconcile events; Kapro emits
fleet-promotion events. Translating across layers loses information
and adds nothing subscribers can't get directly.

### D. Ship `kapro-notification-adapter` as part of the operator
A small binary that subscribes to Kapro CloudEvents and emits
Flux-NC-style payloads. Considered. The right move when 3+ external
users ask for it. Until then, document the recipe (point your
existing Flux Notification Controller `Receiver` at Kapro's sink).

## Consequences

**Easier:**
- Tight scope — Kapro stays narrow on what it owns.
- Public contract (`pkg/events.EventType`) is stable and small enough
  to maintain.
- Existing event routers can subscribe — Argo Events, Flux NC,
  kube-event-exporter, Knative, AWS EventBridge, Google Eventarc,
  Azure Event Grid.

**Harder:**
- Users who want "install Kapro, get Slack" need to also install
  Flux Notification Controller or Argo CD Notifications. Documented
  in `docs/events.md`.
- Discovery: users won't find Kapro by searching "Slack notifications
  for Kubernetes." We win on the fleet-narrative axis instead.

**Locks in:**
- `pkg/events.EventType` constants are public API — stable within
  `v1alpha1`, renaming requires a major version bump.
- The CloudEvents v1.0 envelope shape is the wire contract.
- Per-Promotion handler `on: [Phase]` filter stays phase-scoped only.
  An `onTypes:` filter extension is reserved for a future minor
  release.

## References

- PR #79: Lifecycle hooks (per-Promotion handlers)
- PR #80: CloudEvents vocabulary + operator-level sink
- PR #81/#82: Wave / Stage / Gate CloudEvents
- `docs/events.md` — vocabulary spec and subscriber cookbook
