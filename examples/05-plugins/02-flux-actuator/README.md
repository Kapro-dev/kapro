# Flux Actuator Plugin

This example implements the Kapro Actuator Interface for Flux HelmRelease
objects. It runs as a gRPC server and lets Kapro drive Flux without adding
Flux-specific code to the Kapro operator.

## Behavior

The plugin applies exactly one substrate mutation:

```text
HelmRelease.spec.chart.spec.version = <requested version>
```

It reports convergence when the HelmRelease has the requested chart version and
its `Ready` condition is `True`. Rollback patches the same field to the
previous version supplied by Kapro.

## Run

```bash
go run ./examples/05-plugins/02-flux-actuator --listen :9090 --namespace flux-system
```

The plugin uses in-cluster Kubernetes configuration by default. Outside a
cluster, pass `--kubeconfig` or set `KUBECONFIG`.

## Container

Build the external substrate image from the repository root:

```bash
docker build -f examples/05-plugins/02-flux-actuator/Dockerfile \
  -t ghcr.io/kapro-dev/flux-actuator:v0.3.7 .
```

## Parameters

`Plugin.spec.parameters` or request parameters may contain:

| Name | Purpose |
|---|---|
| `fluxNamespace` | Namespace containing the Flux HelmRelease. Defaults to `flux-system`. |
| `helmRelease` | HelmRelease name. May be `namespace/name`. |
| `helmReleaseName` | Alias for `helmRelease`. |
| `fluxHelmRelease` | Alias for `helmRelease`. |
| `appKey` | Fallback HelmRelease name supplied by Kapro for multi-app promotion runs. |

If no HelmRelease parameter is set, the plugin uses the request target name.

## Registration

The deployable substrate manifest is
`examples/05-plugins/02-flux-actuator/manifests/deployment.yaml`. It creates:

- a `kapro-system/flux-actuator` ServiceAccount, Deployment, and Service;
- a `flux-system/flux-actuator` Role that can `get` and `patch` the `checkout`
  Flux `HelmRelease`;
- a RoleBinding from the Flux namespace to the Kapro plugin ServiceAccount.

Apply it after replacing the image with your published build and adjusting
`rules[].resourceNames` if your HelmRelease is not named `checkout`:

```bash
kubectl apply -f examples/05-plugins/02-flux-actuator/manifests/deployment.yaml
```

The standalone Kapro `Plugin` registration is
`examples/05-plugins/02-flux-actuator/registration.yaml`.

Enable runtime plugin loading in the Kapro operator with:

```bash
KAPRO_ENABLE_PLUGIN_GATEWAY=true
```

## Verify

```bash
go test ./examples/05-plugins/02-flux-actuator
```

The test suite runs the shared KAI conformance harness and substrate-specific
tests against a fake Kubernetes API.

You can also run the external conformance binary against a live plugin server:

```bash
go run ./cmd/kapro-conformance actuator \
  --endpoint localhost:9090 \
  --param fluxNamespace=flux-system \
  --param helmRelease=checkout \
  -o json
```

The conformance run applies the default test version and then rolls the
HelmRelease back to the default previous version, so point it at an isolated
test HelmRelease.

## Public Surfaces

The plugin imports only public Kapro packages:

- `kapro.io/kapro/spec/kai/v1alpha1` for the KAI gRPC contract.
- `kapro.io/kapro/pkg/plugincompat` for the supported contract version.
- `kapro.io/kapro/conformance/actuator` from tests.

It does not import Kapro controller internals.

## Run This Example

Every example has a local runner. Start with the safe check command; this is also the path exercised by CI through `make check-examples`:

```bash
examples/05-plugins/02-flux-actuator/run.sh
```

Run the Go package directly through the same wrapper:

```bash
examples/05-plugins/02-flux-actuator/run.sh test
examples/05-plugins/02-flux-actuator/run.sh run
```

## Expected Result

- `check` and `test` compile the package and run its tests without requiring a Kubernetes cluster.
- `run` starts the example program or prints the SDK object it builds.

## Cleanup

```bash
kubectl delete -f examples/05-plugins/02-flux-actuator --ignore-not-found
```
