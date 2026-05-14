// Package notification defines KNI — the Kapro Notification Interface.
//
// KNI is the event fanout contract for promotion lifecycle events.
// Kapro fires notifications at every phase transition and on failures.
//
// Built-in implementations live in internal/notification/:
//   - notifier.go  — lightweight Slack + Webhook dispatcher (zero extra deps)
//   - engine/      — argoproj/notifications-engine (15+ providers: PagerDuty, OpsGenie, Teams...)
//
// External implementations can implement the Notifier interface and wire in at startup.
//
// # Decoupling from CRD types
//
// KNI deliberately has zero dependency on api/v1alpha1. The release controller
// converts *GatePolicy → NotificationPolicy before calling Notify, so external
// notifier implementations never need to import Kapro's CRD package.
// This mirrors how Kubernetes events carry resource metadata as plain strings,
// not as typed API objects.
//
// The NopNotifier in this package silently drops all events — use it in tests.
package notification

import "context"

// Well-known event types for notification routing.
// These are semantic lifecycle events, independent of FSM phase names.
// Channels filter on Type (not Phase) for stable integration contracts.
const (
	// Release-level events
	EventReleaseStarted   = "kapro.release.started"
	EventReleaseCompleted = "kapro.release.completed"
	EventReleaseFailed    = "kapro.release.failed"
	EventRollbackStarted  = "kapro.release.rollback.started"

	// Stage-level events
	EventStageCompleted = "kapro.release.stage.completed"

	// Gate-level events
	EventGatePassed = "kapro.release.gate.passed"
	EventGateFailed = "kapro.release.gate.failed"

	// Target-level events (one per TargetPhase)
	EventTargetPending      = "kapro.release.target.pending"
	EventTargetVerification = "kapro.release.target.verification"
	EventTargetHealthCheck  = "kapro.release.target.health_check"
	EventTargetSoaking      = "kapro.release.target.soaking"
	EventTargetMetricsCheck = "kapro.release.target.metrics_check"
	EventTargetApplying     = "kapro.release.target.applying"
	EventTargetConverged    = "kapro.release.target.converged"
	EventTargetFailed       = "kapro.release.target.failed"
	EventTargetSkipped      = "kapro.release.target.skipped"
	EventApprovalRequired   = "kapro.release.approval.required"
)

// Event carries the context for a notification.
// All fields are plain strings, no dependency on api/v1alpha1.
//
// Type is the semantic event name (e.g. "kapro.release.target.converged").
// Phase is the raw FSM state (e.g. "Converged"). Type is for external
// integrations, Phase is for internal FSM tracking. Channels filter on Type.
type Event struct {
	// Type is the semantic lifecycle event name (e.g. "kapro.release.target.converged").
	Type string `json:"type,omitempty"`
	// Phase is the FSM phase that triggered this event (e.g. "Converged", "Failed").
	Phase string `json:"phase,omitempty"`
	// Version is the artifact version being promoted.
	Version string `json:"version,omitempty"`
	// Target is the target cluster name.
	Target string `json:"target,omitempty"`
	// Release is the release name.
	Release string `json:"release,omitempty"`
	// Pipeline is the pipeline name.
	Pipeline string `json:"pipeline,omitempty"`
	// Stage is the stage name within the pipeline.
	Stage string `json:"stage,omitempty"`
	// Message is additional context (e.g. error details).
	Message string `json:"message,omitempty"`
	// IsFailure controls error-level formatting (red/alert vs info).
	IsFailure bool `json:"isFailure,omitempty"`
	// ApproveURL is a signed, time-limited URL that creates an Approval CR when POSTed to.
	ApproveURL string `json:"approveUrl,omitempty"`
	// RejectURL is a signed, time-limited URL that fails the Promotion when POSTed to.
	RejectURL string `json:"rejectUrl,omitempty"`
}

// NotificationPolicy carries the notification routing config for a delivery operation.
// It is a plain value type — no dependency on api/v1alpha1 CRD types.
//
// The release controller converts *GatePolicy → NotificationPolicy using
// notificationPolicyFrom() before calling Notify. External Notifier
// implementations receive only this clean value type.
type NotificationPolicy struct {
	// Channels lists every notification destination for this event.
	// An empty slice means no notifications are configured — Notify returns immediately.
	Channels []Channel
}

// Channel is a single notification destination.
type Channel struct {
	// Type is the provider type: "slack" | "webhook" | "email" | "pagerduty" | "opsgenie" | "msteams"
	Type string
	// Target is the primary address:
	//   slack   — incoming webhook URL or channel
	//   webhook — HTTP endpoint URL
	//   email   — unused; see Email field
	Target string
	// Events filters which lifecycle events this channel receives.
	// Empty means all events (no filtering).
	Events []string
	// Format is the webhook payload format: "json" (default) or "cloudevents".
	Format string
	// Email carries SMTP delivery config. Non-nil only when Type == "email".
	Email *EmailConfig
}

// MatchesEvent returns true if the channel should receive the given event.
// An empty Events list means all events match.
func (c Channel) MatchesEvent(event string) bool {
	if len(c.Events) == 0 {
		return true
	}
	for _, e := range c.Events {
		if e == event {
			return true
		}
	}
	return false
}

// EmailConfig carries SMTP delivery configuration extracted from
// GatePolicy.spec.notifications[].email. Plain value — no ObjectReference.
type EmailConfig struct {
	To            []string
	From          string
	SMTPSecretRef string // Kubernetes Secret name (namespace-local)
}

// EmptyPolicy is a convenience zero-value NotificationPolicy — no channels.
// Pass it when no GatePolicy is configured.
var EmptyPolicy = NotificationPolicy{}

// Notifier is KNI v1alpha1: the Kapro Notification Interface.
//
// Notify must never block a promotion — implementations must handle
// errors internally (log + continue). Never return an error from Notify.
// Implementations must be safe for concurrent use.
//
// Contract:
//   - Notify MUST return immediately when policy.Channels is empty
//   - Notify MUST NOT panic on zero-value Event or EmptyPolicy
//   - Notify MUST be safe for concurrent use from multiple goroutines
//   - Notify MUST NOT modify Event or NotificationPolicy
type Notifier interface {
	Notify(ctx context.Context, event Event, policy NotificationPolicy)
}

// NopNotifier silently discards all notifications. Use in tests and as a
// safe zero-value when no notification channel is configured.
type NopNotifier struct{}

func (NopNotifier) Notify(_ context.Context, _ Event, _ NotificationPolicy) {}

// compile-time check: NopNotifier satisfies Notifier.
var _ Notifier = NopNotifier{}
