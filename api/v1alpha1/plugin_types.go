// PluginRegistration CRD: declares external actuator/gate/planner plugin
// endpoints registered with Kapro's extension contracts.
package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ---- PluginRegistration -----------------------------------------------------

// PluginType identifies which Kapro extension contract a plugin implements.
// +kubebuilder:validation:Enum=actuator;gate;planner
type PluginType string

const (
	// PluginTypeActuator registers an implementation of the Kapro Actuator Interface.
	PluginTypeActuator PluginType = "actuator"
	// PluginTypeGate registers an implementation of the Kapro Gate Interface.
	PluginTypeGate PluginType = "gate"
	// PluginTypePlanner registers an implementation of the Kapro Planner Interface.
	PluginTypePlanner PluginType = "planner"
)

// PluginProtocol identifies how Kapro talks to a registered plugin.
// +kubebuilder:validation:Enum=grpc
type PluginProtocol string

const (
	// PluginProtocolGRPC uses the KAI/KGI/KPI gRPC contracts.
	PluginProtocolGRPC PluginProtocol = "grpc"
)

// PluginRegistrationSpec registers an external actuator, gate, or planner plugin endpoint.
// Runtime dispatch is an opt-in preview enabled with KAPRO_ENABLE_PLUGIN_GATEWAY=true.
type PluginRegistrationSpec struct {
	// Type selects which extension contract the plugin implements.
	Type PluginType `json:"type"`
	// Name is the registry key exposed by this plugin, for example "argo/pull"
	// or "slo".
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
	// Protocol selects the wire protocol.
	// +kubebuilder:default="grpc"
	Protocol PluginProtocol `json:"protocol,omitempty"`
	// Endpoint is the plugin endpoint URI, for example
	// dns:///argocd-actuator.kapro-system.svc:9090.
	// +kubebuilder:validation:MinLength=1
	Endpoint string `json:"endpoint"`
	// Timeout bounds one plugin call.
	// +kubebuilder:default="10s"
	Timeout string `json:"timeout,omitempty"`
	// TLSSecretRef references a Secret containing client TLS material or CA data.
	// Cluster-scoped registrations must include the Secret namespace.
	// +optional
	TLSSecretRef *corev1.SecretReference `json:"tlsSecretRef,omitempty"`
	// Parameters are plugin-specific key-value pairs.
	// Kapro core does not interpret unknown parameters.
	// +optional
	Parameters map[string]string `json:"parameters,omitempty"`
}

// PluginRegistrationStatus records plugin discovery and readiness.
type PluginRegistrationStatus struct {
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// Ready indicates whether the plugin endpoint is currently usable.
	Ready bool `json:"ready,omitempty"`
	// LastSeen is the RFC3339 time of the last successful health or capability check.
	LastSeen string `json:"lastSeen,omitempty"`
	// Version is the plugin-reported implementation version.
	Version string `json:"version,omitempty"`
	// ContractVersion is the plugin-reported KAI, KGI, or KPI contract version.
	ContractVersion string `json:"contractVersion,omitempty"`
	// Capabilities are plugin-reported feature names.
	Capabilities []string `json:"capabilities,omitempty"`
	// SchemaHash is a sha256 of (contractVersion + sorted capabilities). The
	// reconciler uses it to detect schema drift between hot-reloads of the same
	// PluginRegistration: when the plugin reports a different set of
	// capabilities or contract version than the previously-stored hash, a
	// SchemaChanged condition is surfaced and an event emitted so operators
	// can notice silent breaking changes from plugin upgrades.
	// +optional
	SchemaHash string `json:"schemaHash,omitempty"`
	// Conditions summarize plugin registration readiness.
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=pluginreg,categories=kapro-all
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.spec.type`
// +kubebuilder:printcolumn:name="Name",type=string,JSONPath=`.spec.name`
// +kubebuilder:printcolumn:name="Protocol",type=string,JSONPath=`.spec.protocol`
// +kubebuilder:printcolumn:name="Ready",type=boolean,JSONPath=`.status.ready`
// +kubebuilder:printcolumn:name="Version",type=string,JSONPath=`.status.version`,priority=1
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// PluginRegistration declares an external actuator, gate, or planner plugin endpoint.
// It is an API preview. Runtime registration is opt-in and hot-loaded after
// readiness probes succeed.
type PluginRegistration struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              PluginRegistrationSpec   `json:"spec,omitempty"`
	Status            PluginRegistrationStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type PluginRegistrationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PluginRegistration `json:"items"`
}
