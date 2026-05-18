// Notification types: inline NotificationSpec embedded in gate policies
// plus the API-preview NotificationProvider and NotificationPolicy CRDs.
package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

// ---- Notification provider/policy API preview ------------------------------

// NotificationProviderSpec declares where lifecycle notifications can be sent.
// It is an API preview and is not wired into runtime dispatch yet.
//
// +kubebuilder:validation:XValidation:rule="self.type != 'webhook' || (has(self.webhook) && (has(self.webhook.url) || (has(self.secretRefs) && self.secretRefs.exists(s, s.purpose == 'webhookURL'))))",message="webhook config requires webhook.url or a secretRef with purpose=webhookURL when type=webhook"
// +kubebuilder:validation:XValidation:rule="self.type != 'slack' || has(self.slack)",message="slack config is required when type=slack"
// +kubebuilder:validation:XValidation:rule="self.type != 'email' || has(self.email)",message="email config is required when type=email"
// +kubebuilder:validation:XValidation:rule="self.type != 'git' || has(self.git)",message="git config is required when type=git"
type NotificationProviderSpec struct {
	// Type selects the notification provider backend.
	// +kubebuilder:validation:Enum=webhook;slack;email;git
	Type string `json:"type"`
	// Webhook configures HTTP POST notification delivery.
	// Required when type=webhook.
	// +optional
	Webhook *NotificationWebhookProviderSpec `json:"webhook,omitempty"`
	// Slack configures Slack notification delivery.
	// Required when type=slack.
	// +optional
	Slack *NotificationSlackProviderSpec `json:"slack,omitempty"`
	// Email configures SMTP notification delivery.
	// Required when type=email.
	// +optional
	Email *NotificationEmailProviderSpec `json:"email,omitempty"`
	// Git configures Git-backed notification delivery, for example audit commits.
	// Required when type=git.
	// +optional
	Git *NotificationGitProviderSpec `json:"git,omitempty"`
	// SecretRefs references provider credentials. Because NotificationProvider
	// is cluster-scoped, each reference must include a namespace.
	// +optional
	SecretRefs []NotificationProviderSecretRef `json:"secretRefs,omitempty"`
	// Parameters are provider-specific key-value settings for future extension.
	// Kapro core does not interpret unknown parameters.
	// +optional
	Parameters map[string]string `json:"parameters,omitempty"`
}

// NotificationProviderSecretRef identifies one credential entry used by a provider.
type NotificationProviderSecretRef struct {
	// Name is the Secret name.
	Name string `json:"name"`
	// Namespace is the Secret namespace.
	Namespace string `json:"namespace"`
	// Key is the optional Secret data key within the Secret.
	// +optional
	Key string `json:"key,omitempty"`
	// Purpose describes how the credential is used, for example token,
	// webhookURL, smtpPassword, or sshPrivateKey.
	// +optional
	Purpose string `json:"purpose,omitempty"`
}

// NotificationWebhookProviderSpec configures HTTP POST notification delivery.
type NotificationWebhookProviderSpec struct {
	// URL is the HTTP endpoint to POST events to.
	// It may be omitted when supplied through secretRefs.
	// +optional
	URL string `json:"url,omitempty"`
	// Format selects the payload format.
	//   json (default): plain JSON event payload.
	//   cloudevents: CloudEvents v1.0 structured content mode.
	// +kubebuilder:validation:Enum=json;cloudevents
	// +kubebuilder:default="json"
	Format string `json:"format,omitempty"`
	// Headers are static HTTP headers sent with every request.
	// Do not put credentials here; use secretRefs instead.
	// +optional
	Headers map[string]string `json:"headers,omitempty"`
}

// NotificationSlackProviderSpec configures Slack notification delivery.
type NotificationSlackProviderSpec struct {
	// Channel is the Slack channel to post to.
	Channel string `json:"channel"`
	// WebhookURL is the Slack incoming webhook URL.
	// It may be omitted when supplied through secretRefs.
	// +optional
	WebhookURL string `json:"webhookUrl,omitempty"`
}

// NotificationEmailProviderSpec configures SMTP notification delivery.
type NotificationEmailProviderSpec struct {
	// To is the default recipient list for this provider.
	// Policies may further narrow when this provider is used.
	// +kubebuilder:validation:MinItems=1
	To []string `json:"to"`
	// From is the sender address.
	From string `json:"from,omitempty"`
	// Host is the SMTP server host.
	Host string `json:"host"`
	// Port is the SMTP server port.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	Port int32 `json:"port,omitempty"`
}

// NotificationGitProviderSpec configures Git-backed notification delivery.
type NotificationGitProviderSpec struct {
	// Repository is the Git repository URL.
	Repository string `json:"repository"`
	// Branch is the branch to write notification records to.
	// +kubebuilder:default="main"
	Branch string `json:"branch,omitempty"`
	// Path is the repository path for notification records.
	Path string `json:"path,omitempty"`
	// AuthorName is used for generated commits.
	// +optional
	AuthorName string `json:"authorName,omitempty"`
	// AuthorEmail is used for generated commits.
	// +optional
	AuthorEmail string `json:"authorEmail,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster,shortName=notifprov,categories=kapro-all
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.spec.type`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// NotificationProvider is an API-preview declaration of where Kapro lifecycle
// notifications can be sent. It is spec-only; runtime dispatch remains future work.
type NotificationProvider struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              NotificationProviderSpec `json:"spec,omitempty"`
}

// +kubebuilder:object:root=true
type NotificationProviderList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NotificationProvider `json:"items"`
}

// NotificationPolicySpec declares when lifecycle notifications should be sent.
// It is an API preview and is not wired into runtime dispatch yet.
type NotificationPolicySpec struct {
	// Subscriptions route matching events to notification providers.
	// +kubebuilder:validation:MinItems=1
	Subscriptions []NotificationSubscription `json:"subscriptions"`
}

// NotificationSubscription routes matching events to one provider.
type NotificationSubscription struct {
	// Name identifies this subscription within the policy.
	// +optional
	Name string `json:"name,omitempty"`
	// ProviderRef references a NotificationProvider by name.
	ProviderRef NotificationProviderRef `json:"providerRef"`
	// Filter selects the lifecycle events delivered to the provider.
	// Empty means all events.
	// +optional
	Filter *NotificationEventFilter `json:"filter,omitempty"`
	// Parameters are subscription-specific key-value settings for future extension.
	// Kapro core does not interpret unknown parameters.
	// +optional
	Parameters map[string]string `json:"parameters,omitempty"`
}

// NotificationProviderRef references a NotificationProvider by name.
type NotificationProviderRef struct {
	// Name is the NotificationProvider metadata.name.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

// NotificationEventFilter selects lifecycle events for a subscription.
type NotificationEventFilter struct {
	// Types filters by stable semantic event type, for example
	// kapro.promotionrun.completed or kapro.promotionrun.target.failed.
	// Empty means all event types.
	// +optional
	Types []string `json:"types,omitempty"`
	// PromotionRunSelector filters by PromotionRun labels.
	// +optional
	PromotionRunSelector *metav1.LabelSelector `json:"promotionrunSelector,omitempty"`
	// PromotionPlans filters by PromotionRun.spec.promotionplans[].name.
	// +optional
	PromotionPlans []string `json:"promotionplans,omitempty"`
	// Stages filters by PromotionPlan stage name.
	// +optional
	Stages []string `json:"stages,omitempty"`
	// Targets filters by FleetCluster name.
	// +optional
	Targets []string `json:"targets,omitempty"`
	// Phases filters by normalized event phase.
	// +optional
	Phases []string `json:"phases,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster,shortName=notifpol,categories=kapro-all
// +kubebuilder:printcolumn:name="Provider",type=string,JSONPath=`.spec.subscriptions[0].providerRef.name`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// NotificationPolicy is an API-preview declaration of when Kapro lifecycle
// notifications should be routed to NotificationProvider objects. It is
// spec-only; runtime dispatch remains future work.
type NotificationPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              NotificationPolicySpec `json:"spec,omitempty"`
}

// +kubebuilder:object:root=true
type NotificationPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NotificationPolicy `json:"items"`
}
