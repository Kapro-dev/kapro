# Plugin Examples

This directory contains runnable external plugin examples and matching
`PluginRegistration` manifests.

| Example | Contract | Registry name | Manifest | Verify |
|---|---|---|---|---|
| `argocd-actuator` | KAI actuator | `argo/pull` | `argocd-actuator-registration.yaml` | `go test ./examples/plugins/argocd-actuator` |
| `argocd-applicationset-actuator` | KAI actuator | `argo/push` | `argocd-applicationset-actuator-registration.yaml` | `go test ./examples/plugins/argocd-applicationset-actuator` |
| `slo-gate` | KGI gate | `slo` | `slo-gate-registration.yaml` | `go test ./examples/plugins/slo-gate` |
| `capacity-planner` | KPI planner | `capacity` | `capacity-planner-registration.yaml` | `go test ./examples/plugins/capacity-planner` |

Runtime actuator and gate loading is an opt-in preview:

```bash
KAPRO_ENABLE_PLUGIN_GATEWAY=true
```

The operator loads ready actuator and gate registrations once at startup.
Planner registrations are probed and reported in status, but runtime planner
dispatch is future work.

Related docs:

- `docs/plugin-authoring.md`
- `docs/plugin-compatibility.md`
- `docs/conformance.md`
