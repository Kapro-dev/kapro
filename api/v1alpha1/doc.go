// Package v1alpha1 contains the Kapro API types.
//
// API group: kapro.io
// Version:   v1alpha1
//
// User-facing CRDs:
//   - Artifact       — immutable OCI bundle, digest-pinned
//   - Pipeline       — reusable rollout template composed of ordered stages
//   - Release        — one rollout execution of an Artifact through one or more Pipelines
//   - MemberCluster  — fleet inventory and observed cluster state reported to the hub
//   - Approval       — human gate signal to unblock one target-cluster rollout or stage
//   - BootstrapToken — short-lived token for first registration of spoke agents
//
// Delivery execution state is stored inline in Release.status.targets
// rather than in a standalone execution CRD.
//
// +kubebuilder:object:generate=true
// +groupName=kapro.io
package v1alpha1
