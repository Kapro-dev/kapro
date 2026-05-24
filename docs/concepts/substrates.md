# Substrates

Kapro decides when a version can advance. Substrates decide how that version is
applied inside or for a target cluster.

## Delivery Modes

| Mode | Best fit | How it works |
|---|---|---|
| `pull` | Edge, private, or outbound-only clusters | A spoke controller watches hub intent and applies from inside the workload cluster. |
| `push` | Centrally reachable clusters | The hub patches a substrate object or Kubernetes API directly. |

These are the values used in `Fleet.spec.delivery.mode` and
`Cluster.spec.delivery.mode`.

## Existing Object Management Policy

`Observe` and `Adopt` are not delivery modes. They are discovery and management
postures for existing Argo CD or Flux objects:

| Policy | Best fit | How it works |
|---|---|---|
| `Observe` | Existing object discovery | Kapro reads existing substrate objects and reports what can be adopted. |
| `Adopt` | Existing object management | Kapro updates only reviewed substrate-native version fields. |

Use these policies through discovery/adoption configuration, for example
`Substrate.spec.discovery.managementPolicy`, not through
`spec.delivery.mode`.

## Built-In Substrates

| `substrate.kind` | Common `execution.mode` | Current use |
|---|---|---|
| `oci` | `spoke-pull` | Greenfield outbound-only delivery through the spoke controller. |
| `flux` | `spoke-pull` or `hub-push` | Existing or generated Flux delivery, depending on cluster configuration. |
| `argo` | `hub-push` | Existing Argo CD Application delivery with reviewed adoption boundaries. |
| `kubernetes-apply` | `hub-push` | Direct Kubernetes server-side apply for bootstrap and simple default delivery. |

Substrate behavior is selected through `Substrate` and cluster delivery
settings. A fleet may mix modes across clusters.

## Staged Delivery Semantics

The OCI spoke substrate uses validation-atomic staged delivery. Kapro renders the
artifact, server-side dry-runs every object, and commits only after the full
dry-run pass succeeds. A dry-run failure leaves live objects untouched and is
reported in `Cluster.status.delivery[app].staging`.

This is not a Kubernetes multi-object transaction. If the commit phase starts
and the API server or network fails partway through, some objects may already be
committed. Kapro reports that as `failurePhase: Applying`, records staged,
committed, and failed object counts, and retries on the next spoke reconcile.

## Existing GitOps Adoption

For existing Flux or Argo CD estates, use observe-first workflows:

```bash
kapro adopt argo . --out ./kapro-connect --namespace argocd --selector kapro.io/import=true
kapro adopt flux . --out ./kapro-connect --namespace flux-system --selector kapro.io/import=true
```

`kapro adopt` is the public command for connecting an existing GitOps repo.
`kapro discover` is the lower-level discovery command name used by diagnostics
and scripted inventory workflows.

Review the generated `Substrate`, `Source`, and discovery status
before switching a substrate to write mode. Kapro should only patch fields that
the owning platform team has explicitly adopted.

For new promotion repositories, use greenfield bootstrap:

```bash
kapro quickstart direct ./promotion-repo --name checkout
kapro quickstart argo ./promotion-repo --name checkout
kapro quickstart flux ./promotion-repo --name checkout
kapro quickstart oci ./promotion-repo --name checkout
```

See [Adoption Guide](../getting-started/adoption.md) for the full decision tree.

## Plugins

When built-in behavior is not enough, `Plugin` can load external:

- actuators for apply/convergence logic;
- gates for safety checks;
- planners for target ordering.

Plugins should pass the matching conformance suite before use in production.
See [Extension Model](../extending/extension-model.md) and [Plugin Authoring](../extending/plugin-authoring.md).
