# Argo CD Actuator Manifests

Deploys the Argo CD actuator service.

```text
Deployment + ServiceAccount + RBAC + Service
```

Apply the deployment first, then apply `../registration.yaml` after the service
is reachable.

```bash
kubectl apply -f examples/05-plugins/00-argocd-actuator/manifests/deployment.yaml
kubectl apply -f examples/05-plugins/00-argocd-actuator/registration.yaml
```

## Run This Example

Every example has a local runner. Start with the safe check command; this is also the path exercised by CI through `make check-examples`:

```bash
examples/05-plugins/00-argocd-actuator/manifests/run.sh
```

After you have a Kubernetes cluster and the required controllers installed, apply the manifests through the wrapper:

```bash
examples/05-plugins/00-argocd-actuator/manifests/run.sh apply
```

## Expected Result

- `check` validates the README, shell syntax, YAML/JSON shape, and stale Kapro API names.
- `apply` runs `kubectl apply -f` for this directory.
- Kubernetes should accept the manifests once the matching CRDs/controllers are installed.

## Cleanup

```bash
kubectl delete -f examples/05-plugins/00-argocd-actuator/manifests --ignore-not-found
```
