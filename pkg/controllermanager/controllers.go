// controllers.go registers all Kapro MVP controllers into the Registry.
// Each InitFunc constructs a reconciler from the shared ControllerContext and
// calls SetupWithManager — the same contract as cloud-controller-manager InitFuncs.
//
// To add a new controller: write an InitFunc here and call Register() in init().
package controllermanager

import (
	"context"
	"fmt"

	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"kapro.io/kapro/internal/controller"
	"kapro.io/kapro/internal/shard"
	internalgate "kapro.io/kapro/internal/gate"
	celgate "kapro.io/kapro/internal/gate/cel"
	jobgate "kapro.io/kapro/internal/gate/job"
	webhookgate "kapro.io/kapro/internal/gate/webhook"
	cosignverifier "kapro.io/kapro/internal/verification/cosign"
	pkggate "kapro.io/kapro/pkg/gate"
)

func init() {
	Register("release", startReleaseController)
	Register("release-target", startReleaseTargetController)
	Register("approval", startApprovalController)
	Register("csrapproval", startCSRApprovalController)
	Register("membercluster", startMemberClusterController)
	Register("source", startSourceController)
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
		"soak":     &internalgate.SoakGate{},
		"metrics":  &internalgate.MetricsGate{},
		"approval": &internalgate.ApprovalGate{Client: c},
		"verification": &internalgate.VerificationGate{
			Verifier:  &cosignverifier.Verifier{},
			KeyReader: &internalgate.ClientSecretKeyReader{Client: c},
		},
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

// startCSRApprovalController starts the CSR approval controller.
// Approves bootstrap CSRs (first registration) and renewal CSRs (cert rotation)
// from Kapro cluster-controllers using the kubernetes.io/kube-apiserver-client signer.
func startCSRApprovalController(_ context.Context, cc ControllerContext) (bool, error) {
	kubeClient, err := kubernetes.NewForConfig(cc.Manager.GetConfig())
	if err != nil {
		return false, err
	}
	if err := (&controller.CSRApprovalReconciler{
		Client:     cc.Manager.GetClient(),
		CertClient: kubeClient.CertificatesV1(),
		Recorder:   cc.Recorder,
	}).SetupWithManager(cc.Manager); err != nil {
		return false, err
	}
	return true, nil
}

// startMemberClusterController starts the MemberCluster reconciler.
// Handles bootstrap SA/kubeconfig provisioning on creation, and RBAC cleanup on deletion.
func startMemberClusterController(_ context.Context, cc ControllerContext) (bool, error) {
	kubeClient, err := kubernetes.NewForConfig(cc.Manager.GetConfig())
	if err != nil {
		return false, err
	}
	if err := (&controller.MemberClusterReconciler{
		Client:     cc.Manager.GetClient(),
		Scheme:     cc.Manager.GetScheme(),
		KubeClient: kubeClient,
		HubAPIURL:  cc.HubAPIURL,
		HubCAData:  cc.HubCAData,
	}).SetupWithManager(cc.Manager); err != nil {
		return false, err
	}
	return true, nil
}

// startSourceController starts the Source reconciler.
// Polls OCI registries for new semver-matching tags and auto-creates Artifact objects.
func startSourceController(_ context.Context, cc ControllerContext) (bool, error) {
	r := &controller.SourceReconciler{
		Client:   cc.Manager.GetClient(),
		Recorder: cc.Recorder,
		Scheme:   cc.Manager.GetScheme(),
	}
	if cc.ShardName != "" {
		r.ShardPredicate = shard.ShardFilter{ShardName: cc.ShardName, IsDefault: true}
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
