package plugin

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
)

const (
	defaultDialTimeout  = 10 * time.Second
	defaultPingInterval = 30 * time.Second
)

// Reconciler watches PluginRegistration CRDs and manages gRPC connections.
//
// On each reconcile:
//  1. Parse the endpoint / socketPath from the spec
//  2. Dial gRPC (with optional mTLS)
//  3. Probe the gRPC health service
//  4. Register the connection in the Registry
//  5. Write Connected + LastPing back to status
//
// On deletion (finalizer removal): close and deregister the connection.
type Reconciler struct {
	client.Client
	Registry *Registry
}

// +kubebuilder:rbac:groups=kapro.io,resources=pluginregistrations,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=kapro.io,resources=pluginregistrations/status,verbs=update;patch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch

func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var pr kaprov1alpha1.PluginRegistration
	if err := r.Get(ctx, req.NamespacedName, &pr); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Handle deletion.
	if !pr.DeletionTimestamp.IsZero() {
		r.Registry.Deregister(pr.Name)
		logger.Info("plugin deregistered", "plugin", pr.Name)
		return ctrl.Result{}, nil
	}

	endpoint, err := r.resolveEndpoint(pr.Spec)
	if err != nil {
		return r.failStatus(ctx, &pr, fmt.Sprintf("endpoint error: %v", err))
	}

	dialOpts, err := r.dialOptions(ctx, pr.Spec)
	if err != nil {
		return r.failStatus(ctx, &pr, fmt.Sprintf("TLS config error: %v", err))
	}

	dialCtx, cancel := context.WithTimeout(ctx, defaultDialTimeout)
	defer cancel()

	conn, err := grpc.DialContext(dialCtx, endpoint, dialOpts...) //nolint:staticcheck // DialContext deprecated in grpc v1.63, but widely used
	if err != nil {
		return r.failStatus(ctx, &pr, fmt.Sprintf("dial failed: %v", err))
	}

	// Probe health — try the standard gRPC health protocol.
	pingOK, caps := r.probePlugin(ctx, conn, pr.Name)
	if !pingOK {
		_ = conn.Close()
		return r.failStatus(ctx, &pr, "health probe failed — plugin not ready")
	}

	// Replace any existing connection.
	r.Registry.Register(pr.Name, &Entry{
		Conn:       conn,
		PluginType: pr.Spec.Type,
		Name:       pr.Name,
	})

	logger.Info("plugin registered", "plugin", pr.Name, "type", pr.Spec.Type, "endpoint", endpoint)

	pr.Status.Connected = true
	pr.Status.LastPing = time.Now().UTC().Format(time.RFC3339)
	pr.Status.Capabilities = caps
	pr.Status.Message = "connected"
	apimeta.SetStatusCondition(&pr.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionTrue,
		Reason:             "Connected",
		Message:            "gRPC connection established",
		LastTransitionTime: metav1.Now(),
	})
	if err := r.Status().Update(ctx, &pr); err != nil {
		logger.Error(err, "failed to update plugin status")
	}

	// Re-probe at the configured interval to detect plugin crashes.
	interval := defaultPingInterval
	if pr.Spec.HealthCheck != nil && pr.Spec.HealthCheck.IntervalSeconds > 0 {
		interval = time.Duration(pr.Spec.HealthCheck.IntervalSeconds) * time.Second
	}
	return ctrl.Result{RequeueAfter: interval}, nil
}

// resolveEndpoint returns the gRPC address from SocketPath or Endpoint.
func (r *Reconciler) resolveEndpoint(spec kaprov1alpha1.PluginRegistrationSpec) (string, error) {
	if spec.SocketPath != "" {
		return "unix://" + spec.SocketPath, nil
	}
	if spec.Endpoint != "" {
		// Normalise: strip grpc:// scheme since grpc.Dial wants host:port or unix://
		ep := strings.TrimPrefix(spec.Endpoint, "grpc://")
		return ep, nil
	}
	return "", fmt.Errorf("neither socketPath nor endpoint set in PluginRegistration spec")
}

// dialOptions builds gRPC dial options. Uses insecure transport for Unix sockets
// and for endpoints without TLS config; mTLS when TLS config is present.
func (r *Reconciler) dialOptions(ctx context.Context, spec kaprov1alpha1.PluginRegistrationSpec) ([]grpc.DialOption, error) {
	opts := []grpc.DialOption{grpc.WithBlock()}

	if spec.SocketPath != "" || spec.TLS == nil {
		opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
		return opts, nil
	}

	// Load mTLS credentials from Secret.
	tlsCreds, err := r.loadTLSCredentials(ctx, spec.TLS.SecretRef)
	if err != nil {
		return nil, err
	}
	opts = append(opts, grpc.WithTransportCredentials(tlsCreds))
	return opts, nil
}

// loadTLSCredentials reads a K8s Secret with keys tls.crt, tls.key, ca.crt
// and constructs mTLS credentials.
func (r *Reconciler) loadTLSCredentials(ctx context.Context, secretName string) (credentials.TransportCredentials, error) {
	var secret corev1.Secret
	// TLS secrets are expected in the operator namespace; use an annotation for cross-ns.
	if err := r.Get(ctx, client.ObjectKey{Namespace: "kapro-system", Name: secretName}, &secret); err != nil {
		return nil, fmt.Errorf("load TLS secret %q: %w", secretName, err)
	}

	certPEM, ok1 := secret.Data["tls.crt"]
	keyPEM, ok2 := secret.Data["tls.key"]
	caPEM, ok3 := secret.Data["ca.crt"]
	if !ok1 || !ok2 || !ok3 {
		return nil, fmt.Errorf("secret %q must have keys: tls.crt, tls.key, ca.crt", secretName)
	}

	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("parse TLS cert/key: %w", err)
	}

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("parse CA bundle from secret %q", secretName)
	}

	return credentials.NewTLS(&tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      pool,
		MinVersion:   tls.VersionTLS13,
	}), nil
}

// probePlugin pings the plugin's gRPC health service and returns (ok, capabilities).
// Falls back gracefully if the health service is not implemented — treats connected
// state as healthy (some simple plugins may not implement gRPC health).
func (r *Reconciler) probePlugin(ctx context.Context, conn *grpc.ClientConn, name string) (bool, []string) {
	logger := log.FromContext(ctx)

	// Wait for connection to be in a usable state.
	if conn.GetState() == connectivity.TransientFailure || conn.GetState() == connectivity.Shutdown {
		return false, nil
	}

	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	hc := healthpb.NewHealthClient(conn)
	resp, err := hc.Check(probeCtx, &healthpb.HealthCheckRequest{Service: "kapro.plugin"})
	if err != nil {
		// Plugin doesn't implement the health service — treat as healthy if connected.
		if conn.GetState() == connectivity.Ready || conn.GetState() == connectivity.Idle {
			logger.V(1).Info("plugin has no health service, treating as healthy", "plugin", name)
			return true, nil
		}
		return false, nil
	}

	if resp.Status != healthpb.HealthCheckResponse_SERVING {
		return false, nil
	}
	return true, nil
}

// failStatus writes a not-connected status to the PluginRegistration.
func (r *Reconciler) failStatus(ctx context.Context, pr *kaprov1alpha1.PluginRegistration, msg string) (ctrl.Result, error) {
	log.FromContext(ctx).Info("plugin registration failed", "plugin", pr.Name, "reason", msg)
	pr.Status.Connected = false
	pr.Status.Message = msg
	apimeta.SetStatusCondition(&pr.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionFalse,
		Reason:             "ConnectFailed",
		Message:            msg,
		LastTransitionTime: metav1.Now(),
	})
	_ = r.Status().Update(ctx, pr)
	return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
}

func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kaprov1alpha1.PluginRegistration{}).
		Complete(r)
}
