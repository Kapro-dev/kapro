// Package controller — FleetCluster bootstrap reconciler.
//
// One reconciler. One writer of FleetCluster.status.bootstrap. Two watches:
//
//   - FleetCluster (primary): TTL → ExpiresAt computation, bootstrap SA +
//     kubeconfig Secret provisioning, finalizer cleanup, condition management.
//   - CertificateSigningRequest (secondary): mapped back to the FleetCluster
//     by parsing the CSR Common Name. The reconcile pass then validates the
//     CSR, approves it via the typed CertificatesV1 client (UpdateApproval),
//     creates the per-cluster ClusterRole + Binding with a resourceNames lock,
//     and marks status.bootstrap.{used, usedAt, boundCSRName, …}.
//
// The CSR signing itself is delegated to the K8s built-in
// `kubernetes.io/kube-apiserver-client` signer — once we approve, the
// kube-controller-manager signs the cert with the apiserver's own client CA.
// There is no Kapro-managed CA. See projects/kapro/specs/fleet-and-oci-delivery-core-spec-2026-05-17.md §3.3.
//
// Pattern mined from the deleted `csrapproval_controller.go` (commit 99a01cd),
// reshaped for FleetCluster + per-cluster RBAC lock + the new
// IssuedClusterRole/IssuedClusterRoleBinding status fields.
package controller

import (
	"context"
	"fmt"
	"strings"
	"time"

	certificatesv1 "k8s.io/api/certificates/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	certificatesv1client "k8s.io/client-go/kubernetes/typed/certificates/v1"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
)

const (
	// KaproSystemNamespace is the canonical namespace for hub-side per-cluster
	// bootstrap ServiceAccounts and the heartbeat Lease.
	KaproSystemNamespace = "kapro-system"

	// csrCNPrefix is the Common Name prefix every Kapro cluster CSR carries.
	// The cluster name follows the colon.
	csrCNPrefix = "kapro-cluster:"

	// csrOrganization is the required Organization in every Kapro cluster CSR.
	// Guards against an attacker requesting O=system:masters.
	csrOrganization = "kapro:cluster-controllers"

	// csrSigner is the K8s built-in client-auth signer. KCM signs approved CSRs
	// for this signer name with the apiserver's own client CA.
	csrSigner = certificatesv1.KubeAPIServerClientSignerName

	// bootstrapSAPrefix is the Username prefix on bootstrap CSRs. The Username
	// is `system:serviceaccount:<podNs>:kapro-bootstrap-<cluster>`.
	bootstrapSAFormat = "kapro-bootstrap-%s"

	// kaproManagedBy is the value applied to app.kubernetes.io/managed-by on
	// every resource created by this reconciler.
	kaproManagedBy = "kapro-operator"

	// clusterRoleNameFmt is the per-cluster long-lived ClusterRole name.
	clusterRoleNameFmt = "kapro:cluster-controller:%s"

	// bootstrapRoleNameFmt is the bootstrap (CSR-create-only) ClusterRole name.
	bootstrapRoleNameFmt = "kapro:bootstrap:%s"

	// bootstrapKubeconfigSecretFmt is the Secret holding the rendered kubeconfig
	// the operator hands to the spoke for its initial CSR submission.
	bootstrapKubeconfigSecretFmt = "kapro-bootstrap-kubeconfig-%s"

	// bootstrapTokenAudience scopes the bootstrap SA token to the CSR submission
	// action — it cannot be used as a generic kube-apiserver bearer token.
	bootstrapTokenAudience = "kapro-bootstrap"

	// bootstrapTokenLifetime is the TTL of the issued SA TokenRequest.
	// Long enough for a kubectl wait + CSR poll round-trip; short enough that a
	// leaked bootstrap kubeconfig stops being useful after one hour.
	bootstrapTokenLifetime = 3600 * time.Second

	// defaultBootstrapTTL is applied when spec.bootstrap.ttl is empty.
	defaultBootstrapTTL = 24 * time.Hour

	// bootstrapMaxTTL caps any operator-supplied TTL. Stops "ttl: 87600h" footguns.
	bootstrapMaxTTL = 7 * 24 * time.Hour
)

// FleetClusterBootstrapReconciler is the sole owner of FleetCluster.status.bootstrap.
type FleetClusterBootstrapReconciler struct {
	client.Client
	Scheme     *runtime.Scheme
	Recorder   record.EventRecorder
	KubeClient kubernetes.Interface
	CertClient certificatesv1client.CertificatesV1Interface

	// HubAPIURL is the externally-reachable hub kube-apiserver URL the spoke
	// will use to submit CSRs and (after bootstrap) talk K8s API. Required.
	HubAPIURL string
	// HubCAData is the PEM CA for HubAPIURL.
	HubCAData []byte
	// PodNamespace is where bootstrap SAs and kubeconfig Secrets live.
	// Defaults to KaproSystemNamespace.
	PodNamespace string
}

// +kubebuilder:rbac:groups=kapro.io,resources=fleetclusters,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=kapro.io,resources=fleetclusters/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kapro.io,resources=fleetclusters/finalizers,verbs=update
// +kubebuilder:rbac:groups=certificates.k8s.io,resources=certificatesigningrequests,verbs=get;list;watch;update
// +kubebuilder:rbac:groups=certificates.k8s.io,resources=certificatesigningrequests/approval,verbs=update
// +kubebuilder:rbac:groups=certificates.k8s.io,resources=signers,resourceNames=kubernetes.io/kube-apiserver-client,verbs=approve
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterroles;clusterrolebindings,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=serviceaccounts,verbs=get;list;watch;create;delete
// +kubebuilder:rbac:groups="",resources=serviceaccounts/token,verbs=create

// Reconcile is keyed on FleetCluster. CSR events are mapped to FleetCluster
// names via csrToFleetCluster() in SetupWithManager.
func (r *FleetClusterBootstrapReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx).WithValues("fleetcluster", req.Name)

	fc := &kaprov1alpha1.FleetCluster{}
	if err := r.Get(ctx, req.NamespacedName, fc); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !fc.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, fc)
	}

	if fc.Spec.Suspend {
		return r.handleSuspended(ctx, fc)
	}

	// FleetClusters without a bootstrap slot aren't our business; they're
	// either cloud-fleet-imported or pre-bootstrapped via kubeconfig.
	// If we previously added a finalizer (spec.bootstrap was set, then removed),
	// drop it now so the operator can delete the FleetCluster without manual
	// finalizer surgery. Per-cluster RBAC for an already-registered cluster
	// remains — operators who remove spec.bootstrap intentionally must clean up
	// the RBAC themselves (or delete the FleetCluster outright).
	if fc.Spec.Bootstrap == nil {
		if containsString(fc.Finalizers, kaprov1alpha1.FleetClusterFinalizer) {
			patch := client.MergeFrom(fc.DeepCopy())
			fc.Finalizers = removeString(fc.Finalizers, kaprov1alpha1.FleetClusterFinalizer)
			if err := r.Patch(ctx, fc, patch); err != nil {
				return ctrl.Result{}, fmt.Errorf("clear finalizer after bootstrap removed: %w", err)
			}
		}
		return ctrl.Result{}, nil
	}

	// Add finalizer before any side-effecting work so cleanup always runs.
	if !containsString(fc.Finalizers, kaprov1alpha1.FleetClusterFinalizer) {
		patch := client.MergeFrom(fc.DeepCopy())
		fc.Finalizers = append(fc.Finalizers, kaprov1alpha1.FleetClusterFinalizer)
		if err := r.Patch(ctx, fc, patch); err != nil {
			return ctrl.Result{}, fmt.Errorf("add finalizer: %w", err)
		}
		return ctrl.Result{RequeueAfter: time.Nanosecond}, nil
	}

	// Phase 1 — compute ExpiresAt from TTL on first observation.
	if mutated, err := r.computeExpiresAt(ctx, fc); err != nil {
		return ctrl.Result{}, fmt.Errorf("compute expiresAt: %w", err)
	} else if mutated {
		return ctrl.Result{RequeueAfter: time.Nanosecond}, nil
	}

	// Phase 2 — expired? Mark stalled. No more provisioning.
	if r.expired(fc) {
		log.Info("bootstrap slot expired", "expiresAt", fc.Spec.Bootstrap.ExpiresAt)
		return r.markExpired(ctx, fc)
	}

	// Phase 3 — always process matching CSRs (bootstrap-pending OR renewal).
	// processCSRsForCluster is idempotent: it skips approved/denied CSRs and
	// re-invokes handleBootstrapCSR / handleRenewalCSR for the rest.
	//
	// Crash-recovery: if a previous reconcile marked status.bootstrap.Used=true
	// but crashed before calling UpdateApproval, the CSR is still pending. We
	// MUST run this phase even when Used==true so the pending CSR is approved
	// on retry; otherwise the spoke is stuck waiting for a cert it will never
	// receive. Regression test: TestReconcile_CrashRecovery_ApprovesPendingCSR.
	if res, err := r.processCSRsForCluster(ctx, fc); err != nil {
		return ctrl.Result{}, fmt.Errorf("process CSRs: %w", err)
	} else if !res.IsZero() {
		return res, nil
	}

	// Phase 4 — already registered? Ensure per-cluster RBAC is in place and
	// mark Registered. (Renewal CSRs are handled in Phase 3 above.)
	if fc.Status.Bootstrap != nil && fc.Status.Bootstrap.Used {
		return r.handleRegistered(ctx, fc)
	}

	// Phase 5 — not registered yet: provision the bootstrap SA + kubeconfig
	// Secret so the spoke has something to authenticate with. Idempotent;
	// also re-issues when the issued SA token is approaching expiry.
	if res, err := r.ensureBootstrapProvisioned(ctx, fc); err != nil {
		return ctrl.Result{}, fmt.Errorf("provision bootstrap: %w", err)
	} else if !res.IsZero() {
		return res, nil
	}

	// Steady-state: bootstrap provisioned but no CSR yet. Set Awaiting condition.
	return r.markAwaitingCSR(ctx, fc)
}

// handleDeletion cleans up bootstrap SA, bootstrap RBAC, kubeconfig Secret,
// and per-cluster long-lived RBAC. Then clears the finalizer.
func (r *FleetClusterBootstrapReconciler) handleDeletion(ctx context.Context, fc *kaprov1alpha1.FleetCluster) (ctrl.Result, error) {
	log := log.FromContext(ctx).WithValues("fleetcluster", fc.Name)
	if !containsString(fc.Finalizers, kaprov1alpha1.FleetClusterFinalizer) {
		return ctrl.Result{}, nil
	}

	if err := r.cleanupBootstrapResources(ctx, fc.Name); err != nil {
		log.Error(err, "failed to cleanup bootstrap resources; will retry")
		return ctrl.Result{}, err
	}
	if err := r.cleanupClusterRBAC(ctx, fc.Name); err != nil {
		log.Error(err, "failed to cleanup cluster RBAC; will retry")
		return ctrl.Result{}, err
	}

	patch := client.MergeFrom(fc.DeepCopy())
	fc.Finalizers = removeString(fc.Finalizers, kaprov1alpha1.FleetClusterFinalizer)
	if err := r.Patch(ctx, fc, patch); err != nil {
		return ctrl.Result{}, fmt.Errorf("clear finalizer: %w", err)
	}

	log.Info("fleetcluster deregistered — RBAC cleaned up")
	if r.Recorder != nil {
		r.Recorder.Eventf(fc, corev1.EventTypeNormal, "Deregistered", "FleetCluster %s removed; RBAC cleaned up", fc.Name)
	}
	return ctrl.Result{}, nil
}

// handleSuspended sets reconciling=false/Suspended and clears Stalled.
func (r *FleetClusterBootstrapReconciler) handleSuspended(ctx context.Context, fc *kaprov1alpha1.FleetCluster) (ctrl.Result, error) {
	patch := client.MergeFrom(fc.DeepCopy())
	fc.Status.ObservedGeneration = fc.Generation
	now := time.Now()
	setCondition(&fc.Status.Conditions, kaprov1alpha1.ConditionTypeReconciling, metav1.ConditionFalse, "Suspended", "fleetcluster is suspended", fc.Generation, now)
	apimeta.RemoveStatusCondition(&fc.Status.Conditions, kaprov1alpha1.ConditionTypeStalled)
	if err := r.Status().Patch(ctx, fc, patch); err != nil {
		return ctrl.Result{}, fmt.Errorf("patch suspended condition: %w", err)
	}
	return ctrl.Result{}, nil
}

// computeExpiresAt fills in spec.bootstrap.expiresAt from TTL on first reconcile.
// Returns (true, nil) if the resource was patched (caller should requeue).
func (r *FleetClusterBootstrapReconciler) computeExpiresAt(ctx context.Context, fc *kaprov1alpha1.FleetCluster) (bool, error) {
	if fc.Spec.Bootstrap.ExpiresAt != nil {
		return false, nil
	}
	ttl := defaultBootstrapTTL
	if fc.Spec.Bootstrap.TTL != "" {
		d, err := time.ParseDuration(fc.Spec.Bootstrap.TTL)
		if err != nil {
			return false, fmt.Errorf("invalid spec.bootstrap.ttl %q: %w", fc.Spec.Bootstrap.TTL, err)
		}
		if d > bootstrapMaxTTL {
			d = bootstrapMaxTTL
		}
		if d <= 0 {
			return false, fmt.Errorf("spec.bootstrap.ttl must be > 0, got %s", fc.Spec.Bootstrap.TTL)
		}
		ttl = d
	}
	expires := fc.CreationTimestamp.Add(ttl)
	patch := client.MergeFrom(fc.DeepCopy())
	t := metav1.NewTime(expires)
	fc.Spec.Bootstrap.ExpiresAt = &t
	if err := r.Patch(ctx, fc, patch); err != nil {
		return false, fmt.Errorf("patch expiresAt: %w", err)
	}
	return true, nil
}

// expired returns true when bootstrap slot is past its expiresAt deadline
// AND has not yet been consumed.
func (r *FleetClusterBootstrapReconciler) expired(fc *kaprov1alpha1.FleetCluster) bool {
	if fc.Status.Bootstrap != nil && fc.Status.Bootstrap.Used {
		return false
	}
	if fc.Spec.Bootstrap.ExpiresAt == nil {
		return false
	}
	return time.Now().After(fc.Spec.Bootstrap.ExpiresAt.Time)
}

// markExpired patches Stalled=True with reason BootstrapExpired.
func (r *FleetClusterBootstrapReconciler) markExpired(ctx context.Context, fc *kaprov1alpha1.FleetCluster) (ctrl.Result, error) {
	patch := client.MergeFrom(fc.DeepCopy())
	fc.Status.ObservedGeneration = fc.Generation
	now := time.Now()
	setCondition(&fc.Status.Conditions, kaprov1alpha1.ConditionTypeStalled, metav1.ConditionTrue, "BootstrapExpired", "bootstrap slot expired before any CSR was submitted; update spec.bootstrap to retry", fc.Generation, now)
	setCondition(&fc.Status.Conditions, kaprov1alpha1.ConditionTypeReconciling, metav1.ConditionFalse, "BootstrapExpired", "stalled: bootstrap expired", fc.Generation, now)
	setCondition(&fc.Status.Conditions, kaprov1alpha1.ConditionTypeRegistered, metav1.ConditionFalse, "BootstrapExpired", "fleetcluster never registered before bootstrap expired", fc.Generation, now)
	setCondition(&fc.Status.Conditions, kaprov1alpha1.ConditionTypeReady, metav1.ConditionFalse, "BootstrapExpired", "fleetcluster is not ready: bootstrap expired", fc.Generation, now)
	if err := r.Status().Patch(ctx, fc, patch); err != nil {
		return ctrl.Result{}, fmt.Errorf("patch expired condition: %w", err)
	}
	if r.Recorder != nil {
		r.Recorder.Eventf(fc, corev1.EventTypeWarning, "BootstrapExpired", "Bootstrap slot expired without a successful CSR")
	}
	return ctrl.Result{}, nil
}

// handleRegistered keeps long-lived per-cluster RBAC in sync after first
// successful registration, and stamps Registered=True.
func (r *FleetClusterBootstrapReconciler) handleRegistered(ctx context.Context, fc *kaprov1alpha1.FleetCluster) (ctrl.Result, error) {
	if err := r.ensureClusterRBAC(ctx, fc.Name); err != nil {
		return ctrl.Result{}, fmt.Errorf("ensure cluster RBAC: %w", err)
	}
	return r.markRegistered(ctx, fc)
}

// markRegistered sets Registered=True. Ready is left to the heartbeat
// reconciler since "registered" and "live" are different states.
// IssuedClusterRole/IssuedClusterRoleBinding are owned by markBootstrapUsed
// during the first-time approval — we do not re-write them here.
// setCondition is idempotent via apimeta.SetStatusCondition, so this is a
// no-op patch when nothing changed.
func (r *FleetClusterBootstrapReconciler) markRegistered(ctx context.Context, fc *kaprov1alpha1.FleetCluster) (ctrl.Result, error) {
	patch := client.MergeFrom(fc.DeepCopy())
	fc.Status.ObservedGeneration = fc.Generation
	now := time.Now()
	setCondition(&fc.Status.Conditions, kaprov1alpha1.ConditionTypeRegistered, metav1.ConditionTrue, "BootstrapConsumed", "fleetcluster registered via approved CSR", fc.Generation, now)
	setCondition(&fc.Status.Conditions, kaprov1alpha1.ConditionTypeReconciling, metav1.ConditionFalse, "Registered", "fleetcluster registration is complete; heartbeat owns Ready", fc.Generation, now)
	apimeta.RemoveStatusCondition(&fc.Status.Conditions, kaprov1alpha1.ConditionTypeStalled)
	if err := r.Status().Patch(ctx, fc, patch); err != nil {
		return ctrl.Result{}, fmt.Errorf("patch registered condition: %w", err)
	}
	return ctrl.Result{}, nil
}

// markAwaitingCSR sets Reconciling=True/AwaitingCSR.
func (r *FleetClusterBootstrapReconciler) markAwaitingCSR(ctx context.Context, fc *kaprov1alpha1.FleetCluster) (ctrl.Result, error) {
	patch := client.MergeFrom(fc.DeepCopy())
	fc.Status.ObservedGeneration = fc.Generation
	now := time.Now()
	setCondition(&fc.Status.Conditions, kaprov1alpha1.ConditionTypeReconciling, metav1.ConditionTrue, "AwaitingCSR", "bootstrap kubeconfig issued; waiting for spoke to submit CSR", fc.Generation, now)
	setCondition(&fc.Status.Conditions, kaprov1alpha1.ConditionTypeRegistered, metav1.ConditionFalse, "AwaitingCSR", "no CSR has been approved yet", fc.Generation, now)
	apimeta.RemoveStatusCondition(&fc.Status.Conditions, kaprov1alpha1.ConditionTypeStalled)
	if err := r.Status().Patch(ctx, fc, patch); err != nil {
		return ctrl.Result{}, fmt.Errorf("patch awaiting CSR condition: %w", err)
	}
	return ctrl.Result{}, nil
}

// processCSRsForCluster scans for pending CSRs whose CN matches this cluster.
// Validates, approves, marks used, ensures RBAC. Idempotent on retry.
//
// TODO(perf, v0.6): list is O(all CSRs in the cluster). At fleet scales with
// frequent renewal CSRs (PR-3) this becomes expensive. Add a label selector
// (kapro.io/fleetcluster=<name>) on CSRs at creation time and switch to a
// filtered List.
func (r *FleetClusterBootstrapReconciler) processCSRsForCluster(ctx context.Context, fc *kaprov1alpha1.FleetCluster) (ctrl.Result, error) {
	var csrList certificatesv1.CertificateSigningRequestList
	if err := r.List(ctx, &csrList); err != nil {
		return ctrl.Result{}, fmt.Errorf("list CSRs: %w", err)
	}
	expectedCN := csrCNPrefix + fc.Name
	for i := range csrList.Items {
		csr := &csrList.Items[i]
		if !r.matchesFleetCluster(csr, fc, expectedCN) {
			continue
		}
		// Skip only CSRs that have already been finalized (approved or denied).
		// Pending CSRs that previously bound this slot (status.bootstrap.Used
		// but never finalized — e.g., controller crashed between
		// markBootstrapUsed and UpdateApproval) MUST be re-processed.
		// handleBootstrapCSR is idempotent: markBootstrapUsed early-returns on
		// the same CSR, ensureClusterRBAC upserts, approveCSR is guarded by
		// isCSRApproved.
		if isCSRApproved(csr) || isCSRDenied(csr) {
			continue
		}
		switch r.classifyCSR(csr, fc) {
		case csrKindBootstrap:
			if err := r.handleBootstrapCSR(ctx, fc, csr); err != nil {
				return ctrl.Result{}, err
			}
		case csrKindRenewal:
			if err := r.handleRenewalCSR(ctx, fc, csr); err != nil {
				return ctrl.Result{}, err
			}
		default:
			continue
		}
		return ctrl.Result{RequeueAfter: time.Nanosecond}, nil
	}
	return ctrl.Result{}, nil
}

// handleRenewalCSR approves a cert-rotation request from an already-registered
// cluster. The K8s apiserver authenticated the requester via x509 client cert,
// and the per-cluster ClusterRoleBinding (issued at first bootstrap) confirms
// the request came from THIS cluster's identity. No token check needed.
//
// If the FleetCluster is suspended we deny — operators may have intentionally
// quarantined the cluster and renewal would let it keep talking to the hub.
func (r *FleetClusterBootstrapReconciler) handleRenewalCSR(
	ctx context.Context,
	fc *kaprov1alpha1.FleetCluster,
	csr *certificatesv1.CertificateSigningRequest,
) error {
	log := log.FromContext(ctx).WithValues("fleetcluster", fc.Name, "csr", csr.Name)
	if fc.Spec.Suspend {
		return r.denyCSR(ctx, csr, "FleetCluster is suspended; renewal denied")
	}
	if isCSRApproved(csr) {
		return nil
	}
	if err := r.approveCSR(ctx, csr); err != nil {
		return fmt.Errorf("approve renewal CSR: %w", err)
	}
	log.Info("renewal CSR approved")
	if r.Recorder != nil {
		r.Recorder.Eventf(fc, corev1.EventTypeNormal, "CertRenewed", "Renewal CSR %s approved", csr.Name)
	}
	return nil
}

// matchesFleetCluster returns true when the CSR is a Kapro CSR — bootstrap
// or renewal — for THIS FleetCluster. Distinguishing the two is done in
// classifyCSR below.
func (r *FleetClusterBootstrapReconciler) matchesFleetCluster(csr *certificatesv1.CertificateSigningRequest, fc *kaprov1alpha1.FleetCluster, expectedCN string) bool {
	if !isKaproCSR(csr) {
		return false
	}
	req, err := parseCSRRequest(csr.Spec.Request)
	if err != nil {
		return false
	}
	if req.Subject.CommonName != expectedCN {
		return false
	}
	return r.classifyCSR(csr, fc) != csrKindUnknown
}

// csrKind discriminates the two CSR shapes the hub accepts.
type csrKind int

const (
	csrKindUnknown csrKind = iota
	// csrKindBootstrap — Username is the per-cluster bootstrap SA. Must
	// validate against spec.bootstrap (unused, not expired, SA name matches
	// the cluster). One-time use; consumes the bootstrap slot.
	csrKindBootstrap
	// csrKindRenewal — Username is the cluster's existing cert identity
	// (system:kapro:cluster-controllers / kapro-cluster:<name>). No token
	// check: the existing cert is proof of identity. The K8s authenticator
	// already verified it before the CSR Create reached us. We just confirm
	// the FleetCluster still exists and isn't suspended.
	csrKindRenewal
)

// classifyCSR returns whether the CSR is a bootstrap or a renewal for this
// FleetCluster, or unknown (ignored).
func (r *FleetClusterBootstrapReconciler) classifyCSR(csr *certificatesv1.CertificateSigningRequest, fc *kaprov1alpha1.FleetCluster) csrKind {
	expectedBootstrapSA := fmt.Sprintf("system:serviceaccount:%s:%s", r.podNS(), fmt.Sprintf(bootstrapSAFormat, fc.Name))
	if csr.Spec.Username == expectedBootstrapSA {
		return csrKindBootstrap
	}
	// Renewal: Username == the cluster's existing cert identity. K8s
	// formats x509-authenticated usernames as exactly the cert CN.
	if csr.Spec.Username == csrCNPrefix+fc.Name {
		// Defense-in-depth: require the requester to also be in the
		// cluster-controllers group. The hub apiserver populates Groups
		// from the cert Organization, which our per-cluster Binding only
		// grants to certs we previously issued.
		for _, g := range csr.Spec.Groups {
			if g == csrOrganization {
				return csrKindRenewal
			}
		}
	}
	return csrKindUnknown
}

// handleBootstrapCSR validates the bootstrap CSR, marks the slot used, ensures
// per-cluster RBAC, and approves the CSR. Ordering is intentional:
//
//  1. ensure RBAC FIRST so the issued cert lands into a writable surface
//  2. mark used SECOND so a crash here cannot lose the binding
//  3. approve LAST so a transient approval failure does not leak the slot
//
// Each step is idempotent.
func (r *FleetClusterBootstrapReconciler) handleBootstrapCSR(
	ctx context.Context,
	fc *kaprov1alpha1.FleetCluster,
	csr *certificatesv1.CertificateSigningRequest,
) error {
	log := log.FromContext(ctx).WithValues("fleetcluster", fc.Name, "csr", csr.Name)

	if r.expired(fc) {
		return r.denyCSR(ctx, csr, "bootstrap slot expired")
	}
	if fc.Status.Bootstrap != nil &&
		fc.Status.Bootstrap.Used &&
		fc.Status.Bootstrap.BoundCSRName != csr.Name {
		return r.denyCSR(ctx, csr, fmt.Sprintf("bootstrap already consumed by CSR %s", fc.Status.Bootstrap.BoundCSRName))
	}

	if err := r.ensureClusterRBAC(ctx, fc.Name); err != nil {
		return fmt.Errorf("ensure cluster RBAC: %w", err)
	}

	if err := r.markBootstrapUsed(ctx, fc, csr.Name); err != nil {
		return fmt.Errorf("mark bootstrap used: %w", err)
	}

	if !isCSRApproved(csr) {
		if err := r.approveCSR(ctx, csr); err != nil {
			return fmt.Errorf("approve CSR: %w", err)
		}
		log.Info("bootstrap CSR approved")
		if r.Recorder != nil {
			r.Recorder.Eventf(fc, corev1.EventTypeNormal, "Registered", "Bootstrap CSR %s approved", csr.Name)
		}
	}

	// Bootstrap SA kept alive briefly so the spoke's polling for the signed cert
	// keeps working; cleanup happens on FleetCluster delete or TTL expiry.
	return nil
}

// markBootstrapUsed updates the FleetCluster status to mark bootstrap as used,
// using Status().Update() (NOT Patch) so resourceVersion is the CAS predicate
// that catches concurrent reconciles attempting double-claim.
func (r *FleetClusterBootstrapReconciler) markBootstrapUsed(ctx context.Context, fc *kaprov1alpha1.FleetCluster, csrName string) error {
	if fc.Status.Bootstrap != nil &&
		fc.Status.Bootstrap.Used &&
		fc.Status.Bootstrap.BoundCSRName == csrName {
		return nil
	}
	now := metav1.Now()
	if fc.Status.Bootstrap == nil {
		fc.Status.Bootstrap = &kaprov1alpha1.FleetClusterBootstrapStatus{}
	}
	fc.Status.Bootstrap.Used = true
	fc.Status.Bootstrap.UsedAt = &now
	fc.Status.Bootstrap.IssuedCredentialFor = fc.Name
	fc.Status.Bootstrap.BoundCSRName = csrName
	fc.Status.Bootstrap.IssuedClusterRole = fmt.Sprintf(clusterRoleNameFmt, fc.Name)
	fc.Status.Bootstrap.IssuedClusterRoleBinding = fmt.Sprintf(clusterRoleNameFmt, fc.Name)
	fc.Status.ObservedGeneration = fc.Generation
	setCondition(&fc.Status.Conditions, kaprov1alpha1.ConditionTypeRegistered, metav1.ConditionTrue, "BootstrapConsumed", fmt.Sprintf("bootstrap credential consumed by CSR %s", csrName), fc.Generation, time.Now())
	return r.Status().Update(ctx, fc)
}

// approveCSR appends the Approved condition and submits via the typed
// CertificatesV1 client. controller-runtime's Status().Update() does NOT route
// through the /approval subresource — using it silently no-ops.
func (r *FleetClusterBootstrapReconciler) approveCSR(ctx context.Context, csr *certificatesv1.CertificateSigningRequest) error {
	csr.Status.Conditions = append(csr.Status.Conditions, certificatesv1.CertificateSigningRequestCondition{
		Type:           certificatesv1.CertificateApproved,
		Status:         corev1.ConditionTrue,
		Reason:         "KaproApproved",
		Message:        "Approved by Kapro FleetCluster bootstrap reconciler",
		LastUpdateTime: metav1.Now(),
	})
	_, err := r.CertClient.CertificateSigningRequests().UpdateApproval(ctx, csr.Name, csr, metav1.UpdateOptions{})
	return err
}

// denyCSR appends the Denied condition.
func (r *FleetClusterBootstrapReconciler) denyCSR(ctx context.Context, csr *certificatesv1.CertificateSigningRequest, reason string) error {
	if isCSRApproved(csr) || isCSRDenied(csr) {
		return nil
	}
	log.FromContext(ctx).Info("denying CSR", "csr", csr.Name, "reason", reason)
	if r.Recorder != nil {
		r.Recorder.Eventf(csr, corev1.EventTypeWarning, "Denied", reason)
	}
	csr.Status.Conditions = append(csr.Status.Conditions, certificatesv1.CertificateSigningRequestCondition{
		Type:           certificatesv1.CertificateDenied,
		Status:         corev1.ConditionTrue,
		Reason:         "KaproDenied",
		Message:        reason,
		LastUpdateTime: metav1.Now(),
	})
	_, err := r.CertClient.CertificateSigningRequests().UpdateApproval(ctx, csr.Name, csr, metav1.UpdateOptions{})
	return err
}

// SetupWithManager wires both FleetCluster and CSR watches. CSR events are
// mapped back to FleetCluster keys via clusterNameFromCSR so the reconciler
// is keyed exclusively on FleetCluster — preserving single-writer semantics
// for status.bootstrap.
func (r *FleetClusterBootstrapReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.CertClient == nil {
		return fmt.Errorf("FleetClusterBootstrapReconciler: CertClient is required for CSR approval")
	}
	if r.KubeClient == nil {
		return fmt.Errorf("FleetClusterBootstrapReconciler: KubeClient is required for bootstrap SA provisioning")
	}
	if r.HubAPIURL == "" {
		return fmt.Errorf("FleetClusterBootstrapReconciler: HubAPIURL is required (set KAPRO_HUB_API_URL)")
	}

	mapCSR := handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
		csr, ok := obj.(*certificatesv1.CertificateSigningRequest)
		if !ok {
			return nil
		}
		if !isKaproCSR(csr) {
			return nil
		}
		req, err := parseCSRRequest(csr.Spec.Request)
		if err != nil {
			return nil
		}
		clusterName := strings.TrimPrefix(req.Subject.CommonName, csrCNPrefix)
		if clusterName == "" {
			return nil
		}
		return []reconcile.Request{{NamespacedName: types.NamespacedName{Name: clusterName}}}
	})

	return ctrl.NewControllerManagedBy(mgr).
		Named("fleetcluster-bootstrap").
		For(&kaprov1alpha1.FleetCluster{}, builder.WithPredicates(fleetClusterBootstrapPredicate())).
		Watches(&certificatesv1.CertificateSigningRequest{}, mapCSR, builder.WithPredicates(csrPredicate())).
		Complete(r)
}

// fleetClusterBootstrapPredicate filters FleetCluster events to those that
// could affect bootstrap state. Reduces unnecessary reconciles on status churn
// from other controllers.
func fleetClusterBootstrapPredicate() predicate.Predicate {
	return predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldFC, oldOK := e.ObjectOld.(*kaprov1alpha1.FleetCluster)
			newFC, newOK := e.ObjectNew.(*kaprov1alpha1.FleetCluster)
			if !oldOK || !newOK {
				return true
			}
			// Reconcile on spec change, finalizer change, deletion, or bootstrap
			// status change (the only status fields we own).
			if oldFC.Generation != newFC.Generation {
				return true
			}
			if !sameFinalizers(oldFC.Finalizers, newFC.Finalizers) {
				return true
			}
			// Compare deletion-state by zero-ness, not by pointer identity. Two
			// non-nil *metav1.Time across cache refreshes hold distinct addresses;
			// raw `!=` always returned true and produced spurious reconciles.
			if oldFC.DeletionTimestamp.IsZero() != newFC.DeletionTimestamp.IsZero() {
				return true
			}
			return !bootstrapStatusEqual(oldFC.Status.Bootstrap, newFC.Status.Bootstrap)
		},
		CreateFunc:  func(e event.CreateEvent) bool { return true },
		DeleteFunc:  func(e event.DeleteEvent) bool { return true },
		GenericFunc: func(e event.GenericEvent) bool { return true },
	}
}

// csrPredicate cheaply filters CSR events to Kapro-signed CSRs only.
func csrPredicate() predicate.Predicate {
	return predicate.NewPredicateFuncs(func(obj client.Object) bool {
		csr, ok := obj.(*certificatesv1.CertificateSigningRequest)
		if !ok {
			return false
		}
		return csr.Spec.SignerName == csrSigner
	})
}

func (r *FleetClusterBootstrapReconciler) podNS() string {
	if r.PodNamespace != "" {
		return r.PodNamespace
	}
	return KaproSystemNamespace
}

// ensureClusterRBAC is the long-lived per-cluster RBAC: a ClusterRole and
// ClusterRoleBinding with resourceNames=[clusterName] that grants the spoke
// the minimum it needs to operate. Idempotent; safe to call repeatedly.
func (r *FleetClusterBootstrapReconciler) ensureClusterRBAC(ctx context.Context, clusterName string) error {
	roleName := fmt.Sprintf(clusterRoleNameFmt, clusterName)
	if err := r.upsertClusterRole(ctx, roleName, clusterName); err != nil {
		return err
	}
	clusterUser := csrCNPrefix + clusterName
	return r.upsertClusterRoleBinding(ctx, roleName, clusterUser, clusterName)
}

// cleanupClusterRBAC deletes the per-cluster ClusterRole + Binding.
// Non-fatal if missing.
func (r *FleetClusterBootstrapReconciler) cleanupClusterRBAC(ctx context.Context, clusterName string) error {
	return r.deleteClusterRBAC(ctx, fmt.Sprintf(clusterRoleNameFmt, clusterName))
}

// cleanupBootstrapResources deletes the bootstrap SA, ClusterRole, Binding,
// and kubeconfig Secret. Called on FleetCluster delete or after TTL expiry.
func (r *FleetClusterBootstrapReconciler) cleanupBootstrapResources(ctx context.Context, clusterName string) error {
	saName := fmt.Sprintf(bootstrapSAFormat, clusterName)
	roleName := fmt.Sprintf(bootstrapRoleNameFmt, clusterName)
	secretName := fmt.Sprintf(bootstrapKubeconfigSecretFmt, clusterName)
	for _, obj := range []client.Object{
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: r.podNS(), Name: secretName}},
		&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Namespace: r.podNS(), Name: saName}},
	} {
		if err := r.Delete(ctx, obj); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete %T %s: %w", obj, obj.GetName(), err)
		}
	}
	if err := r.deleteClusterRBAC(ctx, roleName); err != nil {
		return err
	}
	return nil
}
