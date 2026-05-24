# Composable Gates

Kapro 0.6 keeps enforceable rollout gates inline on `Plan` stages. There is no
separate expression CRD in the public-preview surface; that avoids a
second lifecycle object for gate definitions before runtime semantics are
stable.

Use `Plan.spec.stages[].gate.gate.templates[]` when a stage needs custom logic.
Built-in template types include `cel`, `job`, and `webhook`, and plugins can
register additional gate types.

## Example

```yaml
apiVersion: kapro.io/v1alpha1
kind: Plan
metadata:
  name: canary
  labels:
    kapro.io/team: platform
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
            - name: smoke
              type: cel
              cel:
                expression: "target.phase == 'Converged'"
    - name: prod
      selector:
        matchLabels:
          env: prod
      dependsOn:
        - stage: canary
          strategy: all
      gate:
        mode: auto
        gate:
          templates:
            - name: platform-policy
              type: webhook
              args:
                - name: url
                  value: https://policy.example.com/kapro/prod
                - name: method
                  value: POST
                - name: timeout
                  value: 10s
```

## Composition Model

For the first public preview, compose gates by keeping the stage DAG explicit:

- use `dependsOn` for ordering and soak-time dependencies;
- use multiple `templates[]` entries when all checks must pass for one stage;
- use separate stages when checks should be independently observable;
- use a custom `webhook`, `job`, or plugin gate when the decision belongs to an
  external policy engine or custom API.

`stage.gate.expressionRef` remains reserved for forward compatibility and is
rejected by admission. If Kapro reintroduces reusable gate expressions later,
it will be done behind a new ADR and conformance tests rather than as a hidden
runtime dependency.
