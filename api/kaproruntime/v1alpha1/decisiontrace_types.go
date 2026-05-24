package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// DecisionTraceEventType identifies the controller decision category recorded
// by a DecisionTrace.
// +kubebuilder:validation:Enum=GateEvaluate;BatchProgress;Rollback;Suspend;Stage;Delivery
type DecisionTraceEventType string

const (
	DecisionTraceEventGateEvaluate  DecisionTraceEventType = "GateEvaluate"
	DecisionTraceEventBatchProgress DecisionTraceEventType = "BatchProgress"
	DecisionTraceEventRollback      DecisionTraceEventType = "Rollback"
	DecisionTraceEventSuspend       DecisionTraceEventType = "Suspend"
	DecisionTraceEventStage         DecisionTraceEventType = "Stage"
	DecisionTraceEventDelivery      DecisionTraceEventType = "Delivery"
)

// DecisionTraceEvidence records bounded, non-secret supporting facts for a
// controller decision.
type DecisionTraceEvidence struct {
	// Type identifies the evidence kind, for example gate, planner, or policy.
	Type string `json:"type,omitempty"`
	// Source identifies the subsystem or plugin that produced this evidence.
	Source string `json:"source,omitempty"`
	// Detail carries small key-value facts. Do not store secrets or large
	// payloads here; external archive integrations keep full event envelopes.
	// +optional
	Detail map[string]string `json:"detail,omitempty"`
}

// DecisionTraceSpec is one durable audit record for a controller decision.
type DecisionTraceSpec struct {
	// PromotionRun is the PromotionRun this decision belongs to.
	// +kubebuilder:validation:MinLength=1
	PromotionRun string `json:"promotionRun"`
	// Plan is the plan node within the PromotionRun, when applicable.
	// +optional
	Plan string `json:"plan,omitempty"`
	// Stage is the stage name within the plan, when applicable.
	// +optional
	Stage string `json:"stage,omitempty"`
	// Target is the cluster or rollout target, when applicable.
	// +optional
	Target string `json:"target,omitempty"`
	// EventType identifies the controller decision category.
	EventType DecisionTraceEventType `json:"eventType"`
	// Source is the controller, gate, planner, or plugin that made the decision.
	// +kubebuilder:validation:MinLength=1
	Source string `json:"source"`
	// Phase is the resulting phase or decision outcome.
	// +optional
	Phase string `json:"phase,omitempty"`
	// Reason is a short machine-readable reason.
	// +optional
	Reason string `json:"reason,omitempty"`
	// Message is a bounded human-readable explanation.
	// +optional
	Message string `json:"message,omitempty"`
	// Evidence contains small, non-secret supporting facts for the decision.
	// +optional
	Evidence []DecisionTraceEvidence `json:"evidence,omitempty"`
	// Time is when the decision was made.
	Time metav1.Time `json:"time"`
}

// DecisionTraceStatus reserves the signing surface for v0.3.x follow-up work.
type DecisionTraceStatus struct {
	// Signed records whether this trace has an attached signature.
	Signed bool `json:"signed,omitempty"`
	// SignatureAlgorithm is the signing algorithm used for Signature.
	// +optional
	// +kubebuilder:validation:Enum=Ed25519
	SignatureAlgorithm string `json:"signatureAlgorithm,omitempty"`
	// SignatureKeyID identifies the public key operators should use to verify
	// Signature.
	// +optional
	SignatureKeyID string `json:"signatureKeyID,omitempty"`
	// PayloadDigest is the digest of the canonical DecisionTrace spec payload.
	// +optional
	PayloadDigest string `json:"payloadDigest,omitempty"`
	// Signature is a base64-encoded detached signature over the canonical
	// DecisionTrace spec payload.
	// +optional
	Signature string `json:"signature,omitempty"`
	// SignatureRef points at the external signature record, for example a Rekor
	// entry, when signing is enabled.
	// +optional
	SignatureRef string `json:"signatureRef,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=dtrace,categories=kapro-all
// +kubebuilder:printcolumn:name="Run",type=string,JSONPath=`.spec.promotionRun`
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.spec.eventType`
// +kubebuilder:printcolumn:name="Stage",type=string,JSONPath=`.spec.stage`
// +kubebuilder:printcolumn:name="Target",type=string,JSONPath=`.spec.target`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.spec.phase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// DecisionTrace is a durable append-only audit record for promotion decisions.
// Target.status.decisionTrace remains the Decision API's inline approval trace;
// this CRD is the cluster-wide controller decision stream used by audit tools.
type DecisionTrace struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              DecisionTraceSpec   `json:"spec,omitempty"`
	Status            DecisionTraceStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type DecisionTraceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DecisionTrace `json:"items"`
}
