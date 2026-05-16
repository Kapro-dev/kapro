# Brownfield Backend Examples

These fixtures show how Kapro connects to existing Argo CD or Flux installs
without copying backend credentials or rewriting every workload into Kapro
objects.

## Argo CD

`argo-existing-topology.yaml` represents a hub Argo CD namespace that already
has multiple cluster Secrets, Applications from multiple Git repositories, and
an ApplicationSet. Kapro discovers only objects selected by labels.

```bash
kapro discover argo . \
  --out ./kapro-connect \
  --name checkout \
  --namespace argocd \
  --selector kapro.io/import=true,team=checkout

kubectl apply -f ./kapro-connect/backends/checkout-observe.yaml
```

`kapro adopt argo . --out ./kapro-connect --name checkout` is an alias for the
same observe-first Argo onboarding workflow.

`kapro discover argo` requires the `git` CLI and a Git worktree. It reads
tracked YAML/JSON files from `git ls-files`, scans common GitOps prefixes by
default, and writes `discovery/argo-cache.json` so repeat scans skip unchanged
Git blobs.

The scanner is intentionally bounded: 10,000 candidate files and 1,000
promotion units by default. Prefer `--path-prefix` for unique monorepo layouts;
raise `--max-files` or `--max-units` only when the generated report is still
small enough to review.

The generated `BackendProfile` starts with `managementPolicy: Observe`. Argo CD
keeps cluster credentials, repository credentials, Projects, Applications, and
ApplicationSets. Kapro reads metadata and health through Kubernetes RBAC. After
the discovered graph is correct, switch the profile to
`managementPolicy: Adopt` for selected promotion writes such as
`spec.source.targetRevision`.

Discovery writes `sources/checkout.yaml`, `discovery/argo-discovery.yaml`, and
`discovery/kapro-git-map.yaml`. The source file is the executable mapping used
for Git-native promotion writes:

```bash
kapro source apply \
  --repo . \
  --source ./kapro-connect/sources/checkout.yaml \
  --set checkout-api=2.0.0 \
  --include argocd/environments/dev.json
```

When a mapping matches multiple files, `kapro source apply` requires
`--include` or `--all` before it writes. It also writes only tracked Git files,
so local scratch files cannot be promoted by accident. Runtime `BackendProfile`
status reports full counts and bounded object samples:

```bash
kubectl get backendprofile checkout -o jsonpath='{.status.discoveredApplications}'
kubectl get backendprofile checkout -o jsonpath='{.status.selectedObjects}'
kubectl get backendprofile checkout -o jsonpath='{.status.unsupportedPatterns}'
```

The bounded samples are diagnostic evidence. Counts remain accurate for large
fleets even when only the first sample entries are stored in status.

### Argo Pattern 1: Plain Applications

Use this when teams already create one Argo CD `Application` per workload or per
environment. Label only the Applications Kapro should see:

```yaml
metadata:
  labels:
    kapro.io/import: "true"
    team: checkout
    service: api
    kapro.io/tier: canary
```

Kapro observes selected Applications and can later adopt promotion writes to the
Application revision field. Argo CD still owns sync, drift correction, cluster
credentials, and repository credentials.

### Argo Pattern 2: ApplicationSets

Use this when Argo CD generates Applications from cluster, list, matrix, or Git
generators. Put import labels on the `ApplicationSet` template so generated
Applications are selectable:

```yaml
spec:
  template:
    metadata:
      labels:
        kapro.io/import: "true"
        team: checkout
        kapro.io/tier: production
```

Kapro should start in `Observe` mode and show the generated graph. For Git file
generators, `kapro discover argo` maps `targetRevision: '{{.field}}'` back to
the JSON/YAML generator input field. That input file is the preferred adoption
target because it is the durable Argo source of truth.

For live generated Application adoption, put Kapro delegation labels in the
ApplicationSet template and select generated apps by label:

```yaml
spec:
  template:
    metadata:
      labels:
        kapro.io/managed-by: kapro
        service: checkout-api
```

Then configure the delivery backend with
`applicationSelector.checkout-api: service=checkout-api`.

### Argo Pattern 3: App Of Apps

Use this when a root Argo CD `Application` points at a Git path that defines
child Applications. Kapro should usually discover and promote the child
Applications, not the root app-of-apps object:

```yaml
metadata:
  name: checkout-api-prod-eu
  labels:
    kapro.io/import: "true"
    team: checkout
    kapro.io/tier: production
```

The root app remains Argo CD's packaging mechanism. Kapro adds promotionrun waves,
gates, approvals, and evidence around the children that actually map to
promotion targets. If a team wants the root app to be the promoted unit, label
only the root and keep children unlabelled.

### Argo Clusters And Secrets

Argo CD cluster registration is still the source of truth for credentials.
Kapro can discover cluster Secrets by label and status, but it should not copy
their data:

```yaml
metadata:
  labels:
    argocd.argoproj.io/secret-type: cluster
    kapro.io/import: "true"
    team: checkout
    kapro.io/tier: production
```

For 100 clusters, the onboarding step is labeling or selecting the existing
Argo cluster Secrets and Applications. Kapro does not require 100 new
kubeconfigs or duplicate cluster registrations.

## Flux

`flux-existing-topology.yaml` represents a hub Flux namespace with multiple
GitRepository, Kustomization, and HelmRelease objects. Kapro again discovers by
label instead of requiring every object to be re-authored.

```bash
kapro connect flux ./kapro-connect \
  --namespace flux-system \
  --selector kapro.io/import=true,team=checkout

kubectl apply -f ./kapro-connect/backends/flux-observe.yaml
```

Flux keeps its repository credentials and source references. Kapro stores only
backend references and selected object names.

### Flux Patterns

For plain Flux, label the Kustomizations and HelmReleases that represent
promotion targets. For repo-per-service or repo-per-env setups, label the
GitRepository too so discovery can show where the target came from. Kapro
should promote only selected fields such as an image tag, chart version, or
source revision; Flux keeps source authentication, reconciliation, drift
correction, and workload rollout.

## Greenfield

For new promotion repositories, use `kapro init`:

```bash
kapro init ./promotion-repo --backend argo --name checkout
kapro init ./promotion-repo --backend flux --name checkout --mode pull
kapro init ./promotion-repo --backend argo --name checkout --clusters none
```

`--clusters none` is repo-first mode. It creates backends, source metadata,
promotionplan metadata, and backend-native starter files, but skips `clusters/`,
`kapro/`, and `promotionruns/` until real targets exist.
