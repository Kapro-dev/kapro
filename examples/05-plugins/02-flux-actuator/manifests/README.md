# Flux Actuator Manifests

Deploys the Flux actuator service.

```text
Deployment + ServiceAccount + RBAC + Service
```

Apply the deployment first, then apply `../registration.yaml` after the service
is reachable.

```bash
kubectl apply -f examples/05-plugins/02-flux-actuator/manifests/deployment.yaml
kubectl apply -f examples/05-plugins/02-flux-actuator/registration.yaml
```
