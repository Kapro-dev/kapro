# Backends

Kapro decides when a version can advance. Backends decide how that version is
applied inside or for a target cluster.

## Delivery Modes

| Mode | Best fit | How it works |
|---|---|---|
| `pull` | Edge, private, or outbound-only clusters | A spoke controller watches hub intent and applies from inside the workload cluster. |
| `push` | Centrally reachable clusters | The hub patches a backend object or Kubernetes API directly. |

These are the values used in `Fleet.spec.delivery.mode` and
`Cluster.spec.delivery.mode`.

## Brownfield Management Policy

`Observe` and `Adopt` are not delivery modes. They are discovery and management
postures for existing Argo CD or Flux objects:

| Policy | Best fit | How it works |
|---|---|---|
| `Observe` | Brownfield discovery | Kapro reads existing backend objects and reports what can be adopted. |
| `Adopt` | Brownfield management | Kapro updates only reviewed backend-native version fields. |

Use these policies through discovery/adoption configuration, for example
`Backend.spec.discovery.managementPolicy`, not through
`spec.delivery.mode`.

## Built-In Backend Drivers

| YAML driver | Common runtime | Current use |
|---|---|---|
| `oci` | `Spoke` | Greenfield outbound-only delivery through the spoke controller. |
| `flux` | `Spoke` or `Hub` | Brownfield or generated Flux delivery, depending on cluster configuration. |
| `argo` | `Hub` | Brownfield Application delivery with reviewed adoption boundaries. |

Backend behavior is selected through `Backend` and cluster delivery
settings. A fleet may mix modes across clusters.

## Staged Delivery Semantics

The OCI spoke backend uses validation-atomic staged delivery. Kapro renders the
artifact, server-side dry-runs every object, and commits only after the full
dry-run pass succeeds. A dry-run failure leaves live objects untouched and is
reported in `Cluster.status.delivery[app].staging`.

This is not a Kubernetes multi-object transaction. If the commit phase starts
and the API server or network fails partway through, some objects may already be
committed. Kapro reports that as `failurePhase: Applying`, records staged,
committed, and failed object counts, and retries on the next spoke reconcile.

## Brownfield Adoption

For existing Flux or Argo CD estates, use observe-first workflows:

```bash
kapro bootstrap brownfield argo . --out ./kapro-connect --namespace argocd --selector kapro.io/import=true
kapro bootstrap brownfield flux . --out ./kapro-connect --namespace flux-system --selector kapro.io/import=true
```

`kapro bootstrap brownfield` is the guided wrapper for first-time users.
`kapro adopt` is the shorter brownfield command, and `kapro discover` is the
lower-level discovery command name.

Review the generated `Backend`, `Source`, and discovery status
before switching a backend to write mode. Kapro should only patch fields that
the owning platform team has explicitly adopted.

For new promotion repositories, use greenfield bootstrap:

```bash
kapro bootstrap greenfield ./promotion-repo --backend flux --mode pull --name checkout
kapro bootstrap greenfield ./promotion-repo --backend argo --mode push --name checkout
kapro bootstrap greenfield ./promotion-repo --backend oci --mode pull --name checkout
```

See [Adoption Guide](../getting-started/adoption.md) for the full decision tree.

## Plugins

When built-in behavior is not enough, `Plugin` can load external:

- actuators for apply/convergence logic;
- gates for safety checks;
- planners for target ordering.

Plugins should pass the matching conformance suite before use in production.
See [Extension Model](../extending/extension-model.md) and [Plugin Authoring](../extending/plugin-authoring.md).
