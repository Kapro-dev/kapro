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

For clean-clone verification, use the repository helper:

```bash
scripts/verify-install.sh render
scripts/verify-install.sh cluster
```

See [Clean-Clone Install Verification](install-verification.md) for the full
fresh clone, Kind, image override, cleanup, and demo validation workflow.

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

## Kustomize Bundle

The repository also keeps a Kustomize bundle for simple local installs:

```bash
kubectl apply -k config/default
kubectl -n kapro-system rollout status deployment/kapro-operator
```

The Kustomize bundle disables admission webhooks and uses the published
operator image. Use Helm for configurable production installs.
