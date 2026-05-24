// Approval CRD: the human gate signal that unblocks a waiting Target rollout.
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ---- Approval ---------------------------------------------------------------

// ApprovalSpec is the human signal that unblocks a waiting target.
//
// Identity is deterministic: one cluster-scoped Approval per (promotionrun, ref)
// pair. The object name is "<promotionrun>-<ref>". For target FSM approvals, ref is
// the stable sync key "<promotionrun>-<planRef>-<stage>-<target>", so each
// waiting-approval step requires its own approval object.
type ApprovalSpec struct {
	// PromotionRun is the name of the PromotionRun this approval unblocks.
	// +kubebuilder:validation:Required
	PromotionRun string `json:"promotionRun"`
	// Target is the Cluster name this approval is for.
	// +kubebuilder:validation:Required
	Target string `json:"target"`
	// Ref identifies the exact approval scope within the PromotionRun. For target FSM
	// approvals this is the stable sync key "<promotionrun>-<planRef>-<stage>-<target>".
	// External integrators may use another deterministic ref as long as
	// metadata.name is "<promotionrun>-<ref>".
	Ref string `json:"ref"`
	// ApprovedBy identifies the human approver. May be left empty by the
	// client; the admission mutating webhook fills it in from the request
	// UserInfo (the validating webhook then enforces non-empty). This field
	// is intentionally NOT marked as a required schema field so that the
	// mutating webhook has a chance to populate it before validation runs.
	// +optional
	ApprovedBy string `json:"approvedBy,omitempty"`
	// Bypass skips subsequent gate conditions for the target. Reserved for
	// P0 hotfix escalations; audited via the ApprovalRecorded Event.
	// +optional
	Bypass bool `json:"bypass,omitempty"`
	// Comment is optional free-form justification.
	// +optional
	Comment string `json:"comment,omitempty"`
}

type ApprovalPhase string

const (
	ApprovalPhasePending  ApprovalPhase = "Pending"
	ApprovalPhaseRecorded ApprovalPhase = "Recorded"
)

type ApprovalStatus struct {
	ObservedGeneration int64              `json:"observedGeneration,omitempty"`
	Phase              ApprovalPhase      `json:"phase,omitempty"`
	ProcessedAt        string             `json:"processedAt,omitempty"`
	Conditions         []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=ap,categories=kapro-all
// +kubebuilder:printcolumn:name="PromotionRun",type=string,JSONPath=`.spec.promotionRun`
// +kubebuilder:printcolumn:name="Target",type=string,JSONPath=`.spec.target`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Recorded",type=string,JSONPath=`.status.conditions[?(@.type=="Recorded")].status`
// +kubebuilder:printcolumn:name="Approved By",type=string,JSONPath=`.spec.approvedBy`
// +kubebuilder:printcolumn:name="Bypass",type=boolean,JSONPath=`.spec.bypass`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Approval is the human gate signal that unblocks a waiting target rollout.
// Object name convention: "<promotionrun>-<ref>" as a cluster-scoped object.
type Approval struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              ApprovalSpec   `json:"spec,omitempty"`
	Status            ApprovalStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type ApprovalList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Approval `json:"items"`
}
