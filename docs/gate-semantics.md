# Gate Semantics

Kapro gates use a simple decision model:

```text
Evidence -> Analysis -> Phase
```

The phase remains the only rollout-control field. Evidence explains why that
phase was returned. This keeps the controller deterministic while making gate
decisions auditable and useful to notification systems, dashboards, and future
agent workflows.

## Evidence

Gate evidence is structured, non-secret data persisted on
`ReleaseTarget.status.gates[].evidence[]`. It can include:

- provider and analysis mode;
- query, window, and interval;
- observed value, threshold, and baseline value;
- sample count and confidence;
- reason and optional projection.

Evidence must not contain tokens, headers, secret values, or raw webhook
payloads.

## Metric Analysis Modes

| Mode | Behavior |
|---|---|
| `threshold` | Compare the current Prometheus instant value to `threshold`. This preserves existing behavior when no analysis mode is configured. |
| `sloBurnRate` | Treat the current value as error-budget burn rate. Defaults to `lte` comparison. |
| `baseline` | Compare current value to `analysis.baselineQuery` as `current / baseline`. Defaults to `lte` comparison and threshold `1.0` when omitted. |
| `sequential` | Query a Prometheus range over `window`, require `minSamples`, then require `confidenceThreshold` before returning `Passed` or `Failed`. |
| `changePoint` | Query a Prometheus range over `window` and compare the first half with the second half. Significant regressions fail the gate. |
| `score` | Convert one metric into a 0-100 score and require `scoreThreshold`. This is useful for simple canary scorecards. |

Statistical modes are conservative. Missing data, too few samples, unreachable
Prometheus, non-positive baselines, and low confidence return `Inconclusive`.

Baseline and score modes can set `baselineHealthQuery`. Kapro evaluates that
query before trusting the baseline as a control. If the baseline is unhealthy,
the gate returns `Inconclusive` instead of comparing the canary to bad control
data.

Sequential and change-point modes are deterministic. They do not use randomized
permutation tests or hidden controller state. Given the same spec and Prometheus
response, they return the same phase and evidence.

## Research Basis

The model follows common progressive-delivery practice: explicit canary/control
comparison, SLO/error-budget reasoning, and sequential evaluation to reduce
release exposure while controlling premature decisions. See:

- Michael Lindon, Chris Sanden, and Vache Shirikian, "Rapid Regression Detection in Software Deployments through Sequential Testing", KDD 2022.
- David Daly, William Brown, Henrik Ingo, Jim O'Leary, and David Bradford, "Change Point Detection in Software Performance Testing", 2020.
- Matt Fleming et al., "Hunter: Using Change Point Detection to Hunt for Performance Regressions", 2023.
- Alexander Tarvo et al., "CanaryAdvisor: A Statistical-Based Tool for Canary Testing", ISSTA 2015.
- Google SRE Workbook, "Canarying Releases".
- Argo Rollouts Analysis for Kubernetes-native analysis-run semantics.
