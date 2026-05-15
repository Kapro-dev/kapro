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

## Step 2: Discover The Existing Repo

Run discovery against the Git repository that already contains Argo
Applications, ApplicationSets, and environment files:

```bash
kapro discover argo . \
  --out kapro-connect \
  --name checkout \
  --namespace argocd \
  --selector kapro.io/import=true,team=checkout
```

This generates:

- `backends/checkout-observe.yaml` for observe-first runtime discovery;
- `sources/checkout.yaml` with inferred `PromotionSource` units;
- `discovery/argo-discovery.yaml` with selected, skipped, and unsupported
  patterns.

For ApplicationSet Git file generators, Kapro maps template variables such as
`targetRevision: '{{.gkProjectVersion}}'` back to the generator input file, for
example `argocd/environments/*.json:gkProjectVersion`. That file remains the
source of truth.

## Step 3: Apply The Observe Profile

```bash
kubectl apply -f ./kapro-connect/backends/checkout-observe.yaml
kubectl get backendprofile checkout -o yaml
```

Check:

- `status.conditions[type=DiscoveryReady]`;
- `status.discoveredClusters`;
- `status.discoveredApplications`;
- `status.discoveredApplicationSets`;
- `status.selectedObjects`;
- `status.skippedObjects`;
- `status.unsupportedPatterns`;
- `discovery/argo-discovery.yaml`.

## Step 4: Review Promotion Units

Review the generated `PromotionSource`. In brownfield Argo mode, a unit points
to either an existing Application source or a Git parameter file that feeds an
ApplicationSet.

```yaml
apiVersion: kapro.io/v1alpha1
kind: PromotionSource
metadata:
  name: checkout
spec:
  backendRef: checkout
  units:
    - name: api
      backendKind: ArgoApplicationSource
      namespace: argocd
      versionField: spec.source.targetRevision
    - name: pos-server
      backendKind: GitJSONField
      namespace: argocd
      versionField: argocd/environments/*.json:gkProjectVersion
```

## Step 5: Choose The Adoption Level

| Argo pattern | Recommended Kapro target |
|---|---|
| Plain Applications | The Application source revision. |
| ApplicationSet with Git file generator | The JSON/YAML generator input field. |
| ApplicationSet generated apps | The generated Application only when the team explicitly wants live Application adoption. |
| App-of-apps | Child Applications. Root Applications should normally remain observe-only packaging objects. |

After the discovered graph matches intent, switch the profile:

```yaml
spec:
  discovery:
    enabled: true
    managementPolicy: Adopt
```

The built-in Argo actuator writes only
`Application.spec.source.targetRevision`, sets a hard refresh annotation, and
requests an Argo sync operation. It does not change traffic, Projects,
destinations, repo credentials, cluster Secrets, or local rollout policy.

## Step 6: Promote

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
