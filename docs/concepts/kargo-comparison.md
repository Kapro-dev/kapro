# Kapro and Kargo

Kargo is a strong GitOps promotion workflow platform with deep Argo CD
integration. It tracks releases, maps them to stages, and orchestrates how
versions move through those stages.

Kapro's direction is a substrate-neutral promotion control plane. Kapro decides
whether a version may move, records why, and delegates delivery to a substrate.

Today, Kapro ships production built-ins for Argo CD, Flux, and OCI pull
delivery. The open substrate API also supports custom in-process actuators and
gRPC plugins. Example actuators such as `hello-world` demonstrate the contract;
they are not production-hardened delivery backends.

The distinction is:

| Question | Kapro answer |
| --- | --- |
| What decides whether promotion is allowed? | Kapro gates, approvals, policy, and decision traces. |
| What performs delivery? | A substrate actuator. |
| Must delivery be GitOps? | No. GitOps is one supported substrate family. |
| Can a platform team add its own substrate? | Yes, through the Go actuator SDK or gRPC plugin contract. |

That makes Kapro useful for Argo CD and Flux users without making GitOps the
only delivery model.
