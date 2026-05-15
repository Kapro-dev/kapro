# Backend-Neutral Delivery Architecture

Kapro is a fleet deployment promotion control plane. It owns release intent,
target ordering, gates, approvals, heartbeat freshness, and fleet status. It
does not own traffic shifting or assume a specific GitOps controller.

Kapro supports two connect paths:

- **Greenfield bootstrap:** create the hub, backend profiles, cluster inventory,
  starter bundles, pipelines, gates, and optional spoke agents from Kapro
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

- `Kapro` selects a bundle, a delivery backend, clusters, and stages.
- `BackendProfile` declares a selectable delivery backend.
- `MemberCluster.spec.delivery` selects the per-cluster backend profile.
- `Release` and `ReleaseTarget` store promotion execution state.

## Greenfield Bootstrap

For new fleets, Kapro can be the setup path for promotion infrastructure:

1. Install the Kapro hub controller and Hub Gateway.
2. Create a built-in `BackendProfile` such as `flux` or `argo`.
3. Register or generate `MemberCluster` inventory.
4. Generate a starter `KaproBundle`, `Pipeline`, gates, and example `Release`.
5. Optionally install a spoke agent for pull-mode clusters.

This is platform bootstrap for the release layer, not a replacement for a
platform installer. Tools that bootstrap clusters, ingress, observability, or
base platform services can run before Kapro; Kapro then bootstraps the
promotion control plane on top.

```bash
kapro init ./promotion-repo --backend argo --name checkout
kapro init ./promotion-repo --backend flux --name checkout --mode pull
kapro init ./promotion-repo --backend argo --name checkout --clusters none
```

## Brownfield Connection

For existing fleets, Kapro should avoid asking users to recreate objects that
already exist in Argo CD or Flux. Brownfield connect has three phases:

1. **Observe:** discover backend-native clusters and applications, report graph
   and health, and do not write to backend-owned objects.
2. **Adopt:** bind selected backend objects to Kapro releases and allow Kapro to
   update version fields such as Argo `targetRevision` or Flux input tags.
3. **Manage:** optionally let Kapro generate new bundles, pipelines, and backend
   wiring for teams that want a stronger convention.

Argo CD users can keep cluster Secrets, Applications, ApplicationSets, and
app-of-apps in their Git repo. Kapro adds promotion waves, gates, approvals,
evidence, and fleet-wide status around that existing topology.

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
- `POST /api/v1/releases`

The gateway is intentionally asynchronous. Commands create or patch CRDs; the
controllers and backend adapters perform reconciliation.

`GET /api/v1/graph` and `POST /api/v1/releases` require an
`Authorization: Bearer <token>` header. The operator reuses its configured
approval secret as the gateway bearer token. Request bodies are size-limited
and unknown JSON fields are rejected.
