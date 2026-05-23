# Plugin Examples

This directory contains runnable external plugin examples and matching
`Plugin` manifests.

| Example | Contract | Registry name | Manifest | Verify |
|---|---|---|---|---|
| `argocd-actuator` | KAI actuator | `argo/pull` | `argocd-actuator-registration.yaml` + `argocd-actuator/manifests/deployment.yaml` | `go test ./examples/plugins/argocd-actuator` |
| `argocd-applicationset-actuator` | KAI actuator | `argo/push` | `argocd-applicationset-actuator-registration.yaml` | `go test ./examples/plugins/argocd-applicationset-actuator` |
| `flux-actuator` | KAI actuator | `flux/helmrelease` | `flux-actuator-registration.yaml` + `flux-actuator/manifests/deployment.yaml` | `go test ./examples/plugins/flux-actuator` |
| `slo-gate` | KGI gate | `slo` | `slo-gate-registration.yaml` | `go test ./examples/plugins/slo-gate` |
| `capacity-planner` | KPI planner | `capacity` | `capacity-planner-registration.yaml` | `go test ./examples/plugins/capacity-planner` |

Runtime actuator, gate, and planner loading is an opt-in preview:

```bash
KAPRO_ENABLE_PLUGIN_GATEWAY=true
```

The operator hot-loads ready actuator, gate, and planner registrations after
readiness probes succeed.

The Argo CD and Flux actuators are deployable external substrate proof points:
they ship Dockerfiles, Kubernetes RBAC/Deployment/Service manifests, KAI
conformance tests, and `kapro-conformance` live-endpoint instructions.

Related docs:

- `docs/plugin-authoring.md`
