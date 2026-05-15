# Argo CD Brownfield Migration

This guide is for teams that already run Argo CD with Applications,
ApplicationSets, app-of-apps, and registered clusters.

Kapro should be introduced as a promotion layer, not as a replacement for Argo
CD. Argo keeps cluster credentials, Projects, repo credentials, sync policy,
health checks, and local rollout behavior. Kapro adds releases, waves, gates,
approvals, and fleet evidence.

## Repository Shape

Keep the existing Argo repository structure:

```text
platform-gitops/
  argocd/
    projects/
    applications/
    applicationsets/
    app-of-apps/
  kapro/
    backends/argo-observe.yaml
    sources/checkout.yaml
    pipelines/checkout.yaml
    releases/
```

The Kapro files can live beside Argo files or in a separate hub-config repo.
The important rule is that Argo-native objects remain Argo-native.

## Step 1: Label What Kapro May See

Start with the smallest useful slice, usually one team and one service:

```yaml
metadata:
  labels:
    kapro.io/import: "true"
    team: checkout
    service: api
```

For Argo cluster Secrets:

```yaml
metadata:
  labels:
    argocd.argoproj.io/secret-type: cluster
    kapro.io/import: "true"
    team: checkout
```

For ApplicationSets, put import labels on the template so generated
Applications can be selected:

```yaml
spec:
  template:
    metadata:
      labels:
        kapro.io/import: "true"
        team: checkout
        service: api
```

## Step 2: Generate An Observe Profile

```bash
kapro connect argo ./kapro-connect \
  --namespace argocd \
  --selector kapro.io/import=true,team=checkout
```

Apply only the observe profile first:

```bash
kubectl apply -f ./kapro-connect/backends/argo-observe.yaml
kubectl get backendprofile argo -o yaml
```

Check:

- `status.conditions[type=DiscoveryReady]`;
- `status.discoveredClusters`;
- `status.discoveredApplications`;
- `status.discoveredApplicationSets`;
- `status.selectedObjects`;
- `status.skippedObjects`;
- `status.unsupportedPatterns`.

## Step 3: Model Promotion Units

Create a `PromotionSource` that names the units Kapro will promote. In
brownfield Argo mode, a unit normally points to an existing Application.

```yaml
apiVersion: kapro.io/v1alpha1
kind: PromotionSource
metadata:
  name: checkout
spec:
  backendRef: argo
  units:
    - name: api
      backendKind: Application
      namespace: argocd
      versionField: spec.source.targetRevision
    - name: web
      backendKind: Application
      namespace: argocd
      versionField: spec.source.targetRevision
```

## Step 4: Choose The Adoption Level

| Argo pattern | Recommended Kapro target |
|---|---|
| Plain Applications | The Application. |
| ApplicationSet with generated apps | The generated Application first. Use the ApplicationSet actuator plugin only if one write must update the template for all generated children. |
| App-of-apps | Child Applications. Root Applications should normally remain observe-only packaging objects. |

After the discovered graph matches intent, switch the profile:

```yaml
spec:
  discovery:
    enabled: true
    managementPolicy: Adopt
```

The built-in Argo actuator writes only
`Application.spec.source.targetRevision`. It does not request sync or change
traffic. Use Argo automated sync or an external actuator if your production
policy requires explicit sync requests.

## Step 5: Promote

Create a Release with either one default version or per-unit versions:

```yaml
apiVersion: kapro.io/v1alpha1
kind: Release
metadata:
  name: checkout-2026-05-15
spec:
  version: 1.5.0
  pipelines:
    - checkout
  versions:
    api: 1.5.0
    web: 3.9.1
```

Kapro creates ReleaseTargets, runs gates and approvals, and then calls the Argo
backend for selected targets. Argo CD remains responsible for reconciling the
Application to the cluster.
