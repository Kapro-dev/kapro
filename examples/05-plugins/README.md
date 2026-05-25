# Plugin Examples

This directory contains runnable external plugin examples and matching
`Plugin` manifests.

| Example | Contract | Registry name | Manifest | Verify |
|---|---|---|---|---|
| `00-argocd-actuator` | KAI actuator | `argo/pull` | `00-argocd-actuator/registration.yaml` + `00-argocd-actuator/manifests/deployment.yaml` | `go test ./examples/05-plugins/00-argocd-actuator` |
| `01-argocd-applicationset-actuator` | KAI actuator | `argo/push` | `01-argocd-applicationset-actuator/registration.yaml` | `go test ./examples/05-plugins/01-argocd-applicationset-actuator` |
| `02-flux-actuator` | KAI actuator | `flux/helmrelease` | `02-flux-actuator/registration.yaml` + `02-flux-actuator/manifests/deployment.yaml` | `go test ./examples/05-plugins/02-flux-actuator` |
| `03-slo-gate` | KGI gate | `slo` | `03-slo-gate/registration.yaml` | `go test ./examples/05-plugins/03-slo-gate` |
| `04-capacity-planner` | KPI planner | `capacity` | `04-capacity-planner/registration.yaml` | `go test ./examples/05-plugins/04-capacity-planner` |

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

## Run This Example

Every example has a local runner. Start with the safe check command; this is also the path exercised by CI through `make check-examples`:

```bash
examples/05-plugins/run.sh
```

This directory is an index for smaller examples. Run a child folder next, for example:

```bash
examples/05-plugins/00-argocd-actuator/run.sh
```

## Expected Result

- `check` verifies this directory has its README and runnable script.
- Child example folders contain the concrete YAML, Go, or demo assets.

## Cleanup

No cluster resources are created by `check`. Stop any foreground `run` command with `Ctrl-C`.
