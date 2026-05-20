# Backends

Kapro decides when a version can advance. Backends decide how that version is
applied inside or for a target cluster.

## Connection Modes

| Mode | Best fit | How it works |
|---|---|---|
| `pull` | Edge, private, or outbound-only clusters | A spoke controller watches hub intent and applies from inside the workload cluster. |
| `push` | Centrally reachable clusters | The hub patches a backend object or Kubernetes API directly. |
| `observe` | Brownfield discovery | Kapro reads existing backend objects and reports what can be adopted. |
| `adopt` | Brownfield management | Kapro updates only reviewed backend-native version fields. |

## Built-In Backends

| Backend | Current use |
|---|---|
| OCI pull | Greenfield outbound-only delivery through the spoke controller. |
| Flux | Brownfield or generated Flux delivery, depending on cluster configuration. |
| Argo CD | Brownfield Application delivery with reviewed adoption boundaries. |

Backend behavior is selected through `BackendProfile` and cluster delivery
settings. A fleet may mix modes across clusters.

## Brownfield Adoption

For existing Flux or Argo CD estates, use observe-first workflows:

```bash
kapro adopt argo . --out ./kapro-connect --namespace argocd --selector kapro.io/import=true
kapro adopt flux . --out ./kapro-connect --namespace flux-system --selector kapro.io/import=true
```

`kapro adopt` is the brownfield-friendly wrapper around `kapro discover`; use
`kapro discover` directly when you want the lower-level discovery command name.

Review the generated `BackendProfile`, `PromotionSource`, and discovery status
before switching a backend to write mode. Kapro should only patch fields that
the owning platform team has explicitly adopted.

## Plugins

When built-in behavior is not enough, `PluginRegistration` can load external:

- actuators for apply/convergence logic;
- gates for safety checks;
- planners for target ordering.

Plugins should pass the matching conformance suite before use in production.
See [Extension Model](extension-model.md) and [Plugin Authoring](plugin-authoring.md).
