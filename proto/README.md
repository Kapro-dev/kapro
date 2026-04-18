# Kapro gRPC Contracts

Kapro defines 9 gRPC service contracts — the extension points of the promotion engine.
Analogous to Kubernetes CRI/CSI/CNI: Kapro owns the contracts, implementations are pluggable.

| Contract | Built-in implementation | CNCF ecosystem |
|---|---|---|
| ActuatorService | Flux (fluxcd/pkg/oci+ssa) | Flux CD, ArgoCD |
| GateService | Prometheus + Soak + Approval | KEDA (60+ scalers), Chaos Mesh, OPA, Falco |
| OCIService | oras.land/oras-go/v2 | ORAS, Harbor, Zot |
| HealthService | argoproj/gitops-engine/health | Argo ecosystem |
| NotificationService | argoproj/notifications-engine | Slack, Teams, PagerDuty, 12 more |
| VerificationService | sigstore/cosign/v2 | cosign, in-toto, Notation |
| ClusterTopologyService | ClusterRegistration CRDs | OCM, CAPI |
| PolicyService | OPA Rego bundle | OPA, Kyverno |
| TelemetryService | OpenTelemetry Go SDK | Any OTEL backend |

## Implementing an external plugin

Implement the proto service in any language, run it as a sidecar or separate deployment,
and register it via `PluginRegistration` CRD:

```yaml
apiVersion: kapro.io/v1alpha1
kind: PluginRegistration
metadata:
  name: my-custom-gate
spec:
  type: Gate
  endpoint: grpc://my-custom-gate-svc.kapro-system.svc.cluster.local:50051
```

Kapro will route gate evaluations to your plugin instead of the built-in implementation.
