// Package notification defines KNI — the Kapro Notification Interface.
//
// KNI is the event fanout contract for promotion lifecycle events.
// Kapro fires notifications at every phase transition and on failures.
//
// Built-in implementations live in internal/notification/:
//   - notifier.go  — lightweight Slack + Webhook dispatcher (zero extra deps)
//   - engine/      — argoproj/notifications-engine (15+ providers: PagerDuty, OpsGenie, Teams...)
//
// External implementations register via PluginRegistration CRD and communicate
// over proto/kapro/v1alpha1/notification.proto.
//
// The NopNotifier in this package silently drops all events — for testing.
package notification

import (
	"context"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
)

// Event carries the context for a notification.
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

// Notifier is KNI: the Kapro Notification Interface.
//
// Notify must never block a promotion — implementations must handle
// errors internally (log + continue). Never return an error from Notify.
// Implementations must be safe for concurrent use.
type Notifier interface {
	Notify(ctx context.Context, event Event, policy *kaprov1alpha1.PromotionPolicy)
}

// NopNotifier silently discards all notifications. Use in tests.
type NopNotifier struct{}

func (NopNotifier) Notify(_ context.Context, _ Event, _ *kaprov1alpha1.PromotionPolicy) {}

// compile-time check
var _ Notifier = NopNotifier{}
