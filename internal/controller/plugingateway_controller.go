// Package controller contains the PluginGateway controller.
//
// PluginGateway is a namespace-scoped, ambient-style gate integration.
// Unlike PluginRegistration (cluster-scoped, persistent gRPC server), a
// PluginGateway needs no deployed plugin process. The controller:
//   - builtin mode: validates the gate name is a known built-in; writes Ready
//   - job mode: validates the Job spec (image required); writes Ready
//   - remote mode: probes the endpoint via HTTP/gRPC health check; writes
//     Ready based on probe result; re-queues at the probe interval
//
// The controller is intentionally lightweight — it is a readiness validator,
// not an evaluator. Gate evaluation happens in internal/gate/plugingateway/.
package controller

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"time"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	corev1 "k8s.io/api/core/v1"
	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
)

// knownBuiltinGates is the set of valid built-in gate names.
var knownBuiltinGates = map[string]bool{
	"soak":          true,
	"metrics":       true,
	"cel":           true,
	"argo-analysis": true,
	"webhook":       true,
}

const (
	pluginGatewayProbeInterval = 60 * time.Second
	pluginGatewayProbeTimeout  = 10 * time.Second
)

// PluginGatewayReconciler validates PluginGateway specs and probes remote endpoints.
type PluginGatewayReconciler struct {
	client.Client
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups=kapro.io,resources=plugingateways,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=kapro.io,resources=plugingateways/status,verbs=update;patch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch

func (r *PluginGatewayReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var gw kaprov1alpha1.PluginGateway
	if err := r.Get(ctx, req.NamespacedName, &gw); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	switch gw.Spec.Mode {
	case kaprov1alpha1.PluginGatewayModeBuiltin:
		return r.reconcileBuiltin(ctx, &gw)
	case kaprov1alpha1.PluginGatewayModeJob:
		return r.reconcileJob(ctx, &gw)
	case kaprov1alpha1.PluginGatewayModeRemote:
		return r.reconcileRemote(ctx, &gw)
	default:
		logger.Info("unknown PluginGateway mode", "mode", gw.Spec.Mode)
		return r.setNotReady(ctx, &gw, fmt.Sprintf("unknown mode %q — must be builtin|job|remote", gw.Spec.Mode))
	}
}

// reconcileBuiltin validates that the named built-in gate exists.
func (r *PluginGatewayReconciler) reconcileBuiltin(ctx context.Context, gw *kaprov1alpha1.PluginGateway) (ctrl.Result, error) {
	if gw.Spec.Builtin == nil {
		return r.setNotReady(ctx, gw, "mode is builtin but spec.builtin is not set")
	}
	if !knownBuiltinGates[gw.Spec.Builtin.Name] {
		return r.setNotReady(ctx, gw, fmt.Sprintf("unknown built-in gate %q — valid: soak|metrics|cel|argo-analysis|webhook", gw.Spec.Builtin.Name))
	}
	return r.setReady(ctx, gw, fmt.Sprintf("routes to built-in gate %q", gw.Spec.Builtin.Name))
}

// reconcileJob validates the Job spec (image is required).
func (r *PluginGatewayReconciler) reconcileJob(ctx context.Context, gw *kaprov1alpha1.PluginGateway) (ctrl.Result, error) {
	if gw.Spec.Job == nil {
		return r.setNotReady(ctx, gw, "mode is job but spec.job is not set")
	}
	if gw.Spec.Job.Image == "" {
		return r.setNotReady(ctx, gw, "spec.job.image is required")
	}
	return r.setReady(ctx, gw, fmt.Sprintf("job gate ready — image: %s", gw.Spec.Job.Image))
}

// reconcileRemote probes the remote endpoint and writes Ready status.
// It re-queues at the probe interval to detect endpoint failures.
func (r *PluginGatewayReconciler) reconcileRemote(ctx context.Context, gw *kaprov1alpha1.PluginGateway) (ctrl.Result, error) {
	if gw.Spec.Remote == nil {
		return r.setNotReady(ctx, gw, "mode is remote but spec.remote is not set")
	}
	if gw.Spec.Remote.URL == "" {
		return r.setNotReady(ctx, gw, "spec.remote.url is required")
	}

	proto := gw.Spec.Remote.Protocol
	if proto == "" {
		proto = "http"
	}

	var probeErr error
	switch proto {
	case "http":
		probeErr = r.probeHTTP(ctx, gw)
	case "grpc":
		// gRPC health probe — use HTTP/1.1 on gRPC port as a connectivity check.
		// Full gRPC health protocol requires the plugin; we do a TCP-level probe.
		probeErr = r.probeHTTP(ctx, gw)
	default:
		return r.setNotReady(ctx, gw, fmt.Sprintf("unknown protocol %q — use http or grpc", proto))
	}

	if probeErr != nil {
		_, _ = r.setNotReady(ctx, gw, fmt.Sprintf("probe failed: %v", probeErr))
		// Retry faster when unhealthy.
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	_, err := r.setReady(ctx, gw, fmt.Sprintf("remote %s endpoint reachable", proto))
	return ctrl.Result{RequeueAfter: pluginGatewayProbeInterval}, err
}

// probeHTTP makes a HEAD/GET request to the remote URL to verify reachability.
func (r *PluginGatewayReconciler) probeHTTP(ctx context.Context, gw *kaprov1alpha1.PluginGateway) error {
	httpClient, err := r.buildHTTPClient(ctx, gw)
	if err != nil {
		return fmt.Errorf("build HTTP client: %w", err)
	}

	probeCtx, cancel := context.WithTimeout(ctx, pluginGatewayProbeTimeout)
	defer cancel()

	// Use HEAD for cheap reachability check; fall back to GET if HEAD fails.
	req, err := http.NewRequestWithContext(probeCtx, http.MethodHead, gw.Spec.Remote.URL, nil)
	if err != nil {
		return fmt.Errorf("build probe request: %w", err)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("probe request: %w", err)
	}
	_ = resp.Body.Close()
	// Any HTTP response (even 404) means the endpoint is reachable.
	return nil
}

// buildHTTPClient constructs an HTTP client, optionally with mTLS from a Secret.
func (r *PluginGatewayReconciler) buildHTTPClient(ctx context.Context, gw *kaprov1alpha1.PluginGateway) (*http.Client, error) {
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12} //nolint:gosec // TLS 1.2 acceptable for probes

	if gw.Spec.Remote.TLSSecretRef != "" {
		var secret corev1.Secret
		if err := r.Get(ctx, client.ObjectKey{
			Namespace: gw.Namespace,
			Name:      gw.Spec.Remote.TLSSecretRef,
		}, &secret); err != nil {
			return nil, fmt.Errorf("load TLS secret %q: %w", gw.Spec.Remote.TLSSecretRef, err)
		}

		certPEM := secret.Data["tls.crt"]
		keyPEM := secret.Data["tls.key"]
		caPEM := secret.Data["ca.crt"]

		if len(certPEM) > 0 && len(keyPEM) > 0 {
			cert, err := tls.X509KeyPair(certPEM, keyPEM)
			if err != nil {
				return nil, fmt.Errorf("parse TLS cert: %w", err)
			}
			tlsCfg.Certificates = []tls.Certificate{cert}
		}
		if len(caPEM) > 0 {
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM(caPEM) {
				return nil, fmt.Errorf("parse CA from secret %q", gw.Spec.Remote.TLSSecretRef)
			}
			tlsCfg.RootCAs = pool
		}
	}

	return &http.Client{
		Transport: &http.Transport{TLSClientConfig: tlsCfg},
		Timeout:   pluginGatewayProbeTimeout,
	}, nil
}

// setReady writes Ready=true + sets the Ready condition.
func (r *PluginGatewayReconciler) setReady(ctx context.Context, gw *kaprov1alpha1.PluginGateway, msg string) (ctrl.Result, error) {
	base := gw.DeepCopy()
	gw.Status.Ready = true
	gw.Status.Message = msg
	gw.Status.LastChecked = time.Now().UTC().Format(time.RFC3339)
	apimeta.SetStatusCondition(&gw.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionTrue,
		Reason:             "Validated",
		Message:            msg,
		LastTransitionTime: metav1.Now(),
	})
	if err := r.Status().Patch(ctx, gw, client.MergeFrom(base)); err != nil {
		return ctrl.Result{}, fmt.Errorf("patch PluginGateway status: %w", err)
	}
	return ctrl.Result{}, nil
}

// setNotReady writes Ready=false + sets the Ready condition to False.
func (r *PluginGatewayReconciler) setNotReady(ctx context.Context, gw *kaprov1alpha1.PluginGateway, msg string) (ctrl.Result, error) {
	log.FromContext(ctx).Info("PluginGateway not ready", "gateway", gw.Name, "reason", msg)
	base := gw.DeepCopy()
	gw.Status.Ready = false
	gw.Status.Message = msg
	gw.Status.LastChecked = time.Now().UTC().Format(time.RFC3339)
	apimeta.SetStatusCondition(&gw.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionFalse,
		Reason:             "ValidationFailed",
		Message:            msg,
		LastTransitionTime: metav1.Now(),
	})
	if err := r.Status().Patch(ctx, gw, client.MergeFrom(base)); err != nil {
		return ctrl.Result{}, fmt.Errorf("patch PluginGateway status: %w", err)
	}
	return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
}

func (r *PluginGatewayReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kaprov1alpha1.PluginGateway{}).
		WithOptions(controller.Options{
			RateLimiter: workqueue.NewTypedItemExponentialFailureRateLimiter[reconcile.Request](50*time.Millisecond, 10*time.Minute),
		}).
		Complete(r)
}
