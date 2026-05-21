// Promotion CRD: durable user-facing intent to roll a version through a Fleet
// fleet. The PromotionController creates PromotionRun objects as execution
// attempts; users do not normally write PromotionRun directly.
package v1alpha2

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// PromotionSpec is the durable intent to deliver a version through a Fleet
// fleet. It refers to a parent Fleet (which owns source, plan, clusters,
// delivery) and adds the rollout target (version, scope, optional plan
// override).
type PromotionSpec struct {
	// KaproRef is the name of the Fleet fleet this intent targets.
	// +kubebuilder:validation:MinLength=1
	KaproRef string `json:"fleetRef"`
	// Version is the default revision to deliver across all units.
	// +optional
	Version string `json:"version,omitempty"`
	// Versions maps Unit name to a per-unit revision.
	// Either Version or at least one Versions entry must be set.
	// +optional
	Versions map[string]string `json:"versions,omitempty"`
	// PromotionPlans optionally overrides the inline Fleet.spec.promotionplan
	// for this intent. When unset, the controller derives a single plan ref
	// from the parent Fleet's inline plan.
	// +kubebuilder:validation:MaxItems=64
	// +optional
	PromotionPlans []PlanRef `json:"plans,omitempty"`
	// Scope restricts this Promotion to a subset of the parent fleet.
	// +optional
	Scope *PromotionRunScope `json:"scope,omitempty"`
	// Timeout is the maximum duration for each PromotionRun attempt.
	// +optional
	Timeout string `json:"timeout,omitempty"`
	// Suspended pauses creation of new PromotionRun attempts when true.
	// In-flight attempts are also suspended via PromotionRun.spec.suspended.
	// +kubebuilder:default=false
	Suspended bool `json:"suspended,omitempty"`
	// Lifecycle declares user-defined handlers fired on Promotion phase
	// transitions (Docker-style: Pending, Progressing, Paused, Restarting,
	// Succeeded, Failed, Terminating). Handlers are fire-and-forget: a
	// handler failure does not change the Promotion phase. Outcomes are
	// recorded in `status.lifecycleHandlerResults[]` with bounded history
	// and Prometheus metrics, and surfaced as Kubernetes Events.
	// +optional
	Lifecycle *PromotionLifecycleSpec `json:"lifecycle,omitempty"`
}

// PromotionLifecycleSpec is the user-declared set of handlers fired on
// Promotion phase transitions. Inspired by Docker container hooks
// (PostStart/PreStop) and Argo CD Notifications.
type PromotionLifecycleSpec struct {
	// Handlers is the list of declared handlers. Each handler nominates the
	// phases it cares about via `on:` and supplies either a Webhook or an
	// Event payload.
	// +kubebuilder:validation:MaxItems=32
	Handlers []PromotionLifecycleHandler `json:"handlers,omitempty"`
}

// PromotionLifecycleHandler is one declared handler.
type PromotionLifecycleHandler struct {
	// Name is a stable identifier for this handler. Used as part of the
	// idempotency key so a controller restart does not re-fire a handler
	// that already completed for the same (phase, attempt).
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	Name string `json:"name"`
	// On is the list of phases that trigger this handler. Must be non-empty.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=8
	On []PromotionPhase `json:"on"`
	// Webhook configures an HTTPS POST with a CloudEvents v1.0 payload.
	// Exactly one of Webhook or Event must be set.
	// +optional
	Webhook *PromotionLifecycleWebhook `json:"webhook,omitempty"`
	// Event records an additional Kubernetes Event on the Promotion. Useful
	// for surfacing user-defined reasons (e.g. "Promoted to prod") in
	// addition to the controller's built-in phase events.
	// +optional
	Event *PromotionLifecycleEvent `json:"event,omitempty"`
	// Timeout caps the total time spent invoking this handler (including
	// retries). Default 30s, max 5m.
	// +optional
	Timeout *metav1.Duration `json:"timeout,omitempty"`
	// MaxRetries bounds the number of retry attempts after a transient
	// failure (network error, 5xx response). 4xx responses are not retried.
	// Default 3, max 10.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=10
	// +optional
	MaxRetries *int32 `json:"maxRetries,omitempty"`
}

// PromotionLifecycleWebhook configures a one-shot webhook invocation.
type PromotionLifecycleWebhook struct {
	// URL is the HTTPS endpoint to POST a CloudEvents v1.0 payload to.
	// HTTP (cleartext) is rejected by the dispatcher unless the operator
	// opts in via the KAPRO_LIFECYCLE_INSECURE_WEBHOOKS env var.
	// +kubebuilder:validation:MinLength=1
	URL string `json:"url"`
	// AuthHeader optionally injects a single header (e.g. Authorization)
	// whose value is read from a referenced Secret. The Secret must live
	// in the operator's namespace.
	// +optional
	AuthHeader *PromotionLifecycleAuthHeader `json:"authHeader,omitempty"`
}

// PromotionLifecycleAuthHeader injects one header from a Secret value.
type PromotionLifecycleAuthHeader struct {
	// Name is the HTTP header name to set (e.g. "Authorization").
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
	// SecretName is the name of the Secret containing the header value.
	// The Secret must live in the operator's namespace.
	// +kubebuilder:validation:MinLength=1
	SecretName string `json:"secretName"`
	// SecretKey is the key inside the Secret holding the header value.
	// +kubebuilder:validation:MinLength=1
	SecretKey string `json:"secretKey"`
}

// PromotionLifecycleEvent records an additional Kubernetes Event when the
// handler fires.
type PromotionLifecycleEvent struct {
	// Reason is the Event reason (e.g. "PromotedToProd"). Must satisfy the
	// Kubernetes Event reason regex.
	// +kubebuilder:validation:MinLength=1
	Reason string `json:"reason"`
	// Message is the Event message body. Promotion field references are
	// substituted: {{.Phase}}, {{.PreviousPhase}}, {{.Version}}, {{.Name}},
	// {{.AttemptName}}.
	// +optional
	Message string `json:"message,omitempty"`
	// Type is "Normal" (default) or "Warning".
	// +kubebuilder:validation:Enum=Normal;Warning
	// +kubebuilder:default=Normal
	// +optional
	Type string `json:"type,omitempty"`
}

// PromotionLifecycleHandlerResult records the outcome of one handler
// invocation for one (phase, attempt) tuple. The status field is bounded
// to MaxLifecycleHandlerResults entries, newest first.
type PromotionLifecycleHandlerResult struct {
	// Name matches PromotionLifecycleHandler.name.
	Name string `json:"name"`
	// Phase is the phase transition that fired this handler.
	Phase PromotionPhase `json:"phase"`
	// AttemptName is the PromotionRun name observed at fire time, when one
	// existed. Empty for create/terminate transitions with no active run.
	// +optional
	AttemptName string `json:"attemptName,omitempty"`
	// Kind is "Webhook" or "Event".
	Kind string `json:"kind"`
	// Result is "Succeeded" (final success), "Failed" (final failure after
	// retries), or "InProgress" (handler still running — never persisted to
	// status; the dispatcher waits for terminal outcome before writing).
	Result string `json:"result"`
	// HTTPStatus is the last HTTP response code for webhook handlers.
	// +optional
	HTTPStatus int32 `json:"httpStatus,omitempty"`
	// Attempts is the total number of invocations (1 + retries).
	// +optional
	Attempts int32 `json:"attempts,omitempty"`
	// DurationMs is the wall-clock time spent dispatching this handler.
	// +optional
	DurationMs int64 `json:"durationMs,omitempty"`
	// Message is a one-line human summary (last error, or "ok").
	// +optional
	Message string `json:"message,omitempty"`
	// FiredAt is when dispatch started.
	FiredAt metav1.Time `json:"firedAt"`
}

// PromotionPhase is the coarse lifecycle state of a Promotion intent,
// modeled after the Docker container lifecycle. All values are listed
// here; `RollingBack` is reserved for a future `spec.rollbackTo` field
// and is not yet reachable from any controller transition.
//
//	Pending      -> created, not yet stamped       (Docker: created)
//	Progressing  -> active PromotionRun running    (Docker: running)
//	Paused       -> spec.suspended=true            (Docker: paused)
//	Restarting   -> new attempt after terminal     (Docker: restarting)
//	Succeeded    -> latest attempt completed       (Docker: exited 0)
//	Failed       -> latest attempt failed          (Docker: exited >0)
//	RollingBack  -> rollback to a prior version    (reserved; lights up
//	                when spec.rollbackTo is wired)
//	Terminating  -> deletionTimestamp set, GC      (Docker: removing)
//
// +kubebuilder:validation:Enum=Pending;Progressing;Paused;Restarting;Succeeded;Failed;RollingBack;Terminating
type PromotionPhase string

const (
	PromotionPhasePending     PromotionPhase = "Pending"
	PromotionPhaseProgressing PromotionPhase = "Progressing"
	PromotionPhasePaused      PromotionPhase = "Paused"
	PromotionPhaseRestarting  PromotionPhase = "Restarting"
	PromotionPhaseSucceeded   PromotionPhase = "Succeeded"
	PromotionPhaseFailed      PromotionPhase = "Failed"
	PromotionPhaseRollingBack PromotionPhase = "RollingBack"
	PromotionPhaseTerminating PromotionPhase = "Terminating"
)

// IsTerminal reports whether the phase is a steady-state outcome (no work
// in flight). Callers use this to suppress retries until spec changes.
func (p PromotionPhase) IsTerminal() bool {
	switch p {
	case PromotionPhaseSucceeded, PromotionPhaseFailed:
		return true
	}
	return false
}

// PromotionAttemptRef is one historical or active attempt under a Promotion.
type PromotionAttemptRef struct {
	// Name is the PromotionRun name for this attempt.
	Name string `json:"name"`
	// SpecHash is the deterministic hash of the Promotion spec that produced
	// this attempt. Used to detect spec drift and trigger a new attempt.
	SpecHash string `json:"specHash"`
	// Version is the resolved version applied for this attempt (the value
	// echoed into PromotionRun.spec.version at stamp time).
	// +optional
	Version string `json:"version,omitempty"`
	// Phase is the last-observed PromotionRun.status.phase for this attempt.
	// +optional
	Phase PromotionRunPhase `json:"phase,omitempty"`
	// StartedAt is when the attempt was created.
	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`
	// CompletedAt is when the attempt reached a terminal phase
	// (Complete, Failed, or Superseded).
	// +optional
	CompletedAt *metav1.Time `json:"completedAt,omitempty"`
	// SupersededReason is set when this attempt was marked Superseded
	// instead of finishing naturally.
	// +optional
	SupersededReason string `json:"supersededReason,omitempty"`
}

// PromotionStatus is the observed state of a Promotion intent.
type PromotionStatus struct {
	// ObservedGeneration is the .metadata.generation last reconciled.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// Phase aggregates the active attempt's PromotionRun phase into a coarse
	// Promotion phase.
	Phase PromotionPhase `json:"phase,omitempty"`
	// ActiveAttemptRef points at the current (non-terminal) PromotionRun.
	// nil when no attempt is in flight (all attempts terminal).
	// +optional
	ActiveAttemptRef *PromotionAttemptRef `json:"activeAttemptRef,omitempty"`
	// Attempts records up to the last 20 attempts (newest first). Older
	// attempts remain discoverable via PromotionRun objects with the
	// kapro.io/promotion label.
	// +optional
	// +kubebuilder:validation:MaxItems=20
	Attempts []PromotionAttemptRef `json:"attempts,omitempty"`
	// ResolvedVersion echoes the most recent attempt's version for quick
	// at-a-glance status.
	// +optional
	ResolvedVersion string             `json:"resolvedVersion,omitempty"`
	Conditions      []metav1.Condition `json:"conditions,omitempty"`
	// LifecycleHandlerResults records up to the last 50 handler invocations
	// (newest first). One entry per terminal (Succeeded/Failed) outcome of
	// a (handler, phase, attempt) tuple. In-flight invocations are NOT
	// recorded here — see Kubernetes Events for live dispatch traces.
	// +optional
	// +kubebuilder:validation:MaxItems=50
	LifecycleHandlerResults []PromotionLifecycleHandlerResult `json:"lifecycleHandlerResults,omitempty"`
}

// MaxPromotionAttempts is the cap on Promotion.status.attempts[]. Older
// attempts are pruned but the underlying PromotionRun objects remain.
const MaxPromotionAttempts = 20

// MaxLifecycleHandlerResults is the cap on
// Promotion.status.lifecycleHandlerResults[]. Older results are pruned
// but the corresponding Kubernetes Events remain (subject to the
// cluster's event TTL).
const MaxLifecycleHandlerResults = 50

// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=promo,categories=kapro-all
// +kubebuilder:printcolumn:name="Fleet",type=string,JSONPath=`.spec.kaproRef`
// +kubebuilder:printcolumn:name="Version",type=string,JSONPath=`.spec.version`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Run",type=string,JSONPath=`.status.activeAttemptRef.name`
// +kubebuilder:printcolumn:name="Attempts",type=integer,JSONPath=`.status.attempts.length()`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Promotion is the durable user-facing intent to roll a version through a
// Fleet fleet. The controller materializes intent into one or more
// PromotionRun attempts; PromotionRun and Target are observe-only
// runtime objects.
type Promotion struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              PromotionSpec   `json:"spec,omitempty"`
	Status            PromotionStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type PromotionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Promotion `json:"items"`
}
