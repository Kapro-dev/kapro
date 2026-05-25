# Argo CD ApplicationSet Actuator Plugin

This example implements the Kapro Actuator Interface for Argo CD
ApplicationSets. It is the `argo/push` counterpart to the Application-based
`argo/pull` example.

## Behavior

The plugin applies exactly one substrate mutation:

```text
ApplicationSet.spec.template.spec.source.targetRevision = <requested version>
```

It reports convergence from a generated Argo CD Application:

```text
Application.spec.source.targetRevision == <requested version>
Application.status.sync.status == Synced
Application.status.health.status == Healthy
```

Rollback patches the ApplicationSet template back to the previous version
supplied by Kapro.

## Run

```bash
go run ./examples/05-plugins/01-argocd-applicationset-actuator --listen :9090 --namespace argocd
```

The plugin uses in-cluster Kubernetes configuration by default. Outside a
cluster, pass `--kubeconfig` or use the default local kubeconfig loading rules.

## Parameters

`Plugin.spec.parameters` or request parameters may contain:

| Name | Purpose |
|---|---|
| `argocdNamespace` | Namespace containing the ApplicationSet and generated Application. Defaults to `argocd`. |
| `applicationset` | ApplicationSet name. May be `namespace/name`. |
| `applicationSet` | Alias for `applicationset`. |
| `applicationSetName` | Alias for `applicationset`. |
| `generatedApplication` | Generated Application name used for convergence checks. May be `namespace/name`. |
| `application` | Alias for `generatedApplication`. |
| `appKey` | Fallback name supplied by Kapro for multi-app promotionruns. |

If no ApplicationSet or generated Application parameter is set, the plugin uses
the request target name.

## Registration

The standalone manifest is
`examples/05-plugins/01-argocd-applicationset-actuator/registration.yaml`.

```yaml
apiVersion: kapro.io/v1alpha1
kind: Plugin
metadata:
  name: argocd-applicationset-actuator
spec:
  type: actuator
  name: argo/push
  protocol: grpc
  endpoint: dns:///argocd-applicationset-actuator.kapro-system.svc:9090
  timeout: 10s
  parameters:
    argocdNamespace: argocd
    applicationset: checkout-fleet
    generatedApplication: checkout-prod-eu
```

Enable runtime plugin loading in the Kapro operator with:

```bash
KAPRO_ENABLE_PLUGIN_GATEWAY=true
```

The operator loads ready actuator registrations at startup.

## Verify

```bash
go test ./examples/05-plugins/01-argocd-applicationset-actuator
```

The test suite runs the shared KAI conformance harness and substrate-specific
tests against a fake Kubernetes API.
