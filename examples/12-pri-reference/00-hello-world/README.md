# 00 Hello World PRI Contract

This is the smallest Kapro PRI reference example.

It validates one portable PRI `Promotion` document:

```text
Promotion
  unit: hello
  artifact: hello:v1.0.0
  target: dev
```

No Kubernetes cluster, OCI registry, Flux, or Argo CD installation is required.
This example proves the document contract first. Runtime collection is shown in
the documentation after a real Kapro `PromotionRun` exists.

## Run

```bash
./examples/12-pri-reference/00-hello-world/run.sh
```

The script runs:

```bash
go run ./cmd/kapro pri validate examples/12-pri-reference/00-hello-world
go run ./cmd/kapro pri profile
```

## Expected Result

The validator reports one valid `Promotion` document. The profile command prints
Kapro's `Binding/kapro-reference` and
`ConformanceProfile/kapro-reference`.

## Files

| File | Purpose |
|---|---|
| `promotion.yaml` | Minimal portable PRI Promotion |
| `run.sh` | Executable validation entrypoint |
