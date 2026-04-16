package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
	"kapro.io/kapro/internal/registration"
)

const bootstrapTokenFinalizer = "kapro.io/bootstrap-token-finalizer"

// BootstrapTokenReconciler manages the lifecycle of BootstrapToken CRs.
// Credential issuance is handled by the registration.Server HTTP endpoint.
// This controller handles: finalizer, expiry cleanup, RBAC cleanup on delete.
type BootstrapTokenReconciler struct {
	client.Client
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups=kapro.io,resources=bootstraptokens,verbs=get;list;watch;update;patch;delete
// +kubebuilder:rbac:groups=kapro.io,resources=bootstraptokens/status,verbs=update;patch
// +kubebuilder:rbac:groups=kapro.io,resources=bootstraptokens/finalizers,verbs=update
// +kubebuilder:rbac:groups=kapro.io,resources=clusterregistrations,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups="",resources=serviceaccounts,verbs=get;list;watch;create;delete
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterroles;clusterrolebindings,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=authentication.k8s.io,resources=serviceaccounts/token,verbs=create

func (r *BootstrapTokenReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	var token kaprov1alpha1.BootstrapToken
	if err := r.Get(ctx, req.NamespacedName, &token); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	log.Info("reconciling BootstrapToken", "name", token.Name, "used", token.Status.Used)

	// Handle deletion — clean up owned RBAC resources.
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

	// Mark expired tokens — passive cleanup so they show as used in kubectl.
	if !token.Status.Used && isExpired(&token) {
		log.Info("BootstrapToken expired", "token", token.Name)
		r.Recorder.Event(&token, corev1.EventTypeWarning, "Expired", "BootstrapToken expired without being used")
		now := metav1.Now()
		token.Status.Used = true
		token.Status.UsedAt = &now
		if err := r.Status().Update(ctx, &token); err != nil {
			return ctrl.Result{}, fmt.Errorf("mark expired: %w", err)
		}
	}

	return ctrl.Result{}, nil
}

// handleDeletion cleans up the ServiceAccount, ClusterRole, and ClusterRoleBinding
// created for this cluster when the BootstrapToken was consumed.
func (r *BootstrapTokenReconciler) handleDeletion(ctx context.Context, token *kaprov1alpha1.BootstrapToken) (ctrl.Result, error) {
	log := log.FromContext(ctx)
	clusterName := token.Spec.ClusterName

	if err := r.deleteServiceAccount(ctx, "kapro-cluster-"+clusterName); err != nil {
		return ctrl.Result{}, fmt.Errorf("delete SA: %w", err)
	}
	roleName := "kapro:cluster-controller:" + clusterName
	if err := r.deleteClusterRole(ctx, roleName); err != nil {
		return ctrl.Result{}, fmt.Errorf("delete ClusterRole: %w", err)
	}
	if err := r.deleteClusterRoleBinding(ctx, roleName); err != nil {
		return ctrl.Result{}, fmt.Errorf("delete ClusterRoleBinding: %w", err)
	}

	controllerutil.RemoveFinalizer(token, bootstrapTokenFinalizer)
	if err := r.Update(ctx, token); err != nil {
		return ctrl.Result{}, fmt.Errorf("remove finalizer: %w", err)
	}

	log.Info("BootstrapToken deleted, RBAC cleaned up", "cluster", clusterName)
	r.Recorder.Event(token, corev1.EventTypeNormal, "Deleted", "RBAC resources cleaned up for cluster "+clusterName)
	return ctrl.Result{}, nil
}

func (r *BootstrapTokenReconciler) deleteServiceAccount(ctx context.Context, saName string) error {
	sa := &corev1.ServiceAccount{}
	err := r.Get(ctx, types.NamespacedName{Namespace: registration.KaproSystemNamespace, Name: saName}, sa)
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

func isExpired(token *kaprov1alpha1.BootstrapToken) bool {
	return metav1.Now().After(token.Spec.ExpiresAt.Time)
}

func (r *BootstrapTokenReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kaprov1alpha1.BootstrapToken{}).
		Complete(r)
}
