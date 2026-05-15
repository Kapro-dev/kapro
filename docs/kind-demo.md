# Local Kind Demo

This demo runs Kapro in a local Kind cluster and walks the intended control-plane flow:

1. install Kapro CRDs and the Kapro operator
2. apply a small fleet config with `MemberCluster`, `Pipeline`, `PluginRegistration`, and `ReleaseTrigger`
3. create a `Release`
4. watch the planner bind targets, gates advance, approvals unblock production, and the push/Flux actuator patch a `ResourceSet`
5. inspect rollout status through `Release` and `ReleaseTarget`

The path is intentionally local-only. It does not require production OCI credentials, a real Flux Operator install, real Helm charts, or cosign signatures.

## Quick Start

Prerequisites:

- Docker
- Kind
- kubectl
- Go and `make`

Run:

```bash
scripts/kind-demo.sh up
```

The script creates a `kapro-kind-demo` cluster, builds `kapro-operator:dev`, loads it into Kind, installs the CRDs, deploys the operator, applies local Flux fixture CRDs/resources, and starts the `checkout-kind` release.

Approve production:

```bash
scripts/kind-demo.sh approve
scripts/kind-demo.sh status
```

Clean up:

```bash
scripts/kind-demo.sh down
```

## What The Demo Shows

`examples/kind-demo/config/01-memberclusters.yaml` defines three local fleet entries:

- `checkout-canary`
- `checkout-prod-eu`
- `checkout-prod-us`

Each target uses `spec.delivery.mode: push` with `backendRef: flux`, pointed at
the local fixture `ResourceSet` named `checkout-demo` through
`spec.delivery.parameters.resourceSet`.

`examples/kind-demo/config/02-pipeline.yaml` defines two stages:

- `canary`: selects `kapro.io/tier=canary` and uses a short built-in soak gate.
- `prod`: selects `kapro.io/tier=production`, depends on canary, uses `maxParallel: 1`, and requires manual approval by `demo-sre`.

The default planner orders eligible targets deterministically and records deferrals from the stage strategy in `Release.status.pipelineProgress[].stageProgress[].plannerResults`.

`examples/kind-demo/config/03-release-trigger.yaml` creates a safe, suspended, dry-run `ReleaseTrigger`. It documents the trigger-to-release API path without calling a real registry.

`examples/kind-demo/config/04-release.yaml` creates the live release that drives the rollout.

## Observe

```bash
kubectl --context kind-kapro-kind-demo get releases,releasetargets,memberclusters
kubectl --context kind-kapro-kind-demo get release checkout-kind -o yaml
kubectl --context kind-kapro-kind-demo get releasetargets -o yaml
kubectl --context kind-kapro-kind-demo -n flux-system get resourceset checkout-demo -o yaml
kubectl --context kind-kapro-kind-demo get releasetrigger checkout-kind-trigger -o yaml
kubectl --context kind-kapro-kind-demo get pluginregistrations
```

Before approval, production targets should pause in `WaitingApproval`. After `scripts/kind-demo.sh approve`, the approvals in `examples/kind-demo/approvals/` allow production to advance.

## Limitations

- The `ResourceSet` and `HelmRelease` resources are local fixtures. Their readiness is patched by `scripts/kind-demo.sh`; no Flux controller reconciles workloads.
- `PluginRegistration` objects point at static demo endpoints. The plugin readiness controller is expected to mark them not ready unless you run matching gRPC plugin servers. The built-in planner, gates, and Flux backend adapter drive the rollout.
- The `ReleaseTrigger` is suspended and dry-run because the demo does not start a local OCI registry or signature verifier.
- Webhooks are disabled in the demo operator overlay to keep local setup self-contained.

## Troubleshooting

Check the operator:

```bash
kubectl --context kind-kapro-kind-demo -n kapro-system logs deployment/kapro-operator
kubectl --context kind-kapro-kind-demo -n kapro-system describe pod -l app=kapro-operator
```

Re-apply fixture status if the rollout is waiting for convergence:

```bash
scripts/kind-demo.sh fixtures
```

If a previous run left stale state, delete the cluster and start again:

```bash
scripts/kind-demo.sh down
scripts/kind-demo.sh up
```
