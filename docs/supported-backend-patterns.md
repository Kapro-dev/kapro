# Supported Backend Patterns

This is the current brownfield contract for automatic discovery and generated
Git-native write mappings. Anything listed as `needs-review` is intentionally
generated as a starting point, not as a silent production write.

## Argo CD

| Pattern | Discovery | Generated write target | Confidence |
|---|---|---|---|
| Plain `Application` with `spec.source.targetRevision` | Yes | `ArgoApplicationSource` `spec.source.targetRevision` | High |
| Plain multi-source `Application` with `spec.sources[].targetRevision` | Yes | First non-empty `spec.sources[N].targetRevision` | High |
| App-of-apps root `Application` | Yes | None; marked unsupported for direct promotion | Unsupported |
| App-of-apps child `Application` | Yes when selected by labels | Child `Application` source revision | High |
| `ApplicationSet` object | Yes | None in the built-in live backend | Skipped |
| `ApplicationSet` generated child | Yes by owner reference | None by default; child live edits are skipped because the ApplicationSet owns the child spec | Skipped |
| `ApplicationSet` Git file generator using JSON/YAML parameter files | Yes for git/list/matrix/merge file generators | `GitJSONField` or `GitYAMLField` for the inferred version variable | High or needs-review |

The scanner does not execute arbitrary Go templates, SCM provider generators,
pull request generators, or plugin generators. Those are reported for review or
require an explicit `PromotionSource` mapping.

## Flux

| Pattern | Discovery | Generated write target | Confidence |
|---|---|---|---|
| `GitRepository.spec.ref.tag` | Yes | `GitYAMLField` `spec.ref.tag` | High |
| `GitRepository.spec.ref.semver` | Yes | `GitYAMLField` `spec.ref.semver` | High |
| `GitRepository.spec.ref.digest` | Yes | `GitYAMLField` `spec.ref.digest` | High |
| `GitRepository.spec.ref.branch` | Yes | `GitYAMLField` `spec.ref.branch` | Medium |
| `OCIRepository.spec.ref.*` | Yes | Same ref field as discovered | High or medium |
| `Bucket.spec.ref.*` | Yes | Same ref field as discovered | High or medium |
| `HelmRelease.spec.chart.spec.version` | Yes | `GitYAMLField` `spec.chart.spec.version` | High |
| `HelmRelease.spec.values.image.tag` | Yes | `GitYAMLField` `spec.values.image.tag` | Medium |
| Custom HelmRelease values image tag paths | Yes when the path contains image/container semantics and a `tag` scalar | `GitYAMLField` for the discovered path | Needs-review |
| Kustomize `images[].newTag` in `kustomization.yaml` | Yes | `KustomizeImage` for the image name | High |
| Helm `Chart.yaml` `version` and `appVersion` | Yes | `GitYAMLField` `version` or `appVersion` | Medium |
| Flux `Kustomization` object | Yes | No default direct write target | Skipped |

Flux `Kustomization.spec.path` and `spec.sourceRef` are topology fields. Kapro
does not treat them as universal promotion-version fields. Promote the referenced
source revision, a Kustomize image tag, Helm chart version, or an explicit
field that the team adds to `PromotionSource`.

## Scale And Safety Limits

Discovery uses `git ls-files` and parses only tracked YAML/JSON candidates.
Default limits are:

- `--max-files=10000`
- `--max-units=1000`
- one MiB per parsed file
- common GitOps path prefixes unless `--scan-all` is set

Large repositories should keep discovery scoped with `--path-prefix` and commit
the generated `discovery/kapro-git-map.yaml` for review. Use the benchmarks in
`cmd/kapro/discover_scale_test.go` to measure local repository shapes:

```bash
go test ./cmd/kapro -run '^$' -bench 'Discover.*10000Files' -benchmem
```

`kapro source apply` stages all planned file updates before writing them back.
If any mapped write fails, the original files are left unchanged.
