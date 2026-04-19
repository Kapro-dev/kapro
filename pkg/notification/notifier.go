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
// KNI deliberately has zero dependency on api/v1alpha1. The SyncReconciler
// converts *GatePolicy → NotificationPolicy before calling Notify, so external
// notifier implementations never need to import Kapro's CRD package.
// This mirrors how Kubernetes events carry resource metadata as plain strings,
// not as typed API objects.
//
// The NopNotifier in this package silently drops all events — use it in tests.
package notification

import "context"

// Event carries the context for a notification.
// All fields are plain strings — no dependency on api/v1alpha1.
type Event struct {
	// Phase is the promotion phase that triggered this event (e.g. "Converged", "Failed").
	Phase string
	// Version is the artifact version being promoted.
	Version string
	// Environment is the target environment name.
	Environment string
	// Release is the release name.
	Release string
	// Message is additional context (e.g. error details).
	Message string
	// IsFailure controls error-level formatting (red/alert vs info).
	IsFailure bool
	// ApproveURL is a signed, time-limited URL that creates an Approval CR when POSTed to.
	// Set only when Phase == WaitingApproval. Channel-agnostic — works in email, Teams, Slack, webhooks.
	ApproveURL string
	// RejectURL is a signed, time-limited URL that fails the Promotion when POSTed to.
	// Set only when Phase == WaitingApproval.
	RejectURL string
}

// NotificationPolicy carries the notification routing config for a delivery operation.
// It is a plain value type — no dependency on api/v1alpha1 CRD types.
//
// The SyncReconciler converts *GatePolicy → NotificationPolicy using
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
	//   slack   — incoming webhook URL
	//   webhook — HTTP endpoint URL
	//   pagerduty / opsgenie — service/team ID (credentials come from the operator secret)
	//   email   — unused; see Email field
	Target string
	// Email carries SMTP delivery config. Non-nil only when Type == "email".
	Email *EmailConfig
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
