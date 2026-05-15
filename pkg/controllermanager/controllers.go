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
	"kapro.io/kapro/internal/shard"
	pkggate "kapro.io/kapro/pkg/gate"
)

func init() {
	Register("release", startReleaseController)
	Register("release-target", startReleaseTargetController)
	Register("approval", startApprovalController)
	Register("kapro", startKaproController)
	Register("plugin-registration", startPluginRegistrationController)
	Register("release-trigger", startReleaseTriggerController)
	// csrapproval and membercluster bootstrap removed — Flux Operator handles spoke setup.
}

// startReleaseController starts the Release reconciler.
// Drives the two-level DAG orchestration — walks Pipeline nodes then Stages,
// upserts one ReleaseTarget per (Release, Pipeline, Stage, Target),
// and aggregates child execution state into Release status.
func startReleaseController(_ context.Context, cc ControllerContext) (bool, error) {
	r := &controller.ReleaseReconciler{
		Client:           cc.Manager.GetClient(),
		Recorder:         cc.Recorder,
		Scheme:           cc.Manager.GetScheme(),
		ActuatorRegistry: cc.ActuatorRegistry,
		Notifier:         cc.Notifier,
		ApprovalSecret:   cc.ApprovalSecret,
		ExternalURL:      cc.ExternalURL,
		GateRegistry:     cc.GateRegistry,
	}
	if cc.ShardName != "" {
		r.ShardPredicate = shard.ShardFilter{ShardName: cc.ShardName, IsDefault: true}
	}
	if err := r.SetupWithManager(cc.Manager); err != nil {
		return false, err
	}
	return true, nil
}

func startReleaseTargetController(_ context.Context, cc ControllerContext) (bool, error) {
	r := &controller.ReleaseTargetReconciler{
		Client:           cc.Manager.GetClient(),
		Recorder:         cc.Recorder,
		Scheme:           cc.Manager.GetScheme(),
		ActuatorRegistry: cc.ActuatorRegistry,
		Notifier:         cc.Notifier,
		ApprovalSecret:   cc.ApprovalSecret,
		ExternalURL:      cc.ExternalURL,
		GateRegistry:     cc.GateRegistry,
	}
	if cc.ShardName != "" {
		r.ShardPredicate = shard.ShardFilter{ShardName: cc.ShardName, IsDefault: true}
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
		Client:   cc.Manager.GetClient(),
		Recorder: cc.Recorder,
	}).SetupWithManager(cc.Manager); err != nil {
		return false, err
	}
	return true, nil
}

// startReleaseTriggerController starts the safe-by-default artifact trigger reconciler.
func startReleaseTriggerController(_ context.Context, cc ControllerContext) (bool, error) {
	if err := (&controller.ReleaseTriggerReconciler{
		Client:   cc.Manager.GetClient(),
		Scheme:   cc.Manager.GetScheme(),
		Recorder: cc.Recorder,
		Verifier: &controller.CosignReleaseTriggerVerifier{Client: cc.Manager.GetClient()},
	}).SetupWithManager(cc.Manager); err != nil {
		return false, err
	}
	return true, nil
}

// startKaproController starts the Kapro reconciler.
// Pushes FluxInstance + OCIRepository to spokes, generates MemberClusters and Pipeline on the hub.
func startKaproController(_ context.Context, cc ControllerContext) (bool, error) {
	if err := (&controller.KaproReconciler{
		Client:   cc.Manager.GetClient(),
		Recorder: cc.Recorder,
	}).SetupWithManager(cc.Manager); err != nil {
		return false, err
	}
	return true, nil
}

// CSR approval and MemberCluster bootstrap controllers removed.
// Flux Operator handles spoke setup — no kapro component on spokes.

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
