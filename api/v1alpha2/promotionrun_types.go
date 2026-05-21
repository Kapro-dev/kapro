// Plan template, PromotionRun execution, and Target
// per-cluster rollout types plus their shared stage primitives.
package v1alpha2

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ---- Plan ---------------------------------------------------------------

// StageFailurePolicy controls what Fleet does when a stage fails.
// +kubebuilder:validation:Enum=halt;skip;rollback
type StageFailurePolicy string

const (
	StageFailurePolicyHalt     StageFailurePolicy = "halt"
	StageFailurePolicySkip     StageFailurePolicy = "skip"
	StageFailurePolicyRollback StageFailurePolicy = "rollback"
)

// StageDependency declares that a stage depends on an upstream stage,
// with optional soak time and availability strategy.
// This replaces bare stage name strings — enabling canary-unlock and
// soak-time patterns without heavyweight GateTemplate configuration.
type StageDependency struct {
	// Stage is the upstream stage name that must be satisfied.
	Stage string `json:"stage"`
	// RequiredSoakTime is how long ALL (or ANY, per Strategy) targets in the
	// upstream stage must have been continuously healthy before this stage
	// becomes eligible. Replaces GateTemplate for the most common gate pattern.
	// Zero or nil means no soak — advance as soon as the upstream stage completes.
	// +optional
	RequiredSoakTime *metav1.Duration `json:"requiredSoakTime,omitempty"`
	// Strategy controls when this dependency is considered satisfied.
	//   "all" (default): every target in the upstream stage must be verified.
	//   "any": at least one target in the upstream stage must be verified
	//          (canary-unlock pattern).
	// +kubebuilder:validation:Enum=all;any
	// +kubebuilder:default=all
	// +optional
	Strategy StageDependencyStrategy `json:"strategy,omitempty"`
}

// StageDependencyStrategy controls when an upstream dependency is satisfied.
// +kubebuilder:validation:Enum=all;any
type StageDependencyStrategy string

const (
	// StageDependencyAll requires every target in the upstream stage to be verified.
	StageDependencyAll StageDependencyStrategy = "all"
	// StageDependencyAny requires at least one target in the upstream stage to be verified (canary pattern).
	StageDependencyAny StageDependencyStrategy = "any"
)

// StageStrategySpec controls how many targets a stage may bind concurrently.
type StageStrategySpec struct {
	// MaxParallel limits how many targets in this stage may be non-terminal at once.
	// Zero means unlimited.
	// +kubebuilder:validation:Minimum=1
	// +optional
	MaxParallel int32 `json:"maxParallel,omitempty"`
	// MaxUnavailable is reserved for availability-aware strategies. The current
	// controller records the field but only enforces MaxParallel.
	// +kubebuilder:validation:Minimum=0
	// +optional
	MaxUnavailable int32 `json:"maxUnavailable,omitempty"`
}

// Stage is one node in a Plan's delivery DAG.
// It selects a set of target clusters by label selector, optionally gates them
// with a GatePolicy, and declares ordering via DependsOn.
//
// A single stage can target one or many clusters — the selector determines the
// fleet subset. Add a cluster to a wave by labeling its Cluster object;
// no Plan changes required.
type Stage struct {
	// Name uniquely identifies this stage within the promotionplan.
	Name string `json:"name"`
	// Selector matches the target clusters that belong to this stage.
	Selector metav1.LabelSelector `json:"selector"`
	// DependsOn declares upstream stage dependencies with optional soak time
	// and availability strategy. Each entry names an upstream stage and
	// optionally specifies how long it must be healthy (RequiredSoakTime)
	// and whether all or any upstream targets must pass (Strategy).
	// +optional
	// +kubebuilder:validation:MaxItems=64
	DependsOn []StageDependency `json:"dependsOn,omitempty"`
	// Strategy controls target binding concurrency for this stage.
	// +optional
	Strategy *StageStrategySpec `json:"strategy,omitempty"`
	// Gate is the inline gate policy evaluated after all targets in this
	// stage converge. If nil, the stage advances immediately on convergence.
	// Use for complex gates (webhook, job, approval). For simple soak time,
	// prefer StageDependency.RequiredSoakTime instead.
	// +optional
	Gate *GatePolicySpec `json:"gate,omitempty"`
	// OnFailure controls what Fleet does when this stage fails.
	// halt (default): stop the promotionplan, mark PromotionRun Failed.
	// skip: continue to downstream stages.
	// rollback: stop AND revert all targets promoted by earlier stages.
	// +kubebuilder:default=halt
	// +optional
	OnFailure StageFailurePolicy `json:"onFailure,omitempty"`
}

// PlanSpec defines a reusable progressive delivery path as a flat DAG of stages.
// A Plan is a template — referenced by PromotionRun.spec.promotionplans[].
// Uniqueness and dependency-reference validation is enforced by the admission webhook,
// which can perform DAG checks without the quadratic CEL cost budget constraints.
type PlanSpec struct {
	// MetricPresets defines reusable metric gate snippets referenced by
	// Stage.gate.metrics[].preset. Presets are expanded into each target's
	// gate policy when a PromotionRun binds targets.
	// +optional
	MetricPresets map[string]MetricGate `json:"metricPresets,omitempty"`
	// Stages is the flat DAG of delivery stages.
	// Order is declared via DependsOn, not list position.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=64
	Stages []Stage `json:"stages"`
}

// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:scope=Cluster,shortName=pl,categories=kapro-all
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Plan defines a reusable progressive delivery path as a DAG of stages.
// Each stage selects a fleet subset via label selectors and optionally gates
// advancement with a GatePolicy. Referenced by PromotionRun.spec.promotionplans[].
// Plan is a pure template — it has no controller, no status, no reconciler.
// Validation is enforced by the admission webhook. Execution state lives in PromotionRun.
type Plan struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              PlanSpec `json:"spec,omitempty"`
}

// +kubebuilder:object:root=true
type PlanList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Plan `json:"items"`
}

// ---- PromotionRun ----------------------------------------------------------------

// PlanRef is one node in the PromotionRun's promotionplan DAG.
// Multiple promotionplans can run in parallel; DependsOn declares ordering between them.
type PlanRef struct {
	// Name uniquely identifies this promotionplan node within the PromotionRun.
	Name string `json:"name"`
	// Plan is the name of the Plan CRD to execute.
	Plan string `json:"plan"`
	// DependsOn lists promotionplan node names that must reach Complete before this one starts.
	// +optional
	// +kubebuilder:validation:MaxItems=64
	DependsOn []string `json:"dependsOn,omitempty"`
}

// StageProgress tracks the execution state of one Stage within a promotionplan.
// Designed to render well in k9s describe view — operators see per-stage
// progress like CI promotionplan steps.
type StageProgress struct {
	// Name is the stage name from Plan.spec.stages[].name.
	Name string `json:"name"`
	// Phase is the current state of this stage.
	// +kubebuilder:validation:Enum=Pending;Progressing;Complete;Failed
	Phase string `json:"phase,omitempty"`
	// Total is the number of targets selected by this stage.
	Total int `json:"total,omitempty"`
	// Synced is the number of targets that have reached Converged.
	Synced int `json:"synced,omitempty"`
	// Failed is the number of targets that have reached Failed.
	Failed int `json:"failed,omitempty"`
	// Deferred is the number of eligible targets not yet bound by the planner
	// or stage strategy.
	Deferred int `json:"deferred,omitempty"`
	// PlannerResults records why targets were skipped or deferred during the
	// latest planning cycle. Capped by the controller.
	// +optional
	PlannerResults []PlannerResult `json:"plannerResults,omitempty"`
	// Message is a human-readable summary of stage progress, designed for
	// k9s describe output. Examples:
	//   "2/5 clusters converged, soak: 12m/30m remaining"
	//   "waiting for canary stage"
	//   "blocked: manual approval required for de-prod"
	// +optional
	Message string `json:"message,omitempty"`
	// StartedAt is when this stage first had a Progressing target.
	// +optional
	StartedAt string `json:"startedAt,omitempty"`
	// CompletedAt is when all targets in this stage reached a terminal state.
	// +optional
	CompletedAt string `json:"completedAt,omitempty"`
}

// PlannerResult explains one planner decision for operator visibility.
type PlannerResult struct {
	// Target is the Cluster name affected by the decision.
	Target string `json:"target,omitempty"`
	// Plugin is the planner plugin or built-in strategy that made the decision.
	Plugin string `json:"plugin,omitempty"`
	// Phase is the planner phase, for example Filter, Score, Permit, or Bind.
	Phase string `json:"phase,omitempty"`
	// Reason is a short machine-readable reason.
	Reason string `json:"reason,omitempty"`
	// Message is a human-readable explanation.
	Message string `json:"message,omitempty"`
}

// PromotionPlanProgress tracks the execution state of one promotionplan node in a PromotionRun.
type PromotionPlanProgress struct {
	// Name matches PlanRef.name.
	Name string `json:"name"`
	// Plan is the Plan CRD name.
	Plan string `json:"plan"`
	// ObservedGeneration pins the Plan generation used by this
	// PromotionRun. If the referenced Plan changes while the run is in
	// flight, the controller fails the run instead of silently switching DAGs.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// Phase is the current execution state of this promotionplan node.
	// +kubebuilder:validation:Enum=Pending;Progressing;Complete;Failed
	Phase string `json:"phase,omitempty"`
	// ActiveStage is the name of the currently progressing stage (or the last completed one).
	// Gives operators a quick "where are we?" without expanding StageProgress.
	// +optional
	ActiveStage string `json:"activeStage,omitempty"`
	// StageProgress summarises the state of each stage in this promotionplan.
	StageProgress []StageProgress `json:"stageProgress,omitempty"`
}

// PromotionRunScope restricts a PromotionRun to an explicit subset of clusters.
// Only clusters listed in Targets will receive rollout entries.
type PromotionRunScope struct {
	// Targets is the allowlist of target cluster names.
	// Must be non-empty when Scope is set — an empty list is ignored.
	Targets []string `json:"targets,omitempty"`
}

// Uniqueness and dependency-reference validation is enforced by the admission webhook,
// which can perform DAG checks without the quadratic CEL cost budget constraints.
type PromotionRunSpec struct {
	// Version is the default revision to deliver across the fleet.
	// For brownfield/native sources this is the revision for every unit that is
	// not explicitly listed in versions.
	// +optional
	Version string `json:"version,omitempty"`
	// Versions maps promotion unit name to the backend-native revision to
	// deliver. Use this when a PromotionRun promotes multiple existing Argo/Flux
	// objects together without creating a synthetic application object.
	// +optional
	Versions map[string]string `json:"versions,omitempty"`
	// PromotionPlans is the DAG of promotionplan nodes.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=64
	PromotionPlans []PlanRef `json:"plans"`
	// Suspended pauses all advancement when true.
	// +kubebuilder:default=false
	Suspended bool `json:"suspended,omitempty"`
	// Scope restricts this PromotionRun to a subset of clusters.
	// +optional
	Scope *PromotionRunScope `json:"scope,omitempty"`
	// Timeout is the maximum duration for the entire PromotionRun.
	// +optional
	Timeout string `json:"timeout,omitempty"`
}

type PromotionRunPhase string

const (
	PromotionRunPhasePending     PromotionRunPhase = "Pending"
	PromotionRunPhaseProgressing PromotionRunPhase = "Progressing"
	PromotionRunPhaseComplete    PromotionRunPhase = "Complete"
	PromotionRunPhaseFailed      PromotionRunPhase = "Failed"
	// PromotionRunPhaseSuperseded is set by the PromotionController when a
	// newer attempt is stamped for the same Promotion while this one is
	// still non-terminal. The FSM treats it as terminal (no further work).
	PromotionRunPhaseSuperseded PromotionRunPhase = "Superseded"
)

// IsTerminal reports whether the phase represents a terminal PromotionRun
// state. Reconcilers should stop FSM advancement when this returns true.
func (p PromotionRunPhase) IsTerminal() bool {
	switch p {
	case PromotionRunPhaseComplete, PromotionRunPhaseFailed, PromotionRunPhaseSuperseded:
		return true
	}
	return false
}

// PromotionRunStatus defines the observed state of PromotionRun.
type PromotionRunStatus struct {
	ObservedGeneration int64             `json:"observedGeneration,omitempty"`
	Phase              PromotionRunPhase `json:"phase,omitempty"`
	// ResolvedVersion is the OCI digest or tag resolved from spec.version.
	// Set once in Pending and never changed.
	ResolvedVersion string `json:"resolvedVersion,omitempty"`
	StartedAt       string `json:"startedAt,omitempty"`
	CompletedAt     string `json:"completedAt,omitempty"`
	// PromotionPlanProgress tracks execution state of each promotionplan node in the DAG.
	PromotionPlanProgress []PromotionPlanProgress `json:"planProgress,omitempty"`
	// Targets is deprecated compatibility state. The authoritative per-target
	// rollout state lives in child Target objects.
	Targets []TargetExecutionState `json:"targets,omitempty"`
	// Report is the inline delivery summary.
	Report PromotionRunReportSummary `json:"report,omitempty"`
	// AuditTrail records immutable delivery provenance. Capped at 50 entries.
	AuditTrail []AuditEntry       `json:"auditTrail,omitempty"`
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=rel,categories=kapro-all
// +kubebuilder:printcolumn:name="Version",type=string,JSONPath=`.spec.version`,priority=0
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`,priority=0
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`,priority=0
// +kubebuilder:printcolumn:name="Synced",type=integer,JSONPath=`.status.report.syncedTargets`,priority=0
// +kubebuilder:printcolumn:name="Failed",type=integer,JSONPath=`.status.report.failedTargets`,priority=0
// +kubebuilder:printcolumn:name="Pending",type=integer,JSONPath=`.status.report.pendingTargets`,priority=0
// +kubebuilder:printcolumn:name="Total",type=integer,JSONPath=`.status.report.totalTargets`,priority=0
// +kubebuilder:printcolumn:name="Duration",type=string,JSONPath=`.status.report.duration`,priority=0
// +kubebuilder:printcolumn:name="Suspended",type=boolean,JSONPath=`.spec.suspended`,priority=1
// +kubebuilder:printcolumn:name="Artifacts",type=integer,JSONPath=`.status.report.totalArtifacts`,priority=1
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`,priority=0

// PromotionRun is an immutable execution attempt for a progressive delivery rollout
// across the cluster fleet. User-authored Promotion intent normally stamps
// PromotionRun attempts through the Promotion controller.
// It references an artifact version and PromotionPlans that define the delivery path.
// The PromotionRun controller resolves the promotionplan DAG and creates child
// targets; each Target advances through its own delivery FSM.
// Per-target execution state lives in child Target objects; PromotionRun.status
// stores only rollout summary, promotionplan progress, and audit metadata.
type PromotionRun struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              PromotionRunSpec   `json:"spec,omitempty"`
	Status            PromotionRunStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type PromotionRunList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PromotionRun `json:"items"`
}

// ---- Per-target execution ---------------------------------------------------

// TargetExecutionState records the rollout state of one target cluster.
// Embedded inside TargetStatus (the CRD status type) and retained here
// as the controller's in-memory execution shape.
type TargetExecutionState struct {
	// PromotionRunRef is the owning PromotionRun name.
	PromotionRunRef string `json:"runRef,omitempty"`
	// Target is the target cluster name.
	Target string `json:"target"`
	// PlanRef is the logical promotionplan reference name from PromotionRun.spec.promotionPlans[i].name.
	// Used to disambiguate when the same Plan CRD is referenced multiple times.
	PlanRef string `json:"planRef,omitempty"`
	// Plan is the Plan CRD name this entry belongs to.
	Plan string `json:"plan"`
	// Stage is the stage name within the Plan.
	Stage string `json:"stage"`
	// Version is the OCI digest being delivered.
	Version string `json:"version,omitempty"`
	// Gate is the inline gate policy snapshot applied to this target cluster.
	// +optional
	Gate *GatePolicySpec `json:"gate,omitempty"`
	// AppKey is the key used to look up the current version in Cluster.status.currentVersions.
	// +optional
	AppKey string `json:"appKey,omitempty"`
	// DesiredVersions is the full appKey -> version map for this target rollout.
	// When set, the actuator must converge all of these versions before the target completes.
	// +optional
	DesiredVersions map[string]string `json:"desiredVersions,omitempty"`
	// BackendObjects records the backend-native objects this target expects to
	// converge, for example Argo CD Applications selected by a label selector.
	// It is status evidence only; backend adapters own the actual resources.
	// +optional
	BackendObjects []BackendObjectStatus `json:"backendObjects,omitempty"`
	// Phase is the FSM state of this target rollout.
	Phase      TargetPhase `json:"phase,omitempty"`
	StartedAt  string      `json:"startedAt,omitempty"`
	FinishedAt string      `json:"finishedAt,omitempty"`
	// PhaseEnteredAt records when the current phase was entered (used by gate timeouts).
	PhaseEnteredAt string `json:"phaseEnteredAt,omitempty"`
	Message        string `json:"message,omitempty"`
	// PreviousVersion holds the version before this rollout, used for rollback.
	PreviousVersion string `json:"previousVersion,omitempty"`
	// PreviousVersions holds the pre-rollout appKey -> version snapshot used for rollback.
	// +optional
	PreviousVersions map[string]string `json:"previousVersions,omitempty"`
	// ApprovalSentAt records when the approval notification was last dispatched.
	ApprovalSentAt string `json:"approvalSentAt,omitempty"`
	// Gates is the authoritative snapshot of GateTemplate evaluation state.
	// +kubebuilder:validation:MaxItems=16
	Gates []GateRunStatus `json:"gates,omitempty"`
	// Rollback is true when this entry was created by a rollback trigger.
	Rollback bool `json:"rollback,omitempty"`
	// Rejected is set when a user rejects the approval via the webhook.
	Rejected bool `json:"rejected,omitempty"`
	// RejectedBy is the identity of the user who rejected the approval.
	RejectedBy string `json:"rejectedBy,omitempty"`
	// ApplyIssued is set once Actuator.Apply() has been called for this delivery
	// cycle. Guards against duplicate Apply() calls on subsequent reconciles while
	// the cluster is converging. Reset automatically on each transition into Applying.
	ApplyIssued bool `json:"applyIssued,omitempty"`
	// MissingMCCount tracks consecutive reconciles where the Cluster was not found.
	// When it reaches missingMCFailThreshold the target is transitioned to Failed.
	MissingMCCount int `json:"missingMCCount,omitempty"`
	// HeartbeatStaleSince records when the target's Cluster heartbeat first
	// became stale. Used to implement a configurable timeout — if the heartbeat
	// remains stale for longer than the threshold, the target is failed.
	// Reset when the heartbeat becomes fresh again.
	// +optional
	HeartbeatStaleSince string `json:"heartbeatStaleSince,omitempty"`
	// HeartbeatStaleCount tracks consecutive reconciles that observed a stale
	// Cluster heartbeat. The target fails only after both the stale timeout
	// and the consecutive observation threshold are reached.
	// +optional
	HeartbeatStaleCount int `json:"heartbeatStaleCount,omitempty"`
}

// BackendObjectStatus reports the health of one backend-native object expected
// to converge for a Target.
type BackendObjectStatus struct {
	// APIVersion is the backend object's API version.
	// +optional
	APIVersion string `json:"apiVersion,omitempty"`
	// Kind is the backend object's kind.
	// +optional
	Kind string `json:"kind,omitempty"`
	// Namespace is the backend object's namespace.
	// +optional
	Namespace string `json:"namespace,omitempty"`
	// Name is the backend object's name.
	// +optional
	Name string `json:"name,omitempty"`
	// Unit is the Source/promotionrun unit this object belongs to.
	// +optional
	Unit string `json:"unit,omitempty"`
	// DesiredVersion is the revision Fleet expects this object to run.
	// +optional
	DesiredVersion string `json:"desiredVersion,omitempty"`
	// CurrentVersion is the revision currently reported by the backend object.
	// +optional
	CurrentVersion string `json:"currentVersion,omitempty"`
	// SyncStatus is the backend sync status when available.
	// +optional
	SyncStatus string `json:"syncStatus,omitempty"`
	// HealthStatus is the backend health status when available.
	// +optional
	HealthStatus string `json:"healthStatus,omitempty"`
	// Phase summarizes this object's convergence state.
	// +optional
	Phase string `json:"phase,omitempty"`
	// Message gives a short diagnostic when the object is not converged.
	// +optional
	Message string `json:"message,omitempty"`
}

// TargetSpec defines the immutable identity and desired intent for one
// target rollout entry within a PromotionRun.
type TargetSpec struct {
	// PromotionRunRef is the owning PromotionRun name.
	PromotionRunRef string `json:"runRef"`
	// Target is the target cluster name.
	Target string `json:"target"`
	// PlanRef is the logical promotionplan reference name from PromotionRun.spec.promotionPlans[i].name.
	PlanRef string `json:"planRef,omitempty"`
	// Plan is the Plan CRD name this entry belongs to.
	Plan string `json:"plan"`
	// Stage is the stage name within the Plan.
	Stage string `json:"stage"`
	// Version is the OCI digest being delivered.
	Version string `json:"version,omitempty"`
	// Gate is the inline gate policy snapshot applied to this target cluster.
	// +optional
	Gate *GatePolicySpec `json:"gate,omitempty"`
	// AppKey is the key used to look up the current version in Cluster.status.currentVersions.
	// +optional
	AppKey string `json:"appKey,omitempty"`
	// DesiredVersions is the full appKey -> version map for this target rollout.
	// +optional
	DesiredVersions map[string]string `json:"desiredVersions,omitempty"`
	// Rollback is true when this entry was created by a rollback trigger.
	Rollback bool `json:"rollback,omitempty"`
	// Cancelled is set by the parent PromotionRunReconciler to signal that this
	// target should stop progressing (e.g., stage halted due to peer failure).
	// The child PromotionTargetReconciler observes this and transitions to Failed.
	// This avoids cross-controller status writes — parent owns spec, child owns status.
	// +optional
	Cancelled bool `json:"cancelled,omitempty"`
	// CancelledReason explains why the target was cancelled.
	// +optional
	CancelledReason string `json:"cancelledReason,omitempty"`
}

// TargetStatus is the live execution state for one target rollout.
type TargetStatus struct {
	TargetExecutionState `json:",inline"`
	// ObservedGeneration records the Target generation last processed by
	// the child reconciler.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// Conditions provide the Kubernetes-native readiness/reconciling/stalled contract
	// for this execution object.
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// DecisionTrace stores the audit trail of AI agent and human decisions
	// for this target's approval gates. Written by the Decision API (webhook
	// server), never by the PromotionTargetReconciler.
	// +optional
	DecisionTrace *DecisionTrace `json:"decisionTrace,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=relt,categories=kapro-all
// +kubebuilder:printcolumn:name="PromotionRun",type=string,JSONPath=`.spec.promotionRunRef`
// +kubebuilder:printcolumn:name="Target",type=string,JSONPath=`.spec.target`
// +kubebuilder:printcolumn:name="Plan",type=string,JSONPath=`.spec.promotionPlanRef`
// +kubebuilder:printcolumn:name="Stage",type=string,JSONPath=`.spec.stage`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Version",type=string,JSONPath=`.spec.version`
// +kubebuilder:printcolumn:name="Previous",type=string,JSONPath=`.status.previousVersion`,priority=1
// +kubebuilder:printcolumn:name="Rollback",type=boolean,JSONPath=`.spec.rollback`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Target is the child execution resource for one target rollout entry
// within a PromotionRun. It is the authoritative live state store for rollout
// execution and replaces PromotionRun.status.targets as the persistence layer.
type Target struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              TargetSpec   `json:"spec,omitempty"`
	Status            TargetStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type TargetList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Target `json:"items"`
}

// PromotionRunReportSummary is the inline delivery summary stored in
// PromotionRun.status.report. Counters + PendingApprovals only — per-target and
// per-gate detail live authoritatively in child Target objects (not
// duplicated here).
type PromotionRunReportSummary struct {
	Phase             PromotionRunPhase `json:"phase,omitempty"`
	Artifact          string            `json:"artifact,omitempty"`
	ResolvedVersion   string            `json:"resolvedVersion,omitempty"`
	StartedAt         string            `json:"startedAt,omitempty"`
	CompletedAt       string            `json:"completedAt,omitempty"`
	Duration          string            `json:"duration,omitempty"`
	TotalTargets      int               `json:"totalTargets,omitempty"`
	SyncedTargets     int               `json:"syncedTargets,omitempty"`
	FailedTargets     int               `json:"failedTargets,omitempty"`
	PendingTargets    int               `json:"pendingTargets,omitempty"`
	RolledBackTargets int               `json:"rolledBackTargets,omitempty"`
	// TotalArtifacts is the number of artifacts in the resolved (merged) artifact list.
	TotalArtifacts int `json:"totalArtifacts,omitempty"`
	// DeltaArtifacts is the number of artifacts explicitly changed by this PromotionRun.
	// For derivedFrom promotionruns, inherited artifacts are excluded.
	DeltaArtifacts int `json:"deltaArtifacts,omitempty"`
	// PendingApprovals lists "<promotionrun>-<ref>" Approval names that are
	// awaiting human signal. Derived from Target objects.
	PendingApprovals []string `json:"pendingApprovals,omitempty"`
}

// AuditEntry records the immutable delivery provenance of a completed PromotionRun.
// Stored in PromotionRun.status.auditTrail.
type AuditEntry struct {
	// Artifact is the OCI artifact that was delivered.
	Artifact string `json:"artifact"`
	// PromotionRun is the PromotionRun name.
	PromotionRun string `json:"promotionRun"`
	// DerivedFrom is the parent Artifact name.
	// +optional
	DerivedFrom string `json:"derivedFrom,omitempty"`
	// PromotionRunDerivedFrom is the parent PromotionRun name.
	// +optional
	PromotionRunDerivedFrom string `json:"runDerivedFrom,omitempty"`
	// ChangedUnits lists the units that changed relative to the parent artifact.
	// +optional
	ChangedUnits []string `json:"changedUnits,omitempty"`
	// Scope lists the target cluster names that were targeted. Nil = full-fleet rollout.
	// +optional
	Scope []string `json:"scope,omitempty"`
	// CompletedAt is when the PromotionRun completed.
	CompletedAt string `json:"completedAt,omitempty"`
}

// ---- Rollout execution ------------------------------------------------------

// TargetPhase is the execution state of one target cluster rollout within a PromotionRun.
// +kubebuilder:validation:Enum=Pending;Verification;HealthCheck;Soaking;MetricsCheck;WaitingApproval;Applying;Converged;Failed;Skipped
type TargetPhase string

const (
	TargetPhasePending         TargetPhase = "Pending"
	TargetPhaseVerification    TargetPhase = "Verification"
	TargetPhaseHealthCheck     TargetPhase = "HealthCheck"
	TargetPhaseSoaking         TargetPhase = "Soaking"
	TargetPhaseMetricsCheck    TargetPhase = "MetricsCheck"
	TargetPhaseWaitingApproval TargetPhase = "WaitingApproval"
	TargetPhaseApplying        TargetPhase = "Applying"
	TargetPhaseConverged       TargetPhase = "Converged"
	TargetPhaseFailed          TargetPhase = "Failed"
	// TargetPhaseSkipped means the target was bypassed because onFailure=continue was set
	// on a gate policy. A skipped target does not block subsequent targets in the promotionplan.
	TargetPhaseSkipped TargetPhase = "Skipped"
)
