# Providers

A `Provider` answers two questions about a `FleetCluster`:

1. How does the hub *know about* this cluster (discovery)?
2. How does the hub *reach* this cluster's Kubernetes API (connectivity)?

The choice is per-cluster, selected via `FleetCluster.spec.provider.kind`. New
providers can be added via a `PluginRegistration` (KAI/KGI/KPI gRPC plugin) when
KAPRO_ENABLE_PLUGIN_GATEWAY=true; the table below is the built-in set.

## Status legend

- **Live**: code shipped, exercised by unit tests and at least one integration
  test, listed in the CRD enum.
- **Stub**: appears in the enum and accepts CRs but rejects at runtime with an
  actionable error pointing at the issue tracker.
- **Planned**: tracked on the roadmap; not yet in the enum.

## Built-in providers

| `provider.kind` | Status | What it does | Discovery? | Network model | Notes |
|---|---|---|---|---|---|
| `kubeconfig` | Live | Static kubeconfig file (manual registration) | No | Hub → spoke direct | Dev/test or simple production where a long-lived kubeconfig Secret is acceptable. |
| `gcp-basic` | Live | Direct GKE API endpoint + Workload Identity | No | Hub → spoke direct | Single-project GKE or VPC-peered private GKE. No Fleet membership required. |
| `gcp-fleet` | Live | GKE Fleet API for discovery + Connect Gateway for access | Yes (membership list) | Hub → spoke via Google | Topology-agnostic. Works across any project/VPC/region without peering. Hub identity needs `roles/gkehub.viewer` for discovery + `roles/gkehub.gatewayReader` for access. |
| `gcp-connect-gateway` | Live (PR #PR3) | Connect Gateway access for a *known* membership without discovery | No | Hub → spoke via Google | Lower privilege than `gcp-fleet` (just `gatewayReader`); use when you've already recorded project+location+membership in spec.provider.parameters. |
| `outbound-agent` | Live | Spoke runs `kapro-cluster-controller`; bootstraps via CSR | n/a (spoke self-registers) | Spoke → hub only | The air-gap-friendly path. Closes the gap that the original brain framing said only existed on paper. |
| `eks` | Stub | EKS cluster discovery via AWS API | Planned | Hub → spoke direct | Returns an error today; tracked for Q2. |
| `aks-arc` | Stub | Azure Arc-connected cluster discovery | Planned | Hub → spoke direct | Returns an error today; tracked for Q2. |
| `rhacm` | Stub | Red Hat ACM-managed cluster discovery | Planned | Hub → spoke direct | Returns an error today; tracked for Q2. |
| `capi` | Stub | Cluster API `Cluster` objects as inventory | Planned | Hub → spoke direct | Returns an error today; tracked for Q2 (CNCF-native, highest priority of the stubs). |

## Selecting a provider per cluster

```yaml
apiVersion: kapro.io/v1alpha1
kind: FleetCluster
metadata:
  name: de-store-01
spec:
  provider:
    kind: outbound-agent
    parameters:
      hubURL: https://kapro-hub.example.com:6443
  bootstrap:
    tokenHash: sha256:....
    ttl: 24h
  delivery:
    backendRef: oci          # see docs/actuators.md
    mode: pull               # see docs/push-vs-pull.md
    parameters:
      ociRepository: oci://harbor.de-store-01.local/kapro/bundle
```

## Adding a new provider

Two routes:

1. **In-tree**: add a `*Provider` type in `internal/provider/`, register it in
   the factory switch at `internal/provider/provider.go:91-102`, add the new
   kind to the enum in `api/v1alpha1/fleetcluster_types.go`, run
   `make manifests`.
2. **Out-of-tree (plugin)**: implement the appropriate gRPC contract (KAI for
   actuators, KSP for spoke providers, etc.) and register via a
   `PluginRegistration` CR with `KAPRO_ENABLE_PLUGIN_GATEWAY=true`.

See [`docs/plugin-authoring.md`](plugin-authoring.md) for the plugin route and
[`docs/plugin-compatibility.md`](plugin-compatibility.md) for the contract
version policy.

## See also

- [`docs/push-vs-pull.md`](push-vs-pull.md) — when each provider kind makes sense.
- [`docs/actuators.md`](actuators.md) — the delivery side (what gets applied).
- [`docs/cluster-bootstrap.md`](cluster-bootstrap.md) — the registration
  protocol used by `outbound-agent`.
