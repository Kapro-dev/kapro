// Package v1alpha1 contains the Kapro API types.
//
// API group: kapro.io
// Version:   v1alpha1
//
// Primary user-authored CRDs (KISS path):
//   - Kapro          — fleet entry point: source, delivery, clusters, plan
//   - PromotionRun   — promotion intent; usually created via `kapro promote`
//   - Approval       — human gate signal to unblock one target-cluster rollout or stage
//
// Optional/advanced user-authored CRDs:
//   - PromotionPlan  — reusable promotion template composed of ordered stages
//   - PromotionSource — reusable promotion unit catalog (shared across Kapro objects)
//   - PromotionTrigger — safe-by-default autonomous PromotionRun creation from verified artifacts
//   - PluginRegistration — external actuator, gate, and planner plugin registration
//   - AgentPolicy    — AI trust boundary and audit policy
//
// Controller-managed (observe; do not author directly):
//   - PromotionTarget — one target-cluster execution; owned by a PromotionRun
//   - FleetCluster   — fleet inventory and observed cluster state reported to the hub
//   - FleetClusterTemplate — fleet auto-import template
//   - BackendProfile — delivery backend configuration
//
// PromotionRuns are created by the CLI (`kapro promote`), users applying
// PromotionRun manifests directly, or PromotionTriggers. The Kapro controller
// does not generate PromotionRuns from spec changes on the Kapro object.
//
// API maturity, deprecation, schema compatibility, and upgrade expectations are
// documented in docs/api-stability.md.
//
// +kubebuilder:object:generate=true
// +groupName=kapro.io
package v1alpha1
