package controller

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"strings"

	certificatesv1 "k8s.io/api/certificates/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	certificatesv1client "k8s.io/client-go/kubernetes/typed/certificates/v1"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
)

const (
	// csrCNPrefix is the Common Name prefix for all Kapro cluster-controller CSRs.
	csrCNPrefix = "kapro-cluster:"
	// csrOrganization is the required Organization in every Kapro cluster CSR.
	// Guards against O=system:masters privilege escalation.
	csrOrganization = "kapro:cluster-controllers"
	// csrSigner is the signer name we handle.
	csrSigner = "kubernetes.io/kube-apiserver-client"
	// bootstrapSAPrefix is the prefix of bootstrap ServiceAccounts created by MemberClusterReconciler.
	bootstrapSAPrefix = "system:serviceaccount:" + kaproSystemNamespace + ":kapro-bootstrap-"
)

// CSRApprovalReconciler watches CertificateSigningRequests and automatically
// approves bootstrap and renewal requests from Kapro cluster-controllers.
//
// Bootstrap (first registration):
//  1. CSR CN = "kapro-cluster:<cluster>", O = ["kapro:cluster-controllers"]
//  2. Requester = bootstrap SA created by MemberClusterReconciler
//  3. Validates a non-expired, unused MemberCluster bootstrap token
//  4. Creates MemberCluster + long-lived RBAC for the cluster identity
//  5. Marks MemberCluster bootstrap used BEFORE approving (prevents replay race)
//  6. Approves CSR
//
// Renewal (cert rotation):
//  1. Requester is already the cluster user: "kapro-cluster:<cluster>"
//  2. ManagedCluster must exist
//  3. Approve without token — existing valid cert proves identity
type CSRApprovalReconciler struct {
	client.Client
	// CertClient is the typed client for the approval subresource.
	// Must use UpdateApproval() — r.Status().Update() does NOT work for approvals.
	CertClient certificatesv1client.CertificatesV1Interface
	Recorder   record.EventRecorder
}

// +kubebuilder:rbac:groups=certificates.k8s.io,resources=certificatesigningrequests,verbs=get;list;watch;update
// +kubebuilder:rbac:groups=certificates.k8s.io,resources=certificatesigningrequests/approval,verbs=update
// +kubebuilder:rbac:groups=certificates.k8s.io,resources=signers,resourceNames=kubernetes.io/kube-apiserver-client,verbs=approve
// +kubebuilder:rbac:groups=kapro.io,resources=memberclusters,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=kapro.io,resources=memberclusters/status,verbs=update;patch
// +kubebuilder:rbac:groups=kapro.io,resources=memberclusters/finalizers,verbs=update
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterroles;clusterrolebindings,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=serviceaccounts,verbs=get;list;watch;delete

func (r *CSRApprovalReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	var csr certificatesv1.CertificateSigningRequest
	if err := r.Get(ctx, req.NamespacedName, &csr); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Skip CSRs we don't own.
	if !r.isKaproCSR(&csr) {
		return ctrl.Result{}, nil
	}

	// Skip already-decided CSRs.
	if isCSRApproved(&csr) || isCSRDenied(&csr) {
		return ctrl.Result{}, nil
	}

	clusterName, err := clusterNameFromCN(csr.Spec.Username, &csr)
	if err != nil {
		log.Info("ignoring CSR with invalid CN/username", "csr", csr.Name, "reason", err.Error())
		return ctrl.Result{}, nil
	}

	log.Info("processing Kapro CSR", "csr", csr.Name, "cluster", clusterName, "requester", csr.Spec.Username)

	isBootstrap := strings.HasPrefix(csr.Spec.Username, bootstrapSAPrefix)
	isRenewal := csr.Spec.Username == csrCNPrefix+clusterName

	switch {
	case isBootstrap:
		if err := r.handleBootstrap(ctx, &csr, clusterName); err != nil {
			r.Recorder.Eventf(&csr, corev1.EventTypeWarning, "BootstrapFailed", "Bootstrap failed for cluster %s: %v", clusterName, err)
			return ctrl.Result{}, err
		}
	case isRenewal:
		if err := r.handleRenewal(ctx, &csr, clusterName); err != nil {
			r.Recorder.Eventf(&csr, corev1.EventTypeWarning, "RenewalFailed", "Cert renewal failed for cluster %s: %v", clusterName, err)
			return ctrl.Result{}, err
		}
	default:
		log.Info("denying CSR with unexpected requester", "csr", csr.Name, "requester", csr.Spec.Username)
		return r.denyCSR(ctx, &csr, "requester is neither bootstrap SA nor cluster user")
	}

	return ctrl.Result{}, nil
}

// handleBootstrap processes a first-registration CSR from a bootstrap SA.
func (r *CSRApprovalReconciler) handleBootstrap(ctx context.Context, csr *certificatesv1.CertificateSigningRequest, clusterName string) error {
	log := log.FromContext(ctx)

	// Validate bootstrap SA is for THIS cluster — prevents a leaked bootstrap kubeconfig
	// for cluster A from registering as cluster B.
	expectedSA := bootstrapSAPrefix + clusterName
	if csr.Spec.Username != expectedSA {
		return r.denyCSRErr(ctx, csr, fmt.Sprintf(
			"bootstrap SA %q is not authorised to register cluster %q (expected %q)",
			csr.Spec.Username, clusterName, expectedSA,
		))
	}

	// Find the MemberCluster — it must already exist (platform created it with spec.bootstrap.tokenHash).
	mc, err := r.findValidMemberCluster(ctx, clusterName)
	if err != nil {
		// Check for idempotent retry: bootstrap was already claimed by THIS exact CSR.
		if retry, retryErr := r.findUsedMemberClusterForCSR(ctx, clusterName, csr.Name); retryErr == nil {
			log.Info("retrying CSR approval for previously-bound MemberCluster", "csr", csr.Name, "cluster", clusterName)
			mc = retry
		} else {
			return r.denyCSRErr(ctx, csr, fmt.Sprintf("no valid MemberCluster bootstrap for cluster %s: %v", clusterName, err))
		}
	}

	// Ensure long-lived RBAC (cluster user = CN of the x509 cert).
	clusterUser := csrCNPrefix + clusterName
	if err := r.ensureClusterRBAC(ctx, clusterName, clusterUser); err != nil {
		return fmt.Errorf("ensure cluster RBAC: %w", err)
	}

	// Mark bootstrap used (with BoundCSRName) BEFORE approving — prevents replay race.
	// Uses Status().Update() (not Patch) so the resourceVersion check catches concurrent reconciles.
	if mc.Status.Bootstrap == nil || !mc.Status.Bootstrap.Used {
		if err := r.markBootstrapUsed(ctx, mc, clusterName, csr.Name); err != nil {
			return fmt.Errorf("mark bootstrap used: %w", err)
		}
	}

	if err := r.approveCSR(ctx, csr); err != nil {
		return fmt.Errorf("approve CSR: %w", err)
	}

	// Clean up bootstrap credentials immediately after cert issuance (OCM pattern).
	// Bootstrap SA token becomes useless once the cluster has its x509 cert.
	r.cleanupBootstrapCredentials(ctx, clusterName)

	log.Info("bootstrap CSR approved", "cluster", clusterName, "csr", csr.Name)
	r.Recorder.Eventf(csr, corev1.EventTypeNormal, "Approved", "Bootstrap CSR approved for cluster %s", clusterName)
	return nil
}

// handleRenewal processes a cert-rotation CSR from an already-registered cluster.
func (r *CSRApprovalReconciler) handleRenewal(ctx context.Context, csr *certificatesv1.CertificateSigningRequest, clusterName string) error {
	log := log.FromContext(ctx)

	// The MemberCluster must already exist — no token needed.
	var mc kaprov1alpha1.MemberCluster
	if err := r.Get(ctx, types.NamespacedName{Name: clusterName}, &mc); err != nil {
		return r.denyCSRErr(ctx, csr, fmt.Sprintf("MemberCluster %s not found for renewal: %v", clusterName, err))
	}

	if err := r.approveCSR(ctx, csr); err != nil {
		return fmt.Errorf("approve renewal CSR: %w", err)
	}

	log.Info("renewal CSR approved", "cluster", clusterName, "csr", csr.Name)
	r.Recorder.Eventf(csr, corev1.EventTypeNormal, "Approved", "Renewal CSR approved for cluster %s", clusterName)
	return nil
}

// findValidMemberCluster returns the MemberCluster for clusterName if bootstrap is valid
// (tokenHash set, not used, not expired).
func (r *CSRApprovalReconciler) findValidMemberCluster(ctx context.Context, clusterName string) (*kaprov1alpha1.MemberCluster, error) {
	var mc kaprov1alpha1.MemberCluster
	if err := r.Get(ctx, types.NamespacedName{Name: clusterName}, &mc); err != nil {
		return nil, err
	}
	if mc.Spec.Bootstrap.TokenHash == "" {
		return nil, fmt.Errorf("MemberCluster %q has no bootstrap token configured", clusterName)
	}
	if mc.Status.Bootstrap != nil && mc.Status.Bootstrap.Used {
		return nil, fmt.Errorf("bootstrap for MemberCluster %q is already used", clusterName)
	}
	if mc.Spec.Bootstrap.ExpiresAt != nil && metav1.Now().After(mc.Spec.Bootstrap.ExpiresAt.Time) {
		return nil, fmt.Errorf("bootstrap for MemberCluster %q has expired", clusterName)
	}
	return &mc, nil
}

// findUsedMemberClusterForCSR returns the MemberCluster if bootstrap was already claimed by the given CSR.
// Used to enable idempotent retry when the CSR approval call fails transiently.
func (r *CSRApprovalReconciler) findUsedMemberClusterForCSR(ctx context.Context, clusterName, csrName string) (*kaprov1alpha1.MemberCluster, error) {
	var mc kaprov1alpha1.MemberCluster
	if err := r.Get(ctx, types.NamespacedName{Name: clusterName}, &mc); err != nil {
		return nil, err
	}
	if mc.Status.Bootstrap != nil && mc.Status.Bootstrap.Used && mc.Status.Bootstrap.BoundCSRName == csrName {
		return &mc, nil
	}
	return nil, fmt.Errorf("MemberCluster %q bootstrap not bound to CSR %q", clusterName, csrName)
}

// ensureClusterRBAC creates the long-lived ClusterRole + ClusterRoleBinding for the cluster user.
// The cluster user identity is the x509 CN: "kapro-cluster:<clusterName>".
// Grants: MemberCluster get/patch, and CSR create/get/watch for self-renewal.
func (r *CSRApprovalReconciler) ensureClusterRBAC(ctx context.Context, clusterName, clusterUser string) error {
	roleName := "kapro:cluster-controller:" + clusterName

	role := &rbacv1.ClusterRole{}
	if err := r.Get(ctx, types.NamespacedName{Name: roleName}, role); apierrors.IsNotFound(err) {
		role = &rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{
				Name:   roleName,
				Labels: map[string]string{"kapro.io/cluster": clusterName},
			},
			Rules: []rbacv1.PolicyRule{
				{
					APIGroups:     []string{"kapro.io"},
					Resources:     []string{"memberclusters"},
					ResourceNames: []string{clusterName},
					Verbs:         []string{"get", "update", "patch"},
				},
				{
					APIGroups:     []string{"kapro.io"},
					Resources:     []string{"memberclusters/status"},
					ResourceNames: []string{clusterName},
					Verbs:         []string{"update", "patch"},
				},
				// Self-renewal: cluster-controller submits its own renewal CSRs.
				{
					APIGroups: []string{"certificates.k8s.io"},
					Resources: []string{"certificatesigningrequests"},
					Verbs:     []string{"create", "get", "watch"},
				},
			},
		}
		if err := r.Create(ctx, role); err != nil {
			return fmt.Errorf("create cluster ClusterRole: %w", err)
		}
	} else if err != nil {
		return err
	}

	binding := &rbacv1.ClusterRoleBinding{}
	if err := r.Get(ctx, types.NamespacedName{Name: roleName}, binding); apierrors.IsNotFound(err) {
		binding = &rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: roleName},
			RoleRef:    rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "ClusterRole", Name: roleName},
			Subjects: []rbacv1.Subject{
				// Subject is a User — the x509 CN becomes the username.
				{Kind: "User", Name: clusterUser},
			},
		}
		if err := r.Create(ctx, binding); err != nil {
			return fmt.Errorf("create cluster ClusterRoleBinding: %w", err)
		}
	} else if err != nil {
		return err
	}
	return nil
}

// markBootstrapUsed updates the MemberCluster status to mark bootstrap as used.
// Uses Status().Update() (full object with resourceVersion) — NOT Status().Patch() —
// so the resourceVersion check catches concurrent reconciles attempting double-claim.
func (r *CSRApprovalReconciler) markBootstrapUsed(ctx context.Context, mc *kaprov1alpha1.MemberCluster, clusterName, csrName string) error {
	now := metav1.Now()
	mc.Status.Bootstrap = &kaprov1alpha1.MemberClusterBootstrapStatus{
		Used:                true,
		UsedAt:              &now,
		IssuedCredentialFor: clusterName,
		BoundCSRName:        csrName,
	}
	return r.Status().Update(ctx, mc)
}

// approveCSR appends an Approved condition and calls the approval subresource.
func (r *CSRApprovalReconciler) approveCSR(ctx context.Context, csr *certificatesv1.CertificateSigningRequest) error {
	csr.Status.Conditions = append(csr.Status.Conditions, certificatesv1.CertificateSigningRequestCondition{
		Type:               certificatesv1.CertificateApproved,
		Status:             corev1.ConditionTrue,
		Reason:             "KaproApproved",
		Message:            "Approved by Kapro CSR approval controller",
		LastUpdateTime:     metav1.Now(),
	})
	// Must use typed client UpdateApproval — controller-runtime Status().Update() does not
	// route through the /approval subresource and will silently not approve the CSR.
	_, err := r.CertClient.CertificateSigningRequests().UpdateApproval(ctx, csr.Name, csr, metav1.UpdateOptions{})
	return err
}

// denyCSR appends a Denied condition and calls the approval subresource.
func (r *CSRApprovalReconciler) denyCSR(ctx context.Context, csr *certificatesv1.CertificateSigningRequest, reason string) (ctrl.Result, error) {
	return ctrl.Result{}, r.denyCSRErr(ctx, csr, reason)
}

func (r *CSRApprovalReconciler) denyCSRErr(ctx context.Context, csr *certificatesv1.CertificateSigningRequest, reason string) error {
	log := log.FromContext(ctx)
	log.Info("denying CSR", "csr", csr.Name, "reason", reason)
	r.Recorder.Eventf(csr, corev1.EventTypeWarning, "Denied", reason)
	csr.Status.Conditions = append(csr.Status.Conditions, certificatesv1.CertificateSigningRequestCondition{
		Type:               certificatesv1.CertificateDenied,
		Status:             corev1.ConditionTrue,
		Reason:             "KaproDenied",
		Message:            reason,
		LastUpdateTime:     metav1.Now(),
	})
	_, err := r.CertClient.CertificateSigningRequests().UpdateApproval(ctx, csr.Name, csr, metav1.UpdateOptions{})
	return err
}

// isKaproCSR returns true if this CSR should be handled by Kapro.
// Strict validation: signer, CN prefix, exact Organization, and usages.
func (r *CSRApprovalReconciler) isKaproCSR(csr *certificatesv1.CertificateSigningRequest) bool {
	if csr.Spec.SignerName != csrSigner {
		return false
	}
	csrReq, err := parseCSRRequest(csr.Spec.Request)
	if err != nil {
		return false
	}
	if !strings.HasPrefix(csrReq.Subject.CommonName, csrCNPrefix) {
		return false
	}
	// Organization must be exactly ["kapro:cluster-controllers"] — prevents O=system:masters.
	if len(csrReq.Subject.Organization) != 1 || csrReq.Subject.Organization[0] != csrOrganization {
		return false
	}
	// Usages must be exactly [client auth] — no server auth, no key encipherment beyond clientAuth.
	return hasOnlyClientAuthUsage(csr.Spec.Usages)
}

// clusterNameFromCN extracts cluster name from the CSR request CN (not the csr.Spec.Username).
func clusterNameFromCN(_ string, csr *certificatesv1.CertificateSigningRequest) (string, error) {
	csrReq, err := parseCSRRequest(csr.Spec.Request)
	if err != nil {
		return "", err
	}
	cn := csrReq.Subject.CommonName
	if !strings.HasPrefix(cn, csrCNPrefix) {
		return "", fmt.Errorf("CN %q does not start with %q", cn, csrCNPrefix)
	}
	name := strings.TrimPrefix(cn, csrCNPrefix)
	if name == "" {
		return "", fmt.Errorf("empty cluster name in CN %q", cn)
	}
	return name, nil
}

func parseCSRRequest(raw []byte) (*x509.CertificateRequest, error) {
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, fmt.Errorf("failed to decode PEM")
	}
	return x509.ParseCertificateRequest(block.Bytes)
}

func hasOnlyClientAuthUsage(usages []certificatesv1.KeyUsage) bool {
	if len(usages) == 0 {
		return false
	}
	for _, u := range usages {
		if u != certificatesv1.UsageClientAuth {
			return false
		}
	}
	return true
}

func isCSRApproved(csr *certificatesv1.CertificateSigningRequest) bool {
	for _, c := range csr.Status.Conditions {
		if c.Type == certificatesv1.CertificateApproved {
			return true
		}
	}
	return false
}

func isCSRDenied(csr *certificatesv1.CertificateSigningRequest) bool {
	for _, c := range csr.Status.Conditions {
		if c.Type == certificatesv1.CertificateDenied {
			return true
		}
	}
	return false
}

func (r *CSRApprovalReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&certificatesv1.CertificateSigningRequest{}).
		Complete(r)
}

// cleanupBootstrapCredentials deletes the bootstrap SA, its ClusterRole, and ClusterRoleBinding
// immediately after a bootstrap CSR is approved. This matches the OCM klusterlet pattern:
// once the cluster holds a valid x509 cert, the SA token in the bootstrap kubeconfig is useless,
// and deleting the SA revokes all outstanding tokens immediately.
// Errors are logged but non-fatal — MemberCluster cleanup will handle SA GC on token expiry.
func (r *CSRApprovalReconciler) cleanupBootstrapCredentials(ctx context.Context, clusterName string) {
	log := log.FromContext(ctx)
	saName := "kapro-bootstrap-" + clusterName
	roleName := "kapro:bootstrap:" + clusterName

	for _, fn := range []struct {
		name string
		obj  client.Object
	}{
		{"bootstrap SA", &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Namespace: kaproSystemNamespace, Name: saName}}},
		{"bootstrap ClusterRole", &rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: roleName}}},
		{"bootstrap ClusterRoleBinding", &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: roleName}}},
	} {
		if err := r.Delete(ctx, fn.obj); err != nil && !apierrors.IsNotFound(err) {
			log.Info("non-fatal: bootstrap credential cleanup failed", "resource", fn.name, "cluster", clusterName, "error", err)
		}
	}
}

// MemberClusterReconciler handles MemberCluster deletion: removes the long-lived
// cluster-controller ClusterRole and ClusterRoleBinding, then clears the finalizer.
// This ensures complete de-registration when a cluster is decommissioned.
//
// +kubebuilder:rbac:groups=kapro.io,resources=memberclusters,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=kapro.io,resources=memberclusters/finalizers,verbs=update
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterroles;clusterrolebindings,verbs=delete
type MemberClusterReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

func (r *MemberClusterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	mc := &kaprov1alpha1.MemberCluster{}
	if err := r.Get(ctx, req.NamespacedName, mc); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if mc.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	if !containsString(mc.Finalizers, kaprov1alpha1.MemberClusterFinalizer) {
		return ctrl.Result{}, nil
	}

	// Delete long-lived cluster-controller RBAC (created during bootstrap CSR approval).
	clusterName := mc.Name
	roleName := "kapro:cluster-controller:" + clusterName
	for _, obj := range []client.Object{
		&rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: roleName}},
		&rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: roleName}},
	} {
		if err := r.Delete(ctx, obj); err != nil && !apierrors.IsNotFound(err) {
			log.Error(err, "failed to delete cluster RBAC during deregistration", "cluster", clusterName, "resource", obj.GetName())
			return ctrl.Result{}, err
		}
	}

	// Remove finalizer to allow deletion to proceed.
	mc.Finalizers = removeString(mc.Finalizers, kaprov1alpha1.MemberClusterFinalizer)
	if err := r.Update(ctx, mc); err != nil {
		return ctrl.Result{}, err
	}

	log.Info("cluster deregistered — RBAC cleaned up", "cluster", clusterName)
	return ctrl.Result{}, nil
}

func (r *MemberClusterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kaprov1alpha1.MemberCluster{}).
		Complete(r)
}

func containsString(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}

func removeString(slice []string, s string) []string {
	out := make([]string, 0, len(slice))
	for _, v := range slice {
		if v != s {
			out = append(out, v)
		}
	}
	return out
}
