# DeliveryUnit Public Preview Examples

These examples are ordered like a programming-language tutorial. Each folder
adds one concept and keeps the previous concepts visible in the YAML, so users
can diff neighboring lessons and see what changed.

| Lesson | Concept |
|---|---|
| `00-hello-world/` | Smallest useful DeliveryUnit |
| `01-source-defaults/` | Registry and source defaults |
| `02-rollout-defaults/` | Default Fleet and Plan references |
| `03-promote-one-version/` | Explicit Promotion action |
| `04-two-units/` | Multiple deployable units |
| `05-order-units/` | Wave and dependency ordering |
| `06-environment-overrides/` | Environment-specific values |
| `07-promote-per-unit-versions/` | Different versions per unit |
| `08-safe-oci-trigger/` | Suspended, dry-run OCI trigger |
| `09-trigger-guardrails/` | Cooldown, signatures, metadata, and parameters |

Apply each folder independently while learning the API:

```bash
kubectl apply -f examples/00-deliveryunit-lessons/00-hello-world/
kubectl apply -f examples/00-deliveryunit-lessons/01-source-defaults/
kubectl apply -f examples/00-deliveryunit-lessons/02-rollout-defaults/
kubectl apply -f examples/00-deliveryunit-lessons/03-promote-one-version/
kubectl apply -f examples/00-deliveryunit-lessons/04-two-units/
kubectl apply -f examples/00-deliveryunit-lessons/05-order-units/
kubectl apply -f examples/00-deliveryunit-lessons/06-environment-overrides/
kubectl apply -f examples/00-deliveryunit-lessons/07-promote-per-unit-versions/
kubectl apply -f examples/00-deliveryunit-lessons/08-safe-oci-trigger/
kubectl apply -f examples/00-deliveryunit-lessons/09-trigger-guardrails/
```

The trigger example is intentionally suspended and dry-run by default. Change
those fields only after the registry, signature, Fleet, and Plan policy are
ready for real automatic promotion intent.

Artifact inputs by lesson:

| Lessons | Input |
|---|---|
| `00` to `07` | YAML authoring examples only; no registry is required to read or validate them |
| `08` and `09` | OCI tags if you enable the Trigger; keep suspended for inspection |

Use ORAS with a local registry for the OCI trigger lessons:

```bash
docker run -d --restart=always -p 5001:5000 --name kapro-registry ghcr.io/project-zot/zot-linux-amd64:latest
echo "hello trigger" > artifact.txt
oras push --plain-http localhost:5001/tutorial/hello-world:v0.4.0 \
  --artifact-type application/vnd.kapro.example \
  artifact.txt:text/plain
```

Validate the lesson set locally:

```bash
go test ./examples/00-deliveryunit-lessons
scripts/validate-yaml-json
```

## Run This Example

Every example has a local runner. Start with the safe check command; this is also the path exercised by CI through `make check-examples`:

```bash
examples/00-deliveryunit-lessons/run.sh
```

This directory is an index for smaller examples. Run a child folder next, for example:

```bash
examples/00-deliveryunit-lessons/00-hello-world/run.sh
```

## Expected Result

- `check` verifies this directory has its README and runnable script.
- Child example folders contain the concrete YAML, Go, or demo assets.

## Cleanup

No cluster resources are created by `check`. Stop any foreground `run` command with `Ctrl-C`.
