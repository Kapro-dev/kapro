// Notification types used inline in gate and stage policies.
package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
)

// NotificationSpec configures where and when to send delivery lifecycle events.
type NotificationSpec struct {
	// Type selects the notification backend.
	// +kubebuilder:validation:Enum=webhook;slack;email
	Type string `json:"type"`
	// Events filters which lifecycle events trigger this notification.
	// Uses stable semantic event types. Currently emitted events:
	//   kapro.promotionrun.started, kapro.promotionrun.completed, kapro.promotionrun.failed,
	//   kapro.promotionrun.rollback.started, kapro.promotionrun.stage.completed,
	//   kapro.promotionrun.gate.passed, kapro.promotionrun.gate.failed,
	//   kapro.promotionrun.approval.required,
	//   kapro.promotionrun.target.pending, kapro.promotionrun.target.verification,
	//   kapro.promotionrun.target.health_check, kapro.promotionrun.target.soaking,
	//   kapro.promotionrun.target.metrics_check, kapro.promotionrun.target.applying,
	//   kapro.promotionrun.target.converged, kapro.promotionrun.target.failed,
	//   kapro.promotionrun.target.skipped.
	// Empty means all events.
	// +optional
	Events []string `json:"events,omitempty"`
	// Webhook configures HTTP POST delivery.
	// Required when type=webhook.
	// +optional
	Webhook *WebhookNotifierSpec `json:"webhook,omitempty"`
	// Slack configures Slack message delivery.
	// Required when type=slack.
	// +optional
	Slack *SlackNotifierSpec `json:"slack,omitempty"`
	// Email configures SMTP email delivery.
	// Required when type=email.
	// +optional
	Email *EmailNotifierSpec `json:"email,omitempty"`
}

// WebhookNotifierSpec configures HTTP POST notification delivery.
type WebhookNotifierSpec struct {
	// URL is the HTTP endpoint to POST events to.
	URL string `json:"url"`
	// Format selects the payload format.
	//   json (default): plain JSON event payload.
	//   cloudevents: CloudEvents v1.0 structured content mode.
	// +kubebuilder:validation:Enum=json;cloudevents
	// +kubebuilder:default="json"
	Format string `json:"format,omitempty"`
}

// SlackNotifierSpec configures Slack message delivery.
type SlackNotifierSpec struct {
	// Channel is the Slack channel to post to.
	Channel string `json:"channel"`
}

// EmailNotifierSpec configures SMTP email delivery.
type EmailNotifierSpec struct {
	// +kubebuilder:validation:MinItems=1
	To   []string `json:"to"`
	From string   `json:"from,omitempty"`
	// +kubebuilder:pruning:PreserveUnknownFields
	SmtpSecretRef corev1.LocalObjectReference `json:"smtpSecretRef"`
}
