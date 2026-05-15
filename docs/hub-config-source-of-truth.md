# Hub Config Source of Truth

## Decision

For v1, hub config lives in a dedicated **git repository**. CI validates that repository and applies the rendered YAML to the Kapro hub cluster with `kubectl apply`.

Spoke clusters remain gitless. They consume OCI bundles and report status through `MemberCluster`; they do not watch the hub config repository.

## Why

Kapro separates two sources of truth:

- **Hub config truth:** the git repository that defines fleet inventory, applications, rollout pipelines, and release intent.
- **Runtime artifact truth:** the OCI registry that stores immutable application bundles consumed by spoke clusters.

The hub cluster needs Kubernetes objects such as `MemberCluster`, `KaproApp`, `Pipeline`, and `Release` to drive the fleet. Those objects must be reviewable, reproducible, and auditable. A plain git repository plus CI-driven `kubectl apply` is the v1 operating model.

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
Hub cluster etcd: MemberCluster, KaproApp, Pipeline, Release
   |
   v
Kapro operator
   |
   v
Spoke clusters: pull OCI bundles and report status
```

## What lives in the hub config repo

| Directory | Contents |
|---|---|
| `clusters/` | MemberCluster definitions (one per spoke) |
| `apps/` | KaproApp definitions (component registry, waves, overrides) |
| `pipelines/` | Pipeline definitions (stage DAG, selectors, gates) |
| `releases/` | Release objects (version + pipeline references) |
| `.github/workflows/` | CI that validates, diffs, and applies the repo to the hub |

## What does NOT live in the hub config repo

- OCI bundle contents (those are in the container registry)
- Spoke cluster workloads (those come from OCI bundles)
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
  apps/
    checkout.yaml
  pipelines/
    checkout-progressive.yaml
  releases/
    checkout-v1.2.3.yaml
  .github/
    workflows/
      apply-kapro-hub-config.yaml
```

See [examples/hub-config/](../examples/hub-config/) for the complete sample.

## Apply ordering

Apply objects in dependency order:

1. `clusters/` - registers `MemberCluster` inventory and labels used by selectors.
2. `apps/` - defines reusable application/component metadata.
3. `pipelines/` - defines stage DAGs, cluster selectors, and gate policy.
4. `releases/` - creates release intent that references pipelines and target versions.

This order keeps `Release` creation last, after the objects it references and the clusters it may select are present.

## CI workflow

CI validates every pull request and applies after merge to `main`.

Pull request checks:

```bash
kubectl apply --dry-run=server -f clusters/
kubectl apply --dry-run=server -f apps/
kubectl apply --dry-run=server -f pipelines/
kubectl apply --dry-run=server -f releases/

kubectl diff -f clusters/ || true
kubectl diff -f apps/ || true
kubectl diff -f pipelines/ || true
kubectl diff -f releases/ || true
```

Merge-to-main apply:

```bash
kubectl apply -f clusters/
kubectl apply -f apps/
kubectl apply -f pipelines/
kubectl apply -f releases/
```

Post-apply checks:

```bash
kubectl get memberclusters.kapro.io
kubectl get kaproapps.kapro.io,pipelines.kapro.io,releases.kapro.io
kubectl describe releases.kapro.io checkout-v1-2-3
```

The CI identity needs permission to `get`, `list`, `watch`, `create`, `patch`, `update`, and `delete` the Kapro configuration objects it owns. Production repositories should protect `main` and require the validation job before merge.

## Local checks

Before opening a pull request from the hub config repo:

```bash
kubectl apply --dry-run=server -f clusters/ -f apps/ -f pipelines/ -f releases/
kubectl diff -f clusters/ -f apps/ -f pipelines/ -f releases/ || true
```

`--dry-run=server` requires access to a hub cluster with the Kapro CRDs installed. It catches schema and admission errors that static YAML parsing cannot catch.

## Optional Future Mode: Flux on Hub

Teams that standardize on GitOps controllers can run Flux on the hub cluster and point a Flux `Kustomization` at the hub config repository. In that mode, Flux replaces the CI `kubectl apply` step; the source-of-truth model stays the same.

```
git push -> Flux on hub -> Kapro CRDs in hub etcd -> Kapro operator -> spokes
```

Flux-on-hub remains optional. The v1 documented default is git repository plus CI validation and `kubectl apply`.
