# CNCF Positioning

This document defines how Kapro should be presented to cloud-native users and
CNCF reviewers.

## One-Sentence Description

Kapro is a Kubernetes-native fleet promotion control plane that coordinates safe
artifact rollout across many clusters using explicit gates, backend-neutral
actuators, and auditable status.

## Primary Value Proposition

Kapro fills the fleet promotion layer between artifact creation and per-cluster
delivery.

It helps platform teams:

- promote one immutable artifact version across many clusters;
- stage rollout through canary, regional, and production waves;
- halt progression when gates are inconclusive or failing;
- delegate actual workload reconciliation to Flux, Argo CD, Kubernetes, or
  backend-specific controllers;
- keep promotionrun history inspectable through Kubernetes APIs.

## What To Lead With

Lead with:

- Kubernetes-native APIs and controllers;
- multi-cluster fleet promotion;
- safe rollout ordering and concurrency;
- auditable gates and approvals;
- OCI and GitOps interoperability;
- KAI, KGI, and KPI extension contracts with conformance;
- CloudEvents-compatible lifecycle events;
- security, RBAC, and tenancy boundaries.

Do not lead with:

- statistical gate theory;
- AI agents;
- replacing existing CNCF delivery tools;
- becoming a general CI/CD workflow engine;
- every possible future actuator or plugin.

Advanced gate analysis and agents are supporting capabilities. They strengthen
explainability and integrations, but they are not the product identity.

## Overlap Boundaries

| Adjacent project area | Boundary |
|---|---|
| Flux / Argo CD | They reconcile desired state. Kapro decides fleet rollout order and patches the configured version field through actuators. |
| Argo Rollouts / Flagger | They manage in-cluster traffic shifting. Kapro coordinates cross-cluster waves and can gate on their results. |
| Kargo | Kargo manages artifact promotion through environment stages. Kapro manages fleet-wide cluster target promotion and convergence. |
| Tekton / CI | CI builds, tests, and packages artifacts. Kapro starts after an immutable artifact version exists. |
| Keptn / policy systems | They can provide domain-specific evaluations. Kapro consumes their decision through gates or plugins. |
| Agent frameworks | Agents can explain and recommend. Kapro remains deterministic and policy-bound without agents. |

## Review-Ready Claims

These claims are safe:

- Kapro is Kubernetes-native and stores rollout state in Kubernetes resources.
- Kapro is backend-neutral through actuator contracts.
- Kapro is extensible through narrow, conformance-tested plugin contracts.
- Kapro is conservative: unclear gate data returns `Inconclusive`.
- Kapro complements existing GitOps and progressive-delivery projects.
- Current alpha promotionruns are production-capable only for controlled adopters
  that run the documented verification and accept `v1alpha1` API movement.

Avoid these claims:

- Kapro is the universal deployment platform.
- Kapro replaces Flux, Argo CD, Rollouts, Flagger, Kargo, Tekton, or Keptn.
- Kapro guarantees zero bad deployments.
- Kapro requires AI agents for production safety.
- Kapro's statistical gates are the main reason to adopt it.
- Kapro is GA production-ready before stable APIs, published upgrade history,
  real-world soak, and independent security review exist.

## Sandbox Readiness Focus

Before submission, prioritize:

- clear install path and local demo;
- current architecture docs and API stability policy;
- governance and maintainer model;
- security and threat model;
- conformance instructions for plugins;
- evidence of real use or repeatable demos;
- documented relationship to existing CNCF projects.

Feature depth helps, but CNCF readiness depends more on clear scope,
governance, security, documentation, and adoption path.
