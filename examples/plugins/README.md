# Plugin Examples

This directory contains runnable external plugin examples and matching
`PluginRegistration` manifests.

| Example | Contract | Registry name | Manifest | Verify |
|---|---|---|---|---|
| `argocd-actuator` | KAI actuator | `argo/pull` | `argocd-actuator-registration.yaml` | `go test ./examples/plugins/argocd-actuator` |
| `argocd-applicationset-actuator` | KAI actuator | `argo/push` | `argocd-applicationset-actuator-registration.yaml` | `go test ./examples/plugins/argocd-applicationset-actuator` |
| `slo-gate` | KGI gate | `slo` | `slo-gate-registration.yaml` | `go test ./examples/plugins/slo-gate` |
| `capacity-planner` | KPI planner | `capacity` | `capacity-planner-registration.yaml` | `go test ./examples/plugins/capacity-planner` |

Runtime actuator, gate, and planner loading is an opt-in preview:

```bash
KAPRO_ENABLE_PLUGIN_GATEWAY=true
```

The operator hot-loads ready actuator, gate, and planner registrations after
readiness probes succeed.

Related docs:

- `docs/plugin-authoring.md`
