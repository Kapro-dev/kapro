# Install Kapro

The recommended install path is the Helm chart in `charts/kapro-operator`.
It installs the CRDs, controller Deployment, ServiceAccount, RBAC, admission
webhooks, and baseline approval service together.

## Prerequisites

- Kubernetes cluster access with permission to create CRDs and cluster-scoped RBAC.
- Helm 3.
- cert-manager when `webhook.enabled=true` (the default).

For local clusters without cert-manager, set `webhook.enabled=false`.

## Install

```bash
helm upgrade --install kapro charts/kapro-operator \
  --namespace kapro-system \
  --create-namespace
```

Local install without admission webhooks:

```bash
helm upgrade --install kapro charts/kapro-operator \
  --namespace kapro-system \
  --create-namespace \
  --set webhook.enabled=false
```

Useful baseline settings:

```bash
helm upgrade --install kapro charts/kapro-operator \
  --namespace kapro-system \
  --create-namespace \
  --set externalURL=https://kapro.example.com \
  --set hubAPIURL=https://hub.example.com:6443
```

`externalURL` is used in approval links and optional Decision API callbacks.
`hubAPIURL` should be the hub API server URL reachable from spoke clusters.

## Core and Preview Surfaces

The default install runs the core runtime controllers for promotion orchestration,
target execution, backend profiles, approvals, triggers, plugins, and cluster
heartbeat. The core APIs operators should rely on for fleet promotion are:
`PromotionRun`, `PromotionTarget`, `PromotionPlan`, `FleetCluster`,
`BackendProfile`, `PromotionSource`, and `Approval`.

Preview surfaces are available for early adopters but should be enabled or
exposed deliberately:

| Surface | Default | Enablement |
|---|---|---|
| Decision API and `AgentPolicy` | Disabled | `decisionAPI.enabled=true` and explicit Kubernetes RBAC. |
| Plugin gateway runtime dispatch | Disabled | `pluginGateway.enabled=true` plus installed plugin services and `PluginRegistration` objects. |
| Hub Gateway service exposure | Internal only | `hubGateway.service.enabled=true`; place Kubernetes authn/authz or an identity-aware proxy in front of production exposure. |
| Fleet auto-import providers beyond GCP | Stubbed | Use `FleetClusterTemplate` only for implemented sources; unsupported sources report `SourceNotImplemented`. |
| Inline gate notifications | Runtime | Notification routing is configured inside gate/stage policy; there is no separate public notification provider/policy CRD. |

## Optional Decision API

The approval HTTP server is installed for signed human approval links. The
machine-facing Decision API under `/api/v1` is disabled by default.

Enable it only after granting Kubernetes RBAC to the ServiceAccounts that should
read promotion context or submit decisions:

```bash
helm upgrade --install kapro charts/kapro-operator \
  --namespace kapro-system \
  --create-namespace \
  --set decisionAPI.enabled=true
```

Every Decision API request must include a Kubernetes bearer token. The operator
validates the token with `TokenReview` and checks each requested action with
`SubjectAccessReview` before reading fleet state, writing
`PromotionTarget.status`, or creating `Approval` objects.

Read endpoints are bounded. `GET /api/v1/fleet` and
`GET /api/v1/promotionruns/{name}/context` accept `limit`, `labelSelector`, and
`phase` query parameters and return `page.truncated=true` when more matching
objects exist than were returned or when sparse filters exhaust the server scan
budget.

Example approver RBAC:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: kapro-decision-approver
rules:
  - apiGroups: ["kapro.io"]
    resources: ["promotionruns", "promotiontargets"]
    verbs: ["get"]
  - apiGroups: ["kapro.io"]
    resources: ["promotiontargets/status"]
    verbs: ["update", "patch"]
  - apiGroups: ["kapro.io"]
    resources: ["approvals"]
    verbs: ["create"]
```

Bind this role only to the agent or human-facing ServiceAccount that is allowed
to decide or override promotions.

## Verify

```bash
kubectl -n kapro-system rollout status deployment/kapro-kapro-operator
kubectl get crd | grep kapro.io
kubectl -n kapro-system get deploy,svc,sa
kubectl auth can-i get promotionruns.kapro.io \
  --as=system:serviceaccount:kapro-system:kapro-kapro-operator
```

For clean-clone or release-candidate verification, use the repository helper:

```bash
scripts/verify-install.sh render
scripts/verify-install.sh cluster
```

`render` checks Helm CRD drift, `helm lint`, `helm template --include-crds`,
and `kubectl kustomize config/default`. `cluster` installs the chart into the
current cluster and verifies the operator Deployment, CRDs, ServiceAccount, and
basic PromotionRun read access.

For a fresh clone release check:

```bash
tmpdir="$(mktemp -d)"
git clone https://github.com/Kapro-dev/kapro "${tmpdir}/kapro"
cd "${tmpdir}/kapro"
git checkout v0.1.0
scripts/verify-install.sh render
```

For a disposable Kind install check:

```bash
kind create cluster --name kapro-install-verify
kubectl config use-context kind-kapro-install-verify
scripts/verify-install.sh cluster
kind delete cluster --name kapro-install-verify
```

For a release image that is not the chart default:

```bash
KAPRO_IMAGE_REPOSITORY=ghcr.io/kapro-dev/kapro-operator \
KAPRO_IMAGE_TAG=v0.1.0 \
scripts/verify-install.sh cluster
```

Heavier validation targets are available when you need backend coverage:

```bash
scripts/verify-install.sh kind-demo
scripts/verify-install.sh argo-e2e
scripts/verify-install.sh flux-git-e2e
scripts/verify-install.sh flux-e2e
```

Render checks that do not require a cluster:

```bash
helm lint charts/kapro-operator
helm template kapro charts/kapro-operator --namespace kapro-system --include-crds
kubectl kustomize config/default
```

## Upgrade

Apply CRD changes first, then upgrade the chart:

```bash
kubectl apply -f charts/kapro-operator/crds
helm upgrade kapro charts/kapro-operator --namespace kapro-system
kubectl -n kapro-system rollout status deployment/kapro-kapro-operator
```

## Registering fleet clusters (pull mode)

Kapro v0.5 supports a pull-mode spoke agent (`kapro-cluster-controller`) that
runs inside each workload cluster and reports back to the hub. To register a
new spoke see [cluster-bootstrap.md](cluster-bootstrap.md). The existing push-
mode flow (`kapro spoke add`) is unchanged.

## Uninstall

```bash
helm uninstall kapro --namespace kapro-system
```

Helm does not delete CRDs on uninstall. After backing up or deleting Kapro
custom resources, remove CRDs explicitly:

```bash
kubectl delete -f charts/kapro-operator/crds
kubectl delete namespace kapro-system
```

## Optional Plugin Gateway

The plugin gateway is an opt-in runtime preview. Enabling it only sets
`KAPRO_ENABLE_PLUGIN_GATEWAY=true`; it does not install any plugin service or
demo registration.

```bash
helm upgrade --install kapro charts/kapro-operator \
  --namespace kapro-system \
  --create-namespace \
  --set pluginGateway.enabled=true
```

Install your plugin service, then apply a registration such as:

```bash
kubectl apply -f examples/plugins/slo-gate-registration.yaml
```

The operator probes `PluginRegistration` objects continuously. Ready
registrations with a fresh `status.observedGeneration` are hot-loaded into the
actuator, gate, and planner registries; stale, incompatible, or deleted
registrations are unloaded without restarting the operator.

```bash
kubectl get pluginregistrations.kapro.io
```

## Optional Hub Gateway

The Hub Gateway is a lightweight facade for UI and CLI clients. It reads and
creates Kapro CRDs; it does not mutate delivery backends directly. The graph
endpoint is bounded and supports `resource`, `labelSelector`, `phase`, and
`limit` query parameters.

The built-in bearer token is suitable for local development and port-forwarded
use only. Production exposure should terminate identity through Kubernetes
authentication and authorization or an identity-aware reverse proxy before
traffic reaches the gateway.

```bash
helm upgrade --install kapro charts/kapro-operator \
  --namespace kapro-system \
  --create-namespace \
  --set hubGateway.service.enabled=true
```

## Kustomize Bundle

The repository also keeps a Kustomize bundle for simple local installs:

```bash
kubectl apply -k config/default
kubectl -n kapro-system rollout status deployment/kapro-operator
```

The Kustomize bundle disables admission webhooks and uses the published
operator image. Use Helm for configurable production installs.
