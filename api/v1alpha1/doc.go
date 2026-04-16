// Package v1alpha1 contains the Kapro API types.
//
// API group: kapro.io
// Version:   v1alpha1
//
// CRDs:
//   - Artifact           — immutable OCI bundle, digest-pinned
//   - Environment        — one cluster managed by Kapro
//   - ClusterRegistration — written by kapro-cluster-controller, read by operator
//   - PromotionPolicy    — reusable gate rules (soak, metrics, approval)
//   - Pipeline           — DAG of promotion steps + batch progression
//   - Release            — developer trigger, owns Pipeline
//   - Promotion          — single-cluster gate → apply → converge cycle
//   - BatchRun           — one batch of clusters from Pipeline.progression
//   - Approval           — human gate signal to unblock Promotion or BatchRun
//
// +kubebuilder:object:generate=true
// +groupName=kapro.io
package v1alpha1
