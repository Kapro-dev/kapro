# Kapro Concepts

Kapro separates fleet setup, rollout intent, and runtime execution. Users author
durable intent; controllers create execution records and per-target state.

## Object Model

| Object | Authored by | Purpose |
|---|---|---|
| `Substrate` | Platform team | Registers a delivery adapter and its default settings. |
| `DeliveryUnit` | App or platform team | Declares the app/workload source mappings, trigger intent, and default fleet/plan. |
| `Fleet` | Platform team | Defines the target set: clusters and delivery defaults. |
| `Source` | Kapro controller | Derived source mapping object created from `DeliveryUnit.spec.source`. |
| `Trigger` | Kapro controller | Derived automation object created from `DeliveryUnit.spec.triggers[]`. |
| `Promotion` | App team or automation | Explicitly requests that a version move through a Fleet. |
| `PromotionRun` | Controller | Records one execution attempt stamped from a Promotion. |
| `Target` | Controller | Tracks one cluster/stage execution inside a run. |
| `Cluster` | Platform team or bootstrap controller | Represents one workload cluster and its delivery settings. |
| `Approval` | Human or approval webhook | Carries approve/reject state for a gated target. |

## Authored YAML Shape

The first user-authored path is four objects:

```text
Substrate    names the delivery adapter, for example flux
DeliveryUnit names the app/workload and source mappings
Fleet        names the target clusters and delivery defaults
Promotion    points at the DeliveryUnit and Fleet, then asks for one version to move
```

In the quickstart `Fleet`, clusters carry intent labels such as
`kapro.io/stage: canary` or `kapro.io/stage: production`. Plan stages select
those labels, and downstream stages use `dependsOn` to make the rollout order
explicit.

Inline Fleet plans use a compact selector map:

```yaml
selector:
  kapro.io/stage: canary
```

Standalone `Plan` objects use Kubernetes `LabelSelector` syntax:

```yaml
selector:
  matchLabels:
    kapro.io/stage: canary
```

## Promotion Lifecycle

1. A user or CI system creates or updates a `Promotion`.
2. The Promotion controller stamps a new `PromotionRun` when the effective
   rollout input changes.
3. The PromotionRun controller resolves the selected plan and clusters.
4. The controller creates `Target` children for each selected
   cluster/stage.
5. Each target moves through gates, approval, apply, health, and convergence.
6. Status rolls up from targets to the run and from the active run to the
   Promotion.

`PromotionRun` objects are execution history. A new version creates a new run;
Kapro does not rewrite an old run into a different desired version.

## Stage Plans

A stage selects clusters by label and may declare:

- dependencies on earlier stages;
- maximum parallelism;
- gates such as soak, CEL checks, metrics, or approvals;
- substrate-specific delivery settings inherited from the fleet or cluster.

The plan is reusable. A Promotion supplies the version and optional scope; the
plan supplies rollout shape.

## Substrate Adaptability

Kapro does not require a single deployment substrate. Each cluster can point to a
delivery mode that fits its network and ownership boundary:

- hub-to-cluster push for centrally reachable clusters;
- spoke-side pull for outbound-only clusters;
- Flux or Argo CD integration for existing GitOps estates;
- external plugins for platform-specific apply, gate, or planning logic.

See [Substrates](substrates.md) for the supported modes.

## Generated Objects

For the quickstart path, users normally write:

- `Substrate`
- `DeliveryUnit`
- `Plan`
- `Fleet`
- `Promotion`
- `Approval` when a manual gate blocks

`Source` and `Trigger` are visible Kubernetes objects, but the normal
public-preview path is to let the DeliveryUnit controller derive them from
`DeliveryUnit.spec.source` and `DeliveryUnit.spec.triggers[]`.

Kapro or its controllers generate and update:

- `Source` from `DeliveryUnit.spec.source`
- `Trigger` from `DeliveryUnit.spec.triggers[]`
- `PromotionRun`
- `Target`

Direct `PromotionRun` manifests remain an advanced compatibility path, not the
recommended first-use API.

## Hub Config Source Of Truth

The recommended operating model is a dedicated hub-config Git repository. CI
validates that repository and applies the rendered YAML to the Kapro hub with
`kubectl apply`. Spoke clusters do not watch that repository directly; they
either keep using their existing Argo or Flux source of truth, or they consume
Kapro-generated greenfield delivery objects and report status through
`Cluster`.

Typical layout:

```text
hub-config/
  clusters/
  substrates/
  deliveryunits/
  fleets/
  plans/
  promotions/
  .github/workflows/
```

Apply objects in dependency order: substrates and any standalone clusters,
delivery units or plans first; fleets next; promotions last. Direct `promotionruns/`
can exist as an advanced compatibility path, but first-use repositories should
prefer `promotions/`.

See [examples/quickstart](https://github.com/Kapro-dev/kapro/tree/main/examples/quickstart)
for the preferred Fleet-root Promotion path.

## Gate Semantics

Kapro gates use a simple decision model:

```text
Evidence -> Analysis -> Phase
```

The phase is the rollout-control field. Evidence explains why the phase was
returned and is stored on `Target.status.gates[].evidence[]`.

Gate evidence can include provider, query, window, interval, observed value,
threshold, baseline value, sample count, confidence, reason, and projection.
It must not contain tokens, headers, secret values, or raw webhook payloads.

Metric gates support these analysis modes:

| Mode | Behavior |
|---|---|
| `threshold` | Compare the current Prometheus value to a threshold. |
| `sloBurnRate` | Treat the current value as error-budget burn rate. |
| `baseline` | Compare current value to a baseline query. |
| `sequential` | Query a range and require enough samples before passing or failing. |
| `changePoint` | Compare the first and second halves of a range for regression. |
| `score` | Convert one metric into a score and require `scoreThreshold`. |

Missing data, unhealthy baselines, low confidence, unreachable metrics systems,
and too few samples return `Inconclusive` so unclear evidence does not advance a
fleet by accident.
