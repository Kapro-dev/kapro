// Package v1alpha1 contains the Kapro API types.
//
// API group: kapro.io
// Version:   v1alpha1
//
// User-facing CRDs:
//   - Kapro          — fleet entry point
//   - KaproApp       — application bundle template
//   - Pipeline       — reusable rollout template composed of ordered stages
//   - Release        — one rollout execution of an artifact version through one or more Pipelines
//   - ReleaseTrigger — safe-by-default autonomous Release creation from verified artifacts
//   - ReleaseTarget  — one target-cluster execution owned by a Release
//   - NotificationProvider — API-preview notification destination declaration
//   - NotificationPolicy   — API-preview notification subscription declaration
//   - MemberCluster  — fleet inventory and observed cluster state reported to the hub
//   - PluginRegistration — external actuator, gate, and planner plugin registration
//   - Approval       — human gate signal to unblock one target-cluster rollout or stage
//   - AgentPolicy    — AI trust boundary and audit policy
//
// Delivery execution state is stored inline in Release.status.targets
// rather than in a standalone execution CRD.
//
// +kubebuilder:object:generate=true
// +groupName=kapro.io
package v1alpha1
