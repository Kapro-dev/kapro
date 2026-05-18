# Push vs Pull — Per-Cluster Connectivity Matrix

Kapro supports both **push** (hub dials spoke) and **pull** (spoke dials hub)
delivery, and the choice is **per-cluster**, not global. The same hub serves a
fleet that mixes Argo on a GKE cluster with VPC peering, OCI pull mode on an
air-gap-ish retail store, and Flux on a remote on-prem cluster — all under one
PromotionRun, one DecisionTrace, one approval gate.

This page is the matrix to consult when registering a new cluster.

## Decision matrix

| You have… | Network model | `FleetCluster.spec.provider.kind` | `FleetCluster.spec.delivery.backendRef` | `FleetCluster.spec.delivery.mode` |
|---|---|---|---|---|
| GKE in the same VPC as the hub | Hub → spoke OK | `gcp-basic` or `gcp-fleet` | `flux` or `argo` | `push` |
| GKE in a private VPC (no peering) | Hub → spoke via Google Connect Gateway | `gcp-connect-gateway` | `flux` or `argo` | `push` |
| GKE Fleet membership (any project/region) | Hub → spoke via Connect Gateway, discovery enabled | `gcp-fleet` | `flux` or `argo` | `push` |
| EKS / AKS / on-prem reachable from hub | Hub → spoke OK | `kubeconfig` (manual) | `flux` or `argo` | `push` |
| CAPI-managed cluster | Hub → spoke OK | `capi` (v0.6 stub today) | any | `push` |
| OCM managed cluster | OCM owns the data plane | `kubeconfig` + OCM actuator plugin (Q2) | `oci` via plugin | `push` |
| Retail store / edge / air-gap-ish | Spoke → hub only (outbound HTTPS) | `outbound-agent` | `oci` (OCI Delivery Core) or `flux` | `pull` |
| Sveltos-managed fleet | Sveltos owns the data plane | `kubeconfig` + Sveltos actuator plugin (Q2) | via plugin | `push` |

`provider.kind` answers "how does the hub know about and reach this cluster?"
`delivery.backendRef` + `delivery.mode` answer "who applies the change and where
does that happen?" They are independent axes — for example
`provider.kind=outbound-agent` + `delivery.backendRef=flux` + `delivery.mode=pull`
is the brownfield pull-mode scenario where a remote cluster already runs Flux
and the spoke binary reports Flux's status back to the hub without the hub
ever dialing in.

## The pull-mode contract (outbound-only)

When `delivery.mode=pull`, the hub never opens a connection to the spoke. The
flow is:

1. Hub writes `FleetCluster.spec.desiredVersions[<app>] = <version>`.
2. Spoke `kapro-cluster-controller` (running on the spoke) reads its own
   FleetCluster.spec via the per-cluster RBAC-locked client built at CSR
   bootstrap.
3. Spoke's local Provider implementation (`oci`, `flux`, or an external plugin)
   reconciles the change locally — pulling an OCI artifact, or just observing
   Flux's progress, or whatever the driver does.
4. Spoke patches `FleetCluster.status.delivery[<app>]` with phase + observed
   digest + last error.
5. Hub's reconciler sees the status change and advances the PromotionTarget
   FSM.

The only inbound surface on the hub is the standard Kubernetes API server,
authenticated with the spoke's short-lived client certificate (rotated via the
built-in `kubernetes.io/kube-apiserver-client` signer). No webhook callbacks
from the spoke; no custom HTTP endpoints.

This is what makes Kapro work in:

- retail-store networks behind NAT and per-store firewalls;
- on-prem clusters whose API server is on a private network;
- regulated environments where inbound from the platform team's hub to the
  workload cluster requires a change-control ticket.

## The push-mode contract (hub dials spoke)

When `delivery.mode=push`, the hub holds (or mints) credentials to the spoke
API server and the hub-side actuator (Flux Operator actuator, Argo actuator,
or a custom plugin) patches the spoke's GitOps CR directly.

This is the simpler model when network reachability is available — no spoke
binary install, no CSR bootstrap. Adopters routinely run Argo CD on a hub
cluster with `Application` targets pointed at remote workload clusters; in
that setup Kapro's hub-side Argo actuator patches `Application.spec.source.
targetRevision` and the rest is Argo.

## Mixing push and pull in one fleet

Nothing requires the choice to be uniform. A typical setup:

- Centralized cloud clusters: `gcp-fleet` + `argo` + `push`.
- 50 retail stores: `outbound-agent` + `oci` + `pull`.
- One regulated on-prem cluster: `outbound-agent` + `flux` + `pull`.

All three appear in one `kubectl get fleetclusters` and one PromotionRun can
target them under a single PromotionPlan with stage-level concurrency limits.

## See also

- [`docs/cncf-integration-masterplan.md`](cncf-integration-masterplan.md) — how
  Kapro composes with Flux, Argo, OCM, Sveltos, Kargo without replacing them.
- [`docs/providers.md`](providers.md) — every shipped `provider.kind` with
  status (live / planned / stub).
- [`docs/actuators.md`](actuators.md) — every shipped backend with status.
- [`docs/heartbeat-and-reachability.md`](heartbeat-and-reachability.md) — what
  the hub does when a pull-mode spoke goes silent.
- [`docs/cluster-bootstrap.md`](cluster-bootstrap.md) — the CSR-based
  registration protocol used by `outbound-agent`.
