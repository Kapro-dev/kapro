# Flux Brownfield Migration

This guide is for teams that already run Flux with GitRepository,
OCIRepository, Kustomization, and HelmRelease objects.

Kapro should not replace Flux reconciliation. Flux keeps source credentials,
inventory, health, drift correction, and workload rollout. Kapro adds `Fleet`,
`Source`, `Plan`, and `Promotion` intent around the Flux estate.

The migration path is observe, review, then adopt. Discovery generates a
read-only `Backend`, inferred `Source` units, and review reports. Adoption
should only enable the specific version writes the owning team has reviewed.

## Repository Shape

Keep existing Flux files native:

```text
platform-gitops/
  flux/
    sources/
    kustomizations/
    helmreleases/
  fleets/
    backends/checkout-observe.yaml
    sources/checkout.yaml
    plans/checkout.yaml
    promotions/
```

Greenfield users can still use `kapro init --backend flux`. Brownfield users
should label existing Flux objects and add only the Kapro metadata needed for
promotion.

## Step 1: Label Selected Flux Objects

Label the Kustomizations or HelmReleases that represent the workloads Kapro
should observe:

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

Use the guided brownfield bootstrap first:

```bash
kapro bootstrap brownfield flux . \
  --out ./kapro-connect \
  --name checkout \
  --namespace flux-system \
  --selector kapro.io/import=true,team=checkout
```

This is the recommended first command for new adopters. It delegates to the
same discovery engine as `kapro discover flux`.

You can also run discovery directly:

```bash
kapro discover flux . \
  --out ./kapro-connect \
  --name checkout \
  --namespace flux-system \
  --selector kapro.io/import=true,team=checkout
```

Apply the observe profile:

```bash
kubectl apply -f ./kapro-connect/backends/checkout-observe.yaml
kubectl get backend checkout -o yaml
```

Check `Backend.status.selectedObjects`, `discovery/flux-discovery.yaml`, and
`discovery/kapro-git-map.yaml` before enabling adoption. Observe mode does not
patch Flux objects.

## Step 3: Review Source Units

The discovery command generates `kapro-connect/sources/<name>.yaml` from common
Flux Git-native patterns:

- `GitRepository.spec.ref.tag`, `semver`, `digest`, or reviewed `branch`
- `OCIRepository.spec.ref.tag`, `semver`, `digest`, or reviewed `branch`
- `Bucket.spec.ref.*`
- `HelmRelease.spec.chart.spec.version`
- obvious `HelmRelease.spec.values.image.tag` fields
- reviewed custom HelmRelease values image tags such as
  `spec.values.containers.api.tag`
- Kustomize `images[].newTag` in `kustomization.yaml`
- Helm chart `Chart.yaml` `version` and `appVersion`

Flux `Kustomization` objects are reported but not treated as direct version
write targets because `spec.path` and `spec.sourceRef` are topology/configuration
fields, not a universal promotion version. Use the referenced source object,
the Kustomize image file, or an explicit field you add to `Source`.

```yaml
apiVersion: kapro.io/v1alpha2
kind: Source
metadata:
  name: checkout
spec:
  backendRef: checkout
  units:
    - name: api
      backendKind: GitYAMLField
      namespace: flux-system
      sourcePath: flux/helmreleases/api.yaml
      versionField: spec.chart.spec.version
    - name: web
      backendKind: KustomizeImage
      namespace: flux-system
      sourcePath: apps/web/kustomization.yaml
      versionField: ghcr.io/example/web
```

The exact field depends on how the Flux repo models versions. Generated units
with `confidence: needs-review` should be edited or removed before adoption.
The canonical list of automatic, skipped, and review-required patterns is
[Backends](backends.md).
For concrete failure modes and editing guidance, see
[Discovery Troubleshooting](discovery-troubleshooting.md).

## Step 4: Adopt Only The Version Field

Switch to `managementPolicy: Adopt` only when:

- the selected objects are the intended Flux objects;
- the team has chosen exactly which version field Kapro may write;
- the `Source` is referenced by the intended `Fleet` and `Plan`;
- Flux RBAC grants patch rights only in the target namespace.

Flux continues to reconcile the resulting desired state. Kapro does not write
repository Secrets, workload manifests, traffic resources, or health status.
`Adopt` only changes what the backend is allowed to patch; rollout order still
comes from the `Plan`, and each rollout starts from a reviewed `Promotion`.

## Step 5: Promote

Create a `Promotion` for one or more units. The controller stamps immutable
`PromotionRun` attempts from that intent:

```yaml
apiVersion: kapro.io/v1alpha2
kind: Promotion
metadata:
  name: checkout-2026-05-15
spec:
  fleetRef: checkout
  plans:
    - name: main
      plan: checkout
  versions:
    api: 1.5.0
    web: main-20260515
```

Kapro creates a `PromotionRun` and `Target` records, then coordinates promotion
across the selected targets. Flux applies the selected version inside each
cluster or hub-managed namespace.

## Local Git-Native E2E

Before calling a Flux mapping ready, run:

```bash
scripts/flux-git-e2e.sh
```

The script creates a disposable Git repo and verifies `kapro source apply` can
update representative Flux-native fields: `GitRepository.spec.ref.tag`,
`OCIRepository.spec.ref.tag`, `HelmRelease.spec.chart.spec.version`,
HelmRelease values image tags, Kustomize `images[].newTag`, and Helm
`Chart.yaml` version fields. It also verifies the Flux discovery command
generates the mapping before applying it.

## Live Flux Controller E2E

Before calling a Flux brownfield path release-ready, also run:

```bash
scripts/verify-install.sh flux-e2e
```

This creates a disposable Kind cluster, installs real Flux controllers, serves a
Git fixture inside the cluster, bootstraps Flux from that repo, runs
`kapro adopt flux`, applies the generated `Source` mapping from `v1`
to `v2`, pushes the Git change, and waits for Flux to reconcile the workload
ConfigMap to `v2`.
