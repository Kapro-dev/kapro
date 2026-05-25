# Gate Request SDK Guide

`gate.Request` is shared by two call paths:

- programmable gates registered in-process through `server.Gates`;
- built-in controller gates such as metrics, approval, CEL, Job, and Webhook.

That shared shape keeps existing controller behavior compatible, but it means a
programmable gate sees fields that were originally built for internal gate
implementations. New SDK code should treat the request as two surfaces.

## Use These Fields

Programmable gates should use the ergonomic identity fields and user
parameters:

| Field | Meaning |
| --- | --- |
| `Fleet` | Owning Fleet name when known. |
| `Promotion` | Owning Promotion name when known. |
| `PromotionRun` | Immutable PromotionRun attempt. |
| `Plan` | Plan node being evaluated. |
| `Stage` | Stage being evaluated. |
| `Target` | Target Cluster name. |
| `Version` | Artifact version under rollout. |
| `Parameters` | User-supplied gate parameters. |
| `Logger` | Pre-tagged logger when the controller has one. |

These fields are the SDK-facing contract for new programmable gates. They are
stable within the v0.x SDK compatibility policy and are the fields used by the
examples under `examples/06-sdk-go/03-programmable-gates`.

```go
s.Gates.MustRegister("canary-error-rate", gate.Func(func(ctx context.Context, req gate.Request) (gate.Result, error) {
	threshold, err := strconv.ParseFloat(req.Parameters["threshold"], 64)
	if err != nil {
		return gate.MakeFailed("InvalidThreshold", "threshold must be numeric"), nil
	}

	observed := readErrorRate(ctx, req.Fleet, req.Target, req.Version)
	if observed <= threshold {
		return gate.MakePassed("error rate within threshold"), nil
	}
	return gate.MakeFailed("ErrorRateExceeded", "error rate exceeds threshold"), nil
}))
```

## Compatibility Fields

The following fields remain populated because built-in gates still use them:

| Field | Compatibility use |
| --- | --- |
| `Context` | `*gate.Context` holding the controller-owned target promotion state. Never mutate it. |
| `Policy` | Resolved `GatePolicySpec` for built-in metrics, approval, and verification paths. |
| `MetricIndex` | Index into `Policy.Gate.Metrics` for the legacy metrics path. |
| `Template` | Resolved `GateTemplateSpec` for built-in template gates. |
| `Args` | Merged template defaults plus runtime-injected identity values. |

Do not use these fields in new programmable gates unless you are intentionally
wrapping or adapting existing built-in gate behavior. They expose more
controller detail than most SDK authors need and may be nil depending on the
evaluation path.

## Parameters vs Args

Use `Parameters` when reading values supplied by the user in a gate template or
policy. `Parameters` is intentionally small and excludes controller-injected
identity fields.

Use `Args` only when adapting existing CEL, Job, Webhook, or legacy plugin code
that expects the older merged argument map. `Args` can include runtime values
such as version, target, stage, promotion run, and plan names in addition to
template defaults.

## Migration Pattern

When moving an existing gate implementation to the programmable API:

1. Replace reads of `Args["version"]`, `Args["target"]`, `Args["stage"]`, and
   similar identity keys with `Version`, `Target`, and `Stage`.
2. Replace user configuration reads from `Args` with `Parameters`.
3. Keep `Policy`, `Template`, and `MetricIndex` only if the implementation is
   deliberately sharing code with a built-in gate.
4. Wrap trusted in-process gates with `gate.Recover` while migrating so panics
   become failed gate results instead of crashing the reconciler goroutine.

```go
s.Gates.MustRegister("legacy-budget", gate.Recover(gate.Func(func(ctx context.Context, req gate.Request) (gate.Result, error) {
	budget := req.Parameters["budget"]
	return evaluateBudget(ctx, req.Fleet, req.Stage, req.Target, req.Version, budget)
})))
```

## Stability Notes

`pkg/kapro/gate` is the canonical SDK import path for programmable gates.
`Predicate` is the canonical authoring interface and `PredicateFunc` adapts a
plain Go function. `Gate` and `Func` remain aliases for the same runtime
contract, and `pkg/gate` remains as a compatibility import path.

```go
import "kapro.io/kapro/pkg/kapro/gate"
```

Registries wrap predicates with OpenTelemetry tracing by default. Use
`gate.NewRegistryWithoutTracing()` only in tests or when the predicate is
already wrapped by the caller.

The request type is immutable from the gate's perspective. Implementations
must not mutate maps, pointed-to objects, or status-like fields they receive.
Return a `gate.Result` and let the controller own status persistence.
