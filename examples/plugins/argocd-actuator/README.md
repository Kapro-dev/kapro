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

## Parameters

`PluginRegistration.spec.parameters` or request parameters may contain:

| Name | Purpose |
|---|---|
| `argocdNamespace` | Namespace containing the Argo CD Application. Defaults to `argocd`. |
| `application` | Application name. May be `namespace/name`. |
| `applicationName` | Alias for `application`. |
| `argocdApplication` | Alias for `application`. |
| `appKey` | Fallback application name supplied by Kapro for multi-app releases. |

If no application parameter is set, the plugin uses the request target name.

## Registration

```yaml
apiVersion: kapro.io/v1alpha1
kind: PluginRegistration
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

## Verify

```bash
go test ./examples/plugins/argocd-actuator
```

The test suite runs the shared KAI conformance harness and backend-specific
tests against a fake Kubernetes API.
