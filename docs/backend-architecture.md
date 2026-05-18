# Backend-Neutral Delivery Architecture

Kapro is a fleet deployment promotion control plane. It owns PromotionRun intent,
target ordering, gates, approvals, heartbeat freshness, and fleet status. It
does not own traffic shifting or assume a specific GitOps controller.

Kapro supports two connect paths:

- **Greenfield bootstrap:** create the hub, backend profiles, cluster inventory,
  promotion sources, PromotionPlans, gates, and optional spoke agents from Kapro
  commands or manifests.
- **Brownfield connect:** discover existing Argo CD or Flux topology, observe it
  first, then explicitly adopt selected applications or clusters for promotion.

The rule is simple: greenfield should be easy to start, brownfield should be
easy to trust.

## Source Of Truth

Kubernetes CRDs remain the durable API. The Kapro Hub Gateway is a stateless
facade for UI and CLI clients; it creates or reads CRDs and does not mutate
backend systems directly.

Primary APIs:

- `Kapro` selects a promotion source, a delivery backend, clusters, and stages.
- `BackendProfile` declares a selectable delivery backend.
- `FleetCluster.spec.delivery` selects the per-cluster backend profile.
- `PromotionRun` stores user intent and execution state; `PromotionTarget`
  stores per-target execution state.

## Greenfield Bootstrap

For new fleets, Kapro can be the setup path for promotion infrastructure. The
recommended greenfield default is **OCI pull mode**: the hub records desired
versions on `FleetCluster`, and a spoke-side `kapro-cluster-controller` pulls
and applies the artifact from inside the workload cluster. This keeps the hub
out of spoke network paths and works for outbound-only, edge, and sovereign
fleet shapes.

1. Install the Kapro hub controller and Hub Gateway.
2. Create a built-in `BackendProfile`, preferably `oci` for new outbound-only
   fleets or `flux`/`argo` when the fleet already standardizes on those tools.
3. Register or generate `FleetCluster` inventory.
4. Generate a starter `PromotionSource`, `PromotionPlan`, gates, and example
   `PromotionRun`.
5. Install a spoke agent for pull-mode clusters.

This is platform bootstrap for the promotion layer, not a replacement for a
platform installer. Tools that bootstrap clusters, ingress, observability, or
base platform services can run before Kapro; Kapro then bootstraps the
promotion control plane on top.

```bash
kapro init ./promotion-repo --backend oci --name checkout --mode pull
kapro init ./promotion-repo --backend flux --name checkout --mode pull
kapro init ./promotion-repo --backend argo --name checkout
kapro init ./promotion-repo --backend argo --name checkout --clusters none
```

## Brownfield Connection

For existing fleets, Kapro should avoid asking users to recreate objects that
already exist in Argo CD or Flux. Brownfield connect has three phases:

1. **Observe:** discover backend-native clusters and applications, report graph
   and health, and do not write to backend-owned objects.
2. **Adopt:** bind selected backend objects to Kapro PromotionRuns and allow Kapro to
   update version fields such as Argo `targetRevision` or Flux input tags.
3. **Manage:** optionally let Kapro generate new sources, PromotionPlans, and backend
   wiring for teams that want a stronger convention.

Argo CD users can keep cluster Secrets, Applications, ApplicationSets, and
app-of-apps in their Git repo. Kapro adds promotion waves, gates, approvals,
evidence, and fleet-wide status around that existing topology.

The exact write contract is documented in `backend-ownership.md`. Step-by-step
brownfield onboarding paths are documented in `argo-migration.md` and
`flux-migration.md`. The broader CNCF integration boundary is documented in
`cncf-integration-masterplan.md`.

```bash
kapro connect argo ./kapro-connect --namespace argocd --selector kapro.io/import=true
kapro connect flux ./kapro-connect --namespace flux-system --selector kapro.io/import=true
```

## Backend Profiles

`BackendProfile.spec.driver` is `flux`, `argo`, or `external`.
`spec.runtime` is `Hub`, `Spoke`, or `Both`.

Built-in Flux and Argo adapters are registered by the operator. External
backends are referenced through `spec.pluginRef` and must have a ready
`PluginRegistration`.

Example:

```yaml
apiVersion: kapro.io/v1alpha1
kind: BackendProfile
metadata:
  name: argo
spec:
  driver: argo
  runtime: Hub
```

For existing Argo CD installations, start with discovery in observe mode:

```yaml
apiVersion: kapro.io/v1alpha1
kind: BackendProfile
metadata:
  name: argo
spec:
  driver: argo
  runtime: Hub
  parameters:
    namespace: argocd
  discovery:
    enabled: true
    managementPolicy: Observe
    selector:
      matchLabels:
        kapro.io/import: "true"
```

This lets Kapro count Argo CD cluster Secrets and selected Applications without
taking over writes. Switching `managementPolicy` to `Adopt` is the explicit
step that allows Kapro promotion commands to update Argo target revisions.

By default, discovery is periodically refreshed so optional Argo/Flux CRDs do
not become hard install dependencies. Installations that already have the
backend CRDs present can enable event-triggered refresh for backend objects with
`KAPRO_ENABLE_BACKEND_OBJECT_WATCHES=true`. Core Argo CD cluster Secrets are
watched without that opt-in.

## Backend-Owned Credentials

Brownfield connection does not import or copy backend credentials. Argo CD keeps
cluster Secrets, Git credentials, repository credentials, Projects, Applications,
and ApplicationSets. Flux keeps its GitRepository, OCIRepository, Kustomization,
HelmRelease, and Secret references. Kapro reads backend metadata through
Kubernetes RBAC and stores only references such as `backendRef`, `secretRef`, or
backend object names when it needs them.

When Kapro promotes through a backend, the backend remains responsible for local
authentication, sync, drift correction, rollout, and traffic mechanics. Kapro
updates only the selected promotion field, for example Argo CD
`spec.source.targetRevision`, a Flux input tag, or a Kapro desired-version CRD
that a spoke-side adapter consumes.

## Delivery Config

Backend-specific fields are opaque parameters. Kapro core uses only `mode` and
`backendRef`.

```yaml
spec:
  delivery:
    mode: push
    backendRef: argo
    parameters:
      namespace: argocd
      application: checkout-prod
```

Delivery mode controls where mutation happens:

| Mode | Kapro hub behavior | Backend behavior |
|---|---|---|
| `pull` | Writes desired versions to `FleetCluster.spec.desiredVersions`. | A spoke agent reads that intent and applies it locally through OCI, Flux, Argo, or a plugin. |
| `push` | Mutates backend-owned objects from the hub, such as Argo Applications or Flux Operator ResourceSet inputs. | The selected backend reconciles local rollout, drift, health, and traffic behavior. |

Use pull mode when clusters cannot accept inbound connections from the hub or
when local autonomy is required. Use push mode when the backend's control plane
already lives on the hub and exposing that backend to Kapro is operationally
simpler.

Flux push mode uses parameters such as `resourceSet`, `namespace`, `inputField`,
and `tenantField`. Flux pull mode uses `ociRepository` or
`ociRepository.<appKey>` mappings. Argo push mode uses `application` or
`application.<appKey>`.

The built-in Argo push adapter patches
`Application.spec.source.targetRevision` and checks for Argo CD
`status.sync.status=Synced` plus `status.health.status=Healthy`. It assumes the
selected Application is auto-synced, or that another Argo process performs the
sync after the revision changes. Install a custom Argo actuator plugin for
manual-sync environments that must request Argo operations explicitly.

## Hub Gateway

The operator exposes the Hub Gateway unless `KAPRO_DISABLE_HUB_GATEWAY=true`.
The default address is `:8092` and can be changed with
`KAPRO_HUB_GATEWAY_ADDR`.

Initial endpoints:

- `GET /healthz`
- `GET /api/v1/graph`
- `POST /api/v1/promotionruns`

The gateway is intentionally asynchronous. Commands create or patch CRDs; the
controllers and backend adapters perform reconciliation.

`GET /api/v1/graph` and `POST /api/v1/promotionruns` require an
`Authorization: Bearer <token>` header. The built-in token check is a
development and port-forward convenience, not the production gateway auth
model. Production exposure should put Kubernetes authentication and
authorization, or an identity-aware reverse proxy, in front of the gateway.
Request bodies are size-limited and unknown JSON fields are rejected.

`GET /api/v1/graph` returns bounded responses. Use `resource` to select
`kapros`, `fleetclusters`, `promotionruns`, `promotiontargets`, or
`backendprofiles`; use `labelSelector`, `phase`, and `limit` to keep fleet
reads scoped.
