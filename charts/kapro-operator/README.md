# kapro-operator Helm chart

This chart installs the Kapro CRDs, controller Deployment, ServiceAccount, RBAC,
admission webhook configuration, and baseline services in one promotionrun.

## Install

```bash
helm upgrade --install kapro charts/kapro-operator \
  --namespace kapro-system \
  --create-namespace
```

The default webhook configuration uses cert-manager to create and inject a
self-signed serving certificate. If cert-manager is not installed, either
install it first or disable admission webhooks for local testing:

```bash
helm upgrade --install kapro charts/kapro-operator \
  --namespace kapro-system \
  --create-namespace \
  --set webhook.enabled=false
```

## Upgrade

```bash
helm upgrade kapro charts/kapro-operator \
  --namespace kapro-system
```

CRDs in `crds/` are installed on first install. For CRD upgrades, apply them
explicitly before upgrading the promotionrun:

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
helm upgrade --install kapro charts/kapro-operator \
  --namespace kapro-system \
  --create-namespace \
  --set pluginGateway.enabled=true
```

Then install your plugin service and apply a registration, for example:

```bash
kubectl apply -f examples/plugins/slo-gate-registration.yaml
```
