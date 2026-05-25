# Argo CD Quickstart

This quickstart keeps Argo CD as the cluster reconciler and uses Kapro as the
promotion layer. Kapro updates the Argo CD `Application` target revision for
each target cluster, then waits for Argo to report `Synced` and `Healthy`.

Prerequisites:

- Kapro operator installed with the default public-preview controllers.
- Argo CD installed in the `argocd` namespace.

For a local preview install:

```bash
helm upgrade --install kapro "$KAPRO_CHART" \
  --namespace kapro-system \
  --create-namespace
```

```bash
kapro create argo ./promotion-repo --name checkout
cd promotion-repo
kubectl apply -f substrates/argo.yaml
kubectl wait --for=condition=Ready substrate/argo --timeout=90s
kubectl apply --recursive -f apps -f argo -f clusters -f deliveryunits -f plans -f fleets -f promotions
kubectl get deliveryunits.kapro.io,promotions.kapro.io,promotionruns.runtime.kapro.io,targets.runtime.kapro.io
```

The generated repo includes a `SubstrateClass`, typed `ArgoCDSubstrateConfig`,
`Substrate`, target-specific Argo CD `Application` objects, starter workload
manifests under `apps/`, and Kapro `DeliveryUnit`, `Fleet`, `Plan`, and
`Promotion` objects.
The `DeliveryUnit` owns source mapping intent; `Promotion` remains the explicit
rollout action.
Push the generated repo and replace the placeholder `repoURL` values before
expecting Argo CD to sync. If your Argo Applications already exist with
different names, set `spec.substrate.parameters.application` for one shared
Application or `applicationSelector` for label selection.

Kapro only writes Argo CD `Application` objects that explicitly opt in. The
generated Applications already include the authorization labels. For existing
Applications, add one of these labels or annotations before adopting them:

```yaml
kapro.io/managed-by: kapro
kapro.io/authorized-source: "*"
kapro.io/authorized-unit: checkout
```

For the older checked-in minimal hub API example, use
`examples/quickstart-argo/`.
