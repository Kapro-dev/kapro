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
