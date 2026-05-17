# Backend Ownership Contract

Kapro is a promotion control plane. It does not replace Argo CD, Flux,
Rollouts, Flagger, service meshes, or Kubernetes workload controllers. Backend
adapters own a very small write surface and leave local sync, drift correction,
traffic shifting, credentials, and rollout mechanics to the selected backend.

## Management Policies

| Policy | Meaning | Writes |
|---|---|---|
| `Observe` | Kapro discovers backend-native objects and reports them in `BackendProfile.status`. | None |
| `Adopt` | Kapro may write the documented revision field for selected promotion targets. | Only the backend-specific version field |

`Observe` is the default. Move to `Adopt` only after the discovered graph and
selected objects are correct.

## Argo CD

| Pattern | Kapro observes | Kapro writes in `Adopt` | Kapro never writes |
|---|---|---|---|
| Plain `Application` | Selected `Application` objects and Argo cluster Secrets. | `Application.spec.source.targetRevision`, hard refresh annotation, and `operation.sync`. | `project`, `destination`, repo credentials, cluster Secrets, sync policy, health status, traffic resources. |
| `ApplicationSet` with Git files | `ApplicationSet` and generated `Application` objects. | The declared JSON/YAML generator input field through `kapro source apply`; writes require an explicit `PromotionSource` mapping and scoped file match. | Generators, repo credentials, cluster credentials, sync policy, traffic resources. |
| `ApplicationSet` child | Generated `Application` objects selected by labels or owner references. | Generated child `Application.spec.source.targetRevision` only for explicit live adoption and selector parameters such as `applicationSelector.<unit>`. | `ApplicationSet.spec`, generators, cluster credentials, template metadata. |
| `ApplicationSet` template | `ApplicationSet` objects are counted and sampled as skipped by the built-in backend. | Not written by the built-in actuator. Use the ApplicationSet actuator plugin when the desired ownership level is `ApplicationSet.spec.template.spec.source.targetRevision`. | Generators, repo credentials, sync policy, traffic resources. |
| App-of-apps root | Root `Application` can be discovered but is marked unsupported for direct promotion by default. | None by default. | Child Application definitions unless those children are selected directly. |
| App-of-apps child | Child `Application` objects selected by labels. | `Application.spec.source.targetRevision`. | Root app packaging and sync mechanics. |

Argo CD must still reconcile the Application after Kapro changes the revision.
The built-in live Application actuator requests sync, but Argo CD remains the
owner of sync windows, hooks, health, and local rollout behavior.

Live Argo writes require an explicit delegation marker on every Application:
`kapro.io/managed-by: kapro`, `kapro.io/authorized-source: <source>`, or
`kapro.io/authorized-unit: <unit>`. Put the marker on
`ApplicationSet.spec.template.metadata.labels` for generated apps.

During apply, `PromotionTarget.status.backendObjects` records each selected Argo
Application, desired revision, observed revision, sync status, health status,
and convergence phase.

## Flux

| Pattern | Kapro observes | Kapro writes in `Adopt` or generated mode | Kapro never writes |
|---|---|---|---|
| `HelmRelease` | Selected `HelmRelease` objects. | The selected chart/image/source version field declared by the backend adapter. Generated greenfield pull mode writes Kapro-managed desired state. | Helm values not declared in `PromotionSource`, secrets, target cluster credentials, rollout traffic resources. |
| `Kustomization` | Selected `Kustomization` objects. | No default direct write target. Promote the referenced source revision or a Kustomize image file instead. | Reconciled workload manifests, secrets, drift correction, health status. |
| `GitRepository`, `OCIRepository`, or `Bucket` | Selected Flux source objects. | The declared `spec.ref.tag`, `semver`, `digest`, or reviewed `branch` field through Git-native source apply. | Repository credentials and source authentication. |

Flux keeps source authentication, reconciliation, inventory, health checks, and
drift correction. Kapro supplies promotion order, gates, approvals, PromotionRun
history, and evidence.

## Status Evidence

`BackendProfile.status` records:

- `discoveredClusters`, `discoveredApplications`, and `discoveredApplicationSets`
  as full counts;
- `selectedObjects`, `skippedObjects`, and `unsupportedPatterns` as bounded
  samples;
- `lastDiscoveryTime` and `DiscoveryReady` for freshness and failures.

The object samples are intentionally capped. Counts are the scale signal; status
samples are for operator diagnosis and UI previews.

For the exact automatic vs skipped pattern list, see
[Supported Backend Patterns](supported-backend-patterns.md).
