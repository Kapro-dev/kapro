// Package v1alpha1 contains the Kapro API types.
//
// API group: kapro.io
// Version:   v1alpha1
//
// User-facing CRDs:
//   - Artifact        — immutable OCI bundle, digest-pinned
//   - Environment     — one target cluster managed by Kapro
//   - Pipeline        — DAG of Stages; each Stage selects Environments by label selector
//   - Release         — developer trigger; owns a two-level DAG (Pipeline nodes → Stages → Environments)
//   - GatePolicy      — reusable gate rules (soak, metrics, approval, notification)
//   - GateTemplate    — reusable parameterised gate evaluation config (cel, job, webhook)
//   - ManagedCluster  — fleet registry entry, written by kapro-cluster-controller
//
// Internal / system CRDs:
//   - Sync          — one gate→apply→converge cycle per (Release, Pipeline, Stage, Environment)
//   - Approval      — human gate signal to unblock a Sync
//   - ReleaseReport — audit trail aggregated from all Syncs for a Release
//   - BootstrapToken — short-lived token for kapro-cluster-controller first registration
//
// +kubebuilder:object:generate=true
// +groupName=kapro.io
package v1alpha1
