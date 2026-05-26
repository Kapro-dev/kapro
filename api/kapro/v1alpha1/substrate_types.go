// Substrate CRD: selectable delivery substrate registration with
// optional native-object discovery for migration scenarios.
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ---- Substrate ---------------------------------------------------------

// SubstrateSpec registers a delivery substrate profile that can be selected
// by Fleet or Cluster spec.delivery.ref.
// +kubebuilder:validation:XValidation:rule="!has(self.configRef) || has(self.classRef)",message="configRef requires classRef"
// +kubebuilder:validation:XValidation:rule="has(self.classRef)",message="classRef is required"
type SubstrateSpec struct {
	// ClassRef selects the SubstrateClass that owns the typed substrate config
	// contract for this substrate profile.
	ClassRef *SubstrateClassReference `json:"classRef,omitempty"`
	// ConfigRef points at a typed substrate-owned configuration object, such as
	// ArgoCDSubstrateConfig, KubernetesApplyConfig, or OCIBundleApplyConfig.
	// The referenced kind must be accepted by the selected SubstrateClass.
	// +optional
	ConfigRef *SubstrateObjectReference `json:"configRef,omitempty"`
	// Execution selects the delivery topology for this substrate profile.
	// +optional
	Execution *SubstrateExecutionSpec `json:"execution,omitempty"`
	// PluginRef references a Plugin for external substrate implementations.
	// +optional
	PluginRef string `json:"pluginRef,omitempty"`
	// Discovery configures optional adoption of objects already owned by the
	// substrate, for example Argo CD cluster Secrets and Applications.
	// +optional
	Discovery *SubstrateDiscoverySpec `json:"discovery,omitempty"`
	// Parameters are substrate-specific defaults inherited by selected delivery
	// configs unless overridden there.
	// +optional
	Parameters map[string]string `json:"parameters,omitempty"`
}

// SubstrateDiscoverySpec configures substrate-native discovery for migration.
type SubstrateDiscoverySpec struct {
	// Suspended pauses substrate-native discovery. When the discovery block is
	// present and suspended is false, discovery is active.
	// +optional
	Suspended bool `json:"suspended,omitempty"`
	// ManagementPolicy controls whether Fleet only observes discovered objects
	// or may adopt them for promotion writes.
	// +kubebuilder:validation:Enum=Observe;Adopt
	// +kubebuilder:default="Observe"
	// +optional
	ManagementPolicy string `json:"managementPolicy,omitempty"`
	// Selector limits which substrate-native objects Fleet discovers. For Argo CD
	// this selects Applications and cluster Secrets. For Flux this selects
	// Kustomizations and HelmReleases.
	// +optional
	Selector *metav1.LabelSelector `json:"selector,omitempty"`
	// MaxObjects bounds each substrate-native list call during discovery. When a
	// list returns more objects than this limit, discovery fails closed and asks
	// the user to narrow the selector. Defaults to 1000.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=1000
	// +optional
	MaxObjects int32 `json:"maxObjects,omitempty"`
}

// Active reports whether substrate-native discovery should run.
func (d *SubstrateDiscoverySpec) Active() bool {
	return d != nil && !d.Suspended
}

// SubstrateStatus records substrate discovery and compatibility.
type SubstrateStatus struct {
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	Ready              bool  `json:"ready,omitempty"`
	// ClassName mirrors spec.classRef.name when this Substrate uses the typed
	// SubstrateClass path.
	// +optional
	ClassName string `json:"className,omitempty"`
	// ConfigRef mirrors the resolved typed substrate config reference.
	// +optional
	ConfigRef *SubstrateObjectReference    `json:"configRef,omitempty"`
	Substrate *SubstrateImplementationSpec `json:"substrate,omitempty"`
	Execution *SubstrateExecutionSpec      `json:"execution,omitempty"`
	// LastDiscoveryTime records when substrate-native discovery last completed or
	// failed for this profile.
	// +optional
	LastDiscoveryTime *metav1.Time `json:"lastDiscoveryTime,omitempty"`
	// DiscoveredClusters is the number of substrate-native clusters seen during
	// discovery, for example Argo CD cluster Secrets.
	// +optional
	DiscoveredClusters int32 `json:"discoveredClusters,omitempty"`
	// DiscoveredApplications is the number of substrate-native applications seen
	// during discovery.
	// +optional
	DiscoveredApplications int32 `json:"discoveredApplications,omitempty"`
	// DiscoveredApplicationSets is the number of Argo CD ApplicationSets seen
	// during discovery.
	// +optional
	DiscoveredApplicationSets int32 `json:"discoveredApplicationSets,omitempty"`
	// SelectedObjects is a bounded sample of substrate-native objects Fleet can
	// map to promotion units under the current discovery selector.
	// +optional
	SelectedObjects []DiscoveredSubstrateObject `json:"selectedObjects,omitempty"`
	// SkippedObjects is a bounded sample of substrate-native objects Fleet saw
	// but will not promote directly.
	// +optional
	SkippedObjects []DiscoveredSubstrateObject `json:"skippedObjects,omitempty"`
	// UnsupportedPatterns is a bounded sample of objects that matched discovery
	// but need a different ownership level or an external substrate plugin.
	// +optional
	UnsupportedPatterns []DiscoveredSubstrateObject `json:"unsupportedPatterns,omitempty"`
	// DiscoveryErrors is a bounded sample of non-fatal discovery errors. Fatal
	// errors are also surfaced through the DiscoveryReady condition.
	// +optional
	DiscoveryErrors []string           `json:"discoveryErrors,omitempty"`
	Conditions      []metav1.Condition `json:"conditions,omitempty"`
}

// DiscoveredSubstrateObject describes one substrate-native object found during
// Substrate discovery. The controller keeps this as bounded status
// evidence; counts remain the source of truth for fleet scale.
type DiscoveredSubstrateObject struct {
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
	// Pattern identifies the substrate-native topology pattern, for example
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
// +kubebuilder:resource:scope=Cluster,shortName=sub,categories=kapro-all
// +kubebuilder:printcolumn:name="Class",type=string,JSONPath=`.spec.classRef.name`
// +kubebuilder:printcolumn:name="Execution",type=string,JSONPath=`.spec.execution.mode`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Substrate defines a selectable delivery substrate. Built-in substrates such
// as Flux and Argo are first-party adapters behind this same profile contract.
type Substrate struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              SubstrateSpec   `json:"spec,omitempty"`
	Status            SubstrateStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type SubstrateList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Substrate `json:"items"`
}

// SubstrateKind returns the canonical delivery domain for this Substrate.
func (s SubstrateSpec) SubstrateKind() string {
	if s.ClassRef != nil && s.ClassRef.Name != "" {
		return s.ClassRef.Name
	}
	return ""
}

// ActuatorName returns the canonical actuator implementation name.
func (s SubstrateSpec) ActuatorName() string {
	return s.SubstrateKind()
}

// ExecutionMode returns the canonical delivery topology.
func (s SubstrateSpec) ExecutionMode() ExecutionMode {
	if s.Execution != nil && s.Execution.Mode != "" {
		return s.Execution.Mode
	}
	switch s.SubstrateKind() {
	case string(SubstrateKindFlux), string(SubstrateKindOCI):
		return ExecutionModeSpokePull
	case string(SubstrateKindExternal):
		return ExecutionModeExternalPull
	default:
		return ExecutionModeHubPush
	}
}

// CanonicalSubstrate returns a normalized substrate view for spec or status.
func (s SubstrateSpec) CanonicalSubstrate() *SubstrateImplementationSpec {
	kind := s.SubstrateKind()
	if kind == "" {
		return nil
	}
	return &SubstrateImplementationSpec{Kind: kind, Actuator: s.ActuatorName()}
}

// CanonicalExecution returns a normalized execution view for spec or status.
func (s SubstrateSpec) CanonicalExecution() *SubstrateExecutionSpec {
	mode := s.ExecutionMode()
	if mode == "" {
		return nil
	}
	return &SubstrateExecutionSpec{Mode: mode}
}
