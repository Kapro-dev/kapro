# 07 Promote Per-Unit Versions

Use `spec.versions` when each unit needs its own target version.

```text
Promotion.versions.web    -> web unit
Promotion.versions.worker -> worker unit
```

Apply from the repository root:

```bash
kubectl apply -f examples/00-deliveryunit-lessons/07-promote-per-unit-versions/
```
