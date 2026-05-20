// controllers.go registers all Kapro MVP controllers into the Registry.
// Each InitFunc constructs a reconciler from the shared ControllerContext and
// calls SetupWithManager — the same contract as cloud-controller-manager InitFuncs.
//
// To add a new controller: write an InitFunc here and call Register() in init().
package controllermanager

import (
	"context"
	"fmt"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"kapro.io/kapro/internal/controller"
	internalgate "kapro.io/kapro/internal/gate"
	celgate "kapro.io/kapro/internal/gate/cel"
	jobgate "kapro.io/kapro/internal/gate/job"
	webhookgate "kapro.io/kapro/internal/gate/webhook"
	"kapro.io/kapro/internal/lifecycle"
	pluginadapter "kapro.io/kapro/internal/plugin/adapter"
	"kapro.io/kapro/internal/shard"
	pkggate "kapro.io/kapro/pkg/gate"
)

func init() {
	Register("promotion", startPromotionController)
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
	// fleetcluster-template: universal fleet auto-import (PR-6). Opt-in until
	// per-source IAM is documented (PR-7). Set
	// KAPRO_CONTROLLERS=*,fleetcluster-template or list it explicitly to enable.
	Register("fleetcluster-template", startFleetClusterTemplateController)
	// fleetcluster-heartbeat: cluster reachability via consecutive-failure
	// threshold (PR-8). Sole writer of FleetCluster conditions[Ready] and
	// status.heartbeat. kapro_controller reads conditions[Ready] to surface
	// Phase=Unreachable. Always-on: no cost when no FleetCluster exists.
	Register("fleetcluster-heartbeat", startFleetClusterHeartbeatController)
}

// startPromotionController starts the Promotion intent reconciler.
// Materializes user-authored Promotion objects into PromotionRun attempts and
// mirrors run status back into Promotion.status.
//
// A nil-safe lifecycle dispatcher is attached so:
//   - User-declared `Promotion.spec.lifecycle.handlers` fire asynchronously
//     on phase transitions (per-Promotion ergonomic shortcut).
//   - When KAPRO_EVENTS_SINK_URL is set, every fleet-promotion CloudEvent
//     is also published to the operator-level sink (canonical CNCF
//     integration point — subscribers like Argo Events, Flux Notification
//     Controller, kube-event-exporter consume from there).
//
// Both paths are fire-and-forget; neither blocks reconcile.
func startPromotionController(ctx context.Context, cc ControllerContext) (bool, error) {
	sink, err := lifecycle.SinkFromEnv()
	if err != nil {
		return false, fmt.Errorf("configure events sink: %w", err)
	}
	dispatcher := lifecycle.
		NewDispatcher(
			ctx,
			cc.Manager.GetClient(),
			cc.Recorder,
			cc.PodNamespace,
		).
		WithSink(sink)

	r := &controller.PromotionReconciler{
		Client:    cc.Manager.GetClient(),
		Recorder:  cc.Recorder,
		Scheme:    cc.Manager.GetScheme(),
		Lifecycle: dispatcher,
	}
	if err := r.SetupWithManager(cc.Manager); err != nil {
		return false, err
	}
	return true, nil
}

// startPromotionRunController starts the PromotionRun reconciler.
// Drives the two-level DAG orchestration — walks PromotionPlan nodes then Stages,
// upserts one PromotionTarget per (PromotionRun, PromotionPlan, Stage, Target),
// and aggregates child execution state into PromotionRun status.
func startPromotionRunController(ctx context.Context, cc ControllerContext) (bool, error) {
	stageDispatcher, err := buildStageDispatcher(ctx, cc)
	if err != nil {
		return false, err
	}
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
		StagePublisher:   stageDispatcher,
	}
	if cc.ShardName != "" {
		r.ShardPredicate = shard.ShardFilter{ShardName: cc.ShardName, IsDefault: cc.ShardIsDefault}
	}
	if err := r.SetupWithManager(cc.Manager); err != nil {
		return false, err
	}
	return true, nil
}

func startPromotionTargetController(ctx context.Context, cc ControllerContext) (bool, error) {
	stageDispatcher, err := buildStageDispatcher(ctx, cc)
	if err != nil {
		return false, err
	}
	r := &controller.PromotionTargetReconciler{
		Client:           cc.Manager.GetClient(),
		Recorder:         cc.Recorder,
		Scheme:           cc.Manager.GetScheme(),
		ActuatorRegistry: cc.ActuatorRegistry,
		Notifier:         cc.Notifier,
		ApprovalSecret:   cc.ApprovalSecret,
		ExternalURL:      cc.ExternalURL,
		GateRegistry:     cc.GateRegistry,
		StagePublisher:   stageDispatcher,
	}
	if cc.ShardName != "" {
		r.ShardPredicate = shard.ShardFilter{ShardName: cc.ShardName, IsDefault: cc.ShardIsDefault}
	}
	if err := r.SetupWithManager(cc.Manager); err != nil {
		return false, err
	}
	return true, nil
}

// buildStageDispatcher constructs a lifecycle Dispatcher just for the
// wave/stage/gate CloudEvents sink path. Returns nil (no error) when
// KAPRO_EVENTS_SINK_URL is unset — the reconcilers are nil-safe.
func buildStageDispatcher(ctx context.Context, cc ControllerContext) (controller.StageEventPublisher, error) {
	sink, err := lifecycle.SinkFromEnv()
	if err != nil {
		return nil, fmt.Errorf("configure events sink: %w", err)
	}
	if sink == nil {
		return nil, nil
	}
	return lifecycle.
		NewDispatcher(ctx, cc.Manager.GetClient(), cc.Recorder, cc.PodNamespace).
		WithSink(sink), nil
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
// Returns enabled=false (no error) when KubeClient or CertClient is missing.
// These are populated unconditionally in cmd/operator/main.go from the
// operator's REST config and so are non-nil in production. The escape hatch
// exists for unit tests that don't exercise this controller; a warning is
// logged so a partial-wiring regression in main.go would still be visible
// in operator startup logs rather than silently dropping the controller.
func startFleetClusterBootstrapController(_ context.Context, cc ControllerContext) (bool, error) {
	if cc.KubeClient == nil || cc.CertClient == nil {
		ctrl.Log.WithName("controllermanager").Info(
			"fleetcluster-bootstrap controller skipped: KubeClient or CertClient is nil — expected only in tests, log a bug if you see this in production",
			"kubeClientPresent", cc.KubeClient != nil,
			"certClientPresent", cc.CertClient != nil,
		)
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

// startFleetClusterTemplateController starts the universal fleet auto-import
// reconciler (PR-6). One CRD (FleetClusterTemplate) discovers clusters from
// any supported source (GCP Fleet today; AWS / Azure / RHACM / CAPI / static
// are preview stubs) and upserts FleetCluster objects.
func startFleetClusterTemplateController(_ context.Context, cc ControllerContext) (bool, error) {
	r := &controller.FleetClusterTemplateReconciler{
		Client:   cc.Manager.GetClient(),
		Scheme:   cc.Manager.GetScheme(),
		Recorder: cc.Recorder,
	}
	if err := r.SetupWithManager(cc.Manager); err != nil {
		return false, err
	}
	return true, nil
}

// startFleetClusterHeartbeatController starts the FleetCluster reachability
// reconciler (PR-8). Watches FleetCluster + coordination Lease and writes
// FleetCluster conditions[Ready] + status.heartbeat. Honors per-cluster
// spec.consecutiveFailureThreshold (default 3) to absorb transient network
// blips. kapro_controller is the sole writer of Phase; it surfaces
// Phase=Unreachable when this reconciler has set Ready=False
// reason=Unreachable.
func startFleetClusterHeartbeatController(_ context.Context, cc ControllerContext) (bool, error) {
	r := &controller.FleetClusterHeartbeatReconciler{
		Client:             cc.Manager.GetClient(),
		Scheme:             cc.Manager.GetScheme(),
		Recorder:           cc.Recorder,
		HeartbeatNamespace: cc.HeartbeatNamespace,
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
