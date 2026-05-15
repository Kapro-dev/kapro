# Flux Brownfield Migration

This guide is for teams that already run Flux with GitRepository,
OCIRepository, Kustomization, and HelmRelease objects.

Kapro should not replace Flux reconciliation. Flux keeps source credentials,
inventory, health, drift correction, and workload rollout. Kapro adds release
intent, promotion ordering, gates, approvals, rollback intent, and fleet
evidence.

## Repository Shape

Keep existing Flux files native:

```text
platform-gitops/
  flux/
    sources/
    kustomizations/
    helmreleases/
  kapro/
    backends/flux-observe.yaml
    sources/checkout.yaml
    pipelines/checkout.yaml
    releases/
```

Greenfield users can still use `kapro init --backend flux`. Brownfield users
should label existing Flux objects and add only the Kapro metadata needed for
promotion.

## Step 1: Label Selected Flux Objects

Label the Kustomizations or HelmReleases that represent promotion targets:

```yaml
metadata:
  labels:
    kapro.io/import: "true"
    team: checkout
    service: api
```

If a source object helps operators understand the graph, label the
GitRepository or OCIRepository too. Kapro does not need source credentials.

## Step 2: Generate An Observe Profile

```bash
kapro connect flux ./kapro-connect \
  --namespace flux-system \
  --selector kapro.io/import=true,team=checkout
```

Apply the observe profile:

```bash
kubectl apply -f ./kapro-connect/backends/flux-observe.yaml
kubectl get backendprofile flux -o yaml
```

Check `BackendProfile.status.selectedObjects` before enabling adoption.

## Step 3: Model Promotion Units

```yaml
apiVersion: kapro.io/v1alpha1
kind: PromotionSource
metadata:
  name: checkout
spec:
  backendRef: flux
  units:
    - name: api
      backendKind: HelmRelease
      namespace: flux-system
      versionField: spec.chart.spec.version
    - name: web
      backendKind: Kustomization
      namespace: flux-system
      versionField: spec.sourceRef.name + spec.path + source revision
```

The exact field depends on how the Flux repo models versions. For HelmRelease,
chart version is usually the cleanest promotion field. For Kustomization, teams
often promote by source revision or by a Git path that points at an environment
overlay.

## Step 4: Adopt Only The Version Field

Switch to `managementPolicy: Adopt` only when:

- the selected objects are the intended promotion targets;
- the team has chosen exactly which version field Kapro may write;
- Flux RBAC grants patch rights only in the target namespace.

Flux continues to reconcile the resulting desired state. Kapro does not write
repository Secrets, workload manifests, traffic resources, or health status.

## Step 5: Promote

Use Release versions for one or more units:

```yaml
apiVersion: kapro.io/v1alpha1
kind: Release
metadata:
  name: checkout-2026-05-15
spec:
  pipelines:
    - checkout
  versions:
    api: 1.5.0
    web: main-20260515
```

Kapro coordinates promotion across targets. Flux applies the selected version
inside each cluster or hub-managed namespace.
