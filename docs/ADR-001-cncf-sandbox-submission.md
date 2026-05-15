# ADR-001: CNCF Sandbox Positioning

**Status:** Proposed
**Date:** 2026-04-19
**Updated:** 2026-05-15
**Deciders:** Kapro maintainers

---

## Context

Kapro is a Kubernetes-native fleet promotion control plane. It coordinates when
an immutable artifact version may move across a fleet of Kubernetes clusters.

The CNCF Sandbox question is not whether Kapro is a complete enterprise
platform. Sandbox projects are early-stage. The question is whether Kapro has a
clear cloud-native problem, a bounded scope, credible architecture, and a path to
open governance.

## Positioning Decision

Kapro should be positioned as:

> A Kubernetes-native fleet promotion control plane that coordinates safe
> artifact rollout across many clusters using explicit gates, backend-neutral
> actuators, and auditable status.

Kapro should not be positioned as:

- a CI/CD workflow engine;
- a replacement for Flux, Argo CD, Kargo, Argo Rollouts, Flagger, Tekton, or
  Keptn;
- an AI deployment platform;
- a traffic-shaping system;
- a universal deployment system.

## Problem Kapro Solves

Platform teams running many Kubernetes clusters need a consistent way to answer:

```text
Which clusters are allowed to receive this artifact version now, and why?
```

Existing tools cover adjacent layers:

- CI systems build and test artifacts;
- Flux and Argo CD reconcile desired state into clusters;
- Argo Rollouts and Flagger manage in-cluster traffic shifting;
- Kargo manages artifact promotion through environment stages;
- policy and observability systems evaluate domain-specific signals.

Kapro fills the fleet promotion layer: release topology, target planning,
cross-cluster wave ordering, gate lifecycle, approval state, backend convergence,
and auditable release outcome.

## Architecture Summary

Kapro's core architecture is Kubernetes-native:

- CRDs define Releases, Pipelines, MemberClusters, ReleaseTargets, approvals,
  plugins, notifications, and guarded triggers.
- Controllers reconcile desired release state into per-target rollout state.
- Actuators apply one version to one target through backend-specific adapters.
- Gates decide whether one target may advance and record structured evidence.
- Planner logic filters and orders targets before rollout.
- Lifecycle events and notifications expose release transitions to external
  systems.
- KAI, KGI, and KPI define narrow extension contracts with conformance
  harnesses.

The core state machine remains deterministic. Plugins, gates, triggers,
notifications, and future agents extend the system without owning release state.

## Relationship to Existing CNCF Projects

| Project area | Boundary |
|---|---|
| Flux / Argo CD | Reconcile desired state. Kapro can patch their version fields and wait for convergence. |
| Argo Rollouts / Flagger | Manage traffic inside one cluster. Kapro coordinates waves across clusters and can gate on their result. |
| Kargo | Promotes artifacts through environment stages. Kapro handles fleet target promotion and convergence. |
| Tekton / CI | Builds and validates artifacts. Kapro starts after an immutable artifact exists. |
| Keptn / policy systems | Provide domain-specific evaluation. Kapro consumes them through gates or plugins. |
| Agent frameworks | Explain, recommend, and assist under policy. Kapro core does not require agents. |

This boundary keeps Kapro complementary instead of competitive.

## Gates and Advanced Analysis

Gates are a supporting capability, not the main product identity.

The CNCF-facing message should emphasize simple, operational controls:

- soak timers;
- Prometheus thresholds;
- SLO burn-rate checks;
- artifact verification;
- manual approvals;
- CEL, Job, webhook, and plugin gates;
- auditable evidence in status.

Advanced statistical modes are optional for teams with mature telemetry. They
must remain conservative and explainable: insufficient data returns
`Inconclusive`, and every decision records evidence.

## Agent Boundary

Future agents may help operators understand and act on release evidence.

Agents may:

- summarize gate evidence and release state;
- recommend rollback or approval context;
- explain blocked targets;
- invoke approved actions under `AgentPolicy`.

Agents must not:

- replace the release controller;
- bypass gates or approvals;
- mutate delivery backends directly;
- make production changes without Kubernetes API policy and audit.

This keeps Kapro safe for environments that do not want AI agents while leaving a
clear extension path for teams that do.

## Readiness Assessment

| Dimension | Current posture |
|---|---|
| CNCF fit | Strong: Kubernetes APIs, controller-runtime, OCI, GitOps interoperability, CloudEvents, conformance contracts. |
| Scope clarity | Strong after `docs/vision-and-boundaries.md` and `docs/cncf-positioning.md`. |
| Architecture | Strong: deterministic state machine, narrow extension contracts, auditable status. |
| API maturity | Alpha/preview; acceptable for Sandbox but must be documented clearly. |
| Community | Needs work: more maintainers, public adoption evidence, regular project process. |
| Install/demo path | Must be easy and repeatable before submission. |
| Security | Needs continued hardening: threat model, RBAC, plugin trust, release-trigger safeguards. |

## Decision

Proceed toward CNCF Sandbox only after the project has:

- a repeatable install and local demo;
- current architecture and boundary docs;
- governance and maintainer docs;
- security model and threat model;
- plugin conformance documentation;
- at least one credible usage story or reproducible reference scenario.

The submission should lead with fleet promotion, not statistics or agents.

## Consequences

Kapro must keep the public scope narrow:

- Own fleet promotion.
- Delegate reconciliation, traffic shifting, build, and domain-specific policy.
- Keep advanced features opt-in and explainable.
- Treat agents as policy-bound assistants, not core runtime dependencies.

This positioning gives Kapro a stronger CNCF story and reduces overlap risk with
existing cloud-native projects.
