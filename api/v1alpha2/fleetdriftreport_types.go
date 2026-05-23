// FleetDriftReport CRD: read-only drift summary derived from Cluster,
// PromotionRun, and Target status.
package v1alpha2

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// FleetDriftReportSpec selects the slice of fleet state to observe.
type FleetDriftReportSpec struct {
	// FleetRef limits the report to PromotionRuns stamped for this Fleet.
	// +optional
	FleetRef string `json:"fleetRef,omitempty"`
	// PromotionRunRef limits the report to one PromotionRun's child Targets.
	// +optional
	PromotionRunRef string `json:"promotionRunRef,omitempty"`
	// TargetSelector limits the report to matching Target labels.
	// +optional
	TargetSelector *metav1.LabelSelector `json:"targetSelector,omitempty"`
	// MaxTargets caps status.targets evidence. Defaults to 128.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=512
	// +optional
	MaxTargets *int32 `json:"maxTargets,omitempty"`
	// SyncInterval controls periodic refresh. Defaults to 5m.
	// +optional
	SyncInterval *metav1.Duration `json:"syncInterval,omitempty"`
}

// FleetDriftReportPhase summarizes current drift observation.
// +kubebuilder:validation:Enum=Pending;Current;Drifted;Unknown;Failed
type FleetDriftReportPhase string

const (
	FleetDriftReportPhasePending FleetDriftReportPhase = "Pending"
	FleetDriftReportPhaseCurrent FleetDriftReportPhase = "Current"
	FleetDriftReportPhaseDrifted FleetDriftReportPhase = "Drifted"
	FleetDriftReportPhaseUnknown FleetDriftReportPhase = "Unknown"
	FleetDriftReportPhaseFailed  FleetDriftReportPhase = "Failed"
)

// FleetDriftReportStatus is the controller-owned observation result.
type FleetDriftReportStatus struct {
	ObservedGeneration int64                 `json:"observedGeneration,omitempty"`
	Phase              FleetDriftReportPhase `json:"phase,omitempty"`
	// ObservedAt is the time this report was last computed.
	// +optional
	ObservedAt *metav1.Time `json:"observedAt,omitempty"`
	// Summary aggregates target and backend-object drift.
	Summary FleetDriftSummary `json:"summary,omitempty"`
	// Targets contains bounded evidence for targets that are not current.
	// +kubebuilder:validation:MaxItems=512
	// +optional
	Targets []FleetDriftTarget `json:"targets,omitempty"`
	// Conditions follow the standard Ready/Reconciling/Stalled status contract.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// FleetDriftSummary is a compact count surface for dashboards and kubectl.
type FleetDriftSummary struct {
	TotalTargets          int32 `json:"totalTargets,omitempty"`
	CurrentTargets        int32 `json:"currentTargets,omitempty"`
	DriftedTargets        int32 `json:"driftedTargets,omitempty"`
	PendingTargets        int32 `json:"pendingTargets,omitempty"`
	FailedTargets         int32 `json:"failedTargets,omitempty"`
	UnknownTargets        int32 `json:"unknownTargets,omitempty"`
	TotalBackendObjects   int32 `json:"totalBackendObjects,omitempty"`
	DriftedBackendObjects int32 `json:"driftedBackendObjects,omitempty"`
}

// FleetDriftTarget records one non-current rollout target.
type FleetDriftTarget struct {
	PromotionRun string      `json:"promotionRun,omitempty"`
	PlanRef      string      `json:"planRef,omitempty"`
	Plan         string      `json:"plan,omitempty"`
	Stage        string      `json:"stage,omitempty"`
	Cluster      string      `json:"cluster,omitempty"`
	Phase        TargetPhase `json:"phase,omitempty"`
	// AppVersions contains desired/current comparisons. Capped for status size.
	// +kubebuilder:validation:MaxItems=64
	AppVersions []FleetDriftVersion `json:"appVersions,omitempty"`
	// Objects contains backend-native drift evidence. Capped for status size.
	// +kubebuilder:validation:MaxItems=16
	Objects []FleetDriftObject `json:"objects,omitempty"`
	Reason  string             `json:"reason,omitempty"`
	Message string             `json:"message,omitempty"`
}

// FleetDriftVersion compares desired and observed versions for one app key.
type FleetDriftVersion struct {
	AppKey         string        `json:"appKey,omitempty"`
	DesiredVersion string        `json:"desiredVersion,omitempty"`
	CurrentVersion string        `json:"currentVersion,omitempty"`
	DeliveryPhase  DeliveryPhase `json:"deliveryPhase,omitempty"`
}

// FleetDriftObject carries backend-native object drift evidence.
type FleetDriftObject struct {
	APIVersion     string `json:"apiVersion,omitempty"`
	Kind           string `json:"kind,omitempty"`
	Namespace      string `json:"namespace,omitempty"`
	Name           string `json:"name,omitempty"`
	Unit           string `json:"unit,omitempty"`
	DesiredVersion string `json:"desiredVersion,omitempty"`
	CurrentVersion string `json:"currentVersion,omitempty"`
	SyncStatus     string `json:"syncStatus,omitempty"`
	HealthStatus   string `json:"healthStatus,omitempty"`
	Phase          string `json:"phase,omitempty"`
	Message        string `json:"message,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=fdr,categories=kapro-all
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Fleet",type=string,JSONPath=`.spec.fleetRef`,priority=1
// +kubebuilder:printcolumn:name="PromotionRun",type=string,JSONPath=`.spec.promotionRunRef`,priority=1
// +kubebuilder:printcolumn:name="Drifted",type=integer,JSONPath=`.status.summary.driftedTargets`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// FleetDriftReport is an operator-owned observation report. It never drives
// delivery and never writes Cluster, PromotionRun, or Target status.
type FleetDriftReport struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              FleetDriftReportSpec   `json:"spec,omitempty"`
	Status            FleetDriftReportStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type FleetDriftReportList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []FleetDriftReport `json:"items"`
}
