# Actuators

An **actuator** answers: "given a `PromotionTarget` advancing into Applying,
what concrete change happens on the spoke cluster, and how do we know it
converged?"

Actuators are selected by the `BackendProfile.spec.driver` referenced via
`FleetCluster.spec.delivery.backendRef`. The registry key combines mode and
backend, e.g. `push/flux`, `pull/oci`. `mode` is set per-cluster; see
[`docs/push-vs-pull.md`](push-vs-pull.md).

## Status legend

- **Live**: shipped, unit + integration tested.
- **Planned**: on the Q2/Q3 roadmap.
- **Plugin example**: shipped as a reference plugin in `examples/plugins/`
  rather than built-in.

## Built-in actuators

| Registry key | Status | What it does | Convergence check | Notes |
|---|---|---|---|---|
| `push/flux` | Live (`internal/actuator/fluxoperator`) | Patches a Flux Operator `ResourceSet` input field on the spoke. Single-app: patches the configured `inputField` (default `tag`). Multi-artifact: patches `{appKey}_version` per unit. | Polls the ResourceSet's HelmRelease/Kustomization Ready condition through Flux Operator's status. | Use when the hub orchestrates a hub-side or hub-managed Flux Operator. |
| `push/argo` | Live (`internal/actuator/argo`) | Patches Argo CD `Application.spec.source.targetRevision`. Multi-artifact via per-app target-revision fields. | Reads `Application.status.sync.status == Synced` AND `Application.status.health.status == Healthy`. | ApplicationSet support tracked for Q2 (D7). |
| `pull/flux`, `pull/oci`, `pull/argo` | Live (`internal/actuator/pull`, renamed in PR #PR2) | Records desired version on `FleetCluster.spec.desiredVersions` for the spoke to act on. Never dials the spoke. | Reads `FleetCluster.status.currentVersions` written by the spoke. | The hub side of pull mode. The spoke side is in `internal/spokeprovider/`. |

## Built-in spoke providers (consumed by `pull/*` actuators)

These run inside `kapro-cluster-controller` on the spoke and produce the
status that hub-side pull actuators read.

| `BackendDriver` | Status | What it does | Convergence signal | Notes |
|---|---|---|---|---|
| `oci` | Live (`internal/spokeprovider/outbound`) | OCI Delivery Core. Pulls Helm / Kustomize / Raw-YAML artifacts from an OCI registry and applies them via the two-phase staging engine in `internal/delivery`. | Per-app `FleetCluster.status.delivery[<app>].phase = Converged` after successful commit + readiness. | Greenfield. No external GitOps required. |
| `flux` | Live (`internal/spokeprovider/flux`, PR #PR5) | OBSERVES local `OCIRepository` and (optionally) `HelmRelease` resources and reports state back to hub. Never mutates Flux. | OCIRepository revision matches desired AND (no HelmRelease configured OR HelmRelease Ready=True). | Brownfield. The spoke already runs Flux; Kapro just reports what Flux is doing. |

## Planned (Q2)

| Target | Status | Owner |
|---|---|---|
| Direct Flux `OCIRepository` + `HelmRelease` actuator (sibling to fluxoperator) | Planned Q2 (D6) | for shops running plain Flux without Flux Operator |
| Argo `ApplicationSet` support | Planned Q2 (D7) | observe-and-target rather than patch ApplicationSet templates |
| Sveltos actuator (patches `ClusterProfile.spec.helmCharts[].chartVersion`) | Planned Q2 (D8) | "we use, never replace" Sveltos |
| OCM actuator (`ManifestWork` writer) | Planned Q2 (D8) | OCM cluster inventory + delivery |

## Out-of-tree (plugin) actuators

Any actuator can be implemented as a KAI gRPC plugin and registered via
`PluginRegistration` (with `KAPRO_ENABLE_PLUGIN_GATEWAY=true`). Examples in
the repo:

- `examples/plugins/argocd-actuator/` — reference plugin.
- (Planned) `examples/plugins/ocm-manifestwork-actuator/`
- (Planned) `examples/plugins/sveltos-clustersummary-actuator/`

See [`docs/extension-model.md`](extension-model.md) and
[`docs/plugin-authoring.md`](plugin-authoring.md) for the contract.

## See also

- [`docs/push-vs-pull.md`](push-vs-pull.md) — when each mode applies.
- [`docs/providers.md`](providers.md) — the discovery + connectivity side.
- [`docs/backend-architecture.md`](backend-architecture.md) — how
  BackendProfile + delivery + actuator compose.
- [`docs/cncf-integration-masterplan.md`](cncf-integration-masterplan.md) — the
  integration philosophy ("we use, never replace").
