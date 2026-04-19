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
	// bootstrapSAPrefix is the prefix of bootstrap ServiceAccounts created by BootstrapTokenReconciler.
	bootstrapSAPrefix = "system:serviceaccount:" + kaproSystemNamespace + ":kapro-bootstrap-"
)

// CSRApprovalReconciler watches CertificateSigningRequests and automatically
// approves bootstrap and renewal requests from Kapro cluster-controllers.
//
// Bootstrap (first registration):
//  1. CSR CN = "kapro-cluster:<cluster>", O = ["kapro:cluster-controllers"]
//  2. Requester = bootstrap SA created by BootstrapTokenReconciler
//  3. Validates a non-expired, unused BootstrapToken for the cluster
//  4. Creates ManagedCluster + long-lived RBAC for the cluster identity
//  5. Marks BootstrapToken used BEFORE approving (prevents replay race)
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
// +kubebuilder:rbac:groups=kapro.io,resources=bootstraptokens,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=kapro.io,resources=bootstraptokens/status,verbs=update;patch
// +kubebuilder:rbac:groups=kapro.io,resources=managedclusters,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterroles;clusterrolebindings,verbs=get;list;watch;create;update;patch

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

	// Find and validate the BootstrapToken.
	bt, err := r.findValidBootstrapToken(ctx, clusterName)
	if err != nil {
		// Check for idempotent retry: token already used by THIS exact CSR (transient approval failure).
		if btUsed, retryErr := r.findUsedTokenForCSR(ctx, clusterName, csr.Name); retryErr == nil {
			log.Info("retrying CSR approval for previously-bound CSR", "csr", csr.Name, "cluster", clusterName)
			bt = btUsed
		} else {
			return r.denyCSRErr(ctx, csr, fmt.Sprintf("no valid BootstrapToken found for cluster %s: %v", clusterName, err))
		}
	}

	// Ensure the ManagedCluster exists.
	envRef := bt.Spec.EnvironmentRef
	if envRef == "" {
		envRef = clusterName
	}
	if err := r.ensureManagedCluster(ctx, clusterName, envRef); err != nil {
		return fmt.Errorf("ensure ManagedCluster: %w", err)
	}

	// Ensure long-lived RBAC (cluster user = CN of the x509 cert).
	clusterUser := csrCNPrefix + clusterName
	if err := r.ensureClusterRBAC(ctx, clusterName, clusterUser); err != nil {
		return fmt.Errorf("ensure cluster RBAC: %w", err)
	}

	// Mark token used (with BoundCSRName) BEFORE approving — prevents replay race.
	// BoundCSRName enables idempotent retry if approval fails transiently.
	if !bt.Status.Used {
		if err := r.markTokenUsed(ctx, bt, clusterName, csr.Name); err != nil {
			return fmt.Errorf("mark token used: %w", err)
		}
	}

	if err := r.approveCSR(ctx, csr); err != nil {
		return fmt.Errorf("approve CSR: %w", err)
	}

	log.Info("bootstrap CSR approved", "cluster", clusterName, "csr", csr.Name)
	r.Recorder.Eventf(csr, corev1.EventTypeNormal, "Approved", "Bootstrap CSR approved for cluster %s", clusterName)
	return nil
}

// handleRenewal processes a cert-rotation CSR from an already-registered cluster.
func (r *CSRApprovalReconciler) handleRenewal(ctx context.Context, csr *certificatesv1.CertificateSigningRequest, clusterName string) error {
	log := log.FromContext(ctx)

	// The ManagedCluster must already exist — no token needed.
	var mc kaprov1alpha1.ManagedCluster
	if err := r.Get(ctx, types.NamespacedName{Name: clusterName}, &mc); err != nil {
		return r.denyCSRErr(ctx, csr, fmt.Sprintf("ManagedCluster %s not found for renewal: %v", clusterName, err))
	}

	if err := r.approveCSR(ctx, csr); err != nil {
		return fmt.Errorf("approve renewal CSR: %w", err)
	}

	log.Info("renewal CSR approved", "cluster", clusterName, "csr", csr.Name)
	r.Recorder.Eventf(csr, corev1.EventTypeNormal, "Approved", "Renewal CSR approved for cluster %s", clusterName)
	return nil
}

// findValidBootstrapToken returns the first non-expired, unused BootstrapToken for clusterName.
func (r *CSRApprovalReconciler) findValidBootstrapToken(ctx context.Context, clusterName string) (*kaprov1alpha1.BootstrapToken, error) {
	var list kaprov1alpha1.BootstrapTokenList
	if err := r.List(ctx, &list, client.InNamespace(kaproSystemNamespace)); err != nil {
		return nil, err
	}
	for i := range list.Items {
		bt := &list.Items[i]
		if bt.Spec.ClusterName == clusterName && !bt.Status.Used && !isExpired(bt) {
			return bt, nil
		}
	}
	return nil, fmt.Errorf("no valid BootstrapToken found for cluster %q", clusterName)
}

// ensureManagedCluster creates the ManagedCluster if it doesn't exist.
func (r *CSRApprovalReconciler) ensureManagedCluster(ctx context.Context, clusterName, envRef string) error {
	mc := &kaprov1alpha1.ManagedCluster{}
	err := r.Get(ctx, types.NamespacedName{Name: clusterName}, mc)
	if err == nil {
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	mc = &kaprov1alpha1.ManagedCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name: clusterName,
			Labels: map[string]string{
				"kapro.io/cluster":     clusterName,
				"kapro.io/environment": envRef,
			},
		},
		Spec: kaprov1alpha1.ManagedClusterSpec{
			EnvironmentRef: envRef,
		},
	}
	return r.Create(ctx, mc)
}

// ensureClusterRBAC creates the long-lived ClusterRole + ClusterRoleBinding for the cluster user.
// The cluster user identity is the x509 CN: "kapro-cluster:<clusterName>".
// Grants: ManagedCluster get/patch, Environment list, and CSR create/get/watch for self-renewal.
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
					Resources:     []string{"managedclusters"},
					ResourceNames: []string{clusterName},
					Verbs:         []string{"get", "update", "patch"},
				},
				{
					APIGroups:     []string{"kapro.io"},
					Resources:     []string{"managedclusters/status"},
					ResourceNames: []string{clusterName},
					Verbs:         []string{"update", "patch"},
				},
				{
					APIGroups: []string{"kapro.io"},
					Resources: []string{"environments"},
					Verbs:     []string{"get", "list"},
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

// markTokenUsed patches the BootstrapToken status to used=true BEFORE approving.
// BoundCSRName is recorded so transient approval failures can be retried idempotently.
// This is intentional: approving first then marking used creates a replay window.
func (r *CSRApprovalReconciler) markTokenUsed(ctx context.Context, bt *kaprov1alpha1.BootstrapToken, clusterName, csrName string) error {
	patch := client.MergeFrom(bt.DeepCopy())
	now := metav1.Now()
	bt.Status.Used = true
	bt.Status.UsedAt = &now
	bt.Status.IssuedCredentialFor = clusterName
	bt.Status.BoundCSRName = csrName
	return r.Status().Patch(ctx, bt, patch)
}

// findUsedTokenForCSR finds a BootstrapToken that was already used by the given CSR.
// Used to enable idempotent retry when the CSR approval call fails transiently.
func (r *CSRApprovalReconciler) findUsedTokenForCSR(ctx context.Context, clusterName, csrName string) (*kaprov1alpha1.BootstrapToken, error) {
	var list kaprov1alpha1.BootstrapTokenList
	if err := r.List(ctx, &list, client.InNamespace(kaproSystemNamespace)); err != nil {
		return nil, err
	}
	for i := range list.Items {
		bt := &list.Items[i]
		if bt.Spec.ClusterName == clusterName && bt.Status.Used && bt.Status.BoundCSRName == csrName {
			return bt, nil
		}
	}
	return nil, fmt.Errorf("no token bound to CSR %q for cluster %q", csrName, clusterName)
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
