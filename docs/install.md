# Install Kapro

The recommended public-preview install path is the Helm chart package attached
to the GitHub Release. It installs the CRDs, controller Deployment,
ServiceAccount, RBAC, admission webhooks, and baseline approval service
together. The `0.1.2` chart defaults to
`ghcr.io/kapro-dev/kapro-operator:v0.1.2`.

## Prerequisites

- Kubernetes cluster access with permission to create CRDs and cluster-scoped RBAC.
- Helm 3.

The chart has no other dependencies. By default the admission webhook
uses an auto-generated self-signed serving certificate, so cert-manager
is not required to install. Production clusters that already run
cert-manager can opt back in (see below).

## Install

```bash
KAPRO_VERSION=0.1.2
KAPRO_CHART="https://github.com/Kapro-dev/kapro/releases/download/v${KAPRO_VERSION}/kapro-operator-${KAPRO_VERSION}.tgz"

helm upgrade --install kapro \
  "${KAPRO_CHART}" \
  --namespace kapro-system \
  --create-namespace
```

That is the full install for kind, k3d, EKS without the cert-manager
add-on, and any other cluster where you do not have cert-manager
installed.

For development from a local checkout, set
`KAPRO_CHART=charts/kapro-operator` instead of using the release URL.
The remaining Helm examples use the same `KAPRO_CHART` value.

### Install with cert-manager

If you already run cert-manager (≥ 1.5) and prefer its certificate
lifecycle, enable the cert-manager path:

```bash
helm upgrade --install kapro \
  "${KAPRO_CHART}" \
  --namespace kapro-system \
  --create-namespace \
  --set webhook.certManager.enabled=true
```

The chart then emits a cert-manager Issuer + Certificate and
annotates the webhook configs for caBundle injection. No
chart-managed Secret is created in that mode.

### Install without admission webhooks

If you want the smallest possible install (no webhook = no admission
validation of CRD invariants), turn the webhook off entirely:

```bash
helm upgrade --install kapro \
  "${KAPRO_CHART}" \
  --namespace kapro-system \
  --create-namespace \
  --set webhook.enabled=false
```

Not recommended for production — the webhook is where multi-tenancy
ownership labels, controller-only writes to `PromotionRun`, and
Plan DAG validity is enforced.

Useful baseline settings:

```bash
helm upgrade --install kapro \
  "${KAPRO_CHART}" \
  --namespace kapro-system \
  --create-namespace \
  --set externalURL=https://kapro.example.com \
  --set hubAPIURL=https://hub.example.com:6443
```

`externalURL` is used in approval links and optional Decision API callbacks.
`hubAPIURL` should be the hub API server URL reachable from spoke clusters.

## Core and Preview Surfaces

The default install runs the ADR-0010 core controllers: `fleet`, `plan`,
`promotion`, `promotionrun`, and `cluster`. The `target` controller is an
implicit dependency of `promotionrun` and starts with it. Users normally author
`Fleet`, `Source`, and `Promotion`; controllers generate or update `Cluster`,
`Plan`, `PromotionRun`, and `Target` records.

Preview surfaces are available for early adopters but should be enabled or
exposed deliberately:

| Surface | Default | Enablement |
|---|---|---|
| Decision API and `Policy` | Disabled | `decisionAPI.enabled=true` and explicit Kubernetes RBAC. |
| Backend readiness controller | Disabled | Built-in `flux`, `argo`, and `oci` Backend specs can be referenced without this controller. Add `backend` to `controllers` when external backend readiness or backend-native discovery status is needed. |
| Approval controller | Disabled | Add `approval` to `controllers` when human approval objects should unblock gates. |
| Trigger controller | Disabled | Add `trigger` to `controllers` for autonomous artifact-driven promotions. |
| Plugin controller | Disabled | Add `plugin` to `controllers` so `Plugin.status` readiness is reconciled. |
| Plugin gateway runtime dispatch | Disabled | `pluginGateway.enabled=true` plus `plugin` in `controllers`, installed plugin services, and `Plugin` objects. |
| Hub Gateway service exposure | Internal only | `hubGateway.service.enabled=true`; place Kubernetes authn/authz or an identity-aware proxy in front of production exposure. |
| Spoke CSR bootstrap controller | Disabled | Add `cluster-bootstrap` to `controllers` and set `hubAPIURL` to the hub API server URL reachable from spokes. |
| Fleet auto-import providers beyond GCP | Stubbed | Use `ClusterTemplate` only for implemented sources; unsupported sources report `SourceNotImplemented`. |
| Inline gate notifications | Runtime | Notification routing is configured inside gate/stage policy; there is no separate public notification provider/policy CRD. |

See [Preview Controllers](preview-controllers.md) for the full controller key
map and compatibility aliases.

## Quickstart Paths

Choose the smallest backend path that matches the delivery system you already
run:

| Path | Use when | Example |
|---|---|---|
| Flux | Spokes already reconcile with Flux or Flux Operator. | [First Promotion](first-promotion-10min.md) |
| Argo CD | Argo CD owns one Application per target cluster. | [Argo CD Quickstart](quickstart-argo.md) |
| OCI | Spokes should pull OCI artifacts without Flux or Argo CD. | [OCI Quickstart](quickstart-oci.md) |

## Optional Decision API

The approval HTTP server is installed for signed human approval links. The
machine-facing Decision API under `/api/v1` is disabled by default.

Enable it only after granting Kubernetes RBAC to the ServiceAccounts that should
read promotion context or submit decisions:

```bash
helm upgrade --install kapro \
  "${KAPRO_CHART}" \
  --namespace kapro-system \
  --create-namespace \
  --set decisionAPI.enabled=true
```

Every Decision API request must include a Kubernetes bearer token. The operator
validates the token with `TokenReview` and checks each requested action with
`SubjectAccessReview` before reading fleet state, writing
`Target.status`, or creating `Approval` objects.

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
    resources: ["promotionruns", "targets"]
    verbs: ["get"]
  - apiGroups: ["kapro.io"]
    resources: ["targets/status"]
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
git checkout v0.1.2
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
KAPRO_IMAGE_TAG=v0.1.2 \
scripts/verify-install.sh cluster
```

For a render-only check against the published chart artifact:

```bash
scripts/verify-install.sh release-render
```

For a disposable Kind install check against the published chart artifact:

```bash
kind create cluster --name kapro-release-verify
kubectl config use-context kind-kapro-release-verify
KAPRO_VERIFY_CLEANUP=true scripts/verify-install.sh release-cluster
kind delete cluster --name kapro-release-verify
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

!!! warning "Upgrading from v1alpha1"
    The v1alpha1 to v1alpha2 move is a clean break. Follow the
    [v1alpha1 to v1alpha2 migration guide](migration-v1alpha1-to-v1alpha2.md)
    before applying v1alpha2 CRDs; the chart does not serve v1alpha1 or run
    automatic conversion.

Apply CRD changes first, then upgrade the chart. For the release package,
pull and unpack the chart before applying CRDs:

```bash
tmpdir="$(mktemp -d)"
helm pull "${KAPRO_CHART}" --untar --untardir "${tmpdir}"
kubectl apply -f "${tmpdir}/kapro-operator/crds"
helm upgrade kapro "${KAPRO_CHART}" --namespace kapro-system
kubectl -n kapro-system rollout status deployment/kapro-kapro-operator
```

From a source checkout, `kubectl apply -f charts/kapro-operator/crds` is the
equivalent CRD apply path.

## Registering clusters (pull mode)

Kapro supports a pull-mode spoke agent (`kapro-cluster-controller`) that
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
demo `Plugin`.

```bash
helm upgrade --install kapro \
  "${KAPRO_CHART}" \
  --namespace kapro-system \
  --create-namespace \
  --set pluginGateway.enabled=true \
  --set controllers='{fleet,plan,promotion,promotionrun,cluster,plugin}'
```

Install your plugin service, then apply a `Plugin` manifest such as:

```bash
kubectl apply -f examples/plugins/slo-gate-registration.yaml
```

The `plugin` controller probes `Plugin` objects continuously. Ready plugins
with a fresh `status.observedGeneration` are hot-loaded into the actuator, gate,
and planner registries when the gateway is enabled; stale, incompatible, or
deleted plugins are unloaded without restarting the operator.

```bash
kubectl get plugins.kapro.io
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
helm upgrade --install kapro \
  "${KAPRO_CHART}" \
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
