// DeliveryUnit CRD: canonical user-authored app/workload delivery definition.
//
// DeliveryUnit is the top authoring layer. It describes what exists and how
// source/triggers are shaped. It does not deploy by itself; Promotion remains
// the explicit action object that requests moving version X through a Fleet.
package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// DeliveryUnitSpec defines the durable app/workload delivery identity.
type DeliveryUnitSpec struct {
	// Source is the source catalog Kapro derives into a controller-managed
	// Source object. Users edit source shape here, not in the derived Source.
	Source SourceSpec `json:"source"`
	// Triggers declares optional automatic promotion watchers. Kapro derives
	// one Trigger per entry; those Trigger objects create explicit Promotion
	// action objects when their source observes a matching artifact.
	// +kubebuilder:validation:MaxItems=32
	// +optional
	Triggers []DeliveryUnitTrigger `json:"triggers,omitempty"`
	// DefaultFleetRef is used by CLI and derived triggers when a Promotion
	// action does not explicitly pick a Fleet.
	// +optional
	DefaultFleetRef string `json:"defaultFleetRef,omitempty"`
	// DefaultPlanRef is used by CLI and derived triggers when a Promotion
	// action does not explicitly pick a Plan.
	// +optional
	DefaultPlanRef string `json:"defaultPlanRef,omitempty"`
	// Policies names reusable governance policies that apply to this unit.
	// Enforcement is intentionally owned by policy/gate controllers; this field
	// is the stable attachment point.
	// +kubebuilder:validation:MaxItems=64
	// +optional
	Policies []string `json:"policies,omitempty"`
	// Suspended pauses derivation of Source and Trigger machinery.
	// +kubebuilder:default=false
	// +optional
	Suspended bool `json:"suspended,omitempty"`
}

// DeliveryUnitTrigger is the user-authored trigger intent embedded in a
// DeliveryUnit. The controller derives a Trigger CR from this shape.
//
// +kubebuilder:validation:XValidation:rule="self.source.type != 'oci' || has(self.source.oci)",message="source.oci is required when source.type=oci"
type DeliveryUnitTrigger struct {
	// Name is the stable suffix for the derived Trigger. When empty, "default"
	// is used.
	// +optional
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^([a-z][a-z0-9-]{0,62})?$`
	Name string `json:"name,omitempty"`
	// Suspended pauses this derived watcher. Defaults follow Trigger behavior:
	// safe by default unless explicitly enabled.
	// +optional
	Suspended *bool `json:"suspended,omitempty"`
	// Source configures where artifact changes are observed.
	Source TriggerSource `json:"source"`
	// FleetRef overrides spec.defaultFleetRef for Promotions created by this
	// trigger.
	// +optional
	FleetRef string `json:"fleetRef,omitempty"`
	// PlanRef overrides spec.defaultPlanRef for Promotions created by this
	// trigger.
	// +optional
	PlanRef string `json:"planRef,omitempty"`
	// Cooldown is copied to the derived Trigger.
	// +optional
	Cooldown string `json:"cooldown,omitempty"`
	// MaxActive is copied to the derived Trigger.
	// +kubebuilder:validation:Minimum=1
	// +optional
	MaxActive int32 `json:"maxActive,omitempty"`
	// DryRun records what would be promoted without creating a Promotion.
	// +optional
	DryRun bool `json:"dryRun,omitempty"`
	// Parameters are copied to the derived Trigger.
	// +optional
	Parameters map[string]string `json:"parameters,omitempty"`
	// Labels are added to Promotions created by the derived Trigger.
	// +optional
	Labels map[string]string `json:"labels,omitempty"`
	// Annotations are added to Promotions created by the derived Trigger.
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`
}

// DeliveryUnitStatus records the derived machinery owned by this unit.
type DeliveryUnitStatus struct {
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// SourceRef is the controller-derived Source name.
	// +optional
	SourceRef string `json:"sourceRef,omitempty"`
	// TriggerRefs are the controller-derived Trigger names.
	// +optional
	TriggerRefs []string `json:"triggerRefs,omitempty"`
	// Conditions summarize derivation readiness.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=du,categories=kapro-all
// +kubebuilder:printcolumn:name="Fleet",type=string,JSONPath=`.spec.defaultFleetRef`
// +kubebuilder:printcolumn:name="Plan",type=string,JSONPath=`.spec.defaultPlanRef`
// +kubebuilder:printcolumn:name="Source",type=string,JSONPath=`.status.sourceRef`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// DeliveryUnit is the canonical user-authored app/workload delivery identity.
type DeliveryUnit struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              DeliveryUnitSpec   `json:"spec,omitempty"`
	Status            DeliveryUnitStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type DeliveryUnitList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DeliveryUnit `json:"items"`
}
