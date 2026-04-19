package controller

import (
	"context"
	"fmt"
	"time"

	authv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
)

const bootstrapTokenFinalizer = "kapro.io/bootstrap-token-finalizer" //nolint:gosec // not a credential — it's a Kubernetes finalizer annotation key

// BootstrapTokenReconciler manages the lifecycle of BootstrapToken CRs.
//
// When a new, unused token is created the reconciler automatically provisions:
//   - A bootstrap ServiceAccount (kapro-bootstrap-<cluster>) so the spoke can
//     authenticate to the hub kube-apiserver before it has a client cert.
//   - A ClusterRole + ClusterRoleBinding granting that SA permission to create CSRs.
//   - A bootstrap kubeconfig Secret that bundles the SA token + hub CA + hub URL.
//     Operators copy this Secret to the spoke cluster for the first bootstrap.
//
// After bootstrap the CSRApprovalReconciler takes over, approves the CSR, and
// provisions the long-lived per-cluster RBAC (ManagedCluster get/patch/status).
type BootstrapTokenReconciler struct {
	client.Client
	Recorder record.EventRecorder
	// HubAPIURL is the externally-reachable kube-apiserver URL for this hub cluster.
	// Embedded in the bootstrap kubeconfig so spoke clusters can reach the hub.
	// Required in production; defaults to in-cluster host for local development.
	HubAPIURL string
	// HubCAData is the PEM-encoded CA certificate for the hub kube-apiserver.
	HubCAData []byte
}

// +kubebuilder:rbac:groups=kapro.io,resources=bootstraptokens,verbs=get;list;watch;update;patch;delete
// +kubebuilder:rbac:groups=kapro.io,resources=bootstraptokens/status,verbs=update;patch
// +kubebuilder:rbac:groups=kapro.io,resources=bootstraptokens/finalizers,verbs=update
// +kubebuilder:rbac:groups=kapro.io,resources=managedclusters,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups="",resources=serviceaccounts,verbs=get;list;watch;create;delete
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterroles;clusterrolebindings,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=authentication.k8s.io,resources=serviceaccounts/token,verbs=create
// +kubebuilder:rbac:groups=certificates.k8s.io,resources=certificatesigningrequests,verbs=create;get

func (r *BootstrapTokenReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	var token kaprov1alpha1.BootstrapToken
	if err := r.Get(ctx, req.NamespacedName, &token); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	log.Info("reconciling BootstrapToken", "name", token.Name, "used", token.Status.Used)

	// Handle deletion — clean up owned resources.
	if !token.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, &token)
	}

	// Ensure finalizer is present.
	if !controllerutil.ContainsFinalizer(&token, bootstrapTokenFinalizer) {
		controllerutil.AddFinalizer(&token, bootstrapTokenFinalizer)
		if err := r.Update(ctx, &token); err != nil {
			return ctrl.Result{}, fmt.Errorf("add finalizer: %w", err)
		}
		log.Info("added finalizer", "token", token.Name)
		return ctrl.Result{}, nil
	}

	// Mark expired tokens.
	if !token.Status.Used && isExpired(&token) {
		log.Info("BootstrapToken expired", "token", token.Name)
		r.Recorder.Event(&token, corev1.EventTypeWarning, "Expired", "BootstrapToken expired without being used")
		patch := client.MergeFrom(token.DeepCopy())
		now := metav1.Now()
		token.Status.Used = true
		token.Status.UsedAt = &now
		if err := r.Status().Patch(ctx, &token, patch); err != nil {
			return ctrl.Result{}, fmt.Errorf("mark expired: %w", err)
		}
		return ctrl.Result{}, nil
	}

	// For unused, non-expired tokens: ensure bootstrap prerequisites exist.
	if !token.Status.Used {
		if err := r.ensureBootstrapPrerequisites(ctx, &token); err != nil {
			return ctrl.Result{}, err
		}
	}

	return ctrl.Result{}, nil
}

// ensureBootstrapPrerequisites creates the bootstrap SA, its CSR-creation role,
// and the bootstrap kubeconfig Secret so the spoke can submit its first CSR.
func (r *BootstrapTokenReconciler) ensureBootstrapPrerequisites(ctx context.Context, token *kaprov1alpha1.BootstrapToken) error {
	log := log.FromContext(ctx)
	clusterName := token.Spec.ClusterName
	saName := "kapro-bootstrap-" + clusterName
	roleName := "kapro:bootstrap:" + clusterName
	kubeconfigSecretName := "kapro-bootstrap-kubeconfig-" + clusterName

	if err := r.ensureBootstrapSA(ctx, saName, clusterName); err != nil {
		return fmt.Errorf("ensure bootstrap SA: %w", err)
	}
	if err := r.ensureBootstrapCSRRole(ctx, roleName, clusterName); err != nil {
		return fmt.Errorf("ensure bootstrap CSR role: %w", err)
	}
	if err := r.ensureBootstrapCSRBinding(ctx, roleName, saName); err != nil {
		return fmt.Errorf("ensure bootstrap CSR binding: %w", err)
	}

	// Only create bootstrap kubeconfig Secret once.
	if token.Status.IssuedBootstrapKubeconfig == "" {
		if err := r.createBootstrapKubeconfig(ctx, token, saName, kubeconfigSecretName); err != nil {
			return fmt.Errorf("create bootstrap kubeconfig: %w", err)
		}
		patch := client.MergeFrom(token.DeepCopy())
		token.Status.IssuedBootstrapKubeconfig = kubeconfigSecretName
		if err := r.Status().Patch(ctx, token, patch); err != nil {
			return fmt.Errorf("update status.issuedBootstrapKubeconfig: %w", err)
		}
		log.Info("bootstrap kubeconfig created", "cluster", clusterName, "secret", kubeconfigSecretName)
		r.Recorder.Eventf(token, corev1.EventTypeNormal, "BootstrapReady",
			"Bootstrap kubeconfig Secret %s/%s ready; copy to spoke cluster to register", kaproSystemNamespace, kubeconfigSecretName)
	}

	return nil
}

func (r *BootstrapTokenReconciler) ensureBootstrapSA(ctx context.Context, saName, clusterName string) error {
	sa := &corev1.ServiceAccount{}
	err := r.Get(ctx, types.NamespacedName{Namespace: kaproSystemNamespace, Name: saName}, sa)
	if err == nil {
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	sa = &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      saName,
			Namespace: kaproSystemNamespace,
			Labels:    map[string]string{"kapro.io/cluster": clusterName, "kapro.io/role": "bootstrap"},
		},
	}
	return r.Create(ctx, sa)
}

func (r *BootstrapTokenReconciler) ensureBootstrapCSRRole(ctx context.Context, roleName, clusterName string) error {
	role := &rbacv1.ClusterRole{}
	if err := r.Get(ctx, types.NamespacedName{Name: roleName}, role); err == nil {
		return nil
	} else if !apierrors.IsNotFound(err) {
		return err
	}
	role = &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name:   roleName,
			Labels: map[string]string{"kapro.io/cluster": clusterName, "kapro.io/role": "bootstrap"},
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{"certificates.k8s.io"},
				Resources: []string{"certificatesigningrequests"},
				Verbs:     []string{"create", "get", "watch"},
			},
		},
	}
	return r.Create(ctx, role)
}

func (r *BootstrapTokenReconciler) ensureBootstrapCSRBinding(ctx context.Context, roleName, saName string) error {
	binding := &rbacv1.ClusterRoleBinding{}
	if err := r.Get(ctx, types.NamespacedName{Name: roleName}, binding); err == nil {
		return nil
	} else if !apierrors.IsNotFound(err) {
		return err
	}
	binding = &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: roleName},
		RoleRef:    rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "ClusterRole", Name: roleName},
		Subjects: []rbacv1.Subject{
			{Kind: "ServiceAccount", Name: saName, Namespace: kaproSystemNamespace},
		},
	}
	return r.Create(ctx, binding)
}

// createBootstrapKubeconfig issues a short-lived SA token and packages it with
// the hub CA and URL into a kubeconfig YAML stored as a K8s Secret.
// The token lifetime matches the BootstrapToken expiry.
func (r *BootstrapTokenReconciler) createBootstrapKubeconfig(ctx context.Context, token *kaprov1alpha1.BootstrapToken, saName, secretName string) error {
	expirySecs := int64(time.Until(token.Spec.ExpiresAt.Time).Seconds())
	if expirySecs <= 0 {
		return fmt.Errorf("BootstrapToken already expired")
	}

	treq := &authv1.TokenRequest{
		Spec: authv1.TokenRequestSpec{ExpirationSeconds: &expirySecs},
	}
	sa := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{Name: saName, Namespace: kaproSystemNamespace},
	}
	if err := r.Client.SubResource("token").Create(ctx, sa, treq); err != nil {
		return fmt.Errorf("issue SA token: %w", err)
	}

	kubeconfigBytes, err := buildKubeconfigYAML(r.HubAPIURL, r.HubCAData, saName, treq.Status.Token)
	if err != nil {
		return fmt.Errorf("build kubeconfig: %w", err)
	}

	secret := &corev1.Secret{}
	err = r.Get(ctx, types.NamespacedName{Namespace: kaproSystemNamespace, Name: secretName}, secret)
	if err == nil {
		return nil // already exists
	}
	if !apierrors.IsNotFound(err) {
		return err
	}

	secret = &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: kaproSystemNamespace,
			Labels: map[string]string{
				"kapro.io/cluster": token.Spec.ClusterName,
				"kapro.io/role":    "bootstrap-kubeconfig",
			},
		},
		Data: map[string][]byte{"kubeconfig": kubeconfigBytes},
	}
	return r.Create(ctx, secret)
}

// buildKubeconfigYAML constructs a kubeconfig YAML embedding the hub URL, CA, and SA token.
func buildKubeconfigYAML(hubURL string, hubCAData []byte, saName, saToken string) ([]byte, error) {
	cfg := clientcmdapi.NewConfig()
	cfg.Clusters["hub"] = &clientcmdapi.Cluster{
		Server:                   hubURL,
		CertificateAuthorityData: hubCAData,
	}
	cfg.AuthInfos[saName] = &clientcmdapi.AuthInfo{Token: saToken}
	cfg.Contexts["default"] = &clientcmdapi.Context{
		Cluster:  "hub",
		AuthInfo: saName,
	}
	cfg.CurrentContext = "default"
	return clientcmd.Write(*cfg)
}

// handleDeletion cleans up all resources owned by this BootstrapToken.
func (r *BootstrapTokenReconciler) handleDeletion(ctx context.Context, token *kaprov1alpha1.BootstrapToken) (ctrl.Result, error) {
	log := log.FromContext(ctx)
	clusterName := token.Spec.ClusterName

	// Clean up bootstrap-phase resources only.
	// Long-lived cluster identity (ClusterRole + ClusterRoleBinding) must NOT be deleted here —
	// the spoke cluster-controller may still be running with a valid cert. Cluster de-registration
	// is driven by ManagedCluster deletion, not by cleanup of one-time bootstrap material.
	saName := "kapro-bootstrap-" + clusterName
	_ = r.deleteServiceAccount(ctx, saName)
	bootstrapRoleName := "kapro:bootstrap:" + clusterName
	_ = r.deleteClusterRole(ctx, bootstrapRoleName)
	_ = r.deleteClusterRoleBinding(ctx, bootstrapRoleName)
	_ = r.deleteSecret(ctx, "kapro-bootstrap-kubeconfig-"+clusterName)

	controllerutil.RemoveFinalizer(token, bootstrapTokenFinalizer)
	if err := r.Update(ctx, token); err != nil {
		return ctrl.Result{}, fmt.Errorf("remove finalizer: %w", err)
	}

	log.Info("BootstrapToken deleted, resources cleaned up", "cluster", clusterName)
	r.Recorder.Event(token, corev1.EventTypeNormal, "Deleted", "Bootstrap and cluster-controller resources cleaned up for cluster "+clusterName)
	return ctrl.Result{}, nil
}

func (r *BootstrapTokenReconciler) deleteServiceAccount(ctx context.Context, saName string) error {
	sa := &corev1.ServiceAccount{}
	err := r.Get(ctx, types.NamespacedName{Namespace: kaproSystemNamespace, Name: saName}, sa)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return r.Delete(ctx, sa)
}

func (r *BootstrapTokenReconciler) deleteClusterRole(ctx context.Context, roleName string) error {
	role := &rbacv1.ClusterRole{}
	err := r.Get(ctx, types.NamespacedName{Name: roleName}, role)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return r.Delete(ctx, role)
}

func (r *BootstrapTokenReconciler) deleteClusterRoleBinding(ctx context.Context, bindingName string) error {
	binding := &rbacv1.ClusterRoleBinding{}
	err := r.Get(ctx, types.NamespacedName{Name: bindingName}, binding)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return r.Delete(ctx, binding)
}

func (r *BootstrapTokenReconciler) deleteSecret(ctx context.Context, secretName string) error {
	secret := &corev1.Secret{}
	err := r.Get(ctx, types.NamespacedName{Namespace: kaproSystemNamespace, Name: secretName}, secret)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return r.Delete(ctx, secret)
}

func isExpired(token *kaprov1alpha1.BootstrapToken) bool {
	return metav1.Now().After(token.Spec.ExpiresAt.Time)
}

func (r *BootstrapTokenReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kaprov1alpha1.BootstrapToken{}).
		Complete(r)
}
