package controller

import (
	"context"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
	"kapro.io/kapro/pkg/actuator"
	"kapro.io/kapro/pkg/gate"
	internalgate "kapro.io/kapro/internal/gate"
	celgate "kapro.io/kapro/internal/gate/cel"
	jobgate "kapro.io/kapro/internal/gate/job"
	webhookgate "kapro.io/kapro/internal/gate/webhook"
	kaprometrics "kapro.io/kapro/internal/metrics"
	"kapro.io/kapro/internal/webhook/token"
	pkghealth "kapro.io/kapro/pkg/health"
	"kapro.io/kapro/pkg/notification"
	"kapro.io/kapro/pkg/oci"
	crdprovider "kapro.io/kapro/internal/provider/crd"
)

// notificationPolicyFrom converts a *GatePolicy into the KNI-clean value type.
// This is the single conversion point that keeps api/v1alpha1 out of pkg/notification.
// Returns notification.EmptyPolicy when policy is nil or has no channels configured.
func notificationPolicyFrom(policy *kaprov1alpha1.GatePolicy) notification.NotificationPolicy {
	if policy == nil || len(policy.Spec.Notifications) == 0 {
		return notification.EmptyPolicy
	}
	channels := make([]notification.Channel, 0, len(policy.Spec.Notifications))
	for _, spec := range policy.Spec.Notifications {
		ch := notification.Channel{
			Type:   spec.Type,
			Target: spec.Channel, // Slack uses Channel as webhook URL
		}
		if spec.URL != "" {
			ch.Target = spec.URL // webhook/teams/pagerduty use URL field
		}
		if spec.Email != nil {
			ch.Email = &notification.EmailConfig{
				To:            spec.Email.To,
				From:          spec.Email.From,
				SMTPSecretRef: spec.Email.SmtpSecretRef.Name,
			}
		}
		channels = append(channels, ch)
	}
	return notification.NotificationPolicy{Channels: channels}
}

// SyncReconciler drives a single environment through the gate pipeline.
//
// State machine:
//
//	Pending → Verification → HealthCheck → Soaking → MetricsCheck → WaitingApproval → Applying → Converged | Failed
//
// A Sync is system-managed: created one-per-(Release, Pipeline, Stage, Environment) by
// the ReleaseReconciler when a stage is ready to run. SyncReconciler owns everything
// from "apply gates" through "cluster converged".
type SyncReconciler struct {
	client.Client
	Recorder         record.EventRecorder
	ActuatorRegistry *actuator.Registry
	Provider         *crdprovider.CRDProvider
	// SoakGate blocks until the configured bake period has elapsed.
	// When nil, a zero-config SoakGate is used (passes immediately if no soakTime is set).
	SoakGate gate.Gate
	// MetricsGate queries Prometheus and evaluates metric thresholds.
	// When nil, a zero-config MetricsGate is used (passes immediately if no metrics are configured).
	MetricsGate gate.Gate
	// ApprovalGate blocks until a matching Approval CR is created by a human or pipeline.
	// When nil, a default ApprovalGate{Client} is constructed per-call.
	ApprovalGate gate.Gate
	// VerificationGate verifies artifact signatures before the sync advances.
	// When nil, signature verification is skipped (pass-through).
	VerificationGate gate.Gate
	// HealthAssessor evaluates workload health in the target namespace.
	// When nil, the health check gate is skipped (pass-through).
	HealthAssessor pkghealth.Assessor
	Notifier       notification.Notifier // nil-safe: Notify() checks policy before sending
	// OCIService enables artifact inspection and promotion operations.
	// When nil, OCI operations are skipped.
	OCIService oci.Service
	// ApprovalSecret is the HMAC key used to sign approval/reject tokens.
	// When non-empty alongside ExternalURL, the controller sends one-click
	// approval notifications when a Sync enters WaitingApproval.
	ApprovalSecret []byte
	// ExternalURL is the base URL of the Kapro approval webhook server
	// (e.g. "https://kapro.internal"). Used to build approve/reject URLs in notifications.
	ExternalURL string
	// CELGate evaluates built-in CEL expression GateTemplates (type == "cel").
	// Deprecated: prefer wiring via GateRegistry. Kept as a fallback for tests
	// that do not wire a full registry.
	CELGate gate.Gate
	// GateRegistry resolves GateTemplate.spec.type → Gate implementation.
	// When non-nil, all template-type dispatch goes through the registry —
	// enabling external gate types without modifying this controller.
	// Falls back to the built-in switch (cel/job/webhook) when nil.
	GateRegistry *gate.Registry
	// Scheme is required to set ownerReferences on objects created by this controller
	// (e.g. rollback Syncs). Injected at startup via main.go.
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=kapro.io,resources=syncs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kapro.io,resources=syncs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kapro.io,resources=syncs/finalizers,verbs=update
// +kubebuilder:rbac:groups=kapro.io,resources=gatepolicies,verbs=get;list;watch
// +kubebuilder:rbac:groups=kapro.io,resources=gatetemplates,verbs=get;list;watch
// +kubebuilder:rbac:groups=kapro.io,resources=environments,verbs=get;list;watch
// +kubebuilder:rbac:groups=kapro.io,resources=managedclusters,verbs=get;list;watch
// +kubebuilder:rbac:groups=kapro.io,resources=approvals,verbs=get;list;watch

const syncFinalizer = "kapro.io/sync-cleanup"

func (r *SyncReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	var sync kaprov1alpha1.Sync
	if err := r.Get(ctx, req.NamespacedName, &sync); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	log.Info("reconciling Sync", "name", sync.Name, "phase", sync.Status.Phase, "env", sync.Spec.EnvironmentRef)

	// Handle deletion: mark Failed so the Release controller doesn't wait forever, then remove finalizer.
	if !sync.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(&sync, syncFinalizer) {
			if sync.Status.Phase != kaprov1alpha1.SyncPhaseFailed &&
				sync.Status.Phase != kaprov1alpha1.SyncPhaseConverged {
				_ = r.failSync(ctx, &sync, nil, "Sync deleted before convergence")
			}
			controllerutil.RemoveFinalizer(&sync, syncFinalizer)
			if err := r.Update(ctx, &sync); err != nil {
				return ctrl.Result{}, fmt.Errorf("remove sync finalizer: %w", err)
			}
		}
		return ctrl.Result{}, nil
	}

	// Ensure finalizer is registered before we touch any external state.
	if !controllerutil.ContainsFinalizer(&sync, syncFinalizer) {
		controllerutil.AddFinalizer(&sync, syncFinalizer)
		if err := r.Update(ctx, &sync); err != nil {
			return ctrl.Result{}, fmt.Errorf("add sync finalizer: %w", err)
		}
		return ctrl.Result{Requeue: true}, nil
	}

	switch sync.Status.Phase {
	case "":
		return r.transitionTo(ctx, &sync, kaprov1alpha1.SyncPhasePending)
	case kaprov1alpha1.SyncPhasePending:
		return r.handlePending(ctx, &sync)
	case kaprov1alpha1.SyncPhaseVerification:
		return r.handleVerification(ctx, &sync)
	case kaprov1alpha1.SyncPhaseHealthCheck:
		return r.handleHealthCheck(ctx, &sync)
	case kaprov1alpha1.SyncPhaseSoaking:
		return r.handleSoaking(ctx, &sync)
	case kaprov1alpha1.SyncPhaseMetricsCheck:
		return r.handleMetricsCheck(ctx, &sync)
	case kaprov1alpha1.SyncPhaseWaitingApproval:
		return r.handleWaitingApproval(ctx, &sync)
	case kaprov1alpha1.SyncPhaseApplying:
		return r.handleApplying(ctx, &sync)
	case kaprov1alpha1.SyncPhaseConverged, kaprov1alpha1.SyncPhaseFailed:
		return ctrl.Result{}, nil
	}

	return ctrl.Result{}, nil
}

func (r *SyncReconciler) handlePending(ctx context.Context, sync *kaprov1alpha1.Sync) (ctrl.Result, error) {
	// Check cluster is reachable via ManagedCluster heartbeat.
	mc, err := r.getManagedCluster(ctx, sync.Spec.EnvironmentRef)
	if err != nil {
		return ctrl.Result{RequeueAfter: requeueFast}, nil
	}

	if !isHeartbeatFresh(mc.Status.LastHeartbeat) {
		log.FromContext(ctx).Info("cluster heartbeat stale, waiting", "env", sync.Spec.EnvironmentRef)
		return ctrl.Result{RequeueAfter: requeueNormal}, nil
	}

	return r.transitionTo(ctx, sync, kaprov1alpha1.SyncPhaseVerification)
}

// handleVerification runs the VerificationGate to confirm the artifact has a
// valid signature before advancing to HealthCheck. When VerificationGate is
// nil the phase is skipped immediately.
func (r *SyncReconciler) handleVerification(ctx context.Context, sync *kaprov1alpha1.Sync) (ctrl.Result, error) {
	g := r.VerificationGate
	if g == nil {
		log.FromContext(ctx).Info("VerificationGate is nil — skipping signature verification",
			"sync", sync.Name,
		)
		return r.transitionTo(ctx, sync, kaprov1alpha1.SyncPhaseHealthCheck)
	}

	policy, _ := r.getPolicy(ctx, sync.Spec.PolicyRef)
	result, err := g.Evaluate(ctx, gate.Request{Sync: sync, Policy: policy})
	if err != nil {
		// Hard error — surface to the controller for exponential back-off.
		return ctrl.Result{}, fmt.Errorf("verification gate: %w", err)
	}

	log.FromContext(ctx).Info("verification gate evaluated",
		"phase", result.Phase,
		"message", result.Message,
		"sync", sync.Name,
	)

	if result.IsPassed() {
		r.Recorder.Event(sync, corev1.EventTypeNormal, "GatePassed", "VerificationGate: "+result.Message)
		return r.transitionTo(ctx, sync, kaprov1alpha1.SyncPhaseHealthCheck)
	}

	r.Recorder.Event(sync, corev1.EventTypeWarning, "GateFailed", result.Message)
	after := parseDurationOrDefault(result.RetryAfter)
	return ctrl.Result{RequeueAfter: after}, nil
}

func (r *SyncReconciler) handleHealthCheck(ctx context.Context, sync *kaprov1alpha1.Sync) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	// When no assessor is configured, pass through to Soaking (nil-safe gate pattern).
	if r.HealthAssessor == nil {
		return r.transitionToSoakOrMetrics(ctx, sync)
	}

	result, err := r.HealthAssessor.AssessHealth(ctx, pkghealth.AssessRequest{
		Namespace: sync.Namespace,
	})
	if err != nil {
		log.Error(err, "health assessor error, will retry")
		return ctrl.Result{RequeueAfter: requeueFast}, nil
	}

	log.Info("health check", "overall", result.Overall, "message", result.Message)

	switch result.Overall {
	case pkghealth.StatusDegraded:
		policy, _ := r.getPolicy(ctx, sync.Spec.PolicyRef)
		return ctrl.Result{}, r.failSync(ctx, sync, policy, "health check failed: "+result.Message)
	case pkghealth.StatusProgressing, pkghealth.StatusUnknown, pkghealth.StatusMissing:
		return ctrl.Result{RequeueAfter: requeueNormal}, nil
	default:
		// Healthy, Suspended — proceed.
		return r.transitionToSoakOrMetrics(ctx, sync)
	}
}

// transitionToSoakOrMetrics advances past HealthCheck: to Soaking if a soak is configured,
// otherwise straight to MetricsCheck.
func (r *SyncReconciler) transitionToSoakOrMetrics(ctx context.Context, sync *kaprov1alpha1.Sync) (ctrl.Result, error) {
	policy, err := r.getPolicy(ctx, sync.Spec.PolicyRef)
	if err != nil || policy == nil || policy.Spec.Gate.SoakTime == "" {
		return r.transitionTo(ctx, sync, kaprov1alpha1.SyncPhaseMetricsCheck)
	}
	return r.transitionTo(ctx, sync, kaprov1alpha1.SyncPhaseSoaking)
}

func (r *SyncReconciler) handleSoaking(ctx context.Context, sync *kaprov1alpha1.Sync) (ctrl.Result, error) {
	policy, err := r.getPolicy(ctx, sync.Spec.PolicyRef)
	if err != nil || policy == nil {
		return r.transitionTo(ctx, sync, kaprov1alpha1.SyncPhaseMetricsCheck)
	}

	soakGate := r.SoakGate
	if soakGate == nil {
		soakGate = &internalgate.SoakGate{}
	}

	result, err := soakGate.Evaluate(ctx, gate.Request{Sync: sync, Policy: policy})
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("soak gate: %w", err)
	}

	log.FromContext(ctx).Info("soak gate", "phase", result.Phase, "message", result.Message)

	if result.IsPassed() {
		r.Recorder.Event(sync, corev1.EventTypeNormal, "GatePassed", "SoakGate: "+result.Message)
		return r.transitionTo(ctx, sync, kaprov1alpha1.SyncPhaseMetricsCheck)
	}

	after := parseDurationOrDefault(result.RetryAfter)
	return ctrl.Result{RequeueAfter: after}, nil
}

func (r *SyncReconciler) handleMetricsCheck(ctx context.Context, sync *kaprov1alpha1.Sync) (ctrl.Result, error) {
	policy, err := r.getPolicy(ctx, sync.Spec.PolicyRef)
	if err != nil || policy == nil {
		return r.nextAfterMetrics(ctx, sync, policy)
	}

	// Fast path: nothing to evaluate — skip straight through.
	// Checked explicitly so a policy with only Templates does NOT hit this return.
	if len(policy.Spec.Gate.Metrics) == 0 && len(policy.Spec.Gate.Templates) == 0 {
		return r.nextAfterMetrics(ctx, sync, policy)
	}

	// 1. Evaluate Prometheus metric gates in order; block on the first failure.
	//    GateTemplate evaluation always follows — even when there are no metrics —
	//    so a templates-only policy works correctly.
	if len(policy.Spec.Gate.Metrics) > 0 {
		metricsGate := r.MetricsGate
		if metricsGate == nil {
			metricsGate = &internalgate.MetricsGate{}
		}
		for i, metric := range policy.Spec.Gate.Metrics {
			result, err := metricsGate.Evaluate(ctx, gate.Request{Sync: sync, Policy: policy, MetricIndex: i})
			if err != nil {
				// Non-fatal: log and retry — don't fail the sync on transient errors.
				log.FromContext(ctx).Error(err, "metrics gate error, will retry", "index", i)
				return ctrl.Result{RequeueAfter: requeueNormal}, nil
			}
			log.FromContext(ctx).Info("metrics gate", "index", i, "provider", metric.Provider, "phase", result.Phase, "message", result.Message)
			if !result.IsPassed() {
				r.Recorder.Event(sync, corev1.EventTypeWarning, "GateFailed", result.Message)
				after := parseDurationOrDefault(result.RetryAfter)
				return ctrl.Result{RequeueAfter: after}, nil
			}
		}
	}

	// 2. Evaluate GateTemplate refs. These run after all metrics pass AND when
	//    no metrics are configured (templates-only policy). Previously this block
	//    was unreachable when Metrics was empty due to an early return — that bug
	//    is fixed by the restructure above.
	if len(policy.Spec.Gate.Templates) > 0 {
		allPassed, requeueAfter, err := r.evaluateGateTemplates(ctx, sync, policy)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("evaluateGateTemplates: %w", err)
		}
		if !allPassed {
			return ctrl.Result{RequeueAfter: requeueAfter}, nil
		}
	}

	return r.nextAfterMetrics(ctx, sync, policy)
}

// evaluateGateTemplates evaluates all GateTemplate refs from the policy.
// Results are written to Sync.Status.Gates[] (Kapro's authoritative snapshot).
// Returns: (allPassed, requeueAfter, error).
func (r *SyncReconciler) evaluateGateTemplates(ctx context.Context, sync *kaprov1alpha1.Sync, policy *kaprov1alpha1.GatePolicy) (bool, time.Duration, error) {
	log := log.FromContext(ctx)

	now := time.Now().UTC().Format(time.RFC3339)
	gates := sync.Status.Gates
	if gates == nil {
		gates = make([]kaprov1alpha1.GateRunStatus, 0, len(policy.Spec.Gate.Templates))
	}

	allPassed := true
	requeueAfter := requeueNormal

	for _, ref := range policy.Spec.Gate.Templates {
		// Fetch the GateTemplate CR (cluster-scoped).
		var tmpl kaprov1alpha1.GateTemplate
		if err := r.Get(ctx, client.ObjectKey{Name: ref.Name}, &tmpl); err != nil {
			return false, 0, fmt.Errorf("fetch GateTemplate %q: %w", ref.Name, err)
		}

		// Find or create the status entry for this gate.
		gateStatus := findOrCreateGateStatus(gates, ref.Name, now)

		// Skip already-terminal gates.
		if gateStatus.Phase == kaprov1alpha1.GatePhasePassed {
			continue
		}
		if gateStatus.Phase == kaprov1alpha1.GatePhaseFailed {
			allPassed = false
			continue
		}

		// Resolve args: template defaults < policy overrides < sync context.
		args := resolveSyncArgs(&tmpl, ref.Args, sync)

		// Dispatch to the correct gate impl based on type.
		g, err := r.gateForTemplate(&tmpl)
		if err != nil {
			return false, 0, fmt.Errorf("gate for template %q: %w", ref.Name, err)
		}

		result, err := g.Evaluate(ctx, gate.Request{
			Sync:        sync,
			Template:    &tmpl,
			Args:        args,
		})
		if err != nil {
			log.Error(err, "gate template evaluation error, will retry", "gate", ref.Name)
			gateStatus.Phase = kaprov1alpha1.GatePhaseRunning
			gateStatus.Message = err.Error()
			gateStatus.Attempts++
			setGateStatus(&gates, gateStatus)
			kaprometrics.GateEvaluations.WithLabelValues(tmpl.Spec.Type, "error").Inc()
			allPassed = false
			continue
		}

		phase := result.Phase
		if phase == "" {
			// Gate returned no Phase — default to Inconclusive (safe: retry, don't advance).
			phase = kaprov1alpha1.GatePhaseInconclusive
		}
		kaprometrics.GateEvaluations.WithLabelValues(tmpl.Spec.Type, strings.ToLower(string(phase))).Inc()

		gateStatus.Phase = phase
		gateStatus.Message = result.Message
		gateStatus.Attempts++
		gateStatus.VendorRef = result.VendorRef
		if len(result.Results) > 0 {
			gateStatus.Results = toAPIConditionResults(result.Results)
		}
		if phase != "" && phase != kaprov1alpha1.GatePhaseRunning && phase != kaprov1alpha1.GatePhasePending {
			gateStatus.FinishedAt = now
		}
		setGateStatus(&gates, gateStatus)

		log.Info("gate template evaluated", "gate", ref.Name, "phase", phase, "message", result.Message)
		r.Recorder.Event(sync, corev1.EventTypeNormal, "GateEvaluated",
			fmt.Sprintf("gate %s: %s — %s", ref.Name, phase, result.Message))

		switch phase {
		case kaprov1alpha1.GatePhaseFailed:
			allPassed = false
			// failurePolicy == skip: treat as passed and continue
			if tmpl.Spec.FailurePolicy == "skip" {
				gateStatus.Phase = kaprov1alpha1.GatePhasePassed
				gateStatus.Message = "skipped (failurePolicy=skip)"
				setGateStatus(&gates, gateStatus)
			}
		case kaprov1alpha1.GatePhaseInconclusive:
			allPassed = false
			if tmpl.Spec.InconclusivePolicy == "halt" {
				gateStatus.Phase = kaprov1alpha1.GatePhaseFailed
				setGateStatus(&gates, gateStatus)
			}
		case kaprov1alpha1.GatePhaseRunning, kaprov1alpha1.GatePhasePending:
			allPassed = false
			if d := parseDurationOrDefault(result.RetryAfter); d < requeueAfter || requeueAfter == requeueNormal {
				requeueAfter = d
			}
		}
	}

	// Persist updated gate statuses via merge patch (not Update) to avoid
	// overwriting concurrent status changes from other controllers.
	base := sync.DeepCopy()  // snapshot before mutation
	sync.Status.Gates = gates // apply change
	if err := r.Status().Patch(ctx, sync, client.MergeFrom(base)); err != nil {
		return false, 0, fmt.Errorf("patch gate statuses: %w", err)
	}

	return allPassed, requeueAfter, nil
}

// gateForTemplate resolves a Gate implementation for the given GateTemplate.
//
// Resolution order:
//  1. GateRegistry (preferred) — open; any registered type works, including
//     external types added via cc.GateRegistry.MustRegister at startup.
//  2. Built-in fallback switch — for tests that don't wire a registry.
//
// This design mirrors how Kubernetes resolves CRI implementations:
// the registry is the open extension point; the fallback exists for tests only.
func (r *SyncReconciler) gateForTemplate(tmpl *kaprov1alpha1.GateTemplate) (gate.Gate, error) {
	// Path 1: registry-based dispatch (production path).
	if r.GateRegistry != nil {
		g, err := r.GateRegistry.Resolve(tmpl.Spec.Type)
		if err != nil {
			return nil, fmt.Errorf("gate type %q not registered — add it to BuildGateRegistry or register in main.go: %w",
				tmpl.Spec.Type, err)
		}
		return g, nil
	}

	// Path 2: built-in fallback for tests that don't wire a GateRegistry.
	switch tmpl.Spec.Type {
	case "cel":
		if r.CELGate != nil {
			return r.CELGate, nil
		}
		return &celgate.Gate{Client: r.Client}, nil
	case "webhook":
		return &webhookgate.Gate{}, nil
	case "job":
		return &jobgate.Gate{Client: r.Client}, nil
	default:
		return nil, fmt.Errorf("unknown gate type %q — supported types: cel|job|webhook (or wire a GateRegistry)", tmpl.Spec.Type)
	}
}

// resolveSyncArgs builds the final args map: template defaults < policy overrides < sync context.
func resolveSyncArgs(tmpl *kaprov1alpha1.GateTemplate, policyOverrides map[string]string, sync *kaprov1alpha1.Sync) map[string]string {
	args := make(map[string]string)
	// 1. Template defaults.
	for _, a := range tmpl.Spec.Args {
		if a.Value != "" {
			args[a.Name] = a.Value
		}
	}
	// 2. Policy-level overrides.
	for k, v := range policyOverrides {
		args[k] = v
	}
	// 3. Sync context — always injected.
	if sync != nil {
		args["version"] = sync.Spec.Version
		args["environment"] = sync.Spec.EnvironmentRef
		args["release"] = sync.Spec.ReleaseRef
		args["pipeline"] = sync.Spec.Pipeline
		args["stage"] = sync.Spec.Stage
	}
	return args
}

func (r *SyncReconciler) nextAfterMetrics(ctx context.Context, sync *kaprov1alpha1.Sync, policy *kaprov1alpha1.GatePolicy) (ctrl.Result, error) {
	if policy != nil && policy.Spec.Approval != nil && policy.Spec.Approval.Required {
		return r.transitionTo(ctx, sync, kaprov1alpha1.SyncPhaseWaitingApproval)
	}
	return r.transitionTo(ctx, sync, kaprov1alpha1.SyncPhaseApplying)
}

func (r *SyncReconciler) handleWaitingApproval(ctx context.Context, sync *kaprov1alpha1.Sync) (ctrl.Result, error) {
	// A rejection annotation means a human clicked "Reject" via the webhook.
	if sync.Annotations[webhookAnnotationRejected] == "true" {
		rejectedBy := sync.Annotations[webhookAnnotationRejectedBy]
		if rejectedBy == "" {
			rejectedBy = "unknown"
		}
		policy, _ := r.getPolicy(ctx, sync.Spec.PolicyRef)
		return ctrl.Result{}, r.failSync(ctx, sync, policy,
			fmt.Sprintf("rejected by %s", rejectedBy))
	}

	// Send the approval notification exactly once per WaitingApproval entry.
	// Gate on ApprovalSentAt to survive controller restarts without re-spamming.
	if sync.Status.ApprovalSentAt == "" {
		r.sendApprovalNotification(ctx, sync)
	}

	// Check whether an Approval CR has been created (by webhook or kubectl).
	approvalGate := r.ApprovalGate
	if approvalGate == nil {
		approvalGate = &internalgate.ApprovalGate{Client: r.Client}
	}

	policy, _ := r.getPolicy(ctx, sync.Spec.PolicyRef)
	result, err := approvalGate.Evaluate(ctx, gate.Request{Sync: sync, Policy: policy})
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("approval gate: %w", err)
	}

	log.FromContext(ctx).Info("approval gate", "phase", result.Phase, "message", result.Message)

	if result.IsPassed() {
		return r.transitionTo(ctx, sync, kaprov1alpha1.SyncPhaseApplying)
	}

	r.Recorder.Event(sync, corev1.EventTypeNormal, "WaitingApproval", "Waiting for Approval CR")
	return ctrl.Result{RequeueAfter: requeueNormal}, nil
}

// webhookAnnotation* are the annotation keys set by the approval webhook server
// when a human POSTs to /reject. The controller reads them to call failSync.
const (
	webhookAnnotationRejected   = "kapro.io/rejected"
	webhookAnnotationRejectedBy = "kapro.io/rejected-by"
)

// sendApprovalNotification generates signed approve/reject URLs, dispatches the
// notification via all configured channels, then records ApprovalSentAt in status.
// Errors are logged and never block the sync.
func (r *SyncReconciler) sendApprovalNotification(ctx context.Context, sync *kaprov1alpha1.Sync) {
	logger := log.FromContext(ctx)

	var approveURL, rejectURL string
	if len(r.ApprovalSecret) > 0 && r.ExternalURL != "" {
		var err error
		approveURL, rejectURL, err = r.buildApprovalURLs(sync)
		if err != nil {
			logger.Error(err, "failed to build approval URLs — notification will omit links")
		}
	}

	if r.Notifier != nil {
		policy, _ := r.getPolicy(ctx, sync.Spec.PolicyRef)
		r.Notifier.Notify(ctx, notification.Event{
			Phase:       string(kaprov1alpha1.SyncPhaseWaitingApproval),
			Version:     sync.Spec.Version,
			Environment: sync.Spec.EnvironmentRef,
			Release:     sync.Spec.ReleaseRef,
			Message:     "Approval required to proceed",
			ApproveURL:  approveURL,
			RejectURL:   rejectURL,
		}, notificationPolicyFrom(policy))
	}

	// Persist ApprovalSentAt so we don't re-notify on every requeue.
	patch := client.MergeFrom(sync.DeepCopy())
	sync.Status.ApprovalSentAt = time.Now().UTC().Format(time.RFC3339)
	if err := r.Status().Patch(ctx, sync, patch); err != nil {
		// Non-fatal: worst case we send the notification again after a restart.
		logger.Error(err, "failed to patch ApprovalSentAt — may re-notify on restart")
	}
}

// buildApprovalURLs returns signed approve and reject URLs for the given Sync.
func (r *SyncReconciler) buildApprovalURLs(sync *kaprov1alpha1.Sync) (approveURL, rejectURL string, err error) {
	exp := time.Now().Add(token.DefaultTTL).Unix()

	baseClaims := token.Claims{
		SyncName:    sync.Name,
		Namespace:   sync.Namespace,
		Release:     sync.Spec.ReleaseRef,
		Environment: sync.Spec.EnvironmentRef,
		Version:     sync.Spec.Version,
		UID:         string(sync.UID),
		Exp:         exp,
	}

	approveClaims := baseClaims
	approveClaims.Action = "approve"
	approveToken, err := token.Sign(approveClaims, r.ApprovalSecret)
	if err != nil {
		return "", "", fmt.Errorf("sign approve token: %w", err)
	}

	rejectClaims := baseClaims
	rejectClaims.Action = "reject"
	rejectToken, err := token.Sign(rejectClaims, r.ApprovalSecret)
	if err != nil {
		return "", "", fmt.Errorf("sign reject token: %w", err)
	}

	base := strings.TrimRight(r.ExternalURL, "/")
	approveURL = fmt.Sprintf("%s/approve/%s?token=%s", base, sync.Name, approveToken)
	rejectURL = fmt.Sprintf("%s/reject/%s?token=%s", base, sync.Name, rejectToken)
	return approveURL, rejectURL, nil
}

func (r *SyncReconciler) handleApplying(ctx context.Context, sync *kaprov1alpha1.Sync) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	var env kaprov1alpha1.Environment
	if err := r.Get(ctx, client.ObjectKey{Name: sync.Spec.EnvironmentRef}, &env); err != nil {
		return ctrl.Result{}, fmt.Errorf("environment %s not found: %w", sync.Spec.EnvironmentRef, err)
	}

	// Capture current version for rollback before we change anything.
	if sync.Status.PreviousVersion == "" {
		mc, _ := r.getManagedCluster(ctx, sync.Spec.EnvironmentRef)
		if mc != nil && mc.Status.CurrentVersions[syncAppKey(sync)] != "" {
			patch := client.MergeFrom(sync.DeepCopy())
			sync.Status.PreviousVersion = mc.Status.CurrentVersions[syncAppKey(sync)]
			if err := r.Status().Patch(ctx, sync, patch); err != nil {
				return ctrl.Result{}, fmt.Errorf("capture PreviousVersion: %w", err)
			}
		}
	}

	// Resolve actuator from Environment spec — this is what makes pluggability real.
	if r.ActuatorRegistry != nil {
		act, err := r.ActuatorRegistry.Resolve(env.Spec.Actuator.Type)
		if err != nil {
			log.Error(err, "failed to resolve actuator — check Environment.spec.actuator.type")
			r.Recorder.Event(sync, corev1.EventTypeWarning, "ActuatorResolveFailed", err.Error())
			return ctrl.Result{RequeueAfter: requeueFast}, nil
		}
		if err := act.Apply(ctx, actuator.ApplyRequest{
			Environment:     &env,
			Version:         sync.Spec.Version,
			PreviousVersion: sync.Status.PreviousVersion,
			AppKey:          syncAppKey(sync),
		}); err != nil {
			log.Error(err, "Actuator.Apply failed, will retry")
			r.Recorder.Event(sync, corev1.EventTypeWarning, "ApplyFailed", err.Error())
			return ctrl.Result{RequeueAfter: requeueFast}, nil
		}
		log.Info("Actuator.Apply succeeded — waiting for convergence",
			"env", sync.Spec.EnvironmentRef,
			"actuator", env.Spec.Actuator.Type,
			"version", sync.Spec.Version,
		)
	}

	// Poll ManagedCluster for convergence (set by the cluster agent).
	mc, err := r.getManagedCluster(ctx, sync.Spec.EnvironmentRef)
	if err != nil {
		return ctrl.Result{RequeueAfter: requeueFast}, nil
	}

	if mc.Status.Phase == kaprov1alpha1.ClusterPhaseConverged &&
		mc.Status.CurrentVersions[syncAppKey(sync)] == sync.Spec.Version {
		log.Info("cluster converged", "env", sync.Spec.EnvironmentRef, "version", sync.Spec.Version)
		r.Recorder.Event(sync, corev1.EventTypeNormal, "Applied", fmt.Sprintf("Version %s applied to %s", sync.Spec.Version, sync.Spec.EnvironmentRef))
		patch := client.MergeFrom(sync.DeepCopy())
		sync.Status.Phase = kaprov1alpha1.SyncPhaseConverged
		sync.Status.ObservedGeneration = sync.Generation
		sync.Status.FinishedAt = time.Now().UTC().Format(time.RFC3339)
		apimeta.SetStatusCondition(&sync.Status.Conditions, metav1.Condition{
			Type:               "Ready",
			Status:             metav1.ConditionTrue,
			Reason:             "Converged",
			ObservedGeneration: sync.Generation,
			Message:            fmt.Sprintf("version %s applied to %s", sync.Spec.Version, sync.Spec.EnvironmentRef),
			LastTransitionTime: metav1.Now(),
		})
		if err := r.Status().Patch(ctx, sync, patch); err != nil {
			return ctrl.Result{}, fmt.Errorf("patch converged status: %w", err)
		}
		return ctrl.Result{}, nil
	}

	if mc.Status.Phase == kaprov1alpha1.ClusterPhaseFailed {
		policy, _ := r.getPolicy(ctx, sync.Spec.PolicyRef)
		return ctrl.Result{}, r.failSync(ctx, sync, policy,
			fmt.Sprintf("cluster %s reported Failed phase", sync.Spec.EnvironmentRef))
	}

	log.Info("waiting for convergence",
		"env", sync.Spec.EnvironmentRef,
		"clusterPhase", mc.Status.Phase,
		"currentVersion", mc.Status.CurrentVersions[syncAppKey(sync)],
		"wantVersion", sync.Spec.Version,
	)
	return ctrl.Result{RequeueAfter: requeueNormal}, nil
}

func (r *SyncReconciler) transitionTo(ctx context.Context, sync *kaprov1alpha1.Sync, phase kaprov1alpha1.SyncPhase) (ctrl.Result, error) {
	patch := client.MergeFrom(sync.DeepCopy())
	sync.Status.Phase = phase
	sync.Status.ObservedGeneration = sync.Generation
	// StartedAt marks when the Soaking phase begins — this is the correct clock
	// start for SoakGate duration checks. Setting it at Pending would cause the
	// soak to count time spent in HealthCheck/Verification, expiring it early.
	if phase == kaprov1alpha1.SyncPhaseSoaking && sync.Status.StartedAt == "" {
		sync.Status.StartedAt = time.Now().UTC().Format(time.RFC3339)
	}
	r.Recorder.Event(sync, corev1.EventTypeNormal, "PhaseTransition", fmt.Sprintf("→ %s", phase))
	if err := r.Status().Patch(ctx, sync, patch); err != nil {
		return ctrl.Result{}, fmt.Errorf("patch phase %s: %w", phase, err)
	}
	// Record transition metric.
	result := "success"
	if phase == kaprov1alpha1.SyncPhaseFailed {
		result = "failed"
	}
	kaprometrics.SyncTransitions.WithLabelValues(string(phase), result).Inc()
	// Notify after patch succeeds.
	// WaitingApproval is skipped here — sendApprovalNotification sends a richer
	// actionable notification (with approve/reject URLs) exactly once from handleWaitingApproval.
	if r.Notifier != nil && phase != kaprov1alpha1.SyncPhaseWaitingApproval {
		policy, _ := r.getPolicy(ctx, sync.Spec.PolicyRef)
		r.Notifier.Notify(ctx, notification.Event{
			Phase:       string(phase),
			Version:     sync.Spec.Version,
			Environment: sync.Spec.EnvironmentRef,
			Release:     sync.Spec.ReleaseRef,
			IsFailure:   phase == kaprov1alpha1.SyncPhaseFailed,
		}, notificationPolicyFrom(policy))
	}
	return ctrl.Result{Requeue: true}, nil
}

func (r *SyncReconciler) failSync(ctx context.Context, sync *kaprov1alpha1.Sync, policy *kaprov1alpha1.GatePolicy, msg string) error {
	patch := client.MergeFrom(sync.DeepCopy())
	sync.Status.Phase = kaprov1alpha1.SyncPhaseFailed
	sync.Status.ObservedGeneration = sync.Generation
	sync.Status.FinishedAt = time.Now().UTC().Format(time.RFC3339)
	sync.Status.Message = msg
	sync.Status.Conditions = nil // clear stale conditions before SetStatusCondition
	apimeta.SetStatusCondition(&sync.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionFalse,
		Reason:             "GateFailed",
		ObservedGeneration: sync.Generation,
		Message:            msg,
		LastTransitionTime: metav1.Now(),
	})
	r.Recorder.Event(sync, corev1.EventTypeWarning, "Failed", msg)

	if err := r.Status().Patch(ctx, sync, patch); err != nil {
		return fmt.Errorf("patch failed status: %w", err)
	}

	// Notify failure.
	if r.Notifier != nil {
		r.Notifier.Notify(ctx, notification.Event{
			Phase:       string(kaprov1alpha1.SyncPhaseFailed),
			Version:     sync.Spec.Version,
			Environment: sync.Spec.EnvironmentRef,
			Release:     sync.Spec.ReleaseRef,
			Message:     msg,
			IsFailure:   true,
		}, notificationPolicyFrom(policy))
	}

	// Trigger auto-rollback if policy says so and there's a previous version to roll back to.
	if policy != nil && policy.Spec.OnFailure == "rollback" && sync.Status.PreviousVersion != "" {
		return r.triggerRollback(ctx, sync)
	}

	return nil
}

// triggerRollback calls the actuator's Rollback() to immediately signal the
// delivery system, then creates a new Sync targeting the previous version
// to formally track the rollback through the gate+apply+converge cycle.
func (r *SyncReconciler) triggerRollback(ctx context.Context, failed *kaprov1alpha1.Sync) error {
	log := log.FromContext(ctx)

	// 1. Immediately call actuator.Rollback() so the delivery system starts
	//    reverting without waiting for the new Sync to reconcile.
	var env kaprov1alpha1.Environment
	if err := r.Get(ctx, client.ObjectKey{Name: failed.Spec.EnvironmentRef}, &env); err == nil {
		if r.ActuatorRegistry != nil {
			if act, actErr := r.ActuatorRegistry.Resolve(env.Spec.Actuator.Type); actErr == nil {
				if rbErr := act.Rollback(ctx, &env, failed.Status.PreviousVersion); rbErr != nil {
					log.Error(rbErr, "actuator Rollback() failed — rollback Sync will re-apply it")
					r.Recorder.Event(failed, corev1.EventTypeWarning, "ActuatorRollbackFailed", rbErr.Error())
				} else {
					log.Info("actuator Rollback() succeeded",
						"env", failed.Spec.EnvironmentRef,
						"previousVersion", failed.Status.PreviousVersion,
					)
				}
			}
		}
	}

	rollbackName := failed.Name + "-rollback"

	// Idempotent — don't create a second rollback Sync if one already exists.
	var existing kaprov1alpha1.Sync
	if err := r.Get(ctx, client.ObjectKey{Name: rollbackName, Namespace: failed.Namespace}, &existing); err == nil {
		log.Info("rollback Sync already exists", "name", rollbackName)
		return nil
	}

	rollback := &kaprov1alpha1.Sync{
		ObjectMeta: metav1.ObjectMeta{
			Name:      rollbackName,
			Namespace: failed.Namespace,
			Labels: map[string]string{
				"kapro.io/rollback-for": failed.Name,
				"kapro.io/release":      failed.Spec.ReleaseRef,
			},
		},
		Spec: kaprov1alpha1.SyncSpec{
			ReleaseRef:     failed.Spec.ReleaseRef,
			EnvironmentRef: failed.Spec.EnvironmentRef,
			Pipeline:       failed.Spec.Pipeline,
			Stage:          failed.Spec.Stage,
			Version:        failed.Status.PreviousVersion,
			PolicyRef:      failed.Spec.PolicyRef,
			AppKey:         failed.Spec.AppKey,
		},
	}

	if r.Scheme != nil {
		var release kaprov1alpha1.Release
		if err := r.Get(ctx, client.ObjectKey{Name: failed.Spec.ReleaseRef, Namespace: failed.Namespace}, &release); err == nil {
			if err := controllerutil.SetControllerReference(&release, rollback, r.Scheme); err != nil {
				log.Error(err, "failed to set ownerRef on rollback Sync — will create without owner")
			}
		}
	}

	if err := r.Create(ctx, rollback); err != nil {
		return fmt.Errorf("create rollback Sync: %w", err)
	}

	log.Info("created rollback Sync",
		"name", rollbackName,
		"targetVersion", failed.Status.PreviousVersion,
		"env", failed.Spec.EnvironmentRef,
	)
	r.Recorder.Event(failed, corev1.EventTypeWarning, "RollbackTriggered",
		fmt.Sprintf("Auto-rollback to %s triggered", failed.Status.PreviousVersion))

	return nil
}

func (r *SyncReconciler) getManagedCluster(ctx context.Context, envRef string) (*kaprov1alpha1.ManagedCluster, error) {
	var mcList kaprov1alpha1.ManagedClusterList
	if err := r.List(ctx, &mcList, client.MatchingLabels{
		"kapro.io/environment": envRef,
	}, client.Limit(100)); err != nil {
		return nil, err
	}
	for _, mc := range mcList.Items {
		if mc.Spec.EnvironmentRef == envRef {
			return &mc, nil
		}
	}
	return nil, fmt.Errorf("no ManagedCluster found for environment %s", envRef)
}

func (r *SyncReconciler) getPolicy(ctx context.Context, policyRef string) (*kaprov1alpha1.GatePolicy, error) {
	if policyRef == "" {
		return nil, nil
	}
	var policy kaprov1alpha1.GatePolicy
	if err := r.Get(ctx, client.ObjectKey{Name: policyRef}, &policy); err != nil {
		return nil, err
	}
	return &policy, nil
}

func (r *SyncReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		WithOptions(controller.Options{
			RateLimiter: workqueue.NewTypedItemExponentialFailureRateLimiter[reconcile.Request](50*time.Millisecond, 10*time.Minute),
		}).
		// GenerationChangedPredicate: only react to spec changes, not every
		// status patch the controller writes itself. FSM advancement uses
		// explicit ctrl.Result{RequeueAfter:...} — not watch-triggered events —
		// so this predicate does not break the state machine.
		For(&kaprov1alpha1.Sync{},
			builder.WithPredicates(predicate.GenerationChangedPredicate{}),
		).
		// Watch ManagedClusters so convergence is event-driven, not poll-driven.
		// When a cluster agent writes status.phase=Converged, the owning
		// Sync is woken up immediately rather than waiting for RequeueAfter.
		Watches(
			&kaprov1alpha1.ManagedCluster{},
			handler.EnqueueRequestsFromMapFunc(r.syncsForManagedCluster),
		).
		// Watch Approvals so that a manual approval immediately wakes up the
		// Sync that is stuck in WaitingApproval — without this watch the
		// Sync would only advance on the next RequeueAfter tick.
		Watches(
			&kaprov1alpha1.Approval{},
			handler.EnqueueRequestsFromMapFunc(r.syncForApproval),
		).
		Complete(r)
}

// syncsForManagedCluster returns reconcile.Requests for all in-flight Syncs
// that target the changed ManagedCluster's environment.
func (r *SyncReconciler) syncsForManagedCluster(ctx context.Context, obj client.Object) []ctrl.Request {
	mc, ok := obj.(*kaprov1alpha1.ManagedCluster)
	if !ok {
		return nil
	}
	var syncList kaprov1alpha1.SyncList
	if err := r.List(ctx, &syncList,
		client.MatchingFields{IndexKeyEnvironment: mc.Spec.EnvironmentRef},
		client.Limit(500),
	); err != nil {
		return nil
	}
	var reqs []ctrl.Request
	for i := range syncList.Items {
		s := &syncList.Items[i]
		// Only wake up in-flight Syncs — terminal ones don't need a nudge.
		switch s.Status.Phase {
		case kaprov1alpha1.SyncPhaseConverged, kaprov1alpha1.SyncPhaseFailed:
			continue
		}
		reqs = append(reqs, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(s)})
	}
	return reqs
}

// syncForApproval maps an Approval object to the Sync it unblocks.
// Approval.spec.kind == "Sync" and spec.ref is the environment name.
func (r *SyncReconciler) syncForApproval(ctx context.Context, obj client.Object) []ctrl.Request {
	approval, ok := obj.(*kaprov1alpha1.Approval)
	if !ok {
		return nil
	}
	if approval.Spec.Kind != kaprov1alpha1.ApprovalKindSync {
		return nil
	}
	syncName := approval.Spec.Release + "-" + approval.Spec.Ref
	return []ctrl.Request{{NamespacedName: client.ObjectKey{Name: syncName, Namespace: approval.Namespace}}}
}

// syncAppKey returns the ManagedCluster.status.currentVersions key for this Sync.
// Falls back to the ReleaseRef (artifact name) when AppKey is not set — preserves
// backward compatibility for single-app deployments.
func syncAppKey(sync *kaprov1alpha1.Sync) string {
	if sync.Spec.AppKey != "" {
		return sync.Spec.AppKey
	}
	return sync.Spec.ReleaseRef
}
