package controller

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"strings"
	"time"

	authenticationv1 "k8s.io/api/authentication/v1"
	certificatesv1 "k8s.io/api/certificates/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	certificatesv1client "k8s.io/client-go/kubernetes/typed/certificates/v1"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
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
	kaproManagedBy    = "kapro-operator"
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

	// Bootstrap credentials are intentionally left alive here.
	// cleanupBootstrapCredentials removes the SA token immediately, which races with the
	// cluster-controller polling the CSR for the signed certificate.
	// The bootstrap SA is scoped to create/get CSRs only, and used=true prevents replay.
	// Cleanup happens via cleanupBootstrapResources on MemberCluster deletion or token TTL expiry.

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
				Labels: managedResourceLabels(clusterName, "cluster-controller-rbac"),
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
			ObjectMeta: metav1.ObjectMeta{
				Name:   roleName,
				Labels: managedResourceLabels(clusterName, "cluster-controller-rbac"),
			},
			RoleRef: rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "ClusterRole", Name: roleName},
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
// Merges into the existing bootstrap status so IssuedBootstrapKubeconfig is preserved.
func (r *CSRApprovalReconciler) markBootstrapUsed(ctx context.Context, mc *kaprov1alpha1.MemberCluster, clusterName, csrName string) error {
	now := metav1.Now()
	if mc.Status.Bootstrap == nil {
		mc.Status.Bootstrap = &kaprov1alpha1.MemberClusterBootstrapStatus{}
	}
	mc.Status.Bootstrap.Used = true
	mc.Status.Bootstrap.UsedAt = &now
	mc.Status.Bootstrap.IssuedCredentialFor = clusterName
	mc.Status.Bootstrap.BoundCSRName = csrName
	mc.Status.ObservedGeneration = mc.Generation
	setMemberClusterCondition(mc, "BootstrapReady", metav1.ConditionFalse, "Consumed", "bootstrap credential has been consumed by a successful CSR")
	setMemberClusterCondition(mc, "BootstrapUsed", metav1.ConditionTrue, "CSRBound", fmt.Sprintf("bootstrap credential consumed by CSR %s", csrName))
	return r.Status().Update(ctx, mc)
}

// approveCSR appends an Approved condition and calls the approval subresource.
func (r *CSRApprovalReconciler) approveCSR(ctx context.Context, csr *certificatesv1.CertificateSigningRequest) error {
	csr.Status.Conditions = append(csr.Status.Conditions, certificatesv1.CertificateSigningRequestCondition{
		Type:           certificatesv1.CertificateApproved,
		Status:         corev1.ConditionTrue,
		Reason:         "KaproApproved",
		Message:        "Approved by Kapro CSR approval controller",
		LastUpdateTime: metav1.Now(),
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
		Type:           certificatesv1.CertificateDenied,
		Status:         corev1.ConditionTrue,
		Reason:         "KaproDenied",
		Message:        reason,
		LastUpdateTime: metav1.Now(),
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

// cleanupBootstrapCredentials deletes the bootstrap SA, its ClusterRole, ClusterRoleBinding,
// and kubeconfig Secret immediately after a bootstrap CSR is approved.
// This matches the OCM klusterlet pattern: once the cluster holds a valid x509 cert,
// the SA token in the bootstrap kubeconfig is useless, and deleting the SA revokes
// all outstanding tokens immediately.
// Errors are logged but non-fatal — MemberCluster cleanup will handle SA GC on token expiry.
func (r *CSRApprovalReconciler) cleanupBootstrapCredentials(ctx context.Context, clusterName string) {
	log := log.FromContext(ctx)
	saName := "kapro-bootstrap-" + clusterName
	roleName := "kapro:bootstrap:" + clusterName
	secretName := "kapro-bootstrap-kubeconfig-" + clusterName

	for _, fn := range []struct {
		name string
		obj  client.Object
	}{
		{"bootstrap SA", &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Namespace: kaproSystemNamespace, Name: saName}}},
		{"bootstrap ClusterRole", &rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: roleName}}},
		{"bootstrap ClusterRoleBinding", &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: roleName}}},
		{"bootstrap kubeconfig Secret", &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: kaproSystemNamespace, Name: secretName}}},
	} {
		if err := r.Delete(ctx, fn.obj); err != nil && !apierrors.IsNotFound(err) {
			log.Info("non-fatal: bootstrap credential cleanup failed", "resource", fn.name, "cluster", clusterName, "error", err)
		}
	}
}

// MemberClusterReconciler handles two duties:
//  1. Bootstrap provisioning: when spec.bootstrap.tokenHash is set, creates a scoped
//     ServiceAccount, ClusterRole, ClusterRoleBinding, and kubeconfig Secret so the
//     cluster-controller can submit its first CSR to the hub (KAPRO_BOOTSTRAP_KUBECONFIG_PATH).
//  2. Deregistration: on deletion, removes the long-lived cluster-controller ClusterRole and
//     ClusterRoleBinding, then clears the finalizer.
//
// +kubebuilder:rbac:groups=kapro.io,resources=memberclusters,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=kapro.io,resources=memberclusters/status,verbs=update;patch
// +kubebuilder:rbac:groups=kapro.io,resources=memberclusters/finalizers,verbs=update
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterroles;clusterrolebindings,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=serviceaccounts,verbs=get;list;watch;create;delete
// +kubebuilder:rbac:groups="",resources=serviceaccounts/token,verbs=create
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch;delete
type MemberClusterReconciler struct {
	client.Client
	Scheme     *runtime.Scheme
	KubeClient kubernetes.Interface
	// HubAPIURL is the externally-reachable URL of the hub kube-apiserver.
	// Written into bootstrap kubeconfigs so spoke cluster-controllers can connect.
	// Set from KAPRO_HUB_API_URL; must be reachable from within the spoke cluster network.
	HubAPIURL string
	// HubCAData is the PEM-encoded CA certificate for the hub kube-apiserver.
	HubCAData []byte
}

func (r *MemberClusterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	mc := &kaprov1alpha1.MemberCluster{}
	if err := r.Get(ctx, req.NamespacedName, mc); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !mc.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, mc)
	}

	// Honor spec.suspend — skip all reconciliation for this cluster.
	if mc.Spec.Suspend {
		log.FromContext(ctx).Info("MemberCluster is suspended — skipping reconciliation", "cluster", mc.Name)
		patch := client.MergeFrom(mc.DeepCopy())
		mc.Status.ObservedGeneration = mc.Generation
		setMemberClusterCondition(mc, kaprov1alpha1.ConditionTypeReconciling, metav1.ConditionFalse, "Suspended", "cluster is suspended")
		apimeta.RemoveStatusCondition(&mc.Status.Conditions, kaprov1alpha1.ConditionTypeStalled)
		if err := r.Status().Patch(ctx, mc, patch); err != nil {
			return ctrl.Result{}, fmt.Errorf("patch suspended conditions: %w", err)
		}
		return ctrl.Result{}, nil
	}

	if mc.Spec.Bootstrap != nil && mc.Spec.Bootstrap.TokenHash != "" {
		return r.ensureBootstrapProvisioned(ctx, mc)
	}

	return ctrl.Result{}, nil
}

// handleDeletion removes long-lived cluster-controller RBAC and any remaining bootstrap
// resources, then clears the finalizer to allow Kubernetes to delete the object.
func (r *MemberClusterReconciler) handleDeletion(ctx context.Context, mc *kaprov1alpha1.MemberCluster) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	if !containsString(mc.Finalizers, kaprov1alpha1.MemberClusterFinalizer) {
		return ctrl.Result{}, nil
	}

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

	// Also clean up any bootstrap resources that were not yet consumed by a CSR.
	r.cleanupBootstrapResources(ctx, clusterName)

	mc.Finalizers = removeString(mc.Finalizers, kaprov1alpha1.MemberClusterFinalizer)
	if err := r.Update(ctx, mc); err != nil {
		return ctrl.Result{}, err
	}

	log.Info("cluster deregistered — RBAC cleaned up", "cluster", clusterName)
	return ctrl.Result{}, nil
}

// ensureBootstrapProvisioned creates the bootstrap SA, RBAC, and kubeconfig Secret
// so that the spoke cluster-controller can submit its first registration CSR.
// It is idempotent: a second reconcile after the kubeconfig Secret exists is a no-op.
func (r *MemberClusterReconciler) ensureBootstrapProvisioned(ctx context.Context, mc *kaprov1alpha1.MemberCluster) (ctrl.Result, error) {
	log := log.FromContext(ctx)
	clusterName := mc.Name

	// Add finalizer before creating any resources so deletion always triggers cleanup.
	if !containsString(mc.Finalizers, kaprov1alpha1.MemberClusterFinalizer) {
		mc.Finalizers = append(mc.Finalizers, kaprov1alpha1.MemberClusterFinalizer)
		if err := r.Update(ctx, mc); err != nil {
			return ctrl.Result{}, err
		}
		// Re-fetch after update to get the latest resourceVersion.
		return ctrl.Result{Requeue: true}, nil
	}

	// Idempotent guard: kubeconfig secret already provisioned.
	if mc.Status.Bootstrap != nil && mc.Status.Bootstrap.IssuedBootstrapKubeconfig != "" {
		patch := client.MergeFrom(mc.DeepCopy())
		mc.Status.ObservedGeneration = mc.Generation
		setMemberClusterCondition(mc, "BootstrapReady", metav1.ConditionTrue, "CredentialsIssued", "bootstrap credential is ready for first registration")
		setMemberClusterCondition(mc, kaprov1alpha1.ConditionTypeReconciling, metav1.ConditionFalse, "BootstrapComplete", "bootstrap provisioning complete")
		apimeta.RemoveStatusCondition(&mc.Status.Conditions, kaprov1alpha1.ConditionTypeStalled)
		if err := r.Status().Patch(ctx, mc, patch); err != nil {
			return ctrl.Result{}, fmt.Errorf("patch MemberCluster bootstrap ready condition: %w", err)
		}
		return ctrl.Result{}, nil
	}

	// Don't provision if the bootstrap slot has already expired.
	if mc.Spec.Bootstrap.ExpiresAt != nil && metav1.Now().After(mc.Spec.Bootstrap.ExpiresAt.Time) {
		log.Info("bootstrap slot expired — skipping provisioning", "cluster", clusterName, "expiredAt", mc.Spec.Bootstrap.ExpiresAt)
		patch := client.MergeFrom(mc.DeepCopy())
		mc.Status.ObservedGeneration = mc.Generation
		setMemberClusterCondition(mc, "BootstrapReady", metav1.ConditionFalse, "Expired", "bootstrap credential expired before provisioning")
		setMemberClusterCondition(mc, kaprov1alpha1.ConditionTypeStalled, metav1.ConditionTrue, "BootstrapExpired", "bootstrap slot expired — update spec.bootstrap.expiresAt to retry")
		setMemberClusterCondition(mc, kaprov1alpha1.ConditionTypeReconciling, metav1.ConditionFalse, "BootstrapExpired", "stalled: bootstrap expired")
		if err := r.Status().Patch(ctx, mc, patch); err != nil {
			return ctrl.Result{}, fmt.Errorf("patch expired bootstrap condition: %w", err)
		}
		return ctrl.Result{}, nil
	}

	if r.HubAPIURL == "" {
		return ctrl.Result{}, fmt.Errorf("KAPRO_HUB_API_URL is not set — cannot build bootstrap kubeconfig for cluster %q", clusterName)
	}

	saName := "kapro-bootstrap-" + clusterName
	roleName := "kapro:bootstrap:" + clusterName

	// Create bootstrap ServiceAccount.
	sa := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      saName,
			Namespace: kaproSystemNamespace,
			Labels:    managedResourceLabels(clusterName, "bootstrap"),
		},
	}
	if err := r.Create(ctx, sa); err != nil && !apierrors.IsAlreadyExists(err) {
		return ctrl.Result{}, fmt.Errorf("create bootstrap SA %q: %w", saName, err)
	}

	// Create bootstrap ClusterRole — only allows submitting CSRs (one-time bootstrap action).
	role := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name:   roleName,
			Labels: managedResourceLabels(clusterName, "bootstrap"),
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{"certificates.k8s.io"},
				Resources: []string{"certificatesigningrequests"},
				Verbs:     []string{"create", "get", "watch"},
			},
		},
	}
	if err := r.Create(ctx, role); err != nil && !apierrors.IsAlreadyExists(err) {
		return ctrl.Result{}, fmt.Errorf("create bootstrap ClusterRole %q: %w", roleName, err)
	}

	// Create bootstrap ClusterRoleBinding.
	binding := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:   roleName,
			Labels: managedResourceLabels(clusterName, "bootstrap"),
		},
		RoleRef: rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "ClusterRole", Name: roleName},
		Subjects: []rbacv1.Subject{
			{Kind: "ServiceAccount", Name: saName, Namespace: kaproSystemNamespace},
		},
	}
	if err := r.Create(ctx, binding); err != nil && !apierrors.IsAlreadyExists(err) {
		return ctrl.Result{}, fmt.Errorf("create bootstrap ClusterRoleBinding %q: %w", roleName, err)
	}

	// Compute token TTL: use expiresAt if set, else default to 24h.
	// Cap to 48h and reject if less than 10 minutes remain (token would be useless).
	const defaultTTL = 24 * time.Hour
	const maxTTL = 48 * time.Hour
	const minTTL = 10 * time.Minute
	tokenTTL := defaultTTL
	if mc.Spec.Bootstrap.ExpiresAt != nil {
		remaining := time.Until(mc.Spec.Bootstrap.ExpiresAt.Time)
		if remaining < minTTL {
			return ctrl.Result{}, fmt.Errorf("bootstrap slot for %q expires in %v — too soon to issue kubeconfig token", clusterName, remaining.Round(time.Second))
		}
		if remaining < tokenTTL {
			tokenTTL = remaining
		}
	}
	if tokenTTL > maxTTL {
		tokenTTL = maxTTL
	}
	tokenTTLSeconds := int64(tokenTTL.Seconds())

	// Issue a TokenRequest for the bootstrap SA.
	tokenReq, err := r.KubeClient.CoreV1().ServiceAccounts(kaproSystemNamespace).CreateToken(
		ctx, saName,
		&authenticationv1.TokenRequest{
			Spec: authenticationv1.TokenRequestSpec{
				ExpirationSeconds: &tokenTTLSeconds,
			},
		},
		metav1.CreateOptions{},
	)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("create token for bootstrap SA %q: %w", saName, err)
	}

	// Build and write the bootstrap kubeconfig Secret.
	kubeconfigData, err := r.buildBootstrapKubeconfig(clusterName, tokenReq.Status.Token)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("build bootstrap kubeconfig for cluster %q: %w", clusterName, err)
	}

	secretName := "kapro-bootstrap-kubeconfig-" + clusterName
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: kaproSystemNamespace,
			Labels:    bootstrapSecretLabels(clusterName),
		},
		Data: map[string][]byte{"kubeconfig": kubeconfigData},
	}
	if err := r.Create(ctx, secret); apierrors.IsAlreadyExists(err) {
		existing := &corev1.Secret{}
		if err := r.Get(ctx, types.NamespacedName{Namespace: kaproSystemNamespace, Name: secretName}, existing); err != nil {
			return ctrl.Result{}, err
		}
		patch := client.MergeFrom(existing.DeepCopy())
		if existing.Data == nil {
			existing.Data = map[string][]byte{}
		}
		existing.Data["kubeconfig"] = kubeconfigData
		if err := r.Patch(ctx, existing, patch); err != nil {
			return ctrl.Result{}, fmt.Errorf("update bootstrap kubeconfig secret: %w", err)
		}
	} else if err != nil {
		return ctrl.Result{}, fmt.Errorf("create bootstrap kubeconfig secret: %w", err)
	}

	// Update status: merge, preserving any existing fields (e.g. Used/BoundCSRName from a retry).
	fresh := &kaprov1alpha1.MemberCluster{}
	if err := r.Get(ctx, types.NamespacedName{Name: clusterName}, fresh); err != nil {
		return ctrl.Result{}, err
	}
	if fresh.Status.Bootstrap == nil {
		fresh.Status.Bootstrap = &kaprov1alpha1.MemberClusterBootstrapStatus{}
	}
	fresh.Status.Bootstrap.IssuedBootstrapKubeconfig = secretName
	fresh.Status.ObservedGeneration = fresh.Generation
	setMemberClusterCondition(fresh, "BootstrapReady", metav1.ConditionTrue, "CredentialsIssued", fmt.Sprintf("bootstrap kubeconfig secret %s is ready", secretName))
	setMemberClusterCondition(fresh, kaprov1alpha1.ConditionTypeReconciling, metav1.ConditionFalse, "BootstrapComplete", "bootstrap provisioning complete")
	apimeta.RemoveStatusCondition(&fresh.Status.Conditions, kaprov1alpha1.ConditionTypeStalled)
	if err := r.Status().Update(ctx, fresh); err != nil {
		return ctrl.Result{}, fmt.Errorf("update MemberCluster bootstrap status: %w", err)
	}

	log.Info("bootstrap credentials provisioned", "cluster", clusterName, "secret", secretName)
	return ctrl.Result{}, nil
}

// buildBootstrapKubeconfig produces a kubeconfig YAML for the bootstrap SA token.
// The server URL must be reachable from within the spoke cluster network (KAPRO_HUB_API_URL).
func (r *MemberClusterReconciler) buildBootstrapKubeconfig(clusterName, token string) ([]byte, error) {
	cfg := clientcmdapi.NewConfig()
	cfg.Clusters[clusterName] = &clientcmdapi.Cluster{
		Server:                   r.HubAPIURL,
		CertificateAuthorityData: r.HubCAData,
		// Skip TLS only when no CA data is available — dev/test only.
		InsecureSkipTLSVerify: len(r.HubCAData) == 0,
	}
	cfg.AuthInfos["kapro-bootstrap-"+clusterName] = &clientcmdapi.AuthInfo{
		Token: token,
	}
	cfg.Contexts[clusterName] = &clientcmdapi.Context{
		Cluster:  clusterName,
		AuthInfo: "kapro-bootstrap-" + clusterName,
	}
	cfg.CurrentContext = clusterName
	return clientcmd.Write(*cfg)
}

// cleanupBootstrapResources removes the bootstrap SA, ClusterRole, ClusterRoleBinding,
// and kubeconfig Secret. Called on MemberCluster deletion if the bootstrap CSR was never
// submitted, or if the CSR approval controller has already cleaned up after success.
func (r *MemberClusterReconciler) cleanupBootstrapResources(ctx context.Context, clusterName string) {
	log := log.FromContext(ctx)
	saName := "kapro-bootstrap-" + clusterName
	roleName := "kapro:bootstrap:" + clusterName
	secretName := "kapro-bootstrap-kubeconfig-" + clusterName

	for _, fn := range []struct {
		name string
		obj  client.Object
	}{
		{"bootstrap SA", &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Namespace: kaproSystemNamespace, Name: saName}}},
		{"bootstrap ClusterRole", &rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: roleName}}},
		{"bootstrap ClusterRoleBinding", &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: roleName}}},
		{"bootstrap kubeconfig Secret", &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: kaproSystemNamespace, Name: secretName}}},
	} {
		if err := r.Delete(ctx, fn.obj); err != nil && !apierrors.IsNotFound(err) {
			log.Info("non-fatal: bootstrap resource cleanup failed", "resource", fn.name, "cluster", clusterName, "error", err)
		}
	}
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

func setMemberClusterCondition(mc *kaprov1alpha1.MemberCluster, conditionType string, status metav1.ConditionStatus, reason, message string) {
	apimeta.SetStatusCondition(&mc.Status.Conditions, metav1.Condition{
		Type:               conditionType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: mc.Generation,
		LastTransitionTime: metav1.Now(),
	})
}

func managedResourceLabels(clusterName, component string) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":       "kapro",
		"app.kubernetes.io/part-of":    "kapro",
		"app.kubernetes.io/component":  component,
		"app.kubernetes.io/managed-by": kaproManagedBy,
		"kapro.io/cluster":             clusterName,
		"kapro.io/managed-by":          kaproManagedBy,
	}
}

func bootstrapSecretLabels(clusterName string) map[string]string {
	labels := managedResourceLabels(clusterName, "bootstrap")
	labels["kapro.io/bootstrap-kubeconfig"] = "true"
	return labels
}
