# 09 Trigger Guardrails

Add guardrails after the basic trigger works: cooldown, max active Promotions,
signature requirement, parameters, labels, and annotations.

```text
OCI tags -> signature check -> cooldown/maxActive -> Promotion metadata
```

Apply from the repository root:

```bash
docker run -d --restart=always -p 5001:5000 --name kapro-registry ghcr.io/project-zot/zot-linux-amd64:latest
echo "hello guarded trigger" > artifact.txt
oras push --plain-http localhost:5001/tutorial/hello-world:v0.4.0 \
  --artifact-type application/vnd.kapro.example \
  artifact.txt:text/plain
kubectl apply -f examples/00-deliveryunit-lessons/09-trigger-guardrails/
```

This example sets `requireSignature: true`; keep it suspended until a verifier
and signing flow are configured. Use lesson `08-safe-oci-trigger/` first for a
local unsigned artifact.
