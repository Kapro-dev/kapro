// controllers.go registers all Kapro built-in controllers into the Registry.
// Each InitFunc constructs a reconciler from the shared ControllerContext and
// calls SetupWithManager — the same contract as cloud-controller-manager InitFuncs.
//
// To add a new controller: write an InitFunc here and call Register() in init().
package controllermanager

import (
	"context"

	"kapro.io/kapro/internal/controller"
	internalgate "kapro.io/kapro/internal/gate"
	argogate "kapro.io/kapro/internal/gate/argo"
	celgate "kapro.io/kapro/internal/gate/cel"
	kedagate "kapro.io/kapro/internal/gate/keda"
	kgatewaygate "kapro.io/kapro/internal/gate/kgateway"
	mlflowgate "kapro.io/kapro/internal/gate/mlflow"
	shadowgate "kapro.io/kapro/internal/gate/shadow"
	"kapro.io/kapro/internal/plugin"
	crdprovider "kapro.io/kapro/internal/provider/crd"
	kagentctrl "kapro.io/kapro/internal/trigger/kagent"
)

func init() {
	Register("release", startReleaseController)
	Register("promotion", startPromotionController)
	Register("batch", startBatchController)
	Register("pipeline", startPipelineController)
	Register("plugingateway", startPluginGatewayController)
	Register("approval", startApprovalController)
	Register("bootstraptoken", startBootstrapTokenController)
	Register("kagent", startKAgentController)
}

// startReleaseController starts the Release reconciler.
// Deps: Client, Recorder, Scheme — Scheme is needed to set ownerReferences on
// child Promotion and BatchRun objects.
func startReleaseController(_ context.Context, cc ControllerContext) (bool, error) {
	if err := (&controller.ReleaseReconciler{
		Client:   cc.Manager.GetClient(),
		Recorder: cc.Recorder,
		Scheme:   cc.Manager.GetScheme(),
	}).SetupWithManager(cc.Manager); err != nil {
		return false, err
	}
	return true, nil
}

// startPromotionController starts the Promotion reconciler.
// Deps: full gate set, actuator, provider, OCI, notifier, plugin registry.
func startPromotionController(_ context.Context, cc ControllerContext) (bool, error) {
	// Plugin registry — holds live gRPC connections to PluginRegistration CRs.
	// Registered here so it shares the same manager as Promotion.
	pluginRegistry := plugin.NewRegistry()
	if err := (&plugin.Reconciler{
		Client:   cc.Manager.GetClient(),
		Registry: pluginRegistry,
	}).SetupWithManager(cc.Manager); err != nil {
		return false, err
	}

	provider := &crdprovider.CRDProvider{Client: cc.Manager.GetClient()}

	if err := (&controller.PromotionReconciler{
		Client:           cc.Manager.GetClient(),
		Scheme:           cc.Manager.GetScheme(),
		Recorder:         cc.Recorder,
		ActuatorRegistry: cc.ActuatorRegistry,
		Provider:         provider,
		SoakGate:         toInternalSoak(cc.Gates.Soak),
		MetricsGate:      toInternalMetrics(cc.Gates.Metrics),
		ApprovalGate:     toInternalApproval(cc.Gates.Approval),
		KedaGate:         cc.Gates.Keda,
		MLflowGate:       cc.Gates.MLflow,
		ShadowGate:       cc.Gates.Shadow,
		KGatewayGate:     cc.Gates.KGateway,
		VerificationGate: cc.Gates.Verification,
		HealthAssessor:   cc.HealthAssessor,
		Notifier:         cc.Notifier,
		OCIService:       cc.OCIService,
		PluginRegistry:   pluginRegistry,
		ApprovalSecret:   cc.ApprovalSecret,
		ExternalURL:      cc.ExternalURL,
		CELGate:          &celgate.Gate{Client: cc.Manager.GetClient()},
		ArgoGate:         &argogate.Gate{Client: cc.Manager.GetClient()},
	}).SetupWithManager(cc.Manager); err != nil {
		return false, err
	}
	return true, nil
}

// startBatchController starts the BatchRun reconciler.
func startBatchController(_ context.Context, cc ControllerContext) (bool, error) {
	if err := (&controller.BatchRunReconciler{
		Client:       cc.Manager.GetClient(),
		Recorder:     cc.Recorder,
		SoakGate:     cc.Gates.Soak,
		MetricsGate:  cc.Gates.Metrics,
		ApprovalGate: cc.Gates.Approval,
		KedaGate:     cc.Gates.Keda,
		OCIService:   cc.OCIService,
	}).SetupWithManager(cc.Manager); err != nil {
		return false, err
	}
	return true, nil
}

// startApprovalController starts the Approval reconciler.
func startApprovalController(_ context.Context, cc ControllerContext) (bool, error) {
	if err := (&controller.ApprovalReconciler{
		Client:   cc.Manager.GetClient(),
		Recorder: cc.Recorder,
	}).SetupWithManager(cc.Manager); err != nil {
		return false, err
	}
	return true, nil
}

// startBootstrapTokenController starts the BootstrapToken reconciler.
func startBootstrapTokenController(_ context.Context, cc ControllerContext) (bool, error) {
	if err := (&controller.BootstrapTokenReconciler{
		Client:   cc.Manager.GetClient(),
		Recorder: cc.Recorder,
	}).SetupWithManager(cc.Manager); err != nil {
		return false, err
	}
	return true, nil
}

// startKAgentController starts the KAgent autonomous release trigger.
// KAgent is optional — operators that use external CI triggers can disable it
// with --controllers=*,-kagent.
func startKAgentController(_ context.Context, cc ControllerContext) (bool, error) {
	if err := (&kagentctrl.KAgentReconciler{
		Client:   cc.Manager.GetClient(),
		Recorder: cc.Recorder,
	}).SetupWithManager(cc.Manager); err != nil {
		return false, err
	}
	return true, nil
}

// startPluginGatewayController validates PluginGateway specs and probes remote
// endpoints. It is intentionally lightweight — evaluation happens in
// internal/gate/plugingateway/.
func startPluginGatewayController(_ context.Context, cc ControllerContext) (bool, error) {
	if err := (&controller.PluginGatewayReconciler{
		Client:   cc.Manager.GetClient(),
		Recorder: cc.Recorder,
	}).SetupWithManager(cc.Manager); err != nil {
		return false, err
	}
	return true, nil
}

// startPipelineController starts the Pipeline status-aggregation reconciler.
// Pipeline is read-only from a delivery standpoint — it only updates its own
// status (phase, batchProgress). All scheduling decisions remain in Release and
// BatchRun controllers.
func startPipelineController(_ context.Context, cc ControllerContext) (bool, error) {
	if err := (&controller.PipelineReconciler{
		Client:   cc.Manager.GetClient(),
		Recorder: cc.Recorder,
	}).SetupWithManager(cc.Manager); err != nil {
		return false, err
	}
	return true, nil
}

// ── type adapters ─────────────────────────────────────────────────────────────
// PromotionReconciler uses concrete *internalgate types for the built-in gates
// (not the pkg/gate.Gate interface) because they carry no extra state.
// These helpers cast the interface back to the concrete type; they return nil
// (pass-through) if the gate was not provided or is the wrong type.

func toInternalSoak(g interface{}) *internalgate.SoakGate {
	if g == nil {
		return &internalgate.SoakGate{}
	}
	if sg, ok := g.(*internalgate.SoakGate); ok {
		return sg
	}
	return &internalgate.SoakGate{}
}

func toInternalMetrics(g interface{}) *internalgate.MetricsGate {
	if g == nil {
		return &internalgate.MetricsGate{}
	}
	if mg, ok := g.(*internalgate.MetricsGate); ok {
		return mg
	}
	return &internalgate.MetricsGate{}
}

func toInternalApproval(g interface{}) *internalgate.ApprovalGate {
	if g == nil {
		return nil
	}
	if ag, ok := g.(*internalgate.ApprovalGate); ok {
		return ag
	}
	return nil
}

// ── full-operator gate builder ────────────────────────────────────────────────

// BuildFullGateSet constructs the complete gate set for cmd/operator.
// cmd/operator-core calls BuildCoreGateSet instead (no heavy deps).
func BuildFullGateSet() GateSet {
	return GateSet{
		Soak:     &internalgate.SoakGate{},
		Metrics:  &internalgate.MetricsGate{},
		Keda:     &kedagate.Gate{},
		MLflow:   &mlflowgate.Gate{},
		Shadow:   &shadowgate.Gate{},
		KGateway: &kgatewaygate.Gate{},
	}
}

// BuildCoreGateSet constructs the minimal gate set for cmd/operator-core.
// Heavy gates (keda, mlflow, shadow, kgateway) are absent — use plugins instead.
func BuildCoreGateSet() GateSet {
	return GateSet{
		Soak:    &internalgate.SoakGate{},
		Metrics: &internalgate.MetricsGate{},
	}
}
