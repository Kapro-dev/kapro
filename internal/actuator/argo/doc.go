// Package argo implements the Kapro Actuator Interface for Argo CD push-mode
// delivery. The actuator patches Argo Application.spec.source.targetRevision
// (or per-app targetRevision fields when multiple artifacts are managed) and
// polls Application.status to detect convergence.
//
// Hub-side execution: the hub holds Argo Application objects in its own
// namespace (or in the workload cluster's namespace via Argo's multi-cluster
// destination). Kapro does not install Argo; it expects Argo to be already
// running and configured by the platform team.
//
// Non-goals: ApplicationSet generation, traffic shifting, Argo Rollouts
// integration. Those layers are owned by Argo itself; Kapro only orchestrates
// when and where a new revision should land.
package argo
