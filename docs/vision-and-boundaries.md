# Kapro Vision and Boundaries

Kapro is a Kubernetes-native fleet promotion control plane.

It coordinates when an immutable artifact version may move across a fleet of
Kubernetes clusters. Kapro does not build artifacts, render manifests, replace
GitOps controllers, or manage in-cluster traffic splitting. It owns the
cross-cluster promotion decision and records an auditable PromotionRun history
in Kubernetes status.

## Core Outcome

Kapro should let a platform team answer one operational question:

```text
Which clusters are allowed to receive this artifact version now, and why?
```

The answer is computed from:

- PromotionRun and PromotionPlan topology;
- target cluster inventory and health;
- stage concurrency and planning rules;
- gate evidence and approval state;
- backend convergence reported through actuators.

## Product Boundary

| Area | Kapro owns | Kapro delegates |
|---|---|---|
| Artifact promotion | PromotionRun, PromotionPlan, Stage, PromotionTarget state | CI build and image creation |
| Fleet ordering | Cross-cluster waves, target planning, concurrency | In-cluster traffic splitting |
| Delivery execution | Version patch intent through actuators | Flux, Argo CD, Kubernetes, or backend controllers applying rollout strategy |
| Safety gates | Evidence-based gate lifecycle and status | Domain-specific checks through KGI plugins, Jobs, webhooks, CEL, or metrics |
| Audit | Kubernetes status, Events, CloudEvents, notification policies | External long-term storage and reporting systems |
| Automation | Safe PromotionTrigger policy and guarded PromotionRun creation | Unbounded auto-deploy from every artifact push |
| Extensibility | KAI, KGI, KPI contracts and conformance | Arbitrary internal controller replacement |

## Relationship to Existing Projects

| Project | What it does | Kapro relationship |
|---|---|---|
| Flux | Reconciles desired state into clusters. | Kapro can use Flux as an actuator backend. |
| Argo CD | Reconciles Applications/ApplicationSets. | Kapro can use Argo CD as an actuator backend. |
| Argo Rollouts / Flagger | Manage in-cluster canary, blue-green, and traffic shifting. | Kapro gates on their results; it does not replace traffic management. |
| Kargo | Promotes artifacts through environment stages. | Kapro focuses on fleet-wide cluster waves and can consume artifacts promoted upstream. |
| Tekton / CI systems | Build, test, and package artifacts. | Kapro starts after an artifact is available. |
| Keptn / policy systems | Evaluate domain-specific health and quality signals. | Kapro can call them through gates or plugins. |

See `docs/cncf-integration-masterplan.md` for the integration boundary across
Flux, Argo CD, OCM ManifestWork, Sveltos, Helm, Kargo, Argo Rollouts, Flagger,
Gateway API, and service mesh controllers.

## Gate Positioning

Gates are rollout controls, not the main product. Their purpose is to make
cross-cluster promotion safe and explainable.

Default gates should remain simple:

- soak duration;
- Prometheus threshold;
- SLO burn rate;
- manual approval;
- health and convergence checks;
- signature verification.

Advanced analysis modes are optional. They exist for high-scale teams with
enough traffic and telemetry maturity to use canary/control comparison,
change-point detection, or score-based analysis safely. Kapro must always
record the evidence behind a gate decision and return `Inconclusive` when data
is insufficient.

## Agent Boundary

Future agent integrations should assist operators, not silently control
production.

Agents may:

- summarize PromotionRun and gate evidence;
- recommend next action;
- draft rollback or approval context;
- explain why a target is blocked;
- invoke approved extension points under an `AgentPolicy`.

Agents must not:

- bypass gates or approvals;
- mutate delivery backends directly;
- create unscoped fleet-wide PromotionRuns;
- access secrets outside explicit policy;
- become required for deterministic rollout execution.

Kapro core remains deterministic without agents. Agents consume evidence and
policy; they do not replace the PromotionRun state machine.
