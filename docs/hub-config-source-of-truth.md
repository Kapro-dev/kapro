# Hub Config Source of Truth

## Decision

For the current public pre-stable line, hub config lives in a dedicated **git repository**. CI validates that repository and applies the rendered YAML to the Kapro hub cluster with `kubectl apply`.

Spoke clusters do not watch the hub config repository directly. They either
consume their existing Argo/Flux source of truth or use Kapro-generated
greenfield delivery objects, then report status through `FleetCluster`.

## Why

Kapro separates two sources of truth:

- **Hub config truth:** the git repository that defines fleet inventory,
  backend profiles, promotion sources, rollout plans, and Promotion intent.
  Advanced compatibility repositories may still store direct PromotionRun
  manifests during the pre-stable line.
- **Runtime artifact truth:** the OCI registry, Git repository, chart, image, or
  backend-native object that stores the immutable application version.

The hub cluster needs Kubernetes objects such as `FleetCluster`,
`BackendProfile`, `PromotionSource`, `PromotionPlan`, and `Promotion` to drive
the fleet. Advanced compatibility repos can apply `PromotionRun` directly, but
first-use docs should prefer `Promotion`. These objects must be reviewable,
reproducible, and auditable. A plain git repository plus CI-driven
`kubectl apply` is the documented operating model.

## Architecture

```
Developer
   |
   v
Pull request / merge to main
   |
   v
CI: validate -> diff -> kubectl apply
   |
   v
Hub cluster etcd: FleetCluster, BackendProfile, PromotionSource, PromotionPlan, Promotion
   |
   v
Kapro operator
   |
   v
Delivery backends: Argo/Flux/Git/native apply + FleetCluster status
```

## What lives in the hub config repo

| Directory | Contents |
|---|---|
| `clusters/` | FleetCluster definitions (one per spoke) |
| `backends/` | BackendProfile definitions for Flux, Argo, or external drivers |
| `sources/` | PromotionSource definitions (unit registry, waves, overrides) |
| `promotionplans/` | PromotionPlan definitions (stage DAG, selectors, gates) |
| `promotions/` | Promotion intent objects (preferred public path) |
| `promotionruns/` | Advanced compatibility PromotionRun objects |
| `.github/workflows/` | CI that validates, diffs, and applies the repo to the hub |

## What does NOT live in the hub config repo

- Packaged source contents (those are in the container registry, chart
  repository, or backend-owned Git repository)
- Spoke cluster workloads (those come from OCI sources)
- Secrets (those come from External Secrets Operator or sealed secrets)
- Infrastructure (that comes from Terraform)
- Generated controller status (that belongs in the hub cluster)

## Repository layout

Use one YAML file per object unless there is a strong local reason to group objects. Keep filenames stable so reviews show object-level changes clearly.

```
hub-config/
  clusters/
    canary-eu.yaml
    prod-eu.yaml
    prod-us.yaml
  backends/
    flux.yaml
  sources/
    checkout.yaml
  promotionplans/
    checkout-progressive.yaml
  promotions/
    checkout-v1.2.3.yaml
  promotionruns/             # optional advanced compatibility path
  .github/
    workflows/
      apply-kapro-hub-config.yaml
```

See [examples/hub-config/](../examples/hub-config/) for the advanced direct
PromotionRun compatibility sample, or [examples/quickstart/](../examples/quickstart/)
for the preferred Kapro-root Promotion path.

## Apply ordering

Apply objects in dependency order:

1. `clusters/` - registers `FleetCluster` inventory and labels used by selectors.
2. `backends/` - registers selectable backend profiles and discovery settings.
3. `sources/` - defines reusable PromotionUnit metadata and write mappings.
4. `promotionplans/` - defines stage DAGs, cluster selectors, and gate policy.
5. `promotions/` - creates user-facing promotion intent that references plans and target versions.
6. `promotionruns/` - optional advanced compatibility path for direct run manifests.

This order keeps Promotion reconciliation last, after the objects it references
and the clusters it may select are present. Direct PromotionRun compatibility
manifests should also be applied last.

## CI workflow

CI validates every pull request and applies after merge to `main`.

Pull request checks:

```bash
kubectl apply --dry-run=server -f clusters/
kubectl apply --dry-run=server -f backends/
kubectl apply --dry-run=server -f sources/
kubectl apply --dry-run=server -f promotionplans/
kubectl apply --dry-run=server -f promotions/

kubectl diff -f clusters/ || true
kubectl diff -f backends/ || true
kubectl diff -f sources/ || true
kubectl diff -f promotionplans/ || true
kubectl diff -f promotions/ || true
```

Merge-to-main apply:

```bash
kubectl apply -f clusters/
kubectl apply -f backends/
kubectl apply -f sources/
kubectl apply -f promotionplans/
kubectl apply -f promotions/
```

Post-apply checks:

```bash
kubectl get fleetclusters.kapro.io
kubectl get backendprofiles.kapro.io,promotionsources.kapro.io
kubectl get promotions.kapro.io,promotionruns.kapro.io,promotiontargets.kapro.io
kubectl describe promotions.kapro.io checkout-v1-2-3
```

The CI identity needs permission to `get`, `list`, `watch`, `create`, `patch`, `update`, and `delete` the Kapro configuration objects it owns. Production repositories should protect `main` and require the validation job before merge.

## Local checks

Before opening a pull request from the hub config repo:

```bash
kubectl apply --dry-run=server -f clusters/ -f backends/ -f sources/ -f promotionplans/ -f promotions/
kubectl diff -f clusters/ -f backends/ -f sources/ -f promotionplans/ -f promotions/ || true
```

`--dry-run=server` requires access to a hub cluster with the Kapro CRDs installed. It catches schema and admission errors that static YAML parsing cannot catch.

## Optional Future Mode: Flux on Hub

Teams that standardize on GitOps controllers can run Flux on the hub cluster and point a Flux `Kustomization` at the hub config repository. In that mode, Flux replaces the CI `kubectl apply` step; the source-of-truth model stays the same.

```
git push -> Flux on hub -> Kapro CRDs in hub etcd -> Kapro operator -> spokes
```

Flux-on-hub remains optional. The v1 documented default is git repository plus CI validation and `kubectl apply`.
