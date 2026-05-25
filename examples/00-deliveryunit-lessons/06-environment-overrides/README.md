# 06 Environment Overrides

Overrides keep common defaults in one place while changing values for a stage,
cluster list, or unit.

```text
base values + production selector override -> rendered unit values
```

Apply from the repository root:

```bash
kubectl apply -f examples/00-deliveryunit-lessons/06-environment-overrides/
```
