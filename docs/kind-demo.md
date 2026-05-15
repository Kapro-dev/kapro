# Kind Demo

This demo path is the target end-to-end local flow for CNCF reviewers and new
contributors. It exercises the promotion chain:

```text
ReleaseTrigger -> Release -> planner -> gates -> actuator
```

The install bundle work owns the exact one-command bootstrap. This document
defines the expected demo shape and validation steps.

## Prerequisites

- Docker
- kind
- kubectl
- Helm or kustomize, depending on the install bundle used
- Optional: cosign for signed ReleaseTrigger demonstrations

## Cluster

```bash
kind create cluster --name kapro-demo
kubectl cluster-info --context kind-kapro-demo
```

Install Kapro CRDs, RBAC, controller, webhook, and example config with the
project install bundle once available:

```bash
helm upgrade --install kapro ./charts/kapro-operator \
  --namespace kapro-system \
  --create-namespace
```

For local controller development, use:

```bash
KAPRO_DEV_MODE=1 go run ./cmd/operator
```

## Demo Objects

Apply a minimal pipeline and fleet:

```bash
kubectl apply -f examples/hub-config/apps/checkout.yaml
kubectl apply -f examples/hub-config/clusters/canary-eu.yaml
kubectl apply -f examples/hub-config/clusters/prod-eu.yaml
kubectl apply -f examples/hub-config/clusters/prod-us.yaml
kubectl apply -f examples/hub-config/pipelines/checkout-progressive.yaml
```

Install example plugins when testing the plugin gateway:

```bash
kubectl apply -f examples/plugins/slo-gate-registration.yaml
kubectl apply -f examples/plugins/argocd-actuator-registration.yaml
```

## Manual Release Path

Create a Release directly:

```bash
kubectl apply -f examples/hub-config/releases/checkout-v1.2.3.yaml
kubectl get releases.kapro.io
kubectl get releasetargets.kapro.io
```

Expected behavior:

1. The Release enters `Progressing`.
2. The planner selects canary targets first.
3. Soak, metrics, verification, template, or approval gates run according to
   the Pipeline.
4. The actuator applies the desired version.
5. `ReleaseTarget` objects converge or fail with a status reason.
6. The Release completes when all required targets finish.

## ReleaseTrigger Path

Apply a suspended trigger:

```bash
kubectl apply -f examples/release-trigger/oci-safe-default.yaml
kubectl get releasetriggers.kapro.io
```

For a full automatic demo, configure the trigger repository, tag pattern, and
signature policy for a test OCI artifact. Then unsuspend the trigger:

```bash
kubectl patch releasetrigger checkout-oci \
  --type merge \
  -p '{"spec":{"suspended":false}}'
```

Expected behavior:

1. The trigger observes a matching tag.
2. The tag is resolved to an immutable digest.
3. Signature policy is checked when required.
4. A digest-pinned Release is created from the template.
5. The normal planner, gate, and actuator flow handles rollout.

## Approvals

When a target waits for approval, create the exact deterministic `Approval`
object shown by Release status or events:

```bash
kapro approve <release>/<target> --comment "demo approval"
```

Or apply an `Approval` manifest with `spec.release`, `spec.target`, and
`spec.ref` matching the waiting target.

## Verification Commands

```bash
kubectl get releases.kapro.io -o wide
kubectl get releasetargets.kapro.io -o wide
kubectl describe release <name>
kubectl describe releasetarget <name>
kubectl get events --sort-by=.lastTimestamp
kubectl -n kapro-system port-forward deploy/kapro-operator 8080:8080
curl -s http://127.0.0.1:8080/metrics | grep '^kapro_'
```

The demo is complete when a reviewer can see a ReleaseTrigger-created or
manually-created Release progress through target planning, gates, actuator
application, and terminal status in a local kind cluster.
