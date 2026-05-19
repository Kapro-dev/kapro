# Greenfield Outbound-Only OCI Demo

This example shows a Kapro fleet using the **greenfield** delivery stack:

- spoke clusters that have **no GitOps installed** (no Flux, no Argo);
- spoke clusters that can only reach outbound on HTTPS (air-gap-friendly,
  no inbound from the hub);
- delivery via the built-in **OCI Delivery Core** in
  `kapro-cluster-controller` (Helm, Kustomize, or raw YAML, applied with
  two-phase staging).

For brownfield (existing Argo CD or Flux installations on the spoke),
see [../brownfield](../brownfield/).

## What it demonstrates

Three FleetCluster CRs, all of them registering with the hub via
`provider.kind: outbound-agent` and CSR-based bootstrap, delivering via
`backendRef: oci`, `mode: pull`. No spoke-side Flux / Argo is required;
no inbound connection from the hub to the spoke is required.

Files:

| File | Purpose |
|---|---|
| `fleetclusters.yaml` | Three FleetClusters using `outbound-agent` + `oci` + `pull` mode. |
| `backend-profile.yaml` | BackendProfile selecting the `oci` driver with default OCI pull parameters. |
| `promotionplan.yaml` | A 2-stage plan: canary → general, with a manual approval between stages. |
| `promotionrun.yaml` | Advanced direct PromotionRun compatibility manifest that promotes `v1.2.3` of the bundle to the fleet. |

## How it differs from kind-demo

[`../kind-demo`](../kind-demo/) is the end-to-end runnable harness using
Flux Operator + ResourceSet on a hub-only kind cluster. This example does
**not** depend on Flux. It shows the canonical greenfield configuration
manifests so platform teams can crib from them when introducing Kapro to a
fleet that doesn't yet run any GitOps engine.

## End-to-end on kind (optional)

The full air-gap path needs a spoke binary install per cluster. To exercise
it locally:

1. Stand up the hub:
   ```bash
   make install                   # installs Kapro CRDs + operator
   kubectl apply -f backend-profile.yaml
   kubectl apply -f fleetclusters.yaml
   ```
2. On each spoke kind cluster (separate context), install the
   `kapro-cluster-controller` chart:
   ```bash
   kapro spoke bootstrap de-store-01 \
     --hub-url https://hub.example.com:6443 \
     --secret-out /tmp/de-store-01.yaml > /tmp/de-store-01-values.yaml
   kubectl --context kind-de-store-01 apply -f /tmp/de-store-01.yaml
   helm --kube-context kind-de-store-01 install kapro-cluster-controller \
     ../../charts/kapro-cluster-controller \
     -f /tmp/de-store-01-values.yaml -n kapro-system --create-namespace
   ```
3. Push an OCI bundle to a registry your spokes can reach (Harbor in the
   same kind network, GAR, ECR — anywhere with read access from the spoke
   network), then:
   ```bash
   kubectl apply -f promotionplan.yaml
   kubectl apply -f promotionrun.yaml
   kubectl get promotionruns,promotiontargets -w
   ```

When converged, every FleetCluster reports
`status.delivery[<app>].phase=Converged` with the OCI artifact's digest.

## See also

- [`docs/push-vs-pull.md`](../../docs/push-vs-pull.md) — when to choose
  outbound-agent + pull vs hub-dial + push.
- [`docs/providers.md`](../../docs/providers.md) — `outbound-agent` provider.
- [`docs/actuators.md`](../../docs/actuators.md) — `oci` (greenfield) vs
  `flux` (brownfield) spoke providers.
- [`docs/cluster-bootstrap.md`](../../docs/cluster-bootstrap.md) — the
  CSR-based registration protocol used by outbound-agent.
