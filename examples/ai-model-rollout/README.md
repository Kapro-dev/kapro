# AI Model Progressive Delivery with Kapro

This example demonstrates how to use Kapro for **progressive delivery of AI/ML models** across 15+ country clusters using KServe and MLflow.

## What This Does

- CI builds the retail LLM model, pushes to OCI registry, creates an `Artifact` CR
- Kapro promotes the model through environments: **DE dev → DE prod → FR/FI prod → all countries**
- **MLflow gate** checks model accuracy ≥ 0.93 before any production promotion
- **Prometheus gate** checks p95 inference latency < 500ms
- Manual approval required before each production batch
- Auto-rollback on gate failure

## Architecture

```
CI Pipeline
    ↓ creates
Artifact (llm-retail-v2.1.0)
    ↓ triggers
Release → Pipeline (llm-retail-global-rollout)
    ↓
Promotion: ai-dev → ai-prod (sequential, per-country)
    ↓
Progression: pilot (de) → nordics (fr, fi) → global (all)
    ↓ KServe Actuator patches
InferenceService.spec.predictor.model.storageUri = oci://...v2.1.0
```

## Prerequisites

- Kapro operator running (`kubectl get pods -n kapro-system`)
- KServe installed on target clusters (`kubectl get crd inferenceservices.serving.kserve.io`)
- MLflow tracking server accessible from the operator pod
- Prometheus scraping KServe metrics

## Apply in Order

```bash
# 1. Register environments (once per cluster)
kubectl apply -f environments.yaml

# 2. Create promotion gates
kubectl apply -f gates.yaml

# 3. Create the pipeline template
kubectl apply -f pipeline.yaml

# 4. Register the artifact (done by CI in production)
kubectl apply -f artifact.yaml

# 5. Trigger the release
kubectl apply -f release.yaml
```

## Monitor the Rollout

```bash
# Watch overall release progress
kubectl get release llm-retail-v2.1.0 -n kapro-system -w

# Watch per-environment promotions
kubectl get promotions -n kapro-system -l kapro.io/release=llm-retail-v2.1.0

# See what's waiting for approval
kubectl get promotions -n kapro-system --field-selector=status.phase=WaitingApproval
```

## Approve a Production Promotion

```bash
# Via kubectl (creating an Approval object)
kubectl apply -f - <<EOF
apiVersion: relay.io/v1alpha1
kind: Approval
metadata:
  name: de-prod-llm-retail-v2.1.0
  namespace: kapro-system
spec:
  kind: Promotion
  ref: de-llm-prod
  release: llm-retail-v2.1.0
  approvedBy: alice
  comment: "MLflow accuracy 0.94 on 2h soak, p95 latency 320ms — LGTM"
EOF
```

## Ask Your AI Assistant

With the [Kapro MCP server](../../docs/mcp.md) enabled, you can ask Claude or Copilot:

> "What's the rollout status of llm-retail-v2.1.0?"
> "Which AI model promotions are waiting for approval?"
> "Approve the de-llm-prod promotion for llm-retail-v2.1.0 — accuracy is good"

## Why Kapro for AI Models?

| Challenge | Without Kapro | With Kapro |
|---|---|---|
| Model rollout across 15 countries | Manual scripts per cluster | Single Release CR |
| Accuracy gate before production | Custom CI scripts, fragile | MLflow gate built-in |
| Latency regression detection | Alerts AFTER rollout | Prometheus gate BEFORE promotion |
| Country-by-country rollout | Manual coordination | Wave batches with dependsOn |
| Emergency rollback | SSH to each cluster | One `Approval` with bypass:true |
| AI assistant visibility | None | MCP server — ask Claude |
