// SubstrateClass CRD: cluster-scoped substrate capability class.
package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// SubstrateClassSpec declares the controller binding for one substrate class.
type SubstrateClassSpec struct {
	// ControllerName identifies the controller or plugin responsible for
	// Substrates that select this class. It follows the GatewayClass
	// controllerName convention: a domain-prefixed path such as
	// kapro.io/argo or example.com/company-deployer.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	ControllerName string `json:"controllerName"`
	// ExecutionModes declares admin intent for this class. Supported modes are
	// reported by the responsible controller in status.executionModes.supported.
	// +optional
	ExecutionModes *SubstrateClassExecutionModesSpec `json:"executionModes,omitempty"`
}

// SubstrateClassExecutionModesSpec declares class-level execution defaults.
type SubstrateClassExecutionModesSpec struct {
	// Default is the execution mode used when a Substrate selecting this class
	// omits spec.execution.mode. The controller must report support for this
	// mode in status before Substrates can become Ready.
	// +kubebuilder:default="hub-push"
	// +optional
	Default ExecutionMode `json:"default,omitempty"`
}

// SubstrateClassStatus reports controller-observed substrate capabilities.
type SubstrateClassStatus struct {
	// ObservedGeneration is the SubstrateClass generation last processed by the
	// responsible controller.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// ExecutionModes reports the execution modes the controller actually
	// supports for this class.
	// +optional
	ExecutionModes *SubstrateClassExecutionModesStatus `json:"executionModes,omitempty"`
	// AcceptedConfigKinds lists typed substrate configuration kinds this class
	// accepts through Substrate.spec.configRef.
	// +optional
	AcceptedConfigKinds []SubstrateObjectKindReference `json:"acceptedConfigKinds,omitempty"`
	// Capabilities reports what the responsible controller can do.
	// +optional
	Capabilities *SubstrateCapabilities `json:"capabilities,omitempty"`
	// Conditions summarize class acceptance and controller health.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// SubstrateClassExecutionModesStatus reports controller-supported execution modes.
type SubstrateClassExecutionModesStatus struct {
	// Supported contains the execution modes the controller can actually run.
	// +optional
	Supported []ExecutionMode `json:"supported,omitempty"`
}

// SubstrateCapabilities reports class-level KSI capabilities.
type SubstrateCapabilities struct {
	// Operations reports supported delivery operations.
	// +optional
	Operations *SubstrateOperationCapabilities `json:"operations,omitempty"`
	// Staging reports supported staging semantics.
	// +optional
	Staging *SubstrateStagingCapabilities `json:"staging,omitempty"`
	// InputTypes names accepted version input shapes such as git-revision,
	// raw-yaml, kustomize, helm-chart, model-uri, or webhook-payload.
	// +optional
	InputTypes []string `json:"inputTypes,omitempty"`
	// Attributes carries stable, controller-reported capability attributes used
	// by future selector grammar. Keys are lowercase DNS-label style names;
	// values are strings so controllers can publish booleans, numbers, and
	// enums without changing the CRD schema while Kapro is pre-stable.
	// +optional
	Attributes map[string]string `json:"attributes,omitempty"`
}

// SubstrateOperationCapabilities reports operation support bits.
type SubstrateOperationCapabilities struct {
	// Apply means the substrate can move a target toward desired versions.
	Apply bool `json:"apply,omitempty"`
	// Observe means the substrate can report convergence without changing
	// substrate state.
	Observe bool `json:"observe,omitempty"`
	// DryRun means the substrate can validate an operation without persisting
	// substrate changes.
	DryRun bool `json:"dryRun,omitempty"`
	// Rollback means the substrate has a direct rollback operation.
	Rollback bool `json:"rollback,omitempty"`
	// Discover means the substrate can discover substrate-native objects for
	// existing GitOps adoption.
	Discover bool `json:"discover,omitempty"`
}

// SubstrateStagingCapabilities reports staged delivery support bits.
type SubstrateStagingCapabilities struct {
	// TwoPhase means the substrate can prepare, commit, and discard staged work.
	TwoPhase bool `json:"twoPhase,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=subclass,categories=kapro-all
// +kubebuilder:printcolumn:name="Controller",type=string,JSONPath=`.spec.controllerName`
// +kubebuilder:printcolumn:name="Default",type=string,JSONPath=`.spec.executionModes.default`
// +kubebuilder:printcolumn:name="Accepted",type=string,JSONPath=`.status.conditions[?(@.type=="Accepted")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// SubstrateClass declares one Kapro substrate class. Platform teams author or
// install classes; controllers report accepted config kinds, execution modes,
// and capabilities in status.
type SubstrateClass struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              SubstrateClassSpec   `json:"spec,omitempty"`
	Status            SubstrateClassStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type SubstrateClassList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SubstrateClass `json:"items"`
}
