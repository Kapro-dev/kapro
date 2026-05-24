# Flux Quickstart

This quickstart keeps Flux as the cluster reconciler and uses Kapro as the
promotion layer. Kapro decides when a version may advance; Flux still owns
local sync, health, and drift correction.

Install Kapro with the default public-preview controllers:

```bash
helm upgrade --install kapro "$KAPRO_CHART" \
  --namespace kapro-system \
  --create-namespace
```

Generate a Flux profile repo:

```bash
kapro create flux ./promotion-repo --name checkout
cd promotion-repo
kubectl apply -f substrates/flux.yaml
kubectl wait --for=condition=Ready substrate/flux --timeout=90s
kubectl apply --recursive -f apps -f flux -f clusters -f plans -f fleets -f promotions
kubectl get substrate flux -o yaml
kubectl get fleets.kapro.io,plans.kapro.io,promotions.kapro.io,promotionruns.runtime.kapro.io,targets.runtime.kapro.io
```

The generated repo includes a Flux-shaped starter under `flux/`, workload
manifests under `apps/`, and Kapro `Substrate`, `Fleet`, `Plan`, and `Promotion`
objects. Push the generated repo and replace the placeholder `GitRepository`
URL before expecting Flux to sync. For the older checked-in minimal hub API
example, use `examples/quickstart/`.

Promote a new version:

```bash
kapro promote checkout --version 0.2.0
kapro diag checkout-0-2-0
```

For fully local scripted convergence, run the Kind smoke fixture from the repo
root:

```bash
KAPRO_CI_QUICKSTARTS=direct,flux,argo,oci scripts/ci-kind-smoke.sh
```

For real Flux controller adoption against an existing repo, use
`scripts/verify-install.sh flux-e2e` and the
[Flux Existing GitOps Migration](../migration/flux-migration.md).
