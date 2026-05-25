# Programmable Gates

This example registers two in-process gate types:

- `canary-error-rate`
- `external-readiness`

They are ordinary Go functions adapted with `gate.Func`. Use this pattern when
the gate logic belongs in the same trust boundary as the operator.
