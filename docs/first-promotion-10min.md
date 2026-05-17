# First Promotion in 10 Minutes

This is the shortest greenfield path for seeing Kapro create and reconcile a
Promotion. Use the Kind demo when you want a fully local scripted environment;
use this page when you already have a Kubernetes cluster and want to apply the
smallest useful hub configuration yourself.

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
kubectl apply -f examples/hub-config/backends/flux.yaml
kubectl apply -f examples/hub-config/sources/checkout.yaml
kubectl apply -f examples/hub-config/promotionplans/checkout-progressive.yaml
```

Expected:

```bash
kubectl get backendprofiles,promotionsources,promotionplans
```

shows one backend profile, one source, and one promotion plan.

## 3. Add Or Confirm Targets

```bash
kubectl get fleetclusters
```

If your test cluster has no `FleetCluster` objects yet, use the Kind demo for a
fully scripted hub/spoke setup, or create a small test `FleetCluster` that
matches the labels in the example PromotionPlan.

## 4. Create A Promotion

```bash
kubectl apply -f examples/hub-config/promotions/checkout-v1.2.3.yaml
kubectl get promotions,promotionruns,promotiontargets
```

Expected:

```text
Promotion       created or configured
PromotionRun    created by the controller
PromotionTarget created for each selected target
```

## 5. Watch The Evidence

```bash
kubectl get promotionruns,promotiontargets -w
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
