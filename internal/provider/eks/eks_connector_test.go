package eks

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"math/big"
	"testing"
	"time"

	"golang.org/x/oauth2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
)

// ─── test helpers ────────────────────────────────────────────────────────────

// staticTS returns a fixed access token. Satisfies oauth2.TokenSource.
type staticTS struct{ tok string }

func (s staticTS) Token() (*oauth2.Token, error) {
	return &oauth2.Token{AccessToken: s.tok, Expiry: time.Now().Add(time.Hour)}, nil
}

// errTS always returns an error. Used to test token-source failure paths.
type errTS struct{ msg string }

func (e errTS) Token() (*oauth2.Token, error) { return nil, fmt.Errorf("%s", e.msg) }

// fakeCA returns a valid base64-encoded self-signed CA certificate in PEM format.
// k8srest.HTTPClientFor requires a valid PEM block; raw bytes fail with
// "unable to parse bytes as PEM block".
func fakeCA() string {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic("fakeCA: generating key: " + err.Error())
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "kapro-eks-test-ca"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		IsCA:         true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		panic("fakeCA: creating cert: " + err.Error())
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	return base64.StdEncoding.EncodeToString(pemBytes)
}

// fakeCluster builds a minimal *clusterInfo with the given endpoint.
func fakeCluster(endpoint string) *clusterInfo {
	return &clusterInfo{
		Endpoint: endpoint,
		CAData:   fakeCA(),
	}
}

// newTestConn builds a Connector with injected stubs — no real AWS calls.
func newTestConn(
	cluster *clusterInfo,
	clusterErr error,
	ts oauth2.TokenSource,
) *Connector {
	return &Connector{
		getCluster: func(_ context.Context, _, _ string) (*clusterInfo, error) {
			return cluster, clusterErr
		},
		newTokenSource: func(_ context.Context, _, _ string) (oauth2.TokenSource, error) {
			if ts == nil {
				return nil, fmt.Errorf("injected nil token source")
			}
			return ts, nil
		},
	}
}

// makeEnv returns a minimal Environment with an EKS provider spec.
func makeEnv(region, clusterName string) *kaprov1alpha1.Environment {
	return &kaprov1alpha1.Environment{
		ObjectMeta: metav1.ObjectMeta{Name: "test-env"},
		Spec: kaprov1alpha1.EnvironmentSpec{
			Provider: &kaprov1alpha1.ProviderSpec{
				Type: "eks",
				EKS: &kaprov1alpha1.EKSProviderSpec{
					Region:      region,
					ClusterName: clusterName,
				},
			},
		},
	}
}

// ─── Connect tests ────────────────────────────────────────────────────────────

func TestConnect_NilEnv(t *testing.T) {
	_, err := New().Connect(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error for nil env, got nil")
	}
}

func TestConnect_NoEKSSpec(t *testing.T) {
	env := &kaprov1alpha1.Environment{
		ObjectMeta: metav1.ObjectMeta{Name: "no-spec"},
		Spec: kaprov1alpha1.EnvironmentSpec{
			Provider: &kaprov1alpha1.ProviderSpec{Type: "eks"},
		},
	}
	_, err := New().Connect(context.Background(), env)
	if err == nil {
		t.Fatal("expected error when EKS spec is absent")
	}
}

func TestConnect_ValidationErrors(t *testing.T) {
	cases := []struct {
		label            string
		region, cluster  string
	}{
		{"missing region", "", "my-cluster"},
		{"missing clusterName", "us-east-1", ""},
		{"all empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			c := newTestConn(fakeCluster("1.2.3.4"), nil, staticTS{"tok"})
			_, err := c.Connect(context.Background(), makeEnv(tc.region, tc.cluster))
			if err == nil {
				t.Fatalf("expected validation error for %s", tc.label)
			}
		})
	}
}

func TestConnect_AWSAPIError(t *testing.T) {
	c := newTestConn(nil, fmt.Errorf("network error: connection refused"), nil)
	_, err := c.Connect(context.Background(), makeEnv("us-east-1", "my-cluster"))
	if err == nil {
		t.Fatal("expected error when AWS EKS API fails")
	}
}

func TestConnect_TokenSourceError(t *testing.T) {
	c := newTestConn(fakeCluster("1.2.3.4"), nil, nil) // nil ts → error
	_, err := c.Connect(context.Background(), makeEnv("us-east-1", "my-cluster"))
	if err == nil {
		t.Fatal("expected error when token source fails")
	}
}

func TestConnect_Success(t *testing.T) {
	c := newTestConn(fakeCluster("10.0.0.1"), nil, staticTS{"my-access-token"})
	cfg, err := c.Connect(context.Background(), makeEnv("eu-west-1", "prod-cluster"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Host != "https://10.0.0.1" {
		t.Errorf("host = %q, want %q", cfg.Host, "https://10.0.0.1")
	}
	if cfg.WrapTransport == nil {
		t.Error("WrapTransport must be set (oauth2 token injection)")
	}
	if len(cfg.TLSClientConfig.CAData) == 0 {
		t.Error("TLSClientConfig.CAData must not be empty")
	}
}

// TestConnect_EndpointWithScheme verifies that an endpoint that already carries
// "https://" is not double-prefixed.
func TestConnect_EndpointWithScheme(t *testing.T) {
	c := newTestConn(
		&clusterInfo{Endpoint: "https://abcdef.gr7.us-east-1.eks.amazonaws.com", CAData: fakeCA()},
		nil, staticTS{"tok"},
	)
	cfg, err := c.Connect(context.Background(), makeEnv("us-east-1", "prod"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	const want = "https://abcdef.gr7.us-east-1.eks.amazonaws.com"
	if cfg.Host != want {
		t.Errorf("host = %q, want %q", cfg.Host, want)
	}
}

// ─── buildConfig tests ────────────────────────────────────────────────────────

func TestBuildConfig_EmptyEndpoint(t *testing.T) {
	_, err := buildConfig(fakeCluster(""), staticTS{"tok"})
	if err == nil {
		t.Fatal("expected error for empty cluster endpoint")
	}
}

func TestBuildConfig_InvalidCA(t *testing.T) {
	cluster := &clusterInfo{
		Endpoint: "1.2.3.4",
		CAData:   "!!! not valid base64 !!!",
	}
	_, err := buildConfig(cluster, staticTS{"tok"})
	if err == nil {
		t.Fatal("expected error for invalid CA encoding")
	}
}

func TestBuildConfig_EmptyCA(t *testing.T) {
	cluster := &clusterInfo{
		Endpoint: "1.2.3.4",
		// Valid base64 but decodes to empty bytes.
		CAData: base64.StdEncoding.EncodeToString([]byte{}),
	}
	_, err := buildConfig(cluster, staticTS{"tok"})
	if err == nil {
		t.Fatal("expected error for empty CA")
	}
}

func TestBuildConfig_Valid(t *testing.T) {
	cfg, err := buildConfig(fakeCluster("192.168.1.1"), staticTS{"bearer-token"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Host != "https://192.168.1.1" {
		t.Errorf("host = %q, want %q", cfg.Host, "https://192.168.1.1")
	}
	if cfg.WrapTransport == nil {
		t.Error("WrapTransport must be set for OAuth2 token injection")
	}
	if len(cfg.TLSClientConfig.CAData) == 0 {
		t.Error("CAData must be populated")
	}
	// BearerToken field must NOT be set; token injection is via WrapTransport.
	if cfg.BearerToken != "" {
		t.Errorf("BearerToken must be empty (use WrapTransport instead), got %q", cfg.BearerToken)
	}
}

// ─── validateSpec tests ───────────────────────────────────────────────────────

func TestValidateSpec_AllPresent(t *testing.T) {
	spec := &kaprov1alpha1.EKSProviderSpec{
		Region:      "us-east-1",
		ClusterName: "prod",
	}
	if err := validateSpec(spec, "env-name"); err != nil {
		t.Errorf("unexpected error for valid spec: %v", err)
	}
}

func TestValidateSpec_MissingFields(t *testing.T) {
	cases := []struct {
		label string
		spec  kaprov1alpha1.EKSProviderSpec
	}{
		{"no region", kaprov1alpha1.EKSProviderSpec{ClusterName: "c"}},
		{"no clusterName", kaprov1alpha1.EKSProviderSpec{Region: "us-east-1"}},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			s := tc.spec
			if err := validateSpec(&s, "env"); err == nil {
				t.Fatalf("expected error for %s", tc.label)
			}
		})
	}
}

// ─── IsReachable tests ────────────────────────────────────────────────────────

func TestIsReachable_ConnectError(t *testing.T) {
	// If Connect fails (config error), IsReachable returns (false, error).
	c := newTestConn(nil, fmt.Errorf("cluster not found"), nil)
	reachable, err := c.IsReachable(context.Background(), makeEnv("us-east-1", "c"))
	if reachable {
		t.Error("should not be reachable when Connect fails")
	}
	if err == nil {
		t.Error("expected error when Connect fails")
	}
}

// TestIsReachable_NetworkBlip verifies that a network error returns (false, nil)
// — not an error — so the reconciler retries rather than failing the Sync.
//
// 192.0.2.1 is TEST-NET-1 (RFC 5737), a documentation address that is never
// routed on the internet, so the TCP connection is guaranteed to fail/time-out.
func TestIsReachable_NetworkBlip(t *testing.T) {
	c := newTestConn(fakeCluster("192.0.2.1"), nil, staticTS{"tok"})
	reachable, err := c.IsReachable(context.Background(), makeEnv("us-east-1", "c"))
	// A network blip must return (false, nil).
	// An error here would cause the SyncReconciler to mark the Sync as Failed
	// instead of retrying — that would be wrong.
	if err != nil {
		t.Errorf("network blip should return nil error (got %v); controller must retry, not fail", err)
	}
	if reachable {
		t.Error("TEST-NET-1 address should not be reachable")
	}
}
