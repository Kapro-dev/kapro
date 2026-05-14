# Hub Config Source of Truth

## Decision

For v1, hub config lives in a **git repo** and CI applies YAMLs to the hub cluster. Spokes remain gitless and consume OCI bundles only.

## Why

Kapro's spokes are gitless by design. The OCI artifact is the single source of runtime truth. But the hub cluster needs CRDs (Kapro, KaproApp, Pipeline, Release, MemberCluster) to drive the fleet. Those YAMLs need to live somewhere reproducible.

## Architecture

```
Developer -> git push -> CI pipeline -> kubectl apply -> Hub cluster (etcd)
                                                            |
                                                       Kapro operator
                                                            |
                                                 Spoke clusters (OCI pull)
```

## What lives in the hub config repo

| Directory | Contents |
|---|---|
| `clusters/` | MemberCluster definitions (one per spoke) |
| `apps/` | KaproApp definitions (component registry, waves, overrides) |
| `pipelines/` | Pipeline definitions (stage DAG, selectors, gates) |
| `releases/` | Release objects (version + pipeline references) |

## What does NOT live in the hub config repo

- OCI bundle contents (those are in the container registry)
- Spoke cluster workloads (those come from OCI bundles)
- Secrets (those come from External Secrets Operator or sealed secrets)
- Infrastructure (that comes from Terraform)

## CI workflow

The CI pipeline validates and applies hub config on every push to main:

1. `kubectl apply --dry-run=server` for validation
2. `kubectl diff` to show what will change
3. `kubectl apply` to apply changes
4. Verify Kapro operator reconciles successfully

See [examples/hub-config/](../examples/hub-config/) for a working example.

## Future: Flux on hub

For teams that want full GitOps on the hub, Flux can watch the config repo and reconcile automatically. This is not required for v1 but is the ideal end state:

```
git push -> Flux on hub -> reconciles CRDs -> Kapro operator -> spokes
```

To enable this, install Flux on the hub cluster and point a Kustomization at the config repo. No Kapro changes needed.
