# Argo CD Actuator Plugin

This example implements the Kapro Actuator Interface for Argo CD Applications.
It runs as a gRPC server and lets Kapro drive Argo CD without adding Argo-specific
logic to the Kapro controller.

## Behavior

The plugin applies exactly one backend mutation:

```text
Application.spec.source.targetRevision = <requested version>
```

It reports convergence when the Application is synced and healthy:

```text
Application.status.sync.status == Synced
Application.status.health.status == Healthy
```

Rollback patches `spec.source.targetRevision` to the previous version supplied
by Kapro.

## Run

```bash
go run ./examples/plugins/argocd-actuator --listen :9090 --namespace argocd
```

The plugin uses in-cluster Kubernetes configuration by default. Outside a
cluster, pass `--kubeconfig` or set `KUBECONFIG`.

## Container

Build the external substrate image from the repository root:

```bash
docker build -f examples/plugins/argocd-actuator/Dockerfile \
  -t ghcr.io/kapro-dev/argocd-actuator:latest .
```

For local testing against a kubeconfig:

```bash
docker run --rm -p 9090:9090 \
  -v "$KUBECONFIG:/kubeconfig:ro" \
  ghcr.io/kapro-dev/argocd-actuator:latest \
  --listen :9090 \
  --kubeconfig /kubeconfig \
  --namespace argocd
```

## Parameters

`Plugin.spec.parameters` or request parameters may contain:

| Name | Purpose |
|---|---|
| `argocdNamespace` | Namespace containing the Argo CD Application. Defaults to `argocd`. |
| `application` | Application name. May be `namespace/name`. |
| `applicationName` | Alias for `application`. |
| `argocdApplication` | Alias for `application`. |
| `appKey` | Fallback application name supplied by Kapro for multi-app promotionruns. |

If no application parameter is set, the plugin uses the request target name.

## Registration

The deployable substrate manifest is
`examples/plugins/argocd-actuator/manifests/deployment.yaml`. It creates:

- a `kapro-system/argocd-actuator` ServiceAccount, Deployment, and Service;
- an `argocd/argocd-actuator` Role that can `get` and `patch` Argo CD
  `Application` objects;
- a RoleBinding from the Argo CD namespace to the Kapro plugin ServiceAccount.

Apply it after replacing the image with your published build:

```bash
kubectl apply -f examples/plugins/argocd-actuator/manifests/deployment.yaml
```

The standalone Kapro `Plugin` registration is
`examples/plugins/argocd-actuator-registration.yaml`:

```yaml
apiVersion: kapro.io/v1alpha2
kind: Plugin
metadata:
  name: argocd-actuator
spec:
  type: actuator
  name: argo/pull
  protocol: grpc
  endpoint: dns:///argocd-actuator.kapro-system.svc:9090
  timeout: 10s
  parameters:
    argocdNamespace: argocd
    application: checkout
```

Enable runtime plugin loading in the Kapro operator with:

```bash
KAPRO_ENABLE_PLUGIN_GATEWAY=true
```

The operator loads ready actuator registrations at startup.

The registration endpoint points at the Service from the deployment manifest:

```text
dns:///argocd-actuator.kapro-system.svc:9090
```

## Verify

```bash
go test ./examples/plugins/argocd-actuator
```

The test suite runs the shared KAI conformance harness and backend-specific
tests against a fake Kubernetes API.

You can also run the external conformance binary against a live plugin server:

```bash
go run ./cmd/kapro-conformance actuator \
  --endpoint localhost:9090 \
  --param argocdNamespace=argocd \
  --param application=checkout
```

For CI systems that need structured output:

```bash
go run ./cmd/kapro-conformance actuator \
  --endpoint localhost:9090 \
  --param argocdNamespace=argocd \
  --param application=checkout \
  -o json
```

The conformance run applies the default test version and then rolls the
Application back to the default previous version, so point it at an isolated
test Application.

## Public Surfaces

The plugin imports only public Kapro packages:

- `kapro.io/kapro/spec/kai/v1alpha1` for the KAI gRPC contract.
- `kapro.io/kapro/pkg/plugincompat` for the supported contract version.
- `kapro.io/kapro/conformance/actuator` from tests.

It does not import Kapro controller internals.
