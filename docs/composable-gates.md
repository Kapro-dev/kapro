# Composable Gates

`GateExpression` names and composes gate policies. It supports `ALL`, `ANY`,
`NOT`, `WEIGHTED_SUM`, `THRESHOLD`, and `DELAY` over inline gates and referenced
child expressions.

Use it to model and inspect reusable gate bundles before wiring them into
runtime policy. Keep enforceable v0.1.2 rollout checks inline on
`Plan.spec.stages[].gate`; Plan admission rejects `stage.gate.expressionRef`
until runtime resolution is implemented.

## Enable the Controller

The CRD is installed with Kapro, but the controller is a preview opt-in and is
not part of the default controller set.

```bash
helm upgrade --install kapro ./charts/kapro-operator \
  --namespace kapro-system --create-namespace \
  --set controllers='{fleet,plan,promotion,promotionrun,cluster,gateexpression}'
```

## Example

```yaml
apiVersion: kapro.io/v1alpha2
kind: GateExpression
metadata:
  name: checkout-all-of
  labels:
    kapro.io/team: platform
spec:
  operator: ALL
  operands:
    - expressionRef: checkout-security-checks
    - inlineGate:
        mode: auto
        gate:
          templates:
            - name: smoke
              type: cel
              cel:
                expression: "target.phase == 'Converged'"
```

`stage.gate.expressionRef` is reserved for forward compatibility and is rejected
by Plan admission in v0.1.2. Keep rollout gates inline:

```yaml
apiVersion: kapro.io/v1alpha2
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
```

## Operators

| Operator | Passes when | Fails when | Pending when |
|---|---|---|---|
| `ALL` | every operand passed | any operand failed | at least one operand is pending |
| `ANY` | any operand passed | every operand failed | no operand passed and at least one is pending |
| `NOT` | the single operand failed | the single operand passed | the operand is pending |
| `WEIGHTED_SUM` | passed weights sum to more than `threshold` | even all non-failed operands cannot exceed `threshold` | the final sum still depends on pending operands |
| `THRESHOLD` | at least `threshold` operands passed | too many operands failed to reach `threshold` | the final count still depends on pending operands |
| `DELAY` | `parameters.duration` has elapsed, then the operand passes | `parameters.duration` has elapsed, then the operand fails | the delay window or operand is still pending |

`DELAY` stores `status.firstObservedAt` the first time the controller evaluates
the expression, then mirrors its single operand once the duration has elapsed.

Inline gate operands remain `Pending` in `GateExpression.status` because only
the target runtime has enough context to evaluate a real gate. Referenced child
`GateExpression` objects with `Passed`, `Failed`, or `Pending` status drive the
parent expression status.

`Plan.spec.stages[].gate.expressionRef` is present in the schema for forward
compatibility, but Plan admission rejects it because `Target` reconciliation
does not enforce referenced expressions in v0.1.2.
