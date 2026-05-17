// controllers.go registers all Kapro MVP controllers into the Registry.
// Each InitFunc constructs a reconciler from the shared ControllerContext and
// calls SetupWithManager — the same contract as cloud-controller-manager InitFuncs.
//
// To add a new controller: write an InitFunc here and call Register() in init().
package controllermanager

import (
	"context"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"kapro.io/kapro/internal/controller"
	internalgate "kapro.io/kapro/internal/gate"
	celgate "kapro.io/kapro/internal/gate/cel"
	jobgate "kapro.io/kapro/internal/gate/job"
	webhookgate "kapro.io/kapro/internal/gate/webhook"
	pluginadapter "kapro.io/kapro/internal/plugin/adapter"
	"kapro.io/kapro/internal/shard"
	pkggate "kapro.io/kapro/pkg/gate"
)

func init() {
	Register("promotion", startPromotionController)
	Register("promotion-policy", startPromotionPolicyController)
	Register("promotionrun", startPromotionRunController)
	Register("promotion-target", startPromotionTargetController)
	Register("approval", startApprovalController)
	Register("kapro", startKaproController)
	Register("backend-profile", startBackendProfileController)
	Register("plugin-registration", startPluginRegistrationController)
	Register("promotion-trigger", startPromotionTriggerController)
	// fleetcluster-bootstrap: CSR-native cluster registration + per-cluster RBAC.
	// Opt-in for now (off-by-default) until OCI Delivery Core (PR-3, PR-4) ships
	// a spoke binary to consume the issued bootstrap kubeconfig. Set
	// KAPRO_CONTROLLERS=*,fleetcluster-bootstrap or list it explicitly to enable.
	Register("fleetcluster-bootstrap", startFleetClusterBootstrapController)
}

func startPromotionController(_ context.Context, cc ControllerContext) (bool, error) {
	if err := (&controller.PromotionReconciler{
		Client:   cc.Manager.GetClient(),
		Recorder: cc.Recorder,
		Scheme:   cc.Manager.GetScheme(),
	}).SetupWithManager(cc.Manager); err != nil {
		return false, err
	}
	return true, nil
}

func startPromotionPolicyController(_ context.Context, cc ControllerContext) (bool, error) {
	if err := (&controller.PromotionPolicyReconciler{
		Client: cc.Manager.GetClient(),
	}).SetupWithManager(cc.Manager); err != nil {
		return false, err
	}
	return true, nil
}

// startPromotionRunController starts the PromotionRun reconciler.
// Drives the two-level DAG orchestration — walks PromotionPlan nodes then Stages,
// upserts one PromotionTarget per (PromotionRun, PromotionPlan, Stage, Target),
// and aggregates child execution state into PromotionRun status.
func startPromotionRunController(_ context.Context, cc ControllerContext) (bool, error) {
	r := &controller.PromotionRunReconciler{
		Client:           cc.Manager.GetClient(),
		Recorder:         cc.Recorder,
		Scheme:           cc.Manager.GetScheme(),
		ActuatorRegistry: cc.ActuatorRegistry,
		Notifier:         cc.Notifier,
		ApprovalSecret:   cc.ApprovalSecret,
		ExternalURL:      cc.ExternalURL,
		GateRegistry:     cc.GateRegistry,
		Planner:          cc.Planner,
	}
	if cc.ShardName != "" {
		r.ShardPredicate = shard.ShardFilter{ShardName: cc.ShardName, IsDefault: cc.ShardIsDefault}
	}
	if err := r.SetupWithManager(cc.Manager); err != nil {
		return false, err
	}
	return true, nil
}

func startPromotionTargetController(_ context.Context, cc ControllerContext) (bool, error) {
	r := &controller.PromotionTargetReconciler{
		Client:             cc.Manager.GetClient(),
		Recorder:           cc.Recorder,
		Scheme:             cc.Manager.GetScheme(),
		ActuatorRegistry:   cc.ActuatorRegistry,
		Notifier:           cc.Notifier,
		ApprovalSecret:     cc.ApprovalSecret,
		ExternalURL:        cc.ExternalURL,
		GateRegistry:       cc.GateRegistry,
		HeartbeatNamespace: cc.HeartbeatNamespace,
	}
	if cc.ShardName != "" {
		r.ShardPredicate = shard.ShardFilter{ShardName: cc.ShardName, IsDefault: cc.ShardIsDefault}
	}
	if err := r.SetupWithManager(cc.Manager); err != nil {
		return false, err
	}
	return true, nil
}

// BuildGateRegistry registers every built-in Gate into a single registry.
//
// One mental model: every gate — FSM-phase gates AND template-dispatch gates —
// is a pkg/gate.Gate resolved by name through this registry. The FSM calls
// r.GateRegistry.Resolve("soak" | "metrics" | "approval" | "verification"),
// and GateTemplate evaluation calls Resolve(template.spec.type) for
// user-extensible types (cel, job, webhook, or anything registered by name
// at startup).
//
// External gate types register after this call in main.go:
//
//	reg, err := BuildGateRegistry(c)
//	if err != nil { return err }
//	if err := reg.Register("argo-analysis", &mygate.ArgoAnalysisGate{...}); err != nil { return err }
func BuildGateRegistry(c client.Client) (*pkggate.Registry, error) {
	reg := pkggate.NewRegistry()
	for typeName, impl := range map[string]pkggate.Gate{
		// FSM-phase gates (resolved by fixed name from target_fsm.go handlers).
		"soak":         &internalgate.SoakGate{},
		"metrics":      &internalgate.MetricsGate{},
		"approval":     &internalgate.ApprovalGate{Client: c},
		"verification": &internalgate.VerificationGate{},
		// Template-dispatch gates (resolved by GateTemplate.spec.type).
		"cel":     &celgate.Gate{Client: c},
		"job":     &jobgate.Gate{Client: c},
		"webhook": &webhookgate.Gate{},
	} {
		if err := reg.Register(typeName, impl); err != nil {
			return nil, fmt.Errorf("register built-in gate %q: %w", typeName, err)
		}
	}
	return reg, nil
}

// startApprovalController starts the Approval reconciler.
// Watches Approval objects and unblocks targets waiting in WaitingApproval phase.
func startApprovalController(_ context.Context, cc ControllerContext) (bool, error) {
	if err := (&controller.ApprovalReconciler{
		Client:   cc.Manager.GetClient(),
		Recorder: cc.Recorder,
	}).SetupWithManager(cc.Manager); err != nil {
		return false, err
	}
	return true, nil
}

// startPluginRegistrationController starts the PluginRegistration readiness reconciler.
// It probes capabilities and records readiness for optional plugin runtime registration.
func startPluginRegistrationController(_ context.Context, cc ControllerContext) (bool, error) {
	if err := (&controller.PluginRegistrationReconciler{
		Client:           cc.Manager.GetClient(),
		Recorder:         cc.Recorder,
		RuntimeEnabled:   pluginadapter.EnabledFromEnv(),
		ActuatorRegistry: cc.ActuatorRegistry,
		GateRegistry:     cc.GateRegistry,
		Planner:          cc.Planner,
	}).SetupWithManager(cc.Manager); err != nil {
		return false, err
	}
	return true, nil
}

// startBackendProfileController starts the backend readiness reconciler.
func startBackendProfileController(_ context.Context, cc ControllerContext) (bool, error) {
	if err := (&controller.BackendProfileReconciler{
		Client:   cc.Manager.GetClient(),
		Recorder: cc.Recorder,
	}).SetupWithManager(cc.Manager); err != nil {
		return false, err
	}
	return true, nil
}

// startPromotionTriggerController starts the safe-by-default artifact trigger reconciler.
func startPromotionTriggerController(_ context.Context, cc ControllerContext) (bool, error) {
	if err := (&controller.PromotionTriggerReconciler{
		Client:   cc.Manager.GetClient(),
		Scheme:   cc.Manager.GetScheme(),
		Recorder: cc.Recorder,
	}).SetupWithManager(cc.Manager); err != nil {
		return false, err
	}
	return true, nil
}

// startKaproController starts the Kapro reconciler.
// Pushes FluxInstance + OCIRepository to spokes, generates FleetClusters and PromotionPlan on the hub.
func startKaproController(_ context.Context, cc ControllerContext) (bool, error) {
	if err := (&controller.KaproReconciler{
		Client:   cc.Manager.GetClient(),
		Recorder: cc.Recorder,
	}).SetupWithManager(cc.Manager); err != nil {
		return false, err
	}
	return true, nil
}

// startFleetClusterBootstrapController starts the FleetCluster bootstrap
// reconciler. Provisions per-cluster bootstrap SA + scoped CSR-create RBAC +
// rendered kubeconfig Secret; validates and approves CSRs submitted by the
// spoke; creates the long-lived per-cluster ClusterRole + Binding with a
// resourceNames lock on the issued cluster cert's User identity.
//
// Returns enabled=false (no error) when KubeClient or CertClient is missing —
// these are populated in main.go from the operator's REST config and only
// non-nil in production. Unit tests that don't need the controller may leave
// them unset.
func startFleetClusterBootstrapController(_ context.Context, cc ControllerContext) (bool, error) {
	if cc.KubeClient == nil || cc.CertClient == nil {
		return false, nil
	}
	r := &controller.FleetClusterBootstrapReconciler{
		Client:       cc.Manager.GetClient(),
		Scheme:       cc.Manager.GetScheme(),
		Recorder:     cc.Recorder,
		KubeClient:   cc.KubeClient,
		CertClient:   cc.CertClient,
		HubAPIURL:    cc.HubAPIURL,
		HubCAData:    cc.HubCAData,
		PodNamespace: cc.PodNamespace,
	}
	if err := r.SetupWithManager(cc.Manager); err != nil {
		return false, err
	}
	return true, nil
}

// compile-time checks: all built-in gate implementations satisfy gate.Gate.
// Add a line here whenever a new built-in gate is added to BuildGateRegistry.
var (
	_ pkggate.Gate = (*internalgate.SoakGate)(nil)
	_ pkggate.Gate = (*internalgate.MetricsGate)(nil)
	_ pkggate.Gate = (*internalgate.ApprovalGate)(nil)
	_ pkggate.Gate = (*internalgate.VerificationGate)(nil)
	_ pkggate.Gate = (*celgate.Gate)(nil)
	_ pkggate.Gate = (*jobgate.Gate)(nil)
	_ pkggate.Gate = (*webhookgate.Gate)(nil)
)
