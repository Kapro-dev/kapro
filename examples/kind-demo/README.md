# Kapro Kind Demo

See [../../docs/kind-demo.md](../../docs/kind-demo.md) for the full walkthrough.

The demo manifests are split by role:

- `operator/`: demo kustomize overlay for the local operator
- `crds/`: fixture-only CRDs not shipped by Kapro
- `fixtures/`: fake Flux resources used by the local actuator path
- `config/`: Kapro API objects for the promotionrun flow
- `approvals/`: manual approvals that unblock production
