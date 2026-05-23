package transport

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	oteltrace "go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/test/bufconn"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
)

func TestCredentialsRequireNamespacedTLSSecretRef(t *testing.T) {
	_, err := Credentials(context.Background(), nil, kaprov1alpha2.Plugin{
		Spec: kaprov1alpha2.PluginSpec{
			TLSSecretRef: &corev1.SecretReference{Name: "plugin-tls"},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "client is required") {
		t.Fatalf("error = %v", err)
	}

	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	_, err = Credentials(context.Background(), c, kaprov1alpha2.Plugin{
		Spec: kaprov1alpha2.PluginSpec{
			TLSSecretRef: &corev1.SecretReference{Name: "plugin-tls"},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "both name and namespace") {
		t.Fatalf("error = %v", err)
	}
}

func TestCredentialsLoadsTLSSecret(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "plugin-tls", Namespace: "kapro-system"},
		Data: map[string][]byte{
			"ca.crt": testCAPEM(t),
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(secret).Build()

	creds, err := Credentials(context.Background(), c, kaprov1alpha2.Plugin{
		Spec: kaprov1alpha2.PluginSpec{
			TLSSecretRef: &corev1.SecretReference{Name: "plugin-tls", Namespace: "kapro-system"},
			Parameters:   map[string]string{ParameterTLSServerName: "plugin.test"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if creds.Info().SecurityProtocol == "" {
		t.Fatalf("credentials info = %+v", creds.Info())
	}
}

func TestDialOptionsInstallOTelClientStatsHandler(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	defer otel.SetTracerProvider(previous)

	listener := bufconn.Listen(1024 * 1024)
	t.Cleanup(func() { _ = listener.Close() })
	server := grpc.NewServer()
	healthpb.RegisterHealthServer(server, healthServer{})
	go func() {
		_ = server.Serve(listener)
	}()
	defer server.Stop()

	opts, err := DialOptions(context.Background(), nil, kaprov1alpha2.Plugin{})
	if err != nil {
		t.Fatalf("DialOptions: %v", err)
	}
	opts = append(opts,
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return listener.DialContext(ctx)
		}),
		grpc.WithBlock(), //nolint:staticcheck // grpc.NewClient lacks WithBlock equivalent in older supported versions.
	)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := grpc.DialContext(ctx, "bufnet", opts...) //nolint:staticcheck // grpc.NewClient lacks WithBlock equivalent in older supported versions.
	if err != nil {
		t.Fatalf("DialContext: %v", err)
	}
	defer conn.Close()

	if _, err := healthpb.NewHealthClient(conn).Check(ctx, &healthpb.HealthCheckRequest{}); err != nil {
		t.Fatalf("health check: %v", err)
	}
	if !hasClientSpan(recorder.Ended()) {
		t.Fatalf("expected OTel client span, got %d ended spans", len(recorder.Ended()))
	}
}

type healthServer struct {
	healthpb.UnimplementedHealthServer
}

func (healthServer) Check(context.Context, *healthpb.HealthCheckRequest) (*healthpb.HealthCheckResponse, error) {
	return &healthpb.HealthCheckResponse{Status: healthpb.HealthCheckResponse_SERVING}, nil
}

func hasClientSpan(spans []sdktrace.ReadOnlySpan) bool {
	for _, span := range spans {
		if span.SpanKind() == oteltrace.SpanKindClient && strings.Contains(span.Name(), "Health/Check") {
			return true
		}
	}
	return false
}

func testCAPEM(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "plugin.test"},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}
