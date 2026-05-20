# First Promotion in 10 Minutes

This is the shortest greenfield path for seeing Kapro create Promotion intent
and reconcile a controller-owned PromotionRun attempt. Use the Kind demo when
you want a fully local scripted environment; use this page when you already
have a Kubernetes cluster and want to apply the smallest useful hub
configuration yourself. For a fully scripted local cluster, use the
[Kind demo](../examples/kind-demo/README.md).

## 1. Install The Operator

```bash
helm upgrade --install kapro charts/kapro-operator \
  --namespace kapro-system \
  --create-namespace
kubectl -n kapro-system rollout status deployment/kapro-kapro-operator
```

Expected:

```text
deployment "kapro-kapro-operator" successfully rolled out
```

For local clusters without cert-manager:

```bash
helm upgrade --install kapro charts/kapro-operator \
  --namespace kapro-system \
  --create-namespace \
  --set webhook.enabled=false
```

## 2. Apply A Minimal Hub Config

```bash
kubectl apply -f examples/quickstart/backend-flux.yaml
kubectl apply -f examples/quickstart/kapro.yaml
```

Expected:

```bash
kubectl get backendprofiles,kaproes,promotionplans
```

shows one backend profile, one Kapro fleet object, and one generated promotion
plan. The example `Kapro` also generates two synthetic `FleetCluster` objects
from `spec.clusters`.

## 3. Add Or Confirm Targets

```bash
kubectl get fleetclusters
```

You should see the generated `canary-eu` and `prod-eu` targets from
`examples/quickstart/kapro.yaml`. If none appear, the operator is not
reconciling the `Kapro` object; check the controller logs before creating
manual test targets. Use the Kind demo for a fully scripted hub/spoke setup.

## 4. Promote A Version

```bash
kapro promote checkout --version v1.2.3
kubectl get promotions,promotionruns,promotiontargets
```

Expected:

```text
Promotion       created or updated
PromotionRun    created by the controller
PromotionTarget created for each selected target
```

## 5. Watch The Evidence

```bash
kubectl get promotions,promotionruns,promotiontargets -w
kubectl describe promotiontarget <target-name>
```

Look for:

- target phase progression;
- gate evidence;
- approval wait state, if the PromotionPlan requires approval;
- backend convergence messages.

## Next

- Existing Argo CD users: [Argo Brownfield Migration](argo-migration.md).
- Existing Flux users: [Flux Brownfield Migration](flux-migration.md).
- Discovery or needs-review issues: [Discovery Troubleshooting](discovery-troubleshooting.md).
