# Composable Gates

`GateExpression` is a preview API for naming and composing gate policies. The
first scaffold supports the `ALL` operator only: every referenced expression
must pass before the parent expression passes.

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

## v0.1.2 Limits

`ANY`, `NOT`, `WEIGHTED_SUM`, `THRESHOLD`, and `DELAY` are reserved for a later
release. The CRD enum only accepts `ALL` in v0.1.2, and admission returns a
clear reserved-operator message if a bypassed client submits another operator.

Inline gate operands remain `Pending` in `GateExpression.status` because only
the target runtime has enough context to evaluate a real gate. Referenced child
`GateExpression` objects with `Passed`, `Failed`, or `Pending` status drive the
parent expression status.

`Plan.spec.stages[].gate.expressionRef` is present in the schema for forward
compatibility, but Plan admission rejects it because `Target` reconciliation
does not enforce referenced expressions in v0.1.2.
