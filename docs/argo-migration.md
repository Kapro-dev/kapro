# Argo CD Brownfield Migration

This guide is for teams that already run Argo CD with Applications,
ApplicationSets, app-of-apps, and registered clusters.

Kapro should be introduced as a promotion layer, not as a replacement for Argo
CD. Argo keeps cluster credentials, Projects, repo credentials, sync policy,
health checks, and local rollout behavior. Kapro adds promotion waves, gates,
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
    promotionplans/checkout.yaml
    promotionruns/
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

For ApplicationSets, put import labels on both the ApplicationSet object and
the template. The object labels let Kapro report the ApplicationSet in
discovery status; the template labels let generated Applications be selected:

```yaml
metadata:
  labels:
    kapro.io/import: "true"
    team: checkout
    service: api
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
Applications, ApplicationSets, and environment files. Discovery requires the
`git` CLI and a Git worktree; it reads tracked files from Git's index instead
of walking every file in the checkout.

```bash
kapro discover argo . \
  --out kapro-connect \
  --name checkout \
  --namespace argocd \
  --selector kapro.io/import=true,team=checkout
```

`kapro adopt argo` is the higher-level alias for the same observe-first
workflow. It exists for teams thinking in brownfield adoption terms:

```bash
kapro adopt argo . --out kapro-connect --name checkout
```

By default, discovery scans tracked YAML/JSON files under common GitOps
prefixes: `argocd/`, `apps/`, `clusters/`, `environments/`, and `flux/`.
Use `--path-prefix` for a custom layout or `--scan-all` when the repo does not
follow those prefixes. Repeat runs use `discovery/argo-cache.json` to skip
unchanged Git blobs.

Discovery is bounded by default: at most 10,000 tracked YAML/JSON candidate
files and 1,000 generated promotion units. Use `--max-files` or `--max-units`
only after narrowing `--path-prefix` is not enough. This keeps monorepos from
turning onboarding into an unreviewable import.

You can also point discovery at a remote Git URL. Kapro clones it to a
temporary directory for read-only discovery:

```bash
kapro discover argo https://github.com/example/platform.git \
  --revision main \
  --out kapro-connect \
  --name checkout
```

This generates:

- `backends/checkout-observe.yaml` for observe-first runtime discovery;
- `sources/checkout.yaml` with inferred `PromotionSource` units;
- `discovery/argo-discovery.yaml` with selected, skipped, and unsupported
  patterns;
- `discovery/kapro-git-map.yaml` with confidence and write-target evidence for
  review.

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

If `DiscoveryReady=False`, selected objects are missing, or generated units are
marked `confidence: needs-review`, use
[Discovery Troubleshooting](discovery-troubleshooting.md) before adopting
writes.

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
      sourcePath: argocd/apps/api.yaml
      versionField: spec.source.targetRevision
    - name: pos-server
      backendKind: GitJSONField
      namespace: argocd
      sourcePath: argocd/applicationsets/pos-server.yaml
      versionField: argocd/environments/*.json:gkProjectVersion
```

## Step 5: Apply Git-Native Promotion Writes

For Git-file backed units, Kapro updates the mapped JSON/YAML field in a local
checkout. It does not push automatically; review and commit the diff with your
normal Git workflow.

```bash
kapro source apply \
  --repo . \
  --source kapro-connect/sources/checkout.yaml \
  --set pos-server=1.2.3 \
  --include argocd/environments/dev.json
```

If a generated mapping contains a glob such as `argocd/environments/*.json`,
`kapro source apply` fails unless `--include` scopes the intended file or
`--all` is set. This keeps migration safe for repositories with many
environments. The command reads candidates from `git ls-files`, so it only
writes tracked files in a Git checkout; untracked local files are ignored until
they are added to Git.

To let Kapro commit and push the same Git diff from automation, opt in
explicitly:

```bash
kapro source apply \
  --repo . \
  --source kapro-connect/sources/checkout.yaml \
  --set pos-server=1.2.3 \
  --include argocd/environments/dev.json \
  --commit \
  --push \
  --message "Promote pos-server to 1.2.3"
```

## Step 6: Choose The Adoption Level

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

Live Argo Application adoption is intentionally opt-in. Each Application Kapro
may mutate must carry one of these labels or annotations:

```yaml
metadata:
  labels:
    kapro.io/managed-by: kapro
    # or:
    kapro.io/authorized-source: checkout
```

For ApplicationSet-generated apps, prefer putting the label in
`spec.template.metadata.labels`. Kapro can also select generated apps by a
delivery parameter such as `applicationSelector.pos-server:
kapro.io/import=true,service=pos-server`.

## Step 7: Promote

Create a PromotionRun with either one default version or per-unit versions:

```yaml
apiVersion: kapro.io/v1alpha1
kind: PromotionRun
metadata:
  name: checkout-2026-05-15
spec:
  version: 1.5.0
  promotionPlans:
    - name: main
      promotionPlan: checkout
  versions:
    api: 1.5.0
    web: 3.9.1
```

Kapro creates PromotionTargets, runs gates and approvals, and then calls the Argo
backend for selected targets. Argo CD remains responsible for reconciling the
Application to the cluster.

## Local E2E Proof

Before calling an Argo migration production-ready, run the real Argo CD E2E:

```bash
scripts/argo-e2e.sh run
```

The script creates a Kind cluster, installs Argo CD and Kapro, serves a
throwaway Git repo inside the cluster, runs `kapro adopt argo`, applies the
generated mapping, promotes the repo-native Argo fields with
`kapro source apply`, creates a Kapro `PromotionRun`, and waits for all selected Argo
Applications to become `Synced` and `Healthy` at the promoted revision.

This is the concrete acceptance test for the main brownfield patterns in this
guide: plain Application, multi-source Application, ApplicationSet child backed
by JSON or YAML generator inputs, and app-of-apps child. The root app-of-apps
Application is discovered as packaging evidence but is not used as a write
target.
