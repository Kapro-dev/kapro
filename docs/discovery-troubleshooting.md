# Discovery Troubleshooting

Use this guide when `BackendProfile.status.conditions[type=DiscoveryReady]` is
`False`, discovery finds too little, or generated units have
`confidence: needs-review`.

## First Checks

```bash
kubectl get backendprofile <name> -o yaml
kubectl logs -n kapro-system deployment/kapro-kapro-operator
```

Check these status fields first:

- `status.lastDiscoveryTime`
- `status.conditions`
- `status.selectedObjects`
- `status.skippedObjects`
- `status.unsupportedPatterns`
- `status.discoveredApplications`
- `status.discoveredApplicationSets`
- `status.discoveredClusters`

## Common Causes

| Symptom | Likely cause | Fix |
|---|---|---|
| `DiscoveryReady=False` with RBAC errors | Operator cannot list backend objects | Grant observe RBAC for Argo Applications/ApplicationSets or Flux source/workload objects. |
| No Argo Applications selected | Missing `kapro.io/import=true` label or wrong namespace | Add labels and set the BackendProfile namespace/selector correctly. |
| ApplicationSet object missing from status | Labels exist only on `spec.template.metadata` | Put import labels on both ApplicationSet `metadata.labels` and template labels. |
| ApplicationSet children skipped | The ApplicationSet owns generated Applications | Adopt the ApplicationSet template or generator input file, not the generated child Application. |
| App-of-apps root unsupported | Root app packages child Applications | Select child Applications for writes; keep the root as topology. |
| Flux Kustomization has no write target | `spec.path` is topology, not a universal version | Promote the referenced source object, Kustomize image field, or explicit YAML/JSON field. |
| `confidence: needs-review` | Kapro found a plausible but not canonical version field | Edit or remove the generated `PromotionSource` unit before switching to Adopt. |
| Large repo scan stops early | File or unit limits were reached | Narrow `--path-prefix`; only raise `--max-files` or `--max-units` after narrowing. |

## Editing Needs-Review Units

Generated mappings live under the discovery output directory:

```text
kapro-connect/
  sources/<name>.yaml
  discovery/kapro-git-map.yaml
```

For a needs-review unit:

1. Confirm the file is the intended source of truth.
2. Confirm `versionField` points to the exact field Kapro may write.
3. Rename the unit to something stable and human-readable.
4. Delete the unit if the field is not a promotion version.
5. Commit the reviewed mapping with the rest of the hub config.

Kapro should write only declared version fields. It should not infer write
ownership from arbitrary YAML after adoption.

## Argo Pattern Notes

- Plain `Application`: Kapro may write the configured source target revision.
- Multi-source `Application`: Kapro must write the indexed
  `spec.sources[n].targetRevision` selected by discovery.
- ApplicationSet with Git file generator: prefer writing the generator input
  YAML/JSON field that feeds the template variable.
- ApplicationSet-generated child: treat as skipped unless a dedicated
  ApplicationSet write path owns the template or generator input.
- App-of-apps root: treat as topology; promote selected child Applications.

## Flux Pattern Notes

- `GitRepository` and `OCIRepository`: promote explicit ref fields such as tag,
  digest, or semver.
- `HelmRelease`: promote chart version or reviewed image value fields.
- Kustomize files: promote `images[].newTag` when the image name is exact.
- `Kustomization`: usually topology; promote the source or file it references.

## When To Switch To Adopt

Switch `managementPolicy` from `Observe` to `Adopt` only after:

- selected objects match the intended team/service;
- unsupported and skipped objects are understood;
- needs-review units have been edited or removed;
- RBAC grants only the minimum patch rights needed for the selected fields.
