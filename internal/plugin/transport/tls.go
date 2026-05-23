// Package transport builds gRPC transport credentials for plugin endpoints.
package transport

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"

	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	corev1 "k8s.io/api/core/v1"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
)

const (
	// ParameterTLSServerName optionally overrides the server name used to verify the plugin certificate.
	ParameterTLSServerName = "tlsServerName"

	// ParameterAllowTLS12 opts a registration into accepting TLS 1.2 servers.
	// The default is TLS 1.3 (modern, drops CBC/RC4 weak-cipher exposure).
	// Set to "true" only when wrapping a legacy backend the operator cannot
	// upgrade — emits a warning log each time the registration is probed so
	// the downgrade is auditable.
	ParameterAllowTLS12 = "allowTLS12"
)

// DialOptions returns the base gRPC dial options for a plugin registration.
func DialOptions(ctx context.Context, c client.Reader, reg kaprov1alpha2.Plugin) ([]grpc.DialOption, error) {
	creds, err := Credentials(ctx, c, reg)
	if err != nil {
		return nil, err
	}
	return []grpc.DialOption{
		grpc.WithTransportCredentials(creds),
		grpc.WithStatsHandler(otelgrpc.NewClientHandler()),
	}, nil
}

// Credentials returns insecure credentials when no TLSSecretRef is configured,
// otherwise it builds TLS credentials from the referenced Kubernetes Secret.
// Default MinVersion is TLS 1.3; set spec.parameters.allowTLS12=true to
// downgrade for legacy plugin servers (emits a warning log).
func Credentials(ctx context.Context, c client.Reader, reg kaprov1alpha2.Plugin) (credentials.TransportCredentials, error) {
	logger := log.FromContext(ctx).WithName("plugin-transport").WithValues("plugin", reg.Name)
	if reg.Spec.TLSSecretRef == nil {
		// Loud warning every time we hand back insecure creds — operators
		// should set a tlsSecretRef in production. Cheap (one log per probe)
		// and a clear audit signal in logs / log-based alerts.
		logger.Info("WARNING: plugin transport is INSECURE (no spec.tlsSecretRef configured) — do not use in production")
		return insecure.NewCredentials(), nil
	}
	if c == nil {
		return nil, fmt.Errorf("client is required when tlsSecretRef is configured")
	}
	ref := reg.Spec.TLSSecretRef
	if ref.Name == "" || ref.Namespace == "" {
		return nil, fmt.Errorf("tlsSecretRef requires both name and namespace")
	}

	var secret corev1.Secret
	if err := c.Get(ctx, k8stypes.NamespacedName{Name: ref.Name, Namespace: ref.Namespace}, &secret); err != nil {
		return nil, fmt.Errorf("get tls secret %s/%s: %w", ref.Namespace, ref.Name, err)
	}

	// Default to TLS 1.3 (modern, no weak ciphers). Legacy plugin servers can
	// opt back to TLS 1.2 with spec.parameters.allowTLS12=true; we log the
	// downgrade each time so it shows up on every probe.
	minVersion := uint16(tls.VersionTLS13)
	if reg.Spec.Parameters[ParameterAllowTLS12] == "true" {
		minVersion = tls.VersionTLS12
		logger.Info("WARNING: TLS 1.2 explicitly allowed via parameters.allowTLS12 — prefer upgrading the plugin server to TLS 1.3")
	}
	config := &tls.Config{MinVersion: minVersion} //nolint:gosec // MinVersion is configurable; default is TLS 1.3.
	if serverName := reg.Spec.Parameters[ParameterTLSServerName]; serverName != "" {
		config.ServerName = serverName
	}

	if caPEM := secret.Data["ca.crt"]; len(caPEM) > 0 {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caPEM) {
			return nil, fmt.Errorf("tls secret %s/%s contains invalid ca.crt", ref.Namespace, ref.Name)
		}
		config.RootCAs = pool
	}

	certPEM, hasCert := secret.Data[corev1.TLSCertKey]
	keyPEM, hasKey := secret.Data[corev1.TLSPrivateKeyKey]
	if hasCert || hasKey {
		if !hasCert || !hasKey {
			return nil, fmt.Errorf("tls secret %s/%s must contain both tls.crt and tls.key for client authentication", ref.Namespace, ref.Name)
		}
		cert, err := tls.X509KeyPair(certPEM, keyPEM)
		if err != nil {
			return nil, fmt.Errorf("parse client certificate from tls secret %s/%s: %w", ref.Namespace, ref.Name, err)
		}
		config.Certificates = []tls.Certificate{cert}
	}

	return credentials.NewTLS(config), nil
}
