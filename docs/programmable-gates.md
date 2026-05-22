# Programmable Gates

Programmable gates let a custom Kapro operator register a Go function as a gate
type. Plans reference the registered gate via `type: plugin` and the registered
name, exactly like the existing gRPC plugin path. The registry is shared so an
in-process gate resolves first; the gateway is consulted only when no Go gate
is registered under that name.

```go
s.Gates.MustRegister("canary-error-rate", gate.Func(func(ctx context.Context, req gate.Request) (gate.Result, error) {
	threshold := 0.001
	if raw := req.Parameters["threshold"]; raw != "" {
		parsed, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return gate.MakeFailed("InvalidThreshold", "threshold %q is not a number", raw), nil
		}
		threshold = parsed
	}

	observed := readErrorRate(ctx, req.Fleet, req.Version)
	if observed < threshold {
		return gate.MakePassed(fmt.Sprintf("error rate %.4f < %.4f", observed, threshold)), nil
	}
	return gate.MakeFailed("ErrorRateExceeded", "error rate %.4f >= %.4f", observed, threshold), nil
}))
```

```yaml
apiVersion: kapro.io/v1alpha2
kind: Plan
metadata:
  name: canary
spec:
  stages:
    - name: canary
      selector:
        matchLabels:
          env: canary
      gate:
        mode: auto
        gate:
          templates:
            - name: error-rate
              type: plugin
              plugin:
                name: canary-error-rate
              args:
                - name: threshold
                  value: "0.002"
```

The function receives immutable rollout context, template parameters, and a
pre-tagged logger. Return `gate.MakePassed`, `gate.MakeFailed`, or
`gate.MakeInconclusive`; returning an error records an evaluation error and
retries until `maxAttempts` is exhausted. `MakeInconclusive` is the right
helper when the gate needs more time — the controller's `inconclusivePolicy`
(skip/halt) applies only to inconclusive results, never to pending.

Kapro records programmable and built-in evaluations in
`kapro_gate_evaluations_total{gate_type,result}`. Non-terminal phases collapse
into the `inconclusive` label so existing dashboards continue to work.

## Request Field Guide

`gate.Request` is shared by built-in gates and programmable gates. New
programmable gates should read the ergonomic identity fields and
`Parameters`:

- `Fleet`, `Promotion`, `PromotionRun`, `Plan`, `Stage`, `Target`, and
  `Version` identify the rollout being evaluated.
- `Parameters` contains user-supplied gate parameters from `GateTemplate` args
  or gate policy parameters.
- `Logger` is pre-tagged by the controller when one is available.

The older fields remain populated for the built-in controller paths:

- `Policy` is the resolved `GatePolicySpec`; metrics, approval, verification,
  and other built-in gates may still inspect it.
- `MetricIndex` selects one metric from the legacy metrics gate path.
- `Template` is the resolved `GateTemplateSpec` for template-dispatched
  built-in gates such as CEL, Job, and Webhook.
- `Args` contains merged template defaults plus runtime-injected values.

For new in-process gate code, prefer `Parameters` and the identity fields. Use
`Policy`, `Template`, `MetricIndex`, and `Args` only when adapting existing
built-in gate logic.

## Trust boundary

Programmable gates run inside the operator process and are fully trusted. The
runtime applies no sandbox, no resource budget, and no syscall filter. A buggy
gate can panic the reconciler (the SDK's `gate.Recover` wrapper translates
panics into `Failed` results, but the goroutine still bounces) and a malicious
gate can read every secret the operator's ServiceAccount can read.

Compile only code you own and review into a custom Kapro binary. Code from
third parties, customer-supplied logic, or anything that must be sandboxed for
compliance or tenancy reasons belongs behind one of the boundaried paths
instead:

- **gRPC plugin gateway** — separate process, separate ServiceAccount,
  separate network policy; survives plugin crashes.
- **Webhook gates** — separate service with its own auth and TLS; the
  operator only sees the response payload.
- **Job gates** — runs as a Kubernetes Job in a namespace and ServiceAccount
  the gate author controls.

If you can't answer "who reviews this code before it lands in the operator
binary?" with a name on your own team, you want a webhook or plugin gate, not
a programmable gate.
