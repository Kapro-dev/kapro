# First Promotion in 10 Minutes

This is the shortest greenfield path for seeing Kapro create Promotion intent
and reconcile a controller-owned PromotionRun attempt. Use the Kind demo when
you want a fully local scripted environment; use this page when you already
have a Kubernetes cluster and want to apply the smallest useful hub
configuration yourself. For a fully scripted local cluster, use the
[Kind demo](https://github.com/Kapro-dev/kapro/tree/main/examples/kind-demo).
If you are deciding between Flux, Argo CD, OCI, or brownfield adoption first,
start with the [Adoption Guide](adoption.md).

## 1. Install The Operator

```bash
helm upgrade --install kapro \
  https://github.com/Kapro-dev/kapro/releases/download/v0.1.2/kapro-operator-0.1.2.tgz \
  --namespace kapro-system \
  --create-namespace
kubectl -n kapro-system rollout status deployment/kapro-kapro-operator
```

Expected:

```text
deployment "kapro-kapro-operator" successfully rolled out
```

For throwaway local clusters where you intentionally want to skip admission
webhooks:

```bash
helm upgrade --install kapro \
  https://github.com/Kapro-dev/kapro/releases/download/v0.1.2/kapro-operator-0.1.2.tgz \
  --namespace kapro-system \
  --create-namespace \
  --set webhook.enabled=false
```

When working from a local checkout before the release is published, use
`charts/kapro-operator` in place of the release URL.

## 2. Apply A Minimal Hub Config

The quickstart proves the hub API path: Backend, Fleet, generated Cluster and
Plan records, Promotion intent, and controller-owned runtime objects. Pull-mode
examples still need reachable registries and spoke delivery wiring before they
can prove real workload convergence.

```bash
kubectl apply -f examples/quickstart/backend-flux.yaml
kubectl apply -f examples/quickstart/kapro.yaml
```

Expected:

```bash
kubectl get backends,fleets,plans
```

shows one `Backend`, one `Fleet`, and one generated `Plan`. The example
`Fleet` also generates two synthetic `Cluster` objects
from `spec.clusters`.

## 3. Add Or Confirm Targets

```bash
kubectl get clusters
```

You should see the generated `checkout-canary-eu` and
`checkout-production-eu` clusters from `examples/quickstart/kapro.yaml`. If
none appear, the operator is not reconciling the `Fleet` object; check the
controller logs before creating manual test targets. Use the Kind demo for a
fully scripted hub/spoke setup.

## 4. Promote A Version

```bash
kubectl apply -f examples/quickstart/promotion.yaml
kubectl get promotions,promotionruns,targets
```

Expected:

```text
Promotion       checkout-v1-2-3
PromotionRun    created by the controller
Target         created for each selected target
```

## 5. Watch The Evidence

```bash
kubectl get promotions,promotionruns,targets -w
kubectl describe target <target-name>
```

Look for:

- target phase progression;
- aggregate target counts in `PromotionRun.status.summary`;
- per-target detail in child `Target` objects;
- backend convergence messages, when spoke delivery is wired;
- gate evidence or approval wait state, if you later add gates to the Plan.

## Next

- Argo CD greenfield path: [Argo CD Quickstart](quickstart-argo.md).
- OCI-only greenfield path: [OCI Quickstart](quickstart-oci.md).
- Existing Argo CD users: [Argo Brownfield Migration](argo-migration.md).
- Existing Flux users: [Flux Brownfield Migration](flux-migration.md).
- Discovery or needs-review issues: [Discovery Troubleshooting](discovery-troubleshooting.md).
