// Package v1alpha1 contains the Kapro API types.
//
// API group: kapro.io
// Version:   v1alpha1
//
// User-facing CRDs:
//   - Kapro          — fleet entry point
//   - FleetCluster   — fleet inventory and observed cluster state reported to the hub
//   - PromotionPlan  — reusable promotion template composed of ordered stages
//   - PromotionRun   — user-facing promotion intent and execution attempt
//   - PromotionTarget — one target-cluster execution owned by a PromotionRun
//   - PromotionTrigger — safe-by-default autonomous PromotionRun creation from verified artifacts
//   - PromotionSource — native promotion unit source
//   - PluginRegistration — external actuator, gate, and planner plugin registration
//   - Approval       — human gate signal to unblock one target-cluster rollout or stage
//   - AgentPolicy    — AI trust boundary and audit policy
//
// API maturity, deprecation, schema compatibility, and upgrade expectations are
// documented in docs/api-stability.md.
//
// +kubebuilder:object:generate=true
// +groupName=kapro.io
package v1alpha1
