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
	internalgate "kapro.io/kapro/internal/gate"
	celgate "kapro.io/kapro/internal/gate/cel"
	jobgate "kapro.io/kapro/internal/gate/job"
	webhookgate "kapro.io/kapro/internal/gate/webhook"
	cosignverifier "kapro.io/kapro/internal/verification/cosign"
	crdprovider "kapro.io/kapro/internal/provider/crd"
	pkggate "kapro.io/kapro/pkg/gate"
)

func init() {
	Register("release", startReleaseController)
	Register("releasereport", startReleaseReportController)
	Register("sync", startSyncController)
	Register("pipeline", startPipelineController)
	Register("approval", startApprovalController)
	Register("bootstraptoken", startBootstrapTokenController)
	Register("csrapproval", startCSRApprovalController)
	Register("managedcluster", startManagedClusterController)
}

// startReleaseController starts the Release reconciler.
// Owns the two-level DAG orchestration — walks Pipeline nodes then Stages,
// creates one Sync per (Release, Pipeline, Stage, Environment).
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

// startReleaseReportController starts the ReleaseReport aggregation reconciler.
// Purely observational — never mutates Syncs or Releases.
// Disable with KAPRO_CONTROLLERS=*,-releasereport if audit reports are not needed.
func startReleaseReportController(_ context.Context, cc ControllerContext) (bool, error) {
	if err := (&controller.ReleaseReportReconciler{
		Client:   cc.Manager.GetClient(),
		Recorder: cc.Recorder,
	}).SetupWithManager(cc.Manager); err != nil {
		return false, err
	}
	return true, nil
}

// BuildGateRegistry registers all built-in template-dispatch gate types.
// External gate types register after this call in main.go:
//
//	reg, err := BuildGateRegistry(c)
//	if err != nil { return err }
//	if err := reg.Register("argo-analysis", &mygate.ArgoAnalysisGate{...}); err != nil { return err }
//
// The registry is intentionally separate from BuildGateSet: GateSet holds the
// FSM-phase gates (bound to fixed phases), while GateRegistry holds the
// template-dispatch gates (looked up by GateTemplate.spec.type at runtime).
func BuildGateRegistry(c client.Client) (*pkggate.Registry, error) {
	reg := pkggate.NewRegistry()
	for typeName, impl := range map[string]pkggate.Gate{
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

// startSyncController starts the Sync FSM reconciler.
// Drives one environment through: Verification → HealthCheck → Soaking →
// MetricsCheck → WaitingApproval → Applying → Converged | Failed.
// Built-in gates: soak, metrics (Prometheus), approval, health, verification (cosign), CEL.
func startSyncController(_ context.Context, cc ControllerContext) (bool, error) {
	provider := &crdprovider.CRDProvider{Client: cc.Manager.GetClient()}

	if err := (&controller.SyncReconciler{
		Client:           cc.Manager.GetClient(),
		Scheme:           cc.Manager.GetScheme(),
		Recorder:         cc.Recorder,
		ActuatorRegistry: cc.ActuatorRegistry,
		Provider:         provider,
		SoakGate:         cc.Gates.Soak,
		MetricsGate:      cc.Gates.Metrics,
		ApprovalGate:     cc.Gates.Approval,
		VerificationGate: cc.Gates.Verification,
		HealthAssessor:   cc.HealthAssessor,
		Notifier:         cc.Notifier,
		OCIService:       cc.OCIService,
		ApprovalSecret:   cc.ApprovalSecret,
		ExternalURL:      cc.ExternalURL,
		CELGate:          cc.Gates.CEL,
		GateRegistry:     cc.GateRegistry,
	}).SetupWithManager(cc.Manager); err != nil {
		return false, err
	}
	return true, nil
}

// startApprovalController starts the Approval reconciler.
// Watches Approval objects and unblocks Syncs waiting in WaitingApproval phase.
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
// Creates bootstrap SA + kubeconfig Secret so spoke clusters can submit CSRs.
func startBootstrapTokenController(_ context.Context, cc ControllerContext) (bool, error) {
	if err := (&controller.BootstrapTokenReconciler{
		Client:    cc.Manager.GetClient(),
		Recorder:  cc.Recorder,
		HubAPIURL: cc.HubAPIURL,
		HubCAData: cc.HubCAData,
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

// startManagedClusterController starts the ManagedCluster deregistration reconciler.
// Handles finalizer-based cleanup of long-lived cluster RBAC when a ManagedCluster is deleted.
func startManagedClusterController(_ context.Context, cc ControllerContext) (bool, error) {
	if err := (&controller.ManagedClusterReconciler{
		Client: cc.Manager.GetClient(),
		Scheme: cc.Manager.GetScheme(),
	}).SetupWithManager(cc.Manager); err != nil {
		return false, err
	}
	return true, nil
}

// startPipelineController starts the Pipeline status-aggregation reconciler.
// Read-only from a scheduling standpoint — only updates Pipeline.status
// (phase, stageProgress) by watching Sync objects it owns.
func startPipelineController(_ context.Context, cc ControllerContext) (bool, error) {
	if err := (&controller.PipelineReconciler{
		Client:   cc.Manager.GetClient(),
		Recorder: cc.Recorder,
	}).SetupWithManager(cc.Manager); err != nil {
		return false, err
	}
	return true, nil
}

// BuildGateSet constructs the full MVP gate set.
// c is the controller-runtime client used by ApprovalGate, VerificationGate, and CELGate.
// All five gates are wired and ready — callers do not need to set fields after calling this.
//
// Symmetry invariant: every gate that SyncReconciler uses must be constructed here,
// including CEL (used for GateTemplate dispatch, not a fixed FSM phase). This makes
// BuildGateSet the single source of truth for gate wiring across the entire operator.
func BuildGateSet(c client.Client) GateSet {
	return GateSet{
		Soak:    &internalgate.SoakGate{},
		Metrics: &internalgate.MetricsGate{},
		Approval: &internalgate.ApprovalGate{Client: c},
		Verification: &internalgate.VerificationGate{
			Verifier:  &cosignverifier.Verifier{},
			KeyReader: &internalgate.ClientSecretKeyReader{Client: c},
		},
		// CEL is dispatched via gateForTemplate (type=="cel"), not a fixed FSM phase,
		// but lives here for symmetry: all gate construction happens in one place.
		CEL: &celgate.Gate{Client: c},
	}
}

// compile-time checks: all built-in gate implementations satisfy gate.Gate.
// Add a line here whenever a new built-in gate is added to BuildGateSet or BuildGateRegistry.
var (
	// FSM-phase gates (BuildGateSet)
	_ pkggate.Gate = (*internalgate.SoakGate)(nil)
	_ pkggate.Gate = (*internalgate.MetricsGate)(nil)
	_ pkggate.Gate = (*internalgate.ApprovalGate)(nil)
	_ pkggate.Gate = (*internalgate.VerificationGate)(nil)
	_ pkggate.Gate = (*celgate.Gate)(nil)
	// Template-dispatch gates (BuildGateRegistry)
	_ pkggate.Gate = (*jobgate.Gate)(nil)
	_ pkggate.Gate = (*webhookgate.Gate)(nil)
)
