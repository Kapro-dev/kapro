package controller

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	authv1 "k8s.io/api/authentication/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
)

const (
	// kaproSystemNamespace is where operator-managed service accounts are created.
	kaproSystemNamespace = "kapro-system"
	// tokenExpirySeconds — issued SA tokens are valid for 1 hour; cluster-controller renews before expiry.
	tokenExpirySeconds = int64(3600)
)

// BootstrapTokenReconciler provisions per-cluster credentials from a BootstrapToken CR.
//
// Flow:
//  1. Developer runs `kapro cluster bootstrap --name <cluster>` → BootstrapToken CR created.
//  2. This controller sees the CR (status.used == false, not expired).
//  3. Creates a ServiceAccount + ClusterRole (scoped to one ClusterRegistration) + ClusterRoleBinding.
//  4. Issues a short-lived token via TokenRequest API.
//  5. Writes a kubeconfig-compatible Secret so the cluster-controller can authenticate back.
//  6. Marks BootstrapToken as used — one-time, no replay.
type BootstrapTokenReconciler struct {
	client.Client
}

// +kubebuilder:rbac:groups=kapro.io,resources=bootstraptokens,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=kapro.io,resources=bootstraptokens/status,verbs=update;patch
// +kubebuilder:rbac:groups=kapro.io,resources=clusterregistrations,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups="",resources=serviceaccounts,verbs=get;list;watch;create
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterroles;clusterrolebindings,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=authentication.k8s.io,resources=serviceaccounts/token,verbs=create

func (r *BootstrapTokenReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	var token kaprov1alpha1.BootstrapToken
	if err := r.Get(ctx, req.NamespacedName, &token); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Already used — nothing to do.
	if token.Status.Used {
		return ctrl.Result{}, nil
	}

	// Expired — mark as used to prevent lingering tokens.
	if time.Now().After(token.Spec.ExpiresAt.Time) {
		log.Info("BootstrapToken expired, marking used", "name", token.Name)
		return r.markUsed(ctx, &token, "")
	}

	clusterName := token.Spec.ClusterName
	saName := "kapro-cluster-" + clusterName

	// Ensure ClusterRegistration placeholder exists so the new cluster can write to it.
	if err := r.ensureClusterRegistration(ctx, clusterName, token.Spec.Labels); err != nil {
		return ctrl.Result{}, fmt.Errorf("ensure ClusterRegistration: %w", err)
	}

	// Ensure ServiceAccount.
	if err := r.ensureServiceAccount(ctx, saName, clusterName); err != nil {
		return ctrl.Result{}, fmt.Errorf("ensure SA: %w", err)
	}

	// Ensure ClusterRole — only allows updating its own ClusterRegistration.
	roleName := "kapro:cluster-controller:" + clusterName
	if err := r.ensureClusterRole(ctx, roleName, clusterName); err != nil {
		return ctrl.Result{}, fmt.Errorf("ensure ClusterRole: %w", err)
	}

	// Ensure ClusterRoleBinding.
	if err := r.ensureClusterRoleBinding(ctx, roleName, saName); err != nil {
		return ctrl.Result{}, fmt.Errorf("ensure ClusterRoleBinding: %w", err)
	}

	// Issue token via TokenRequest API.
	issuedToken, err := r.issueToken(ctx, saName)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("issue token: %w", err)
	}

	// Write credentials Secret — cluster-controller mounts this to authenticate back.
	secretName := "kapro-cluster-" + clusterName + "-credentials"
	if err := r.ensureCredentialsSecret(ctx, secretName, clusterName, issuedToken); err != nil {
		return ctrl.Result{}, fmt.Errorf("ensure credentials secret: %w", err)
	}

	log.Info("bootstrap complete",
		"cluster", clusterName,
		"serviceAccount", saName,
		"credentialsSecret", secretName,
	)

	return r.markUsed(ctx, &token, saName)
}

func (r *BootstrapTokenReconciler) ensureClusterRegistration(ctx context.Context, clusterName string, labels map[string]string) error {
	reg := &kaprov1alpha1.ClusterRegistration{}
	err := r.Get(ctx, types.NamespacedName{Name: clusterName}, reg)
	if err == nil {
		return nil // already exists
	}
	if !apierrors.IsNotFound(err) {
		return err
	}

	reg = &kaprov1alpha1.ClusterRegistration{
		ObjectMeta: metav1.ObjectMeta{
			Name:   clusterName,
			Labels: labels,
		},
		Spec: kaprov1alpha1.ClusterRegistrationSpec{},
	}
	return r.Create(ctx, reg)
}

func (r *BootstrapTokenReconciler) ensureServiceAccount(ctx context.Context, saName, clusterName string) error {
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
			Labels: map[string]string{
				"kapro.io/cluster": clusterName,
			},
		},
	}
	return r.Create(ctx, sa)
}

func (r *BootstrapTokenReconciler) ensureClusterRole(ctx context.Context, roleName, clusterName string) error {
	role := &rbacv1.ClusterRole{}
	err := r.Get(ctx, types.NamespacedName{Name: roleName}, role)
	if err == nil {
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	role = &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name: roleName,
			Labels: map[string]string{
				"kapro.io/cluster": clusterName,
			},
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups:     []string{"kapro.io"},
				Resources:     []string{"clusterregistrations"},
				ResourceNames: []string{clusterName}, // blast-radius isolation
				Verbs:         []string{"get", "update", "patch"},
			},
			{
				APIGroups:     []string{"kapro.io"},
				Resources:     []string{"clusterregistrations/status"},
				ResourceNames: []string{clusterName},
				Verbs:         []string{"update", "patch"},
			},
		},
	}
	return r.Create(ctx, role)
}

func (r *BootstrapTokenReconciler) ensureClusterRoleBinding(ctx context.Context, roleName, saName string) error {
	bindingName := roleName
	binding := &rbacv1.ClusterRoleBinding{}
	err := r.Get(ctx, types.NamespacedName{Name: bindingName}, binding)
	if err == nil {
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	binding = &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: bindingName,
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     roleName,
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      saName,
				Namespace: kaproSystemNamespace,
			},
		},
	}
	return r.Create(ctx, binding)
}

func (r *BootstrapTokenReconciler) issueToken(ctx context.Context, saName string) (string, error) {
	expiry := tokenExpirySeconds
	treq := &authv1.TokenRequest{
		Spec: authv1.TokenRequestSpec{
			ExpirationSeconds: &expiry,
		},
	}

	sa := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      saName,
			Namespace: kaproSystemNamespace,
		},
	}

	if err := r.SubResource("token").Create(ctx, sa, treq); err != nil {
		return "", err
	}
	return treq.Status.Token, nil
}

func (r *BootstrapTokenReconciler) ensureCredentialsSecret(ctx context.Context, secretName, clusterName, token string) error {
	secret := &corev1.Secret{}
	err := r.Get(ctx, types.NamespacedName{Namespace: kaproSystemNamespace, Name: secretName}, secret)
	if err == nil {
		// Update existing — token was refreshed.
		secret.Data["token"] = []byte(token)
		return r.Update(ctx, secret)
	}
	if !apierrors.IsNotFound(err) {
		return err
	}

	secret = &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: kaproSystemNamespace,
			Labels: map[string]string{
				"kapro.io/cluster":     clusterName,
				"kapro.io/secret-type": "cluster-credentials",
			},
			Annotations: map[string]string{
				"kapro.io/issued-at": time.Now().UTC().Format(time.RFC3339),
			},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			"cluster-name": []byte(clusterName),
			"token":        []byte(token),
		},
	}
	return r.Create(ctx, secret)
}

func (r *BootstrapTokenReconciler) markUsed(ctx context.Context, token *kaprov1alpha1.BootstrapToken, saName string) (ctrl.Result, error) {
	now := metav1.Now()
	token.Status.Used = true
	token.Status.UsedAt = &now
	if saName != "" {
		token.Status.IssuedCredentialFor = saName
	}
	if err := r.Status().Update(ctx, token); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *BootstrapTokenReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kaprov1alpha1.BootstrapToken{}).
		Complete(r)
}
