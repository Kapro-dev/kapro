# 01 Source Defaults

Add the registry once, then let each unit inherit `repo` and
`targetNamespace` from `source.defaults`.

```text
registry + defaults -> unit inherits source settings
```

Apply from the repository root:

```bash
kubectl apply -f examples/00-deliveryunit-lessons/01-source-defaults/
```
