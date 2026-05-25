# Quickstarts

Complete substrate-specific examples. Start with Flux unless you already know
which execution path you want.

```text
DeliveryUnit + Plan + Fleet + Promotion
              |
              v
        chosen substrate
```

| Folder | Use When |
|---|---|
| `00-flux/` | Default GitOps quickstart |
| `01-direct/` | Direct Kubernetes apply without GitOps |
| `02-argo/` | Argo CD owns application sync |
| `03-oci/` | Spokes pull OCI bundle artifacts |

Validate quickstart YAML with:

```bash
scripts/validate-yaml-json
go test ./internal/lint
```
