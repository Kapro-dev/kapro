// Package events defines Kapro's stable CloudEvents v1.0 vocabulary for
// fleet promotion lifecycle. It is the public contract third parties
// import to subscribe to Kapro events from Argo Events, Flux Notification
// Controller, kube-event-exporter, Knative, or any CloudEvents-aware
// system.
//
// # Versioning policy
//
// EventType constants are part of the public API. Once an EventType is
// added to a Kapro release it MUST NOT be renamed or removed in any
// subsequent v1alpha1 release; subscribers depend on the literal string.
// New EventType values may be added in minor releases. Removal requires
// a major version bump (v1beta1, v1).
//
// # CloudEvents envelope
//
// All events are published as CloudEvents v1.0 (RFC 0050) with the
// following bindings:
//   - specversion : "1.0"
//   - id          : random 128-bit hex (subscribers may dedupe on this)
//   - type        : one of the EventType constants in this package
//   - source      : "/apis/kapro.io/v1alpha1/promotions/<name>"
//   - subject     : <promotion-name>
//   - time        : RFC3339 timestamp at emit
//   - datacontenttype : "application/json"
//   - data        : struct documented at EventData below
//
// # Type taxonomy
//
// The vocabulary follows reverse-DNS naming under the kapro.io/ root,
// segmented by lifecycle scope:
//
//	kapro.io/promotion.*           - whole-Promotion transitions
//	kapro.io/promotion.attempt.*   - per-PromotionRun execution attempts
//	kapro.io/promotion.wave.*      - Plan DAG node transitions
//	kapro.io/promotion.stage.*     - Stage transitions inside a wave
//	kapro.io/promotion.stage.gate.* - gate evaluation outcomes per target
//
// Per-cluster events (kapro.io/promotion.target.*) are intentionally
// NOT part of this vocabulary. Per-cluster reconcile state is Flux
// Notification Controller's and Argo CD Notifications' domain;
// duplicating it at the Kapro layer adds noise without adding signal.
// Subscribers wanting per-cluster detail should consume Flux/Argo
// events directly. See docs/adr/0005-withdraw-target-namespace.md.
package events

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

// EventType is the CloudEvents `type` field. The constants below form the
// stable vocabulary subscribers depend on.
type EventType string

const (
	// EventPromotionCreated fires once when the controller first observes a
	// Promotion (transition into Pending). Equivalent to Docker "created".
	EventPromotionCreated EventType = "kapro.io/promotion.created"

	// EventPromotionProgressing fires when an attempt is rolling out.
	// Equivalent to Docker "running".
	EventPromotionProgressing EventType = "kapro.io/promotion.progressing"

	// EventPromotionPaused fires when spec.suspended=true is observed.
	// Equivalent to Docker "paused".
	EventPromotionPaused EventType = "kapro.io/promotion.paused"

	// EventPromotionResumed fires when spec.suspended transitions from true
	// to false (the next non-Paused phase fires this synthetic event).
	EventPromotionResumed EventType = "kapro.io/promotion.resumed"

	// EventPromotionRestarting fires when a new attempt is stamped after
	// a prior terminal attempt. Equivalent to Docker "restarting".
	EventPromotionRestarting EventType = "kapro.io/promotion.restarting"

	// EventPromotionSucceeded fires when the latest attempt reaches
	// terminal success. Equivalent to Docker "exited 0".
	EventPromotionSucceeded EventType = "kapro.io/promotion.succeeded"

	// EventPromotionFailed fires when the latest attempt reaches terminal
	// failure. Equivalent to Docker "exited >0".
	EventPromotionFailed EventType = "kapro.io/promotion.failed"

	// EventPromotionRollingBack is reserved for the future
	// spec.rollbackTo path. Listed here so subscribers can pre-register;
	// the controller does not emit it yet.
	EventPromotionRollingBack EventType = "kapro.io/promotion.rollingBack"

	// EventPromotionTerminating fires once when deletionTimestamp is set.
	// Equivalent to Docker "removing".
	EventPromotionTerminating EventType = "kapro.io/promotion.terminating"

	// EventPromotionAttemptStamped fires each time the controller creates
	// a new PromotionRun attempt for the Promotion (first attempt and
	// every subsequent spec/template-hash change).
	EventPromotionAttemptStamped EventType = "kapro.io/promotion.attempt.stamped"

	// EventPromotionAttemptSuperseded fires when a previously non-terminal
	// PromotionRun attempt is marked Superseded because a newer attempt
	// was stamped under the same Promotion.
	EventPromotionAttemptSuperseded EventType = "kapro.io/promotion.attempt.superseded"

	// --- Wave-level events (one Plan node = one wave) ----------------

	// EventPromotionWaveEntered fires once when a Plan DAG node
	// transitions from Pending to Progressing (its dependencies are
	// satisfied and stage execution has started).
	EventPromotionWaveEntered EventType = "kapro.io/promotion.wave.entered"

	// EventPromotionWaveCompleted fires once when a Plan DAG node
	// reaches a terminal phase. Subscribers should branch on
	// `data.reason`, which carries the canonical lowercase tokens
	// "complete" (success) or "failed". The human-readable sentence
	// lives in `data.message`. `data.phase` is the PromotionRun phase,
	// not the wave's local phase.
	EventPromotionWaveCompleted EventType = "kapro.io/promotion.wave.completed"

	// --- Stage-level events (one Stage inside a Plan) ----------------

	// EventPromotionStageEntered fires once when a Stage transitions from
	// Pending to Progressing (at least one matching target started rolling).
	EventPromotionStageEntered EventType = "kapro.io/promotion.stage.entered"

	// EventPromotionStageCompleted fires once when every target in a Stage
	// reaches Converged. Aligned with the existing notification engine's
	// "stage completed" notification — Kapro emits both.
	EventPromotionStageCompleted EventType = "kapro.io/promotion.stage.completed"

	// --- Gate-level events (per-target, since each target evaluates the stage gate) -----

	// EventPromotionStageGateWaiting fires once when a stage's GateTemplate
	// first enters evaluation for a target (the gate has Started but not
	// yet returned Passed or Failed). Subscribers can use this to surface
	// "approval required" / "soak time in progress" / "metrics gathering"
	// in dashboards.
	EventPromotionStageGateWaiting EventType = "kapro.io/promotion.stage.gate.waiting"

	// EventPromotionStageGatePassed fires when a gate returns Passed for
	// a target. Mirrors the existing notification.EventGatePassed.
	EventPromotionStageGatePassed EventType = "kapro.io/promotion.stage.gate.passed"

	// EventPromotionStageGateFailed fires when a gate returns Failed for
	// a target (terminal — retry logic has been exhausted or the gate's
	// failure policy says "halt"). Mirrors notification.EventGateFailed.
	EventPromotionStageGateFailed EventType = "kapro.io/promotion.stage.gate.failed"
)

// AllEventTypes returns the canonical list of EventType constants in
// declaration order. Useful for documentation generators and integration
// test sweeps. The order is stable and may grow but never shrink within
// a major version.
func AllEventTypes() []EventType {
	return []EventType{
		EventPromotionCreated,
		EventPromotionProgressing,
		EventPromotionPaused,
		EventPromotionResumed,
		EventPromotionRestarting,
		EventPromotionSucceeded,
		EventPromotionFailed,
		EventPromotionRollingBack,
		EventPromotionTerminating,
		EventPromotionAttemptStamped,
		EventPromotionAttemptSuperseded,
		EventPromotionWaveEntered,
		EventPromotionWaveCompleted,
		EventPromotionStageEntered,
		EventPromotionStageCompleted,
		EventPromotionStageGateWaiting,
		EventPromotionStageGatePassed,
		EventPromotionStageGateFailed,
	}
}

// Event is the in-process representation of a Kapro lifecycle event
// before it is rendered to a CloudEvents JSON envelope. Subscribers
// outside the Kapro process see only the rendered envelope.
type Event struct {
	// Type is the CloudEvents `type` (one of the EventType constants).
	Type EventType
	// PromotionName is the Promotion the event is about. Used as both
	// CloudEvents `subject` and as the trailing path of `source`.
	PromotionName string
	// PromotionUID is the Kubernetes UID for traceability across renames.
	// +optional
	PromotionUID string
	// FleetRef is the parent Fleet name. Provided in `data` so
	// fleet-scope filtering works without re-fetching the Promotion.
	FleetRef string
	// Phase is the dispatch-time phase the event was emitted under.
	// For whole-Promotion and attempt events this is
	// Promotion.status.phase. For wave / stage / stage.gate / target
	// events this is the owning PromotionRun.status.phase — a
	// run-scoped mirror of the Promotion phase. The scoped phase
	// (wave/stage/gate state) is conveyed via the event type plus the
	// Wave/Stage/Gate/Target fields, not by overloading Phase.
	Phase string
	// PreviousPhase is the prior status.phase, for transition events.
	// +optional
	PreviousPhase string
	// Version is the Promotion.spec.version (echoed for convenience).
	// +optional
	Version string
	// AttemptName is the active or just-terminal PromotionRun name when
	// the event is attempt-scoped. Empty for purely Promotion-level
	// transitions with no active run (e.g. Terminating, Paused on create).
	// +optional
	AttemptName string
	// Wave is the Plan DAG node name (the value of
	// PromotionRun.spec.plans[].name). Set for wave-, stage-,
	// and gate-scoped events; empty otherwise.
	// +optional
	Wave string
	// Stage is the Stage name within the Plan. Set for stage-
	// and gate-scoped events; empty otherwise.
	// +optional
	Stage string
	// Gate is the gate name within the Stage. Set only for
	// kapro.io/promotion.stage.gate.* events.
	// +optional
	Gate string
	// Target is the Cluster name. Set for
	// kapro.io/promotion.stage.gate.* events (gates are evaluated
	// per-target). There are intentionally no per-target events
	// outside the gate scope; per-cluster reconcile is Flux
	// Notification Controller's / Argo CD Notifications' job.
	// +optional
	Target string
	// Reason is a short machine-readable cause (e.g. "AttemptSucceeded",
	// "SupersededByNewPromotionAttempt").
	// +optional
	Reason string
	// Message is a one-line human summary.
	// +optional
	Message string
	// Time is the emit time. Defaults to time.Now() when zero.
	// +optional
	Time time.Time
}

// EventData is the struct serialized as the CloudEvents `data` field.
// Subscribers should unmarshal CloudEvents `data` into this shape (or
// the equivalent in their language). New fields may be added in minor
// releases; existing fields are stable across v1alpha1.
type EventData struct {
	Promotion    string `json:"promotion"`
	PromotionUID string `json:"promotionUID,omitempty"`
	FleetRef     string `json:"fleet,omitempty"`
	// Phase is the Promotion.status.phase for whole-Promotion / attempt
	// events, and the PromotionRun.status.phase for wave/stage/gate
	// events. See Event.Phase for the full semantic.
	Phase         string `json:"phase"`
	PreviousPhase string `json:"previousPhase,omitempty"`
	Version       string `json:"version,omitempty"`
	AttemptName   string `json:"attemptName,omitempty"`
	// Wave is set on kapro.io/promotion.wave.*, .stage.*, and
	// .stage.gate.* events. Empty on whole-Promotion and attempt events.
	Wave string `json:"wave,omitempty"`
	// Stage is set on .stage.* and .stage.gate.* events.
	Stage string `json:"stage,omitempty"`
	// Gate is set only on .stage.gate.* events.
	Gate string `json:"gate,omitempty"`
	// Target is set on .stage.gate.* events (gates evaluate per cluster).
	Target  string `json:"target,omitempty"`
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message,omitempty"`
}

// Envelope is the CloudEvents v1.0 structured-mode JSON envelope. It is
// exported so subscribers can use it as a target type for unmarshaling.
// The struct mirrors the field set required by the CloudEvents v1.0
// spec — `specversion`, `id`, `source`, `type`, optional `subject`,
// `time`, `datacontenttype`, and `data`.
type Envelope struct {
	SpecVersion     string    `json:"specversion"`
	ID              string    `json:"id"`
	Source          string    `json:"source"`
	Type            EventType `json:"type"`
	Subject         string    `json:"subject,omitempty"`
	Time            string    `json:"time"`
	DataContentType string    `json:"datacontenttype"`
	Data            EventData `json:"data"`
}

// Render produces the CloudEvents v1.0 JSON envelope for an Event.
// Returns the raw bytes and the rendered Envelope (the latter is
// returned for callers who want to log the structured form before
// transmission).
func Render(e Event) ([]byte, Envelope, error) {
	id, err := randomID()
	if err != nil {
		return nil, Envelope{}, fmt.Errorf("generate cloudevents id: %w", err)
	}
	t := e.Time
	if t.IsZero() {
		t = time.Now().UTC()
	} else {
		t = t.UTC()
	}
	env := Envelope{
		SpecVersion:     "1.0",
		ID:              id,
		Type:            e.Type,
		Source:          "/apis/kapro.io/v1alpha1/promotions/" + e.PromotionName,
		Subject:         e.PromotionName,
		Time:            t.Format(time.RFC3339Nano),
		DataContentType: "application/json",
		Data: EventData{
			Promotion:     e.PromotionName,
			PromotionUID:  e.PromotionUID,
			FleetRef:      e.FleetRef,
			Phase:         e.Phase,
			PreviousPhase: e.PreviousPhase,
			Version:       e.Version,
			AttemptName:   e.AttemptName,
			Wave:          e.Wave,
			Stage:         e.Stage,
			Gate:          e.Gate,
			Target:        e.Target,
			Reason:        e.Reason,
			Message:       e.Message,
		},
	}
	body, err := json.Marshal(env)
	if err != nil {
		return nil, env, fmt.Errorf("marshal cloudevents envelope: %w", err)
	}
	return body, env, nil
}

func randomID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
