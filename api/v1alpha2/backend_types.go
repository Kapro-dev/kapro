// Backend CRD: selectable delivery backend registration with
// optional native-object discovery for migration scenarios.
package v1alpha2

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ---- Backend ---------------------------------------------------------

// BackendSpec registers a delivery backend profile that can be selected
// by Fleet or Cluster delivery.backendRef.
// +kubebuilder:validation:XValidation:rule="self.driver == \"external\" ? (has(self.pluginRef) && self.pluginRef != \"\") : (!has(self.pluginRef) || self.pluginRef == \"\")",message="pluginRef must be set when driver is external, and must be omitted otherwise"
type BackendSpec struct {
	// Driver identifies the backend implementation family.
	Driver BackendDriver `json:"driver"`
	// Adapter explicitly names the adapter implementation. When empty, Kapro
	// derives the built-in adapter name from driver for compatibility.
	// +optional
	Adapter string `json:"adapter,omitempty"`
	// Runtime identifies where this backend can run.
	// +kubebuilder:default="Both"
	Runtime BackendRuntime `json:"runtime,omitempty"`
	// PluginRef references a Plugin when driver=external.
	// +optional
	PluginRef string `json:"pluginRef,omitempty"`
	// Discovery configures optional adoption of objects already owned by the
	// backend, for example Argo CD cluster Secrets and Applications.
	// +optional
	Discovery *BackendDiscoverySpec `json:"discovery,omitempty"`
	// Parameters are backend-specific defaults inherited by selected delivery
	// configs unless overridden there.
	// +optional
	Parameters map[string]string `json:"parameters,omitempty"`
}

// BackendDiscoverySpec configures backend-native discovery for migration.
type BackendDiscoverySpec struct {
	// Enabled turns on backend-native discovery.
	// +optional
	Enabled bool `json:"enabled,omitempty"`
	// ManagementPolicy controls whether Fleet only observes discovered objects
	// or may adopt them for promotion writes.
	// +kubebuilder:validation:Enum=Observe;Adopt
	// +kubebuilder:default="Observe"
	// +optional
	ManagementPolicy string `json:"managementPolicy,omitempty"`
	// Selector limits which backend-native objects Fleet discovers. For Argo CD
	// this selects Applications and cluster Secrets. For Flux this selects
	// Kustomizations and HelmReleases.
	// +optional
	Selector *metav1.LabelSelector `json:"selector,omitempty"`
	// MaxObjects bounds each backend-native list call during discovery. When a
	// list returns more objects than this limit, discovery fails closed and asks
	// the user to narrow the selector. Defaults to 1000.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=1000
	// +optional
	MaxObjects int32 `json:"maxObjects,omitempty"`
}

// BackendStatus records backend discovery and compatibility.
type BackendStatus struct {
	ObservedGeneration int64          `json:"observedGeneration,omitempty"`
	Ready              bool           `json:"ready,omitempty"`
	Driver             BackendDriver  `json:"driver,omitempty"`
	Runtime            BackendRuntime `json:"runtime,omitempty"`
	// LastDiscoveryTime records when backend-native discovery last completed or
	// failed for this profile.
	// +optional
	LastDiscoveryTime *metav1.Time `json:"lastDiscoveryTime,omitempty"`
	// DiscoveredClusters is the number of backend-native clusters seen during
	// discovery, for example Argo CD cluster Secrets.
	// +optional
	DiscoveredClusters int32 `json:"discoveredClusters,omitempty"`
	// DiscoveredApplications is the number of backend-native applications seen
	// during discovery.
	// +optional
	DiscoveredApplications int32 `json:"discoveredApplications,omitempty"`
	// DiscoveredApplicationSets is the number of Argo CD ApplicationSets seen
	// during discovery.
	// +optional
	DiscoveredApplicationSets int32 `json:"discoveredApplicationSets,omitempty"`
	// SelectedObjects is a bounded sample of backend-native objects Fleet can
	// map to promotion units under the current discovery selector.
	// +optional
	SelectedObjects []DiscoveredBackendObject `json:"selectedObjects,omitempty"`
	// SkippedObjects is a bounded sample of backend-native objects Fleet saw
	// but will not promote directly.
	// +optional
	SkippedObjects []DiscoveredBackendObject `json:"skippedObjects,omitempty"`
	// UnsupportedPatterns is a bounded sample of objects that matched discovery
	// but need a different ownership level or an external backend plugin.
	// +optional
	UnsupportedPatterns []DiscoveredBackendObject `json:"unsupportedPatterns,omitempty"`
	// DiscoveryErrors is a bounded sample of non-fatal discovery errors. Fatal
	// errors are also surfaced through the DiscoveryReady condition.
	// +optional
	DiscoveryErrors []string           `json:"discoveryErrors,omitempty"`
	Conditions      []metav1.Condition `json:"conditions,omitempty"`
}

// DiscoveredBackendObject describes one backend-native object found during
// Backend discovery. The controller keeps this as bounded status
// evidence; counts remain the source of truth for fleet scale.
type DiscoveredBackendObject struct {
	// APIVersion is the discovered object's API version.
	// +optional
	APIVersion string `json:"apiVersion,omitempty"`
	// Kind is the discovered object's Kubernetes kind.
	// +optional
	Kind string `json:"kind,omitempty"`
	// Namespace is the discovered object's namespace.
	// +optional
	Namespace string `json:"namespace,omitempty"`
	// Name is the discovered object's name.
	// +optional
	Name string `json:"name,omitempty"`
	// Pattern identifies the backend-native topology pattern, for example
	// application, applicationset-child, app-of-apps-root, helmrelease, or
	// kustomization.
	// +optional
	Pattern string `json:"pattern,omitempty"`
	// Reason explains why the object was selected, skipped, or unsupported.
	// +optional
	Reason string `json:"reason,omitempty"`
	// Unit is the inferred Source unit name when available.
	// +optional
	Unit string `json:"unit,omitempty"`
	// VersionField is the field Fleet would write in Adopt mode when available.
	// +optional
	VersionField string `json:"versionField,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=be,categories=kapro-all
// +kubebuilder:printcolumn:name="Driver",type=string,JSONPath=`.spec.driver`
// +kubebuilder:printcolumn:name="Runtime",type=string,JSONPath=`.spec.runtime`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Backend defines a selectable delivery backend. Built-in backends such
// as Flux and Argo are first-party adapters behind this same profile contract.
type Backend struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              BackendSpec   `json:"spec,omitempty"`
	Status            BackendStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type BackendList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Backend `json:"items"`
}
