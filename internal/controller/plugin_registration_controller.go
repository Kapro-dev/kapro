package controller

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
	"kapro.io/kapro/internal/plugin/probe"
)

// PluginRegistrationReconciler probes external plugin registrations and records
// readiness status. It does not register plugins into release execution.
type PluginRegistrationReconciler struct {
	client.Client
	Recorder record.EventRecorder
	Prober   PluginProber
}

const pluginRegistrationMetricsFinalizer = "kapro.io/plugin-registration-metrics"

// PluginProber is the dependency used to probe plugin endpoints.
type PluginProber interface {
	Probe(ctx context.Context, reg kaprov1alpha1.PluginRegistration) probe.Result
}

// +kubebuilder:rbac:groups=kapro.io,resources=pluginregistrations,verbs=get;list;watch
// +kubebuilder:rbac:groups=kapro.io,resources=pluginregistrations,verbs=update;patch
// +kubebuilder:rbac:groups=kapro.io,resources=pluginregistrations/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get

func (r *PluginRegistrationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	var reg kaprov1alpha1.PluginRegistration
	if err := r.Get(ctx, req.NamespacedName, &reg); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !reg.DeletionTimestamp.IsZero() {
		probe.ForgetReadiness(reg)
		if controllerutil.RemoveFinalizer(&reg, pluginRegistrationMetricsFinalizer) {
			if err := r.Update(ctx, &reg); err != nil {
				return ctrl.Result{}, fmt.Errorf("remove plugin registration metrics finalizer: %w", err)
			}
		}
		return ctrl.Result{}, nil
	}

	if controllerutil.AddFinalizer(&reg, pluginRegistrationMetricsFinalizer) {
		if err := r.Update(ctx, &reg); err != nil {
			return ctrl.Result{}, fmt.Errorf("add plugin registration metrics finalizer: %w", err)
		}
	}

	prober := r.Prober
	if prober == nil {
		prober = probe.Prober{Client: r.Client}
	}
	result := prober.Probe(ctx, reg)
	patch := client.MergeFrom(reg.DeepCopy())
	now := metav1.Now()
	reg.Status.ObservedGeneration = reg.Generation
	reg.Status.Ready = result.Ready
	reg.Status.Version = result.Version
	reg.Status.Capabilities = result.Capabilities
	if result.Ready {
		reg.Status.LastSeen = now.UTC().Format(time.RFC3339)
	}

	status := metav1.ConditionFalse
	if result.Ready {
		status = metav1.ConditionTrue
	}
	apimeta.SetStatusCondition(&reg.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             status,
		Reason:             result.Reason,
		Message:            result.Message,
		ObservedGeneration: reg.Generation,
		LastTransitionTime: now,
	})
	apimeta.SetStatusCondition(&reg.Status.Conditions, metav1.Condition{
		Type:               kaprov1alpha1.ConditionTypeReconciling,
		Status:             metav1.ConditionFalse,
		Reason:             result.Reason,
		Message:            "plugin registration probe completed",
		ObservedGeneration: reg.Generation,
		LastTransitionTime: now,
	})
	if result.Ready {
		apimeta.RemoveStatusCondition(&reg.Status.Conditions, kaprov1alpha1.ConditionTypeStalled)
	} else {
		apimeta.SetStatusCondition(&reg.Status.Conditions, metav1.Condition{
			Type:               kaprov1alpha1.ConditionTypeStalled,
			Status:             metav1.ConditionTrue,
			Reason:             result.Reason,
			Message:            result.Message,
			ObservedGeneration: reg.Generation,
			LastTransitionTime: now,
		})
	}

	if err := r.Status().Patch(ctx, &reg, patch); err != nil {
		return ctrl.Result{}, fmt.Errorf("patch plugin registration status: %w", err)
	}

	eventType := corev1.EventTypeWarning
	if result.Ready {
		eventType = corev1.EventTypeNormal
	}
	r.Recorder.Event(&reg, eventType, result.Reason, result.Message)
	log.Info("plugin registration probed", "name", reg.Name, "type", reg.Spec.Type, "ready", result.Ready, "reason", result.Reason)

	return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
}

func (r *PluginRegistrationReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kaprov1alpha1.PluginRegistration{}).
		Complete(r)
}
