// Package transport builds gRPC transport credentials for plugin endpoints.
package transport

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	corev1 "k8s.io/api/core/v1"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
)

const (
	// ParameterTLSServerName optionally overrides the server name used to verify the plugin certificate.
	ParameterTLSServerName = "tlsServerName"
)

// DialOptions returns the base gRPC dial options for a plugin registration.
func DialOptions(ctx context.Context, c client.Reader, reg kaprov1alpha1.PluginRegistration) ([]grpc.DialOption, error) {
	creds, err := Credentials(ctx, c, reg)
	if err != nil {
		return nil, err
	}
	return []grpc.DialOption{grpc.WithTransportCredentials(creds)}, nil
}

// Credentials returns insecure credentials when no TLSSecretRef is configured,
// otherwise it builds TLS credentials from the referenced Kubernetes Secret.
func Credentials(ctx context.Context, c client.Reader, reg kaprov1alpha1.PluginRegistration) (credentials.TransportCredentials, error) {
	if reg.Spec.TLSSecretRef == nil {
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

	config := &tls.Config{MinVersion: tls.VersionTLS12} //nolint:gosec // TLS 1.2 is the Kubernetes baseline for broad plugin compatibility.
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
