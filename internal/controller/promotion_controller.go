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
	plugingwgate "kapro.io/kapro/internal/gate/plugingateway"
	webhookgate "kapro.io/kapro/internal/gate/webhook"
	kaprometrics "kapro.io/kapro/internal/metrics"
	"kapro.io/kapro/internal/plugin"
	"kapro.io/kapro/internal/plugin/bridge"
	"kapro.io/kapro/internal/webhook/token"
	pkghealth "kapro.io/kapro/pkg/health"
	"kapro.io/kapro/pkg/notification"
	"kapro.io/kapro/pkg/oci"
	crdprovider "kapro.io/kapro/internal/provider/crd"
)

// PromotionReconciler drives a single cluster through the gate pipeline.
//
// State machine:
//
//	Pending → Verification → HealthCheck → Soaking → MetricsCheck → WaitingApproval → Applying → Converged | Failed
type PromotionReconciler struct {
	client.Client
	Recorder        record.EventRecorder
	ActuatorRegistry *actuator.Registry
	Provider     *crdprovider.CRDProvider
	SoakGate     *internalgate.SoakGate
	MetricsGate  *internalgate.MetricsGate
	ApprovalGate *internalgate.ApprovalGate
	// KedaGate evaluates KEDA-provider metrics (provider == "keda").
	// When nil, KEDA metrics are skipped (pass-through).
	KedaGate gate.Gate
	// MLflowGate evaluates MLflow Model Registry metrics (provider == "mlflow").
	// When nil, MLflow metrics are skipped (pass-through).
	MLflowGate gate.Gate
	// ShadowGate validates deployment safety by comparing shadow (mirrored)
	// traffic responses against production (provider == "shadow").
	// When nil, shadow metrics are skipped (pass-through).
	ShadowGate gate.Gate
	// KGatewayGate checks kgateway traffic policy health and canary weights
	// (provider == "kgateway"). When nil, kgateway metrics are skipped.
	KGatewayGate gate.Gate
	// VerificationGate verifies artifact signatures before the promotion advances.
	// When nil, signature verification is skipped (pass-through).
	VerificationGate gate.Gate
	// HealthAssessor evaluates workload health in the target namespace.
	// When nil, the health check gate is skipped (pass-through).
	HealthAssessor pkghealth.Assessor
	Notifier       notification.Notifier // nil-safe: Notify() checks policy before sending
	// OCIService enables artifact inspection and promotion operations.
	// When nil, OCI operations are skipped.
	OCIService oci.Service
	// PluginRegistry holds active gRPC connections to registered plugins.
	// When a metric provider is not matched by a built-in gate, the registry
	// is consulted to find a plugin that registered with matching type/name.
	// When nil, unknown providers fall through to MetricsGate.
	PluginRegistry *plugin.Registry
	// ApprovalSecret is the HMAC key used to sign approval/reject tokens.
	// When non-empty alongside ExternalURL, the controller sends one-click
	// approval notifications when a Promotion enters WaitingApproval.
	ApprovalSecret []byte
	// ExternalURL is the base URL of the Kapro approval webhook server
	// (e.g. "https://kapro.internal"). Used to build approve/reject URLs in notifications.
	ExternalURL string
	// CELGate evaluates built-in CEL expression GateTemplates (type == "cel").
	// When nil, a zero-allocation CELGate is created per evaluation.
	CELGate gate.Gate
	// ArgoGate evaluates Argo Rollouts AnalysisRun GateTemplates (type == "argo-analysis").
	// When nil, the argo-analysis gate type is unavailable and evaluations fail.
	ArgoGate gate.Gate
	// Scheme is required to set ownerReferences on objects created by this controller
	// (e.g. rollback Promotions). Injected at startup via main.go.
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=kapro.io,resources=promotions,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kapro.io,resources=promotions/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kapro.io,resources=promotions/finalizers,verbs=update
// +kubebuilder:rbac:groups=kapro.io,resources=promotionpolicies,verbs=get;list;watch
// +kubebuilder:rbac:groups=kapro.io,resources=gatetemplates,verbs=get;list;watch
// +kubebuilder:rbac:groups=kapro.io,resources=environments,verbs=get;list;watch
// +kubebuilder:rbac:groups=kapro.io,resources=clusterregistrations,verbs=get;list;watch
// +kubebuilder:rbac:groups=kapro.io,resources=approvals,verbs=get;list;watch

const promotionFinalizer = "kapro.io/promotion-cleanup"

func (r *PromotionReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	var promo kaprov1alpha1.Promotion
	if err := r.Get(ctx, req.NamespacedName, &promo); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	log.Info("reconciling Promotion", "name", promo.Name, "phase", promo.Status.Phase, "env", promo.Spec.EnvironmentRef)

	// Handle deletion: mark Failed so BatchRun doesn't wait forever, then remove finalizer.
	if !promo.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(&promo, promotionFinalizer) {
			if promo.Status.Phase != kaprov1alpha1.PromotionPhaseFailed &&
				promo.Status.Phase != kaprov1alpha1.PromotionPhaseConverged {
				_ = r.failPromotion(ctx, &promo, nil, "Promotion deleted before convergence")
			}
			controllerutil.RemoveFinalizer(&promo, promotionFinalizer)
			if err := r.Update(ctx, &promo); err != nil {
				return ctrl.Result{}, fmt.Errorf("remove promotion finalizer: %w", err)
			}
		}
		return ctrl.Result{}, nil
	}

	// Ensure finalizer is registered before we touch any external state.
	if !controllerutil.ContainsFinalizer(&promo, promotionFinalizer) {
		controllerutil.AddFinalizer(&promo, promotionFinalizer)
		if err := r.Update(ctx, &promo); err != nil {
			return ctrl.Result{}, fmt.Errorf("add promotion finalizer: %w", err)
		}
		return ctrl.Result{Requeue: true}, nil
	}

	switch promo.Status.Phase {
	case "":
		return r.transitionTo(ctx, &promo, kaprov1alpha1.PromotionPhasePending)
	case kaprov1alpha1.PromotionPhasePending:
		return r.handlePending(ctx, &promo)
	case kaprov1alpha1.PromotionPhaseVerification:
		return r.handleVerification(ctx, &promo)
	case kaprov1alpha1.PromotionPhaseHealthCheck:
		return r.handleHealthCheck(ctx, &promo)
	case kaprov1alpha1.PromotionPhaseSoaking:
		return r.handleSoaking(ctx, &promo)
	case kaprov1alpha1.PromotionPhaseMetricsCheck:
		return r.handleMetricsCheck(ctx, &promo)
	case kaprov1alpha1.PromotionPhaseWaitingApproval:
		return r.handleWaitingApproval(ctx, &promo)
	case kaprov1alpha1.PromotionPhaseApplying:
		return r.handleApplying(ctx, &promo)
	case kaprov1alpha1.PromotionPhaseConverged, kaprov1alpha1.PromotionPhaseFailed:
		return ctrl.Result{}, nil
	}

	return ctrl.Result{}, nil
}

func (r *PromotionReconciler) handlePending(ctx context.Context, promo *kaprov1alpha1.Promotion) (ctrl.Result, error) {
	// Check cluster is reachable via ClusterRegistration heartbeat
	reg, err := r.getRegistration(ctx, promo.Spec.EnvironmentRef)
	if err != nil {
		return ctrl.Result{RequeueAfter: requeueFast}, nil
	}

	if !isHeartbeatFresh(reg.Status.LastHeartbeat) {
		log.FromContext(ctx).Info("cluster heartbeat stale, waiting", "env", promo.Spec.EnvironmentRef)
		return ctrl.Result{RequeueAfter: requeueNormal}, nil
	}

	return r.transitionTo(ctx, promo, kaprov1alpha1.PromotionPhaseVerification)
}

// handleVerification runs the VerificationGate to confirm the artifact has a
// valid signature before advancing to HealthCheck.  When VerificationGate is
// nil the phase is skipped immediately.
func (r *PromotionReconciler) handleVerification(ctx context.Context, promo *kaprov1alpha1.Promotion) (ctrl.Result, error) {
	g := r.VerificationGate
	if g == nil {
		log.FromContext(ctx).Info("VerificationGate is nil — skipping signature verification",
			"promotion", promo.Name,
		)
		return r.transitionTo(ctx, promo, kaprov1alpha1.PromotionPhaseHealthCheck)
	}

	policy, _ := r.getPolicy(ctx, promo.Spec.PolicyRef)
	result, err := g.Evaluate(ctx, gate.Request{Promotion: promo, Policy: policy})
	if err != nil {
		// Hard error — surface to the controller for exponential back-off.
		return ctrl.Result{}, fmt.Errorf("verification gate: %w", err)
	}

	log.FromContext(ctx).Info("verification gate evaluated",
		"passed", result.Passed,
		"message", result.Message,
		"promotion", promo.Name,
	)

	if result.Passed {
		r.Recorder.Event(promo, corev1.EventTypeNormal, "GatePassed", "VerificationGate: "+result.Message)
		return r.transitionTo(ctx, promo, kaprov1alpha1.PromotionPhaseHealthCheck)
	}

	r.Recorder.Event(promo, corev1.EventTypeWarning, "GateFailed", result.Message)
	after := parseDurationOrDefault(result.RetryAfter)
	return ctrl.Result{RequeueAfter: after}, nil
}

func (r *PromotionReconciler) handleHealthCheck(ctx context.Context, promo *kaprov1alpha1.Promotion) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	// When no assessor is configured, pass through to Soaking (nil-safe gate pattern).
	if r.HealthAssessor == nil {
		return r.transitionToSoakOrMetrics(ctx, promo)
	}

	result, err := r.HealthAssessor.AssessHealth(ctx, pkghealth.AssessRequest{
		Namespace: promo.Namespace,
	})
	if err != nil {
		log.Error(err, "health assessor error, will retry")
		return ctrl.Result{RequeueAfter: requeueFast}, nil
	}

	log.Info("health check", "overall", result.Overall, "message", result.Message)

	switch result.Overall {
	case pkghealth.StatusDegraded:
		policy, _ := r.getPolicy(ctx, promo.Spec.PolicyRef)
		return ctrl.Result{}, r.failPromotion(ctx, promo, policy, "health check failed: "+result.Message)
	case pkghealth.StatusProgressing, pkghealth.StatusUnknown, pkghealth.StatusMissing:
		return ctrl.Result{RequeueAfter: requeueNormal}, nil
	default:
		// Healthy, Suspended — proceed.
		return r.transitionToSoakOrMetrics(ctx, promo)
	}
}

// transitionToSoakOrMetrics advances past HealthCheck: to Soaking if a soak is configured,
// otherwise straight to MetricsCheck.
func (r *PromotionReconciler) transitionToSoakOrMetrics(ctx context.Context, promo *kaprov1alpha1.Promotion) (ctrl.Result, error) {
	policy, err := r.getPolicy(ctx, promo.Spec.PolicyRef)
	if err != nil || policy == nil || policy.Spec.Gate.SoakTime == "" {
		return r.transitionTo(ctx, promo, kaprov1alpha1.PromotionPhaseMetricsCheck)
	}
	return r.transitionTo(ctx, promo, kaprov1alpha1.PromotionPhaseSoaking)
}

func (r *PromotionReconciler) handleSoaking(ctx context.Context, promo *kaprov1alpha1.Promotion) (ctrl.Result, error) {
	policy, err := r.getPolicy(ctx, promo.Spec.PolicyRef)
	if err != nil || policy == nil {
		return r.transitionTo(ctx, promo, kaprov1alpha1.PromotionPhaseMetricsCheck)
	}

	g := r.SoakGate
	if g == nil {
		g = &internalgate.SoakGate{}
	}

	result, err := g.Evaluate(ctx, gate.Request{Promotion: promo, Policy: policy})
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("soak gate: %w", err)
	}

	log.FromContext(ctx).Info("soak gate", "passed", result.Passed, "message", result.Message)

	if result.Passed {
		r.Recorder.Event(promo, corev1.EventTypeNormal, "GatePassed", "SoakGate: "+result.Message)
		return r.transitionTo(ctx, promo, kaprov1alpha1.PromotionPhaseMetricsCheck)
	}

	after := parseDurationOrDefault(result.RetryAfter)
	return ctrl.Result{RequeueAfter: after}, nil
}

func (r *PromotionReconciler) handleMetricsCheck(ctx context.Context, promo *kaprov1alpha1.Promotion) (ctrl.Result, error) {
	policy, err := r.getPolicy(ctx, promo.Spec.PolicyRef)
	if err != nil || policy == nil || len(policy.Spec.Gate.Metrics) == 0 {
		return r.nextAfterMetrics(ctx, promo, policy)
	}

	metricsGate := r.MetricsGate
	if metricsGate == nil {
		metricsGate = &internalgate.MetricsGate{}
	}

	// Evaluate each metric in order; block on the first failure.
	// Dispatch to the KEDA gate when provider == "keda", MLflow gate when
	// provider == "mlflow", otherwise use MetricsGate.
	for i, metric := range policy.Spec.Gate.Metrics {
		var g gate.Gate
		switch metric.Provider {
		case "keda":
			g = r.KedaGate
			if g == nil {
				log.FromContext(ctx).Info("KEDA metric configured but KedaGate is nil — passing through", "index", i)
				continue
			}
		case "mlflow":
			g = r.MLflowGate
			if g == nil {
				log.FromContext(ctx).Info("MLflow metric configured but MLflowGate is nil — passing through", "index", i)
				continue
			}
		case "shadow":
			g = r.ShadowGate
			if g == nil {
				log.FromContext(ctx).Info("shadow metric configured but ShadowGate is nil — passing through", "index", i)
				continue
			}
		case "kgateway":
			g = r.KGatewayGate
			if g == nil {
				log.FromContext(ctx).Info("kgateway metric configured but KGatewayGate is nil — passing through", "index", i)
				continue
			}
		default:
			// Check plugin registry for a gate plugin registered under this provider name.
			if r.PluginRegistry != nil {
				if entry, err := r.PluginRegistry.Get(metric.Provider); err == nil && entry.Conn != nil {
					g = &bridge.GateBridge{PluginName: metric.Provider, Conn: entry.Conn}
					log.FromContext(ctx).Info("routing metric to plugin gate", "provider", metric.Provider)
				}
			}
			if g == nil {
				g = metricsGate
			}
		}

		result, err := g.Evaluate(ctx, gate.Request{Promotion: promo, Policy: policy, MetricIndex: i})
		if err != nil {
			// Non-fatal: log and retry — don't fail the promotion on transient errors.
			log.FromContext(ctx).Error(err, "metrics gate error, will retry", "index", i)
			return ctrl.Result{RequeueAfter: requeueNormal}, nil
		}
		log.FromContext(ctx).Info("metrics gate", "index", i, "provider", metric.Provider, "passed", result.Passed, "message", result.Message)
		if !result.Passed {
			r.Recorder.Event(promo, corev1.EventTypeWarning, "GateFailed", result.Message)
			after := parseDurationOrDefault(result.RetryAfter)
			return ctrl.Result{RequeueAfter: after}, nil
		}
	}

	// GateTemplate path — if the policy references any GateTemplates, evaluate them.
	if policy != nil && len(policy.Spec.Gate.Templates) > 0 {
		allPassed, requeueAfter, err := r.evaluateGateTemplates(ctx, promo, policy)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("evaluateGateTemplates: %w", err)
		}
		if !allPassed {
			return ctrl.Result{RequeueAfter: requeueAfter}, nil
		}
	}

	return r.nextAfterMetrics(ctx, promo, policy)
}

// evaluateGateTemplates evaluates all GateTemplate refs from the policy.
// Results are written to Promotion.Status.Gates[] (Kapro's authoritative snapshot).
// Returns: (allPassed, requeueAfter, error).
// The state machine reads Status.Gates[] — same pattern as kubelet reads ContainerStatus.
func (r *PromotionReconciler) evaluateGateTemplates(ctx context.Context, promo *kaprov1alpha1.Promotion, policy *kaprov1alpha1.PromotionPolicy) (bool, time.Duration, error) {
	log := log.FromContext(ctx)

	now := time.Now().UTC().Format(time.RFC3339)
	gates := promo.Status.Gates
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

		// Resolve args: template defaults < policy overrides < promotion context.
		args := resolveArgs(&tmpl, ref.Args, promo)

		// Dispatch to the correct gate impl based on type.
		g, err := r.gateForTemplate(&tmpl)
		if err != nil {
			return false, 0, fmt.Errorf("gate for template %q: %w", ref.Name, err)
		}

		result, err := g.Evaluate(ctx, gate.Request{
			Promotion: promo,
			Template:  &tmpl,
			Args:      args,
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

		// Normalise phase from Passed bool when Phase is not set by the gate impl.
		phase := result.Phase
		if phase == "" {
			if result.Passed {
				phase = kaprov1alpha1.GatePhasePassed
			} else {
				phase = kaprov1alpha1.GatePhaseFailed
			}
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
		r.Recorder.Event(promo, corev1.EventTypeNormal, "GateEvaluated",
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
	base := promo.DeepCopy()   // snapshot before mutation
	promo.Status.Gates = gates // apply change
	if err := r.Status().Patch(ctx, promo, client.MergeFrom(base)); err != nil {
		return false, 0, fmt.Errorf("patch gate statuses: %w", err)
	}

	return allPassed, requeueAfter, nil
}

// gateForTemplate returns the Gate implementation for the given GateTemplate type.
// Kapro calls Gate.Evaluate() without knowing which runner is behind it — same as kubelet → CRI.
func (r *PromotionReconciler) gateForTemplate(tmpl *kaprov1alpha1.GateTemplate) (gate.Gate, error) {
	switch tmpl.Spec.Type {
	case "cel":
		if r.CELGate != nil {
			return r.CELGate, nil
		}
		return &celgate.Gate{Client: r.Client}, nil
	case "argo-analysis":
		if r.ArgoGate != nil {
			return r.ArgoGate, nil
		}
		return nil, fmt.Errorf("argo-analysis gate requested but ArgoGate is not wired — set ArgoGate in PromotionReconciler")
	case "webhook":
		return &webhookgate.Gate{}, nil
	case "job":
		return &jobgate.Gate{Client: r.Client}, nil
	case "plugin-gateway":
		return &plugingwgate.Gate{Client: r.Client}, nil
	default:
		// Try plugin registry for externally registered gate types.
		if r.PluginRegistry != nil {
			if entry, err := r.PluginRegistry.Get(tmpl.Spec.Type); err == nil && entry.Conn != nil {
				return &bridge.GateBridge{PluginName: tmpl.Spec.Type, Conn: entry.Conn}, nil
			}
		}
		return nil, fmt.Errorf("unknown gate type %q — register a plugin or use: cel|job|webhook|argo-analysis", tmpl.Spec.Type)
	}
}

// resolveArgs builds the final args map: template defaults < policy overrides < promotion context.
func resolveArgs(tmpl *kaprov1alpha1.GateTemplate, policyOverrides map[string]string, promo *kaprov1alpha1.Promotion) map[string]string {
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
	// 3. Promotion context — always injected.
	if promo != nil {
		args["version"] = promo.Spec.Version
		args["environment"] = promo.Spec.EnvironmentRef
		args["release"] = promo.Spec.ReleaseRef
	}
	return args
}

func findOrCreateGateStatus(gates []kaprov1alpha1.GateRunStatus, name, now string) kaprov1alpha1.GateRunStatus {
	for _, g := range gates {
		if g.Name == name {
			return g
		}
	}
	return kaprov1alpha1.GateRunStatus{
		Name:      name,
		Phase:     kaprov1alpha1.GatePhasePending,
		StartedAt: now,
	}
}

func setGateStatus(gates *[]kaprov1alpha1.GateRunStatus, gs kaprov1alpha1.GateRunStatus) {
	for i, g := range *gates {
		if g.Name == gs.Name {
			(*gates)[i] = gs
			return
		}
	}
	*gates = append(*gates, gs)
}

func toAPIConditionResults(in []gate.ConditionResult) []kaprov1alpha1.GateConditionResult {
	out := make([]kaprov1alpha1.GateConditionResult, len(in))
	for i, r := range in {
		out[i] = kaprov1alpha1.GateConditionResult{
			Name:    r.Name,
			Phase:   r.Phase,
			Value:   r.Value,
			Message: r.Message,
		}
	}
	return out
}

func (r *PromotionReconciler) nextAfterMetrics(ctx context.Context, promo *kaprov1alpha1.Promotion, policy *kaprov1alpha1.PromotionPolicy) (ctrl.Result, error) {
	if policy != nil && policy.Spec.Approval != nil && policy.Spec.Approval.Required {
		return r.transitionTo(ctx, promo, kaprov1alpha1.PromotionPhaseWaitingApproval)
	}
	return r.transitionTo(ctx, promo, kaprov1alpha1.PromotionPhaseApplying)
}

func (r *PromotionReconciler) handleWaitingApproval(ctx context.Context, promo *kaprov1alpha1.Promotion) (ctrl.Result, error) {
	// A rejection annotation means a human clicked "Reject" via the webhook.
	// Delegate the failure to failPromotion() to preserve all controller invariants
	// (FinishedAt, conditions, events, rollback signal).
	if promo.Annotations[webhookAnnotationRejected] == "true" {
		rejectedBy := promo.Annotations[webhookAnnotationRejectedBy]
		if rejectedBy == "" {
			rejectedBy = "unknown"
		}
		policy, _ := r.getPolicy(ctx, promo.Spec.PolicyRef)
		return ctrl.Result{}, r.failPromotion(ctx, promo, policy,
			fmt.Sprintf("rejected by %s", rejectedBy))
	}

	// Send the approval notification exactly once per WaitingApproval entry.
	// Gate on ApprovalSentAt to survive controller restarts without re-spamming.
	if promo.Status.ApprovalSentAt == "" {
		r.sendApprovalNotification(ctx, promo)
	}

	// Check whether an Approval CR has been created (by webhook or kubectl).
	g := r.ApprovalGate
	if g == nil {
		g = &internalgate.ApprovalGate{Client: r.Client}
	}

	policy, _ := r.getPolicy(ctx, promo.Spec.PolicyRef)
	result, err := g.Evaluate(ctx, gate.Request{Promotion: promo, Policy: policy})
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("approval gate: %w", err)
	}

	log.FromContext(ctx).Info("approval gate", "passed", result.Passed, "message", result.Message)

	if result.Passed {
		return r.transitionTo(ctx, promo, kaprov1alpha1.PromotionPhaseApplying)
	}

	r.Recorder.Event(promo, corev1.EventTypeNormal, "WaitingApproval", "Waiting for Approval CR")
	return ctrl.Result{RequeueAfter: requeueNormal}, nil
}

// webhookAnnotation* are the annotation keys set by the approval webhook server
// when a human POSTs to /reject. The controller reads them to call failPromotion.
const (
	webhookAnnotationRejected   = "kapro.io/rejected"
	webhookAnnotationRejectedBy = "kapro.io/rejected-by"
)

// sendApprovalNotification generates signed approve/reject URLs, dispatches the
// notification via all configured channels, then records ApprovalSentAt in status.
// Errors are logged and never block the promotion.
func (r *PromotionReconciler) sendApprovalNotification(ctx context.Context, promo *kaprov1alpha1.Promotion) {
	logger := log.FromContext(ctx)

	var approveURL, rejectURL string
	if len(r.ApprovalSecret) > 0 && r.ExternalURL != "" {
		var err error
		approveURL, rejectURL, err = r.buildApprovalURLs(promo)
		if err != nil {
			logger.Error(err, "failed to build approval URLs — notification will omit links")
		}
	}

	if r.Notifier != nil {
		policy, _ := r.getPolicy(ctx, promo.Spec.PolicyRef)
		r.Notifier.Notify(ctx, notification.Event{
			Phase:       string(kaprov1alpha1.PromotionPhaseWaitingApproval),
			Version:     promo.Spec.Version,
			Environment: promo.Spec.EnvironmentRef,
			Release:     promo.Spec.ReleaseRef,
			Message:     "Approval required to proceed",
			ApproveURL:  approveURL,
			RejectURL:   rejectURL,
		}, policy)
	}

	// Persist ApprovalSentAt so we don't re-notify on every requeue.
	patch := client.MergeFrom(promo.DeepCopy())
	promo.Status.ApprovalSentAt = time.Now().UTC().Format(time.RFC3339)
	if err := r.Status().Patch(ctx, promo, patch); err != nil {
		// Non-fatal: worst case we send the notification again after a restart.
		logger.Error(err, "failed to patch ApprovalSentAt — may re-notify on restart")
	}
}

// buildApprovalURLs returns signed approve and reject URLs for the given Promotion.
func (r *PromotionReconciler) buildApprovalURLs(promo *kaprov1alpha1.Promotion) (approveURL, rejectURL string, err error) {
	exp := time.Now().Add(token.DefaultTTL).Unix()

	baseClaims := token.Claims{
		PromotionName: promo.Name,
		Namespace:     promo.Namespace,
		Release:       promo.Spec.ReleaseRef,
		Environment:   promo.Spec.EnvironmentRef,
		Version:       promo.Spec.Version,
		UID:           string(promo.UID),
		Exp:           exp,
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
	approveURL = fmt.Sprintf("%s/approve/%s?token=%s", base, promo.Name, approveToken)
	rejectURL = fmt.Sprintf("%s/reject/%s?token=%s", base, promo.Name, rejectToken)
	return approveURL, rejectURL, nil
}

func (r *PromotionReconciler) handleApplying(ctx context.Context, promo *kaprov1alpha1.Promotion) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	var env kaprov1alpha1.Environment
	if err := r.Get(ctx, client.ObjectKey{Name: promo.Spec.EnvironmentRef}, &env); err != nil {
		return ctrl.Result{}, fmt.Errorf("environment %s not found: %w", promo.Spec.EnvironmentRef, err)
	}

	// Capture current version for rollback before we change anything.
	if promo.Status.PreviousVersion == "" {
		reg, _ := r.getRegistration(ctx, promo.Spec.EnvironmentRef)
		if reg != nil && reg.Status.CurrentVersions[promoAppKey(promo)] != "" {
			patch := client.MergeFrom(promo.DeepCopy())
			promo.Status.PreviousVersion = reg.Status.CurrentVersions[promoAppKey(promo)]
			if err := r.Status().Patch(ctx, promo, patch); err != nil {
				return ctrl.Result{}, fmt.Errorf("capture PreviousVersion: %w", err)
			}
		}
	}

	// Resolve actuator from Environment spec — this is what makes pluggability real.
	if r.ActuatorRegistry != nil {
		act, err := r.ActuatorRegistry.Resolve(env.Spec.Actuator.Type)
		if err != nil {
			log.Error(err, "failed to resolve actuator — check Environment.spec.actuator.type")
			r.Recorder.Event(promo, corev1.EventTypeWarning, "ActuatorResolveFailed", err.Error())
			return ctrl.Result{RequeueAfter: requeueFast}, nil
		}
		if err := act.Apply(ctx, actuator.ApplyRequest{
			Environment:     &env,
			Version:         promo.Spec.Version,
			PreviousVersion: promo.Status.PreviousVersion,
			AppKey:          promoAppKey(promo),
		}); err != nil {
			log.Error(err, "Actuator.Apply failed, will retry")
			r.Recorder.Event(promo, corev1.EventTypeWarning, "ApplyFailed", err.Error())
			return ctrl.Result{RequeueAfter: requeueFast}, nil
		}
		log.Info("Actuator.Apply succeeded — waiting for convergence",
			"env", promo.Spec.EnvironmentRef,
			"actuator", env.Spec.Actuator.Type,
			"version", promo.Spec.Version,
		)
	}

	// Poll ClusterRegistration for convergence (set by cluster-controller).
	reg, err := r.getRegistration(ctx, promo.Spec.EnvironmentRef)
	if err != nil {
		return ctrl.Result{RequeueAfter: requeueFast}, nil
	}

	if reg.Status.Phase == kaprov1alpha1.ClusterPhaseConverged &&
		reg.Status.CurrentVersions[promoAppKey(promo)] == promo.Spec.Version {
		log.Info("cluster converged", "env", promo.Spec.EnvironmentRef, "version", promo.Spec.Version)
		r.Recorder.Event(promo, corev1.EventTypeNormal, "Applied", fmt.Sprintf("Version %s applied to %s", promo.Spec.Version, promo.Spec.EnvironmentRef))
		patch := client.MergeFrom(promo.DeepCopy())
		promo.Status.Phase = kaprov1alpha1.PromotionPhaseConverged
		promo.Status.ObservedGeneration = promo.Generation
		promo.Status.FinishedAt = time.Now().UTC().Format(time.RFC3339)
		apimeta.SetStatusCondition(&promo.Status.Conditions, metav1.Condition{
			Type:               "Ready",
			Status:             metav1.ConditionTrue,
			Reason:             "Converged",
			ObservedGeneration: promo.Generation,
			Message:            fmt.Sprintf("version %s applied to %s", promo.Spec.Version, promo.Spec.EnvironmentRef),
			LastTransitionTime: metav1.Now(),
		})
		if err := r.Status().Patch(ctx, promo, patch); err != nil {
			return ctrl.Result{}, fmt.Errorf("patch converged status: %w", err)
		}
		return ctrl.Result{}, nil
	}

	if reg.Status.Phase == kaprov1alpha1.ClusterPhaseFailed {
		policy, _ := r.getPolicy(ctx, promo.Spec.PolicyRef)
		return ctrl.Result{}, r.failPromotion(ctx, promo, policy,
			fmt.Sprintf("cluster %s reported Failed phase", promo.Spec.EnvironmentRef))
	}

	log.Info("waiting for convergence",
		"env", promo.Spec.EnvironmentRef,
		"clusterPhase", reg.Status.Phase,
		"currentVersion", reg.Status.CurrentVersions[promoAppKey(promo)],
		"wantVersion", promo.Spec.Version,
	)
	return ctrl.Result{RequeueAfter: requeueNormal}, nil
}

func (r *PromotionReconciler) transitionTo(ctx context.Context, promo *kaprov1alpha1.Promotion, phase kaprov1alpha1.PromotionPhase) (ctrl.Result, error) {
	patch := client.MergeFrom(promo.DeepCopy())
	promo.Status.Phase = phase
	promo.Status.ObservedGeneration = promo.Generation
	// StartedAt marks when the Soaking phase begins — this is the correct clock
	// start for SoakGate duration checks. Setting it at Pending would cause the
	// soak to count time spent in HealthCheck/Verification, expiring it early.
	if phase == kaprov1alpha1.PromotionPhaseSoaking && promo.Status.StartedAt == "" {
		promo.Status.StartedAt = time.Now().UTC().Format(time.RFC3339)
	}
	r.Recorder.Event(promo, corev1.EventTypeNormal, "PhaseTransition", fmt.Sprintf("→ %s", phase))
	if err := r.Status().Patch(ctx, promo, patch); err != nil {
		return ctrl.Result{}, fmt.Errorf("patch phase %s: %w", phase, err)
	}
	// Record transition metric.
	result := "success"
	if phase == kaprov1alpha1.PromotionPhaseFailed {
		result = "failed"
	}
	kaprometrics.PromotionTransitions.WithLabelValues(string(phase), result).Inc()
	// Notify after patch succeeds.
	// WaitingApproval is skipped here — sendApprovalNotification sends a richer
	// actionable notification (with approve/reject URLs) exactly once from handleWaitingApproval.
	if r.Notifier != nil && phase != kaprov1alpha1.PromotionPhaseWaitingApproval {
		policy, _ := r.getPolicy(ctx, promo.Spec.PolicyRef)
		r.Notifier.Notify(ctx, notification.Event{
			Phase:       string(phase),
			Version:     promo.Spec.Version,
			Environment: promo.Spec.EnvironmentRef,
			Release:     promo.Spec.ReleaseRef,
			IsFailure:   phase == kaprov1alpha1.PromotionPhaseFailed,
		}, policy)
	}
	return ctrl.Result{Requeue: true}, nil
}

func (r *PromotionReconciler) failPromotion(ctx context.Context, promo *kaprov1alpha1.Promotion, policy *kaprov1alpha1.PromotionPolicy, msg string) error {
	patch := client.MergeFrom(promo.DeepCopy())
	promo.Status.Phase = kaprov1alpha1.PromotionPhaseFailed
	promo.Status.ObservedGeneration = promo.Generation
	promo.Status.FinishedAt = time.Now().UTC().Format(time.RFC3339)
	promo.Status.Message = msg
	promo.Status.Conditions = nil // clear stale conditions before SetStatusCondition
	apimeta.SetStatusCondition(&promo.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionFalse,
		Reason:             "GateFailed",
		ObservedGeneration: promo.Generation,
		Message:            msg,
		LastTransitionTime: metav1.Now(),
	})
	r.Recorder.Event(promo, corev1.EventTypeWarning, "Failed", msg)

	if err := r.Status().Patch(ctx, promo, patch); err != nil {
		return fmt.Errorf("patch failed status: %w", err)
	}

	// Notify failure.
	if r.Notifier != nil {
		r.Notifier.Notify(ctx, notification.Event{
			Phase:       string(kaprov1alpha1.PromotionPhaseFailed),
			Version:     promo.Spec.Version,
			Environment: promo.Spec.EnvironmentRef,
			Release:     promo.Spec.ReleaseRef,
			Message:     msg,
			IsFailure:   true,
		}, policy)
	}

	// Trigger auto-rollback if policy says so and there's a previous version to roll back to.
	if policy != nil && policy.Spec.OnFailure == "rollback" && promo.Status.PreviousVersion != "" {
		return r.triggerRollback(ctx, promo)
	}

	return nil
}

// triggerRollback calls the actuator's Rollback() to immediately signal the
// delivery system, then creates a new Promotion targeting the previous version
// to formally track the rollback through the gate+apply+converge cycle.
func (r *PromotionReconciler) triggerRollback(ctx context.Context, failed *kaprov1alpha1.Promotion) error {
	log := log.FromContext(ctx)

	// 1. Immediately call actuator.Rollback() so the delivery system starts
	//    reverting without waiting for the new Promotion to reconcile.
	var env kaprov1alpha1.Environment
	if err := r.Get(ctx, client.ObjectKey{Name: failed.Spec.EnvironmentRef}, &env); err == nil {
		if r.ActuatorRegistry != nil {
			if act, actErr := r.ActuatorRegistry.Resolve(env.Spec.Actuator.Type); actErr == nil {
				if rbErr := act.Rollback(ctx, &env, failed.Status.PreviousVersion); rbErr != nil {
					log.Error(rbErr, "actuator Rollback() failed — rollback Promotion will re-apply it")
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

	// Idempotent — don't create a second rollback Promotion if one already exists.
	var existing kaprov1alpha1.Promotion
	if err := r.Get(ctx, client.ObjectKey{Name: rollbackName, Namespace: failed.Namespace}, &existing); err == nil {
		log.Info("rollback Promotion already exists", "name", rollbackName)
		return nil
	}

	rollback := &kaprov1alpha1.Promotion{
		ObjectMeta: metav1.ObjectMeta{
			Name:      rollbackName,
			Namespace: failed.Namespace,
			Labels: map[string]string{
				"kapro.io/rollback-for": failed.Name,
				"kapro.io/release":      failed.Spec.ReleaseRef,
			},
		},
		Spec: kaprov1alpha1.PromotionSpec{
			ReleaseRef:     failed.Spec.ReleaseRef,
			EnvironmentRef: failed.Spec.EnvironmentRef,
			Version:        failed.Status.PreviousVersion,
			PolicyRef:      failed.Spec.PolicyRef,
			AppKey:         failed.Spec.AppKey,
		},
	}

	if r.Scheme != nil {
		var release kaprov1alpha1.Release
		if err := r.Get(ctx, client.ObjectKey{Name: failed.Spec.ReleaseRef, Namespace: failed.Namespace}, &release); err == nil {
			if err := controllerutil.SetControllerReference(&release, rollback, r.Scheme); err != nil {
				log.Error(err, "failed to set ownerRef on rollback Promotion — will create without owner")
			}
		}
	}

	if err := r.Create(ctx, rollback); err != nil {
		return fmt.Errorf("create rollback Promotion: %w", err)
	}

	log.Info("created rollback Promotion",
		"name", rollbackName,
		"targetVersion", failed.Status.PreviousVersion,
		"env", failed.Spec.EnvironmentRef,
	)
	r.Recorder.Event(failed, corev1.EventTypeWarning, "RollbackTriggered",
		fmt.Sprintf("Auto-rollback to %s triggered", failed.Status.PreviousVersion))

	return nil
}

func (r *PromotionReconciler) getRegistration(ctx context.Context, envRef string) (*kaprov1alpha1.ClusterRegistration, error) {
	var regList kaprov1alpha1.ClusterRegistrationList
	if err := r.List(ctx, &regList, client.MatchingLabels{
		"kapro.io/environment": envRef,
	}, client.Limit(100)); err != nil {
		return nil, err
	}
	for _, reg := range regList.Items {
		if reg.Spec.EnvironmentRef == envRef {
			return &reg, nil
		}
	}
	return nil, fmt.Errorf("no ClusterRegistration found for environment %s", envRef)
}

func (r *PromotionReconciler) getPolicy(ctx context.Context, policyRef string) (*kaprov1alpha1.PromotionPolicy, error) {
	if policyRef == "" {
		return nil, nil
	}
	var policy kaprov1alpha1.PromotionPolicy
	if err := r.Get(ctx, client.ObjectKey{Name: policyRef}, &policy); err != nil {
		return nil, err
	}
	return &policy, nil
}

func isHeartbeatFresh(lastHeartbeat string) bool {
	if lastHeartbeat == "" {
		return false
	}
	t, err := time.Parse(time.RFC3339, lastHeartbeat)
	if err != nil {
		return false
	}
	return time.Since(t) < requeueSlow
}

func (r *PromotionReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		WithOptions(controller.Options{
			RateLimiter: workqueue.NewTypedItemExponentialFailureRateLimiter[reconcile.Request](50*time.Millisecond, 10*time.Minute),
		}).
		// GenerationChangedPredicate: only react to spec changes, not every
		// status patch the controller writes itself.  FSM advancement uses
		// explicit ctrl.Result{RequeueAfter:...} — not watch-triggered events —
		// so this predicate does not break the state machine.
		For(&kaprov1alpha1.Promotion{},
			builder.WithPredicates(predicate.GenerationChangedPredicate{}),
		).
		// Watch ClusterRegistrations so convergence is event-driven, not poll-driven.
		// When a cluster-controller writes status.phase=Converged, the owning
		// Promotion is woken up immediately rather than waiting for RequeueAfter.
		// This eliminates the 30s thundering herd at scale (100 Promotions × 30s).
		Watches(
			&kaprov1alpha1.ClusterRegistration{},
			handler.EnqueueRequestsFromMapFunc(r.promotionsForClusterRegistration),
		).
		// Watch Approvals so that a manual approval immediately wakes up the
		// Promotion that is stuck in WaitingApproval — without this watch the
		// Promotion would only advance on the next RequeueAfter tick.
		Watches(
			&kaprov1alpha1.Approval{},
			handler.EnqueueRequestsFromMapFunc(r.promotionForApproval),
		).
		Complete(r)
}

// promotionsForClusterRegistration returns reconcile.Requests for all
// in-flight Promotions that target the changed ClusterRegistration's
// environment.  Called on every ClusterRegistration status change.
func (r *PromotionReconciler) promotionsForClusterRegistration(ctx context.Context, obj client.Object) []ctrl.Request {
	reg, ok := obj.(*kaprov1alpha1.ClusterRegistration)
	if !ok {
		return nil
	}
	var promoList kaprov1alpha1.PromotionList
	if err := r.List(ctx, &promoList,
		client.MatchingFields{IndexKeyEnvironment: reg.Spec.EnvironmentRef},
		client.Limit(500),
	); err != nil {
		return nil
	}
	var reqs []ctrl.Request
	for i := range promoList.Items {
		p := &promoList.Items[i]
		// Only wake up in-flight Promotions — terminal ones don't need a nudge.
		switch p.Status.Phase {
		case kaprov1alpha1.PromotionPhaseConverged, kaprov1alpha1.PromotionPhaseFailed:
			continue
		}
		reqs = append(reqs, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(p)})
	}
	return reqs
}

// promotionForApproval maps an Approval object to the Promotion it unblocks.
// Approval.spec.kind == "Promotion" and spec.ref is the environment name.
// Promotion names follow the convention: <release>-<environmentRef>.
// Called on every Approval create/update so the waiting Promotion is
// woken up immediately without waiting for the next RequeueAfter tick.
func (r *PromotionReconciler) promotionForApproval(ctx context.Context, obj client.Object) []ctrl.Request {
	approval, ok := obj.(*kaprov1alpha1.Approval)
	if !ok {
		return nil
	}
	if approval.Spec.Kind != kaprov1alpha1.ApprovalKindPromotion {
		return nil
	}
	promoName := approval.Spec.Release + "-" + approval.Spec.Ref
	return []ctrl.Request{{NamespacedName: client.ObjectKey{Name: promoName, Namespace: approval.Namespace}}}
}

// promoAppKey returns the ClusterRegistration.status.currentVersions key for this Promotion.
// Falls back to the ReleaseRef (artifact name) when AppKey is not set — preserves
// backward compatibility for single-app deployments.
func promoAppKey(promo *kaprov1alpha1.Promotion) string {
	if promo.Spec.AppKey != "" {
		return promo.Spec.AppKey
	}
	return promo.Spec.ReleaseRef
}
