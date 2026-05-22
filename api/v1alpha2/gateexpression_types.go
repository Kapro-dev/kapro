// GateExpression CRD: preview composition surface for reusable gates.
package v1alpha2

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// GateExpressionSpec composes existing gates via named operators.
type GateExpressionSpec struct {
	// Operator selects the composition operator.
	// +kubebuilder:validation:Enum=ALL;ANY;NOT;WEIGHTED_SUM;THRESHOLD;DELAY
	Operator string `json:"operator"`
	// Operands are the gates or child expressions evaluated by this expression.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=128
	Operands []GateExpressionOperand `json:"operands"`
	// Weights[i] applies to Operands[i] for WEIGHTED_SUM. Reserved for v0.2.0.
	// +kubebuilder:validation:MaxItems=128
	// +kubebuilder:validation:items:Minimum=0
	// +optional
	Weights []int32 `json:"weights,omitempty"`
	// Threshold applies to THRESHOLD and WEIGHTED_SUM. Reserved for v0.2.0.
	// +kubebuilder:validation:Minimum=1
	// +optional
	Threshold *int32 `json:"threshold,omitempty"`
	// Parameters carries operator-specific options. DELAY requires
	// parameters.duration as a Go duration string.
	// +optional
	Parameters map[string]string `json:"parameters,omitempty"`
}

// GateExpressionOperand points at either an inline gate policy or another
// GateExpression. Exactly one field must be set.
// +kubebuilder:validation:XValidation:rule="has(self.inlineGate) != has(self.expressionRef)",message="exactly one of inlineGate or expressionRef must be set"
// +kubebuilder:validation:XValidation:rule="!has(self.inlineGate) || !has(self.inlineGate.expressionRef)",message="inlineGate must not set expressionRef; use operand.expressionRef"
type GateExpressionOperand struct {
	// InlineGate is a normal Kapro gate policy evaluated at runtime by the
	// target controller. The GateExpression controller treats inline gates as
	// Pending because it has no target-specific context.
	// +optional
	InlineGate *GatePolicySpec `json:"inlineGate,omitempty"`
	// ExpressionRef names another GateExpression in the same cluster scope.
	// +kubebuilder:validation:MinLength=1
	// +optional
	ExpressionRef string `json:"expressionRef,omitempty"`
}

// GateExpressionStatus records the latest composition outcome.
type GateExpressionStatus struct {
	ObservedGeneration int64  `json:"observedGeneration,omitempty"`
	Phase              string `json:"phase,omitempty"`
	Reason             string `json:"reason,omitempty"`
	// FirstObservedAt records when a DELAY expression first began waiting.
	// +optional
	FirstObservedAt *metav1.Time       `json:"firstObservedAt,omitempty"`
	Conditions      []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=gex,categories=kapro-all
// +kubebuilder:printcolumn:name="Operator",type=string,JSONPath=`.spec.operator`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// GateExpression is a preview API for composing gate policies. In v0.1.2 only
// ALL is accepted; other operators are reserved until their semantics graduate.
type GateExpression struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              GateExpressionSpec   `json:"spec,omitempty"`
	Status            GateExpressionStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type GateExpressionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []GateExpression `json:"items"`
}
