# Interface Overview

Kapro has two extension paths:

- in-process Go interfaces compiled into a Kapro controller or cluster
  controller binary;
- out-of-process gRPC plugins reached through the plugin gateway.

They are complementary. The in-process interfaces are for first-party or tightly
coupled controller integrations. The gRPC interfaces are for independently
deployed plugins where the implementation should not link into the Kapro
process.

```text
kapro controller
  -> in-process registries
       KSI / KSP / Go actuator, gate, and planner packages
  -> plugin gateway
       KAI / KGI / KPI gRPC plugins
```

## Contracts

| Name | Contract | Location | Use when |
|---|---|---|---|
| KSI | Kapro Substrate Interface | `pkg/kapro/substrate` | You need a hub-side Go substrate implementation that validates, applies, and observes a `SubstrateClass`/`Substrate` pair. |
| KSP | Kapro Spoke Provider | `pkg/spokeprovider` | You need a spoke-side Go provider for pull or outbound cluster reconciliation in `kapro-cluster-controller`. |
| KAI | Kapro Actuator Interface | `spec/kai/v1alpha1` | You need an external gRPC actuator that applies, observes convergence, or rolls back one target. |
| KGI | Kapro Gate Interface | `spec/kgi/v1alpha1` | You need an external gRPC gate that decides whether one target may advance. |
| KPI | Kapro Planning Interface | `spec/kpi/v1alpha1` | You need an external gRPC planner that filters, orders, or defers eligible targets before binding. |

## Choosing an Interface

Implement KSI when the substrate is part of the hub controller process and
needs typed substrate class/config wiring.

Implement KSP when the integration runs on the workload cluster side and owns a
local reconciliation loop for one `SubstrateKind`.

Implement KAI when the delivery action should run out of process, such as a
team-owned deployment service or a proprietary delivery API.

Implement KGI when the safety decision should run out of process, such as a
metrics, compliance, or approval service.

Implement KPI when target selection needs custom ordering, capacity checks, or
defer/skip decisions before Kapro binds rollout work.

KSI and KAI are not duplicate CRDs. KSI models the in-process substrate contract
inside Kapro. KAI is the out-of-process actuator RPC contract reached through
the plugin gateway.

KSP and KAI are also different boundaries. KSP is spoke-side pull or outbound
reconciliation inside the cluster controller. KAI is an external hub-side
actuator plugin called by the Kapro controller.
