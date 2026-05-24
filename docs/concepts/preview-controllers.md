# Preview Controllers

Kapro installs every CRD in the chart, but the operator does not run every
controller by default. The public-preview default keeps the first install small:

```yaml
controllers:
  - fleet
  - plan
  - promotion
  - promotionrun
  - cluster
```

`promotionrun` also starts the `target` controller because `Target` is the
runtime child state for each execution attempt. You do not need to list
`target` separately.

## Controller Keys

| Controller key | Default | Purpose |
|---|---:|---|
| `fleet` | Yes | Reconciles `Fleet` objects and generated setup resources. |
| `plan` | Yes | Names the `Plan` template surface. This key is accepted for selection symmetry; `Plan` has no reconciler. |
| `promotion` | Yes | Stamps controller-owned `PromotionRun` attempts from user-authored `Promotion` intent. |
| `promotionrun` | Yes | Orchestrates run execution and creates `Target` children. |
| `target` | Implicit | Executes per-cluster or per-stage runtime state for a run. |
| `cluster` | Yes | Maintains cluster heartbeat and readiness status. |
| `backend` | No | Writes external backend readiness and backend-native discovery status. Built-in `flux`, `argo`, and `oci` Backend specs are usable without this controller. |
| `approval` | No | Reconciles `Approval` objects that unblock approval gates. |
| `gateexpression` | No | Reconciles `GateExpression` preview composition status. |
| `fleetdriftreport` | No | Computes read-only desired-vs-observed drift summaries from `Target` and `Cluster` status. |
| `trigger` | No | Creates or updates `Promotion` from artifact changes. |
| `plugin` | No | Reconciles plugin readiness when the plugin gateway is enabled. |
| `cluster-bootstrap` | No | Provisions CSR bootstrap material for spoke cluster registration. |
| `clustertemplate` | No | Imports clusters from provider-backed templates. |

## Examples

Run only the default core:

```bash
helm upgrade --install kapro "$KAPRO_CHART" \
  --namespace kapro-system \
  --create-namespace
```

Add artifact triggers and human approval gates:

```bash
helm upgrade --install kapro "$KAPRO_CHART" \
  --namespace kapro-system \
  --create-namespace \
  --set controllers='{fleet,plan,promotion,promotionrun,cluster,trigger,approval}'
```

Run every canonical controller:

```bash
helm upgrade --install kapro "$KAPRO_CHART" \
  --namespace kapro-system \
  --create-namespace \
  --set controllers='{*}'
```

## Compatibility Aliases

Older controller keys are accepted as aliases and normalized before startup, so
they do not start duplicate controllers:

| Old key | Canonical key |
|---|---|
| `kapro` | `fleet` |
| `promotion-target` | `target` |
| `fleetcluster-heartbeat` | `cluster` |
| `gate-expression` | `gateexpression` |
| `backend-profile` | `backend` |
| `fleet-drift-report` | `fleetdriftreport` |
| `plugin-registration` | `plugin` |
| `promotion-trigger` | `trigger` |
| `fleetcluster-bootstrap` | `cluster-bootstrap` |
| `fleetcluster-template` | `clustertemplate` |

Prefer canonical keys in new installs and examples.
