// Backend CRD: selectable delivery backend registration with
// optional native-object discovery for migration scenarios.
package v1alpha2

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ---- Backend ---------------------------------------------------------

// BackendSpec registers a delivery backend profile that can be selected
// by Fleet or Cluster delivery.backendRef.
// +kubebuilder:validation:XValidation:rule="!(has(self.driver) && has(self.substrate))",message="set either deprecated driver/adapter/runtime or new substrate/execution, not both"
// +kubebuilder:validation:XValidation:rule="!(has(self.classRef) && (has(self.driver) || has(self.substrate)))",message="set either classRef, substrate, or deprecated driver, not multiple backend selection shapes"
// +kubebuilder:validation:XValidation:rule="!(has(self.classRef) && has(self.adapter) && self.adapter != \"\")",message="classRef cannot be combined with deprecated adapter"
// +kubebuilder:validation:XValidation:rule="!has(self.configRef) || has(self.classRef)",message="configRef requires classRef"
// +kubebuilder:validation:XValidation:rule="has(self.driver) || has(self.substrate) || has(self.classRef)",message="one of classRef, deprecated driver, or substrate is required"
// +kubebuilder:validation:XValidation:rule="(has(self.driver) && self.driver == \"external\") ? (has(self.pluginRef) && self.pluginRef != \"\") : true",message="pluginRef must be set when deprecated driver is external"
type BackendSpec struct {
	// ClassRef selects the SubstrateClass that owns the typed substrate config
	// contract for this backend profile. This is the preferred Phase-1
	// substrate API; existing Backend.spec.substrate and deprecated
	// driver/adapter/runtime fields remain as compatibility paths.
	// +optional
	ClassRef *SubstrateClassReference `json:"classRef,omitempty"`
	// ConfigRef points at a typed substrate-owned configuration object, such as
	// ArgoCDSubstrateConfig, KubernetesApplyConfig, or WebhookSubstrateConfig.
	// The referenced kind must be accepted by the selected SubstrateClass.
	// +optional
	ConfigRef *SubstrateObjectReference `json:"configRef,omitempty"`
	// Substrate identifies the open delivery domain and actuator implementation.
	// New Backend objects should use substrate instead of driver/adapter.
	// +optional
	Substrate *BackendSubstrateSpec `json:"substrate,omitempty"`
	// Execution selects the delivery topology for this backend profile.
	// New Backend objects should use execution instead of runtime.
	// +optional
	Execution *BackendExecutionSpec `json:"execution,omitempty"`
	// Driver identifies the backend implementation family.
	//
	// Deprecated compatibility field: use spec.substrate.kind. This field will
	// be removed in v0.5.20.
	// +optional
	Driver BackendDriver `json:"driver,omitempty"`
	// Adapter explicitly names the adapter implementation.
	//
	// Deprecated compatibility field: use spec.substrate.actuator. This field
	// will be removed in v0.5.20.
	// +optional
	Adapter string `json:"adapter,omitempty"`
	// Runtime identifies where this backend can run.
	//
	// Deprecated compatibility field: use spec.execution.mode. This field will
	// be removed in v0.5.20.
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
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	Ready              bool  `json:"ready,omitempty"`
	// ClassName mirrors spec.classRef.name when this Backend uses the typed
	// SubstrateClass path.
	// +optional
	ClassName string `json:"className,omitempty"`
	// ConfigRef mirrors the resolved typed substrate config reference.
	// +optional
	ConfigRef *SubstrateObjectReference `json:"configRef,omitempty"`
	Substrate *BackendSubstrateSpec     `json:"substrate,omitempty"`
	Execution *BackendExecutionSpec     `json:"execution,omitempty"`
	// Driver mirrors the deprecated spec.driver compatibility field.
	//
	// Deprecated compatibility field: use status.substrate.kind.
	Driver BackendDriver `json:"driver,omitempty"`
	// Runtime mirrors the deprecated spec.runtime compatibility field.
	//
	// Deprecated compatibility field: use status.execution.mode.
	Runtime BackendRuntime `json:"runtime,omitempty"`
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
// +kubebuilder:printcolumn:name="Class",type=string,JSONPath=`.spec.classRef.name`
// +kubebuilder:printcolumn:name="Substrate",type=string,JSONPath=`.spec.substrate.kind`
// +kubebuilder:printcolumn:name="Execution",type=string,JSONPath=`.spec.execution.mode`
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

// SubstrateKind returns the canonical delivery domain for this Backend.
func (s BackendSpec) SubstrateKind() string {
	if s.Substrate != nil && s.Substrate.Kind != "" {
		return s.Substrate.Kind
	}
	if s.ClassRef != nil && s.ClassRef.Name != "" {
		return s.ClassRef.Name
	}
	return string(s.Driver)
}

// ActuatorName returns the canonical actuator implementation name.
func (s BackendSpec) ActuatorName() string {
	if s.Substrate != nil && s.Substrate.Actuator != "" {
		return s.Substrate.Actuator
	}
	if s.Adapter != "" {
		return s.Adapter
	}
	return DefaultActuatorForSubstrate(s.SubstrateKind())
}

// ExecutionMode returns the canonical delivery topology.
func (s BackendSpec) ExecutionMode() ExecutionMode {
	if s.Execution != nil && s.Execution.Mode != "" {
		return s.Execution.Mode
	}
	switch s.Runtime {
	case BackendRuntimeSpoke:
		return ExecutionModeSpokePull
	case BackendRuntimeHub:
		return ExecutionModeHubPush
	}
	switch s.SubstrateKind() {
	case string(BackendDriverFlux), string(BackendDriverOCI):
		return ExecutionModeSpokePull
	case string(BackendDriverExternal):
		return ExecutionModeExternalPull
	default:
		return ExecutionModeHubPush
	}
}

// CanonicalSubstrate returns a normalized substrate view for spec or status.
func (s BackendSpec) CanonicalSubstrate() *BackendSubstrateSpec {
	kind := s.SubstrateKind()
	if kind == "" {
		return nil
	}
	return &BackendSubstrateSpec{Kind: kind, Actuator: s.ActuatorName()}
}

// CanonicalExecution returns a normalized execution view for spec or status.
func (s BackendSpec) CanonicalExecution() *BackendExecutionSpec {
	mode := s.ExecutionMode()
	if mode == "" {
		return nil
	}
	return &BackendExecutionSpec{Mode: mode}
}

// DefaultActuatorForSubstrate returns the built-in actuator default for a
// documented substrate kind. Unknown custom substrates resolve to their kind.
func DefaultActuatorForSubstrate(kind string) string {
	switch kind {
	case string(BackendDriverArgo):
		return "argo-cd"
	case string(BackendDriverFlux):
		return "flux"
	case string(BackendDriverOCI):
		return "oci"
	default:
		return kind
	}
}
