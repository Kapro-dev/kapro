# Kapro Kind Demo

This demo runs Kapro in a local Kind cluster and exercises the runtime
control-plane flow:

1. install Kapro CRDs and the Kapro operator;
2. apply a small fleet config with `Cluster`, `Plan`,
   `Plugin`, and `Trigger`;
3. create a compatibility `PromotionRun` fixture for the local runtime demo;
4. watch the planner bind targets, gates advance, approvals unblock
   production, and the push/Flux actuator patch a `ResourceSet`;
5. inspect rollout status through `PromotionRun` and `Target`.

The path is local-only. It does not require production OCI credentials, a real
Flux Operator install, real Helm charts, or cosign signatures.

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

The script creates a `kapro-kind-demo` cluster, builds `kapro-operator:dev`,
loads it into Kind, installs the CRDs, deploys the operator, applies local Flux
fixture CRDs/resources, and starts the `checkout-kind` compatibility
PromotionRun.

Approve production:

```bash
scripts/kind-demo.sh approve
scripts/kind-demo.sh status
```

Clean up:

```bash
scripts/kind-demo.sh down
```

## Files

The demo manifests are split by role:

- `operator/`: demo kustomize overlay for the local operator.
- `crds/`: fixture-only CRDs not shipped by Kapro.
- `fixtures/`: fake Flux resources used by the local actuator path.
- `config/`: Kapro API objects for the local runtime fixture.
- `approvals/`: manual approvals that unblock production.

## What The Demo Shows

`config/01-clusters.yaml` defines three local fleet entries:

- `checkout-canary`
- `checkout-prod-eu`
- `checkout-prod-us`

Each target uses `spec.delivery.mode: push` with `ref: flux`, pointed at
the local fixture `ResourceSet` named `checkout-demo` through
`spec.delivery.parameters.resourceSet`.

`config/02-plan.yaml` defines two stages:

- `canary`: selects `kapro.io/tier=canary` and uses a short built-in soak gate.
- `prod`: selects `kapro.io/tier=production`, depends on canary, uses
  `maxParallel: 1`, and requires manual approval by `demo-sre`.

`config/03-promotion-trigger.yaml` creates a safe, suspended, dry-run
`Trigger`. It documents the trigger-to-Promotion API path without
calling a real registry.

`config/04-promotionrun.yaml` creates the live compatibility PromotionRun
fixture that drives this local rollout. The public user path is to author
`Promotion` and let the controller stamp PromotionRun attempts; this fixture
keeps the demo deterministic because its approval names are precomputed.

## Observe

```bash
kubectl --context kind-kapro-kind-demo get promotionruns.runtime.kapro.io,targets.runtime.kapro.io,clusters.kapro.io
kubectl --context kind-kapro-kind-demo get promotionrun checkout-kind -o yaml
kubectl --context kind-kapro-kind-demo get targets -o yaml
kubectl --context kind-kapro-kind-demo -n flux-system get resourceset checkout-demo -o yaml
kubectl --context kind-kapro-kind-demo get trigger checkout-kind-trigger -o yaml
kubectl --context kind-kapro-kind-demo get plugins
```

Before approval, production targets should pause in `WaitingApproval`. After
`scripts/kind-demo.sh approve`, the approvals in `approvals/` allow production
to advance.

## Limitations

- The `ResourceSet` and `HelmRelease` resources are local fixtures. Their
  readiness is patched by `scripts/kind-demo.sh`; no Flux controller reconciles
  workloads.
- `Plugin` objects point at static demo endpoints. The plugin
  readiness controller is expected to mark them not ready unless you run
  matching gRPC plugin servers. The built-in planner, gates, and Flux substrate
  adapter drive the rollout.
- The `Trigger` is suspended and dry-run because the demo does not
  start a local OCI registry or signature verifier.
- Webhooks are disabled in the demo operator overlay to keep local setup
  self-contained.

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

## Run This Example

Every example has a local runner. Start with the safe check command; this is also the path exercised by CI through `make check-examples`:

```bash
examples/10-kind-demo/run.sh
```

For the full local lifecycle, use the demo driver from the repository root:

```bash
scripts/kind-demo.sh up
scripts/kind-demo.sh status
scripts/kind-demo.sh approve
scripts/kind-demo.sh down
```

## Expected Result

- `check` validates the README, shell syntax, YAML/JSON shape, and stale Kapro API names.
- `apply` runs `kubectl apply -f` for this directory.
- Kubernetes should accept the manifests once the matching CRDs/controllers are installed.

## Cleanup

```bash
scripts/kind-demo.sh down
```
