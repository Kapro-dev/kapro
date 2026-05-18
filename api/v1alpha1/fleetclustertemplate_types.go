// FleetClusterTemplate CRD and the per-cloud source branches used to
// auto-import clusters as FleetCluster objects.
package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ---- FleetClusterTemplate ---------------------------------------------------

// FleetClusterTemplate auto-imports clusters from any supported fleet source
// (cloud, on-prem, or RHACM/CAPI management cluster) as FleetCluster objects.
//
// This is the universal fleet-templating CRD — ArgoCD ApplicationSet-shaped.
// Exactly one branch of spec.source is set; the reconciler dispatches to a
// matching Discoverer implementation, and each discovered cluster becomes a
// FleetCluster carrying:
//   - ownerReference back to this template (garbage-collection on delete);
//   - label kapro.io/managed-by=fleetclustertemplate so the reconciler can
//     identify what it owns and never touch hand-authored FleetClusters;
//   - the spec from spec.template, plus a derived spec.provider.kind matching
//     the source branch (gcp → gcp-fleet, aws → eks, rhacm → rhacm, etc.).
//
// New cloud or platform support adds one Discoverer implementation and one
// branch on FleetClusterTemplateSource — no new CRD per cloud.
type FleetClusterTemplateSource struct {
	// GCP discovers memberships from GKE Fleet (Hub) API in a project.
	// Imported clusters get spec.provider.kind=gcp-fleet (Connect Gateway).
	// +optional
	GCP *GCPFleetSource `json:"gcp,omitempty"`

	// AWS discovers EKS clusters in an account/region.
	// Imported clusters get spec.provider.kind=eks.
	// Stub in v0.5; surfaced as a Stalled condition when set.
	// +optional
	AWS *AWSFleetSource `json:"aws,omitempty"`

	// Azure discovers AKS / Azure Arc-connected clusters in a subscription.
	// Imported clusters get spec.provider.kind=aks-arc.
	// Stub in v0.5.
	// +optional
	Azure *AzureFleetSource `json:"azure,omitempty"`

	// RHACM watches open-cluster-management.io ManagedCluster CRs on the
	// local hub. Imported clusters get spec.provider.kind=rhacm.
	// Stub in v0.5.
	// +optional
	RHACM *RHACMFleetSource `json:"rhacm,omitempty"`

	// CAPI watches cluster.x-k8s.io Cluster CRs on the management cluster
	// and pairs each with its kubeconfig Secret. Imported clusters get
	// spec.provider.kind=capi. Stub in v0.5.
	// +optional
	CAPI *CAPIFleetSource `json:"capi,omitempty"`

	// Static is an operator-supplied list with kubeconfig Secret references
	// for on-prem / bare-metal clusters with no Fleet API. Imported
	// clusters get spec.provider.kind=kubeconfig. Stub in v0.5.
	// +optional
	Static *StaticFleetSource `json:"static,omitempty"`
}

// GCPFleetSource configures GKE Fleet auto-import.
type GCPFleetSource struct {
	// Project is the GCP project whose Fleet memberships are imported.
	// +kubebuilder:validation:MinLength=1
	Project string `json:"project"`
}

// AWSFleetSource configures EKS auto-import. Stub in v0.5.
type AWSFleetSource struct {
	// Region is the AWS region to enumerate EKS clusters in.
	// +kubebuilder:validation:MinLength=1
	Region string `json:"region"`
	// AccountID optionally narrows discovery to one AWS account. When empty,
	// the hub identity's default account is used.
	// +optional
	AccountID string `json:"accountID,omitempty"`
}

// AzureFleetSource configures AKS / Arc auto-import. Stub in v0.5.
type AzureFleetSource struct {
	// SubscriptionID is the Azure subscription to enumerate clusters in.
	// +kubebuilder:validation:MinLength=1
	SubscriptionID string `json:"subscriptionID"`
	// ResourceGroup optionally narrows discovery to one resource group.
	// +optional
	ResourceGroup string `json:"resourceGroup,omitempty"`
}

// RHACMFleetSource configures Red Hat ACM ManagedCluster auto-import.
// Stub in v0.5.
type RHACMFleetSource struct {
	// Namespace is the namespace to watch ManagedCluster CRs in. When
	// empty, the cluster-scoped ManagedCluster API is used (default RHACM
	// deployment).
	// +optional
	Namespace string `json:"namespace,omitempty"`
}

// CAPIFleetSource configures Cluster API auto-import. Stub in v0.5.
type CAPIFleetSource struct {
	// Namespace is the namespace to watch CAPI Cluster CRs in. Empty means
	// all namespaces.
	// +optional
	Namespace string `json:"namespace,omitempty"`
}

// StaticFleetSource enumerates on-prem clusters via operator-supplied
// kubeconfig Secret references. Stub in v0.5.
type StaticFleetSource struct {
	// Clusters lists each on-prem cluster and its kubeconfig Secret.
	// +kubebuilder:validation:MinItems=1
	Clusters []StaticClusterEntry `json:"clusters"`
}

// StaticClusterEntry is one on-prem cluster in a StaticFleetSource.
type StaticClusterEntry struct {
	// Name is the FleetCluster name to create.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
	// KubeconfigSecretRef references a Secret with a "kubeconfig" key.
	// +optional
	KubeconfigSecretRef *corev1.SecretReference `json:"kubeconfigSecretRef,omitempty"`
	// Labels are merged onto the imported FleetCluster's metadata.labels.
	// +optional
	Labels map[string]string `json:"labels,omitempty"`
}

// FleetClusterTemplateSpec defines the desired state of a fleet-template.
type FleetClusterTemplateSpec struct {
	// Source selects exactly one discovery branch. Mis-set combinations
	// (none or multiple) are rejected at admission time.
	Source FleetClusterTemplateSource `json:"source"`

	// Selector filters the discovered cluster set by labels reported by the
	// source. Empty selector imports everything the source returns.
	// +optional
	Selector *metav1.LabelSelector `json:"selector,omitempty"`

	// Interval is how often the source is polled. Go duration format
	// ("5m", "1h"). Sources that use CRD watches (RHACM, CAPI) treat this
	// as a resync floor.
	// +kubebuilder:default="5m"
	// +optional
	Interval string `json:"interval,omitempty"`

	// Suspend pauses reconciliation. Existing imported FleetClusters are
	// left untouched.
	// +optional
	Suspend bool `json:"suspend,omitempty"`

	// Prune deletes imported FleetClusters whose source entry has
	// disappeared. Default false (conservative) — operator opts into the
	// spec §3.5 "full lifecycle" intent. Deletion runs the FleetCluster
	// finalizer so per-cluster RBAC is cleaned up.
	// +optional
	Prune bool `json:"prune,omitempty"`

	// Template is applied verbatim to each imported FleetCluster.
	// spec.provider.kind is derived from the source branch and ignored
	// here if set.
	Template FleetClusterTemplateBody `json:"template"`
}

// FleetClusterTemplateBody is the FleetCluster shape rendered for each
// discovered cluster. Mirrors corev1.PodTemplateSpec layout: metadata
// (labels/annotations) + spec.
type FleetClusterTemplateBody struct {
	// Metadata holds labels and annotations merged onto each imported
	// FleetCluster. Source-reported labels are layered underneath these
	// (template labels win on conflict).
	// +optional
	Metadata FleetClusterTemplateMetadata `json:"metadata,omitempty"`
	// Spec is the FleetClusterSpec applied to each imported cluster.
	// spec.provider.kind is derived from the source branch.
	Spec FleetClusterSpec `json:"spec"`
}

// FleetClusterTemplateMetadata is the subset of ObjectMeta supported in
// templates. Name is derived from the discovered cluster name.
type FleetClusterTemplateMetadata struct {
	// +optional
	Labels map[string]string `json:"labels,omitempty"`
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`
}

// FleetClusterTemplateStatus reports the observed state of the auto-import.
type FleetClusterTemplateStatus struct {
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// LastSyncTime is the timestamp of the last successful source poll.
	// +optional
	LastSyncTime *metav1.Time `json:"lastSyncTime,omitempty"`

	// DiscoveredClusters is the count returned by the source (pre selector).
	// +optional
	DiscoveredClusters int32 `json:"discoveredClusters,omitempty"`

	// ImportedClusters is the count of FleetCluster objects currently
	// owned by this template (post selector).
	// +optional
	ImportedClusters int32 `json:"importedClusters,omitempty"`

	// SourceKind echoes the active source branch (gcp, aws, rhacm, ...).
	// +optional
	SourceKind string `json:"sourceKind,omitempty"`

	// Conditions summarise template readiness (Ready, Stalled).
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=fct;fleettemplate,categories=kapro-all
// +kubebuilder:printcolumn:name="Source",type=string,JSONPath=`.status.sourceKind`
// +kubebuilder:printcolumn:name="Discovered",type=integer,JSONPath=`.status.discoveredClusters`
// +kubebuilder:printcolumn:name="Imported",type=integer,JSONPath=`.status.importedClusters`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="LastSync",type=date,JSONPath=`.status.lastSyncTime`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// FleetClusterTemplate is the universal fleet auto-import CRD. One template
// per cohort: one Fleet/Account/Subscription/ManagedClusterSet × one
// delivery shape. Imported FleetClusters carry ownerRef + managed-by label.
type FleetClusterTemplate struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              FleetClusterTemplateSpec   `json:"spec,omitempty"`
	Status            FleetClusterTemplateStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type FleetClusterTemplateList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []FleetClusterTemplate `json:"items"`
}

// FleetClusterTemplateManagedByLabel marks FleetCluster objects produced
// by a FleetClusterTemplate. The reconciler updates only objects carrying
// this label — absence means hand-authored, do not touch.
const FleetClusterTemplateManagedByLabel = "kapro.io/managed-by"

// FleetClusterTemplateManagedByValue is the value written to
// FleetClusterTemplateManagedByLabel by the template reconciler.
const FleetClusterTemplateManagedByValue = "fleetclustertemplate"

// FleetClusterTemplateNameLabel records which FleetClusterTemplate owns
// an imported FleetCluster. Useful for label-selecting all clusters from
// one template.
const FleetClusterTemplateNameLabel = "kapro.io/fleet-cluster-template"
