# kapro-operator Helm chart

This chart installs the Kapro CRDs, controller Deployment, ServiceAccount, RBAC,
admission webhook configuration, and baseline services in one Helm release.

## Install

From this source checkout:

```bash
helm upgrade --install kapro charts/kapro-operator \
  --namespace kapro-system \
  --create-namespace
```

For public-preview installs, prefer the packaged chart attached to the GitHub
Release:

```bash
helm upgrade --install kapro \
  https://github.com/Kapro-dev/kapro/releases/download/v0.6.0/kapro-operator-0.6.0.tgz \
  --namespace kapro-system \
  --create-namespace
```

The default webhook configuration generates a self-signed serving certificate
without cert-manager. If you already run cert-manager and prefer its certificate
lifecycle, set `webhook.certManager.enabled=true`.

For clusters with NetworkPolicy enforcement, opt in to the chart-managed
operator policy:

```bash
helm upgrade --install kapro \
  https://github.com/Kapro-dev/kapro/releases/download/v0.6.0/kapro-operator-0.6.0.tgz \
  --namespace kapro-system \
  --create-namespace \
  --set networkPolicy.enabled=true
```

The policy admits only the operator's known health, metrics, webhook, approval,
and hub-gateway ports. Use `networkPolicy.ingress.from` and
`networkPolicy.egress.*` values for stricter production namespaces.

## Upgrade

The `v0.6.x` chart is a clean pre-stable break: user-authored CRDs are served
from `kapro.io/v1alpha1` and controller-owned runtime CRDs are served from
`runtime.kapro.io/v1alpha1`. The chart does not perform automatic conversion
from older alpha manifests.

```bash
helm upgrade kapro charts/kapro-operator \
  --namespace kapro-system
```

CRDs in `crds/` are installed on first install. For CRD upgrades, apply them
explicitly before upgrading the Helm release:

```bash
kubectl apply -f charts/kapro-operator/crds
helm upgrade kapro charts/kapro-operator --namespace kapro-system
```

## Uninstall

```bash
helm uninstall kapro --namespace kapro-system
kubectl delete -f charts/kapro-operator/crds
```

Helm intentionally leaves CRDs behind on uninstall. Delete CRDs only after
backing up or removing Kapro custom resources.

## Verify Locally

```bash
helm lint charts/kapro-operator
helm template kapro charts/kapro-operator --namespace kapro-system --include-crds
kubectl kustomize config/default
go test ./...
```

The repository install verifier wraps the chart render checks and CRD sync
check:

```bash
scripts/verify-install.sh render
```

## Plugin Gateway Preview

The runtime plugin gateway is disabled by default and this chart does not
install demo plugins. To opt in:

```bash
helm upgrade --install kapro \
  https://github.com/Kapro-dev/kapro/releases/download/v0.6.0/kapro-operator-0.6.0.tgz \
  --namespace kapro-system \
  --create-namespace \
  --set pluginGateway.enabled=true \
  --set controllers='{fleet,plan,promotion,promotionrun,cluster,substrateclass,substrate,plugin}'
```

Then install your plugin service and apply a registration, for example:

```bash
kubectl apply -f examples/plugins/slo-gate-registration.yaml
```

## Preview Features

The default install runs the ADR-0010 core controllers plus the 0.6 substrate
readiness controllers: `fleet`, `plan`, `promotion`, `promotionrun`, `cluster`,
`substrateclass`, and `substrate`. The `target` controller starts implicitly
whenever `promotionrun` is enabled.

Preview surfaces are explicit opt-ins or spec-only APIs:

| Surface | Default | Opt-in |
|---|---|---|
| Decision API and `Policy` | Disabled | `--set decisionAPI.enabled=true` plus Kubernetes RBAC |
| SubstrateClass status controller | Enabled | Keep `substrateclass` in `controllers` for generated `Substrate.spec.classRef` profiles |
| Substrate readiness controller | Enabled | Keep `substrate` in `controllers` so generated `Cluster` objects can reference Ready substrates |
| SubstrateDiscoveryPolicy discovery controller | Disabled | Add `substratediscoverypolicy` to `controllers` for live `kapro adopt --apply` discovery |
| Approval controller | Disabled | Add `approval` to `controllers` |
| Trigger controller | Disabled | Add `trigger` to `controllers` |
| Plugin controller | Disabled | Add `plugin` to `controllers` |
| Runtime plugin gateway | Disabled | `--set pluginGateway.enabled=true` plus `plugin` in `controllers` |
| Hub Gateway Service | Internal listener only | `--set hubGateway.service.enabled=true` and place Kubernetes authn/authz or an identity proxy in front |
| Spoke CSR bootstrap controller | Disabled | Add `cluster-bootstrap` to `controllers` and set `hubAPIURL` |
| ClusterTemplate controller | Disabled | Add `clustertemplate` to `controllers` |
| Inline gate notifications | Runtime | No separate public notification provider/policy CRDs |
