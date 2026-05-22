# Programmable Gates

Programmable gates let a custom Kapro operator register a Go function as a gate
type. Plans reference the registered type exactly like built-in `cel`, `job`,
or `webhook` gates.

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
              type: canary-error-rate
              args:
                - name: threshold
                  value: "0.002"
```

The function receives immutable rollout context, template parameters, and a
pre-tagged logger. Return `gate.MakePassed`, `gate.MakeFailed`, or
`gate.MakePending`; returning an error records an evaluation error and retries
until `maxAttempts` is exhausted.

Kapro records programmable and built-in evaluations in
`kapro_gate_evaluations_total{gate_type,result}`.
