package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// helper: build a minimal signed cert valid for `lifetime` from now.
func makeTestCert(t *testing.T, cn string, lifetime time.Duration) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-1 * time.Minute),
		NotAfter:     time.Now().Add(lifetime),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse cert: %v", err)
	}
	return cert, key
}

func newFakeLocalClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	s := runtime.NewScheme()
	if err := corev1.AddToScheme(s); err != nil {
		t.Fatalf("core AddToScheme: %v", err)
	}
	return fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).Build()
}

func TestEncodeDecodeCert(t *testing.T) {
	cert, _ := makeTestCert(t, "kapro-cluster:de-prod-01", 24*time.Hour)
	encoded := encodeCert(cert)
	if len(encoded) == 0 {
		t.Fatal("empty encoded cert")
	}
	decoded, err := decodeCert(encoded)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded.Subject.CommonName != cert.Subject.CommonName {
		t.Errorf("round-trip CN mismatch: got %q want %q", decoded.Subject.CommonName, cert.Subject.CommonName)
	}
}

func TestEncodeDecodeKey(t *testing.T) {
	_, key := makeTestCert(t, "kapro-cluster:de-prod-01", time.Hour)
	encoded := encodeKey(key)
	if len(encoded) == 0 {
		t.Fatal("empty encoded key")
	}
	if !startsWithBytes(encoded, []byte("-----BEGIN EC PRIVATE KEY-----")) {
		t.Errorf("expected EC PRIVATE KEY block; got: %s", encoded[:min(40, len(encoded))])
	}
	decoded, err := decodeKey(encoded)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded.D.Cmp(key.D) != 0 {
		t.Error("round-trip key mismatch")
	}
}

func TestSecretStore_LoadMissing(t *testing.T) {
	c := newFakeLocalClient(t)
	s := &secretStore{client: c, namespace: "kapro-system", name: "kapro-hub-credentials"}
	cert, key, ok, err := s.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if ok {
		t.Errorf("Load should return ok=false for missing Secret; got cert=%v key=%v", cert, key)
	}
}

func TestSecretStore_SaveThenLoad(t *testing.T) {
	cert, key := makeTestCert(t, "kapro-cluster:de-prod-01", 24*time.Hour)
	c := newFakeLocalClient(t)
	s := &secretStore{client: c, namespace: "kapro-system", name: "kapro-hub-credentials"}

	if err := s.Save(context.Background(), encodeCert(cert), encodeKey(key)); err != nil {
		t.Fatalf("Save: %v", err)
	}

	gotCert, gotKey, ok, err := s.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !ok {
		t.Fatal("Load returned ok=false after Save")
	}
	if gotCert.Subject.CommonName != cert.Subject.CommonName {
		t.Errorf("CN mismatch: got %q want %q", gotCert.Subject.CommonName, cert.Subject.CommonName)
	}
	if gotKey.D.Cmp(key.D) != 0 {
		t.Error("key mismatch")
	}

	// Saving again must be idempotent (upsert).
	if err := s.Save(context.Background(), encodeCert(cert), encodeKey(key)); err != nil {
		t.Fatalf("second Save: %v", err)
	}
}

func TestSecretStore_SaveUpdatesExisting(t *testing.T) {
	old := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "kapro-hub-credentials", Namespace: "kapro-system"},
		Data:       map[string][]byte{"tls.crt": []byte("old"), "tls.key": []byte("old")},
	}
	c := newFakeLocalClient(t, old)
	s := &secretStore{client: c, namespace: "kapro-system", name: "kapro-hub-credentials"}

	cert, key := makeTestCert(t, "kapro-cluster:de-prod-01", time.Hour)
	if err := s.Save(context.Background(), encodeCert(cert), encodeKey(key)); err != nil {
		t.Fatalf("Save: %v", err)
	}
	gotCert, _, _, err := s.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if gotCert.Subject.CommonName != cert.Subject.CommonName {
		t.Errorf("Save did not update existing Secret; CN got=%q want=%q", gotCert.Subject.CommonName, cert.Subject.CommonName)
	}
}

func TestSecretStore_LoadReportsCorruption(t *testing.T) {
	bad := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "kapro-hub-credentials", Namespace: "kapro-system"},
		Data:       map[string][]byte{"tls.crt": []byte("garbage"), "tls.key": []byte("garbage")},
	}
	c := newFakeLocalClient(t, bad)
	s := &secretStore{client: c, namespace: "kapro-system", name: "kapro-hub-credentials"}

	_, _, ok, err := s.Load(context.Background())
	if err == nil {
		t.Fatal("expected error decoding corrupt cert PEM")
	}
	if ok {
		t.Error("ok should be false on decode error")
	}
}

func TestSecretStore_EnsureNamespace_Idempotent(t *testing.T) {
	c := newFakeLocalClient(t)
	s := &secretStore{client: c, namespace: "kapro-system", name: "kapro-hub-credentials"}

	if err := s.ensureNamespace(context.Background()); err != nil {
		t.Fatalf("first ensureNamespace: %v", err)
	}
	if err := s.ensureNamespace(context.Background()); err != nil {
		t.Fatalf("second ensureNamespace (should be idempotent): %v", err)
	}
	ns := &corev1.Namespace{}
	if err := c.Get(context.Background(), client.ObjectKey{Name: "kapro-system"}, ns); err != nil {
		if apierrors.IsNotFound(err) {
			t.Fatal("namespace was not created")
		}
		t.Fatalf("get namespace: %v", err)
	}
}

func TestDecodeCABundle(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"raw PEM", "-----BEGIN CERTIFICATE-----\nXYZ\n-----END CERTIFICATE-----\n", "-----BEGIN CERTIFICATE-----\nXYZ\n-----END CERTIFICATE-----\n"},
		{"base64", "aGVsbG8=", "hello"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := string(decodeCABundle(c.in))
			if got != c.want {
				t.Errorf("decodeCABundle(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestSanitizeName(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"kapro-cluster:de-prod-01", "kapro-cluster-de-prod-01"},
		{"DE-PROD-01", "de-prod-01"},
		{"a.b.c", "a.b.c"},
		{"weird/chars*here", "weird-chars-here"},
	}
	for _, c := range cases {
		if got := sanitizeName(c.in); got != c.want {
			t.Errorf("sanitizeName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestCertManager_AcceptIfValid(t *testing.T) {
	m := &certManager{}
	// Valid: not expired.
	cert, key := makeTestCert(t, "kapro-cluster:de-prod-01", 24*time.Hour)
	if !m.acceptIfValid(cert, key) {
		t.Error("valid 24h cert should be accepted")
	}
	if m.CurrentNotAfter().IsZero() {
		t.Error("CurrentNotAfter should be set after accept")
	}

	// About to expire (< 10 minutes): rejected.
	m2 := &certManager{}
	near, key2 := makeTestCert(t, "kapro-cluster:de-prod-01", 5*time.Minute)
	if m2.acceptIfValid(near, key2) {
		t.Error("cert with <10min remaining should NOT be accepted (forces re-bootstrap)")
	}

	// Nil: rejected.
	if (&certManager{}).acceptIfValid(nil, nil) {
		t.Error("nil cert/key must not be accepted")
	}
}

func TestCertManager_ShouldRenew(t *testing.T) {
	cert, key := makeTestCert(t, "kapro-cluster:de-prod-01", 24*time.Hour)
	m := &certManager{renewBefore: 0.5}
	m.cert, m.key = cert, key
	if m.shouldRenew() {
		t.Error("brand-new cert should NOT need renewal yet")
	}

	// Cert past renewBefore deadline (lifetime/2 elapsed).
	expired := *cert
	expired.NotBefore = time.Now().Add(-25 * time.Hour)
	expired.NotAfter = time.Now().Add(-time.Hour)
	m2 := &certManager{renewBefore: 0.5}
	m2.cert = &expired
	if !m2.shouldRenew() {
		t.Error("past-deadline cert should need renewal")
	}
}

func TestValidateCertOptions(t *testing.T) {
	store := &secretStore{}
	tpl := &x509.CertificateRequest{Subject: pkix.Name{CommonName: "kapro-cluster:de"}}

	t.Run("missing template", func(t *testing.T) {
		if err := validateCertOptions(&certManagerOptions{SignerName: "x", HubAPIURL: "h", Store: store}); err == nil {
			t.Error("expected error on missing template")
		}
	})
	t.Run("missing signer", func(t *testing.T) {
		if err := validateCertOptions(&certManagerOptions{Template: tpl, HubAPIURL: "h", Store: store}); err == nil {
			t.Error("expected error on missing signer")
		}
	})
	t.Run("missing hub URL", func(t *testing.T) {
		if err := validateCertOptions(&certManagerOptions{Template: tpl, SignerName: "x", Store: store}); err == nil {
			t.Error("expected error on missing hub URL")
		}
	})
	t.Run("defaults applied", func(t *testing.T) {
		opts := &certManagerOptions{Template: tpl, SignerName: "x", HubAPIURL: "h", Store: store}
		if err := validateCertOptions(opts); err != nil {
			t.Fatalf("validate: %v", err)
		}
		if opts.WaitForFirstCert == 0 || opts.WaitForCertInterval == 0 || opts.RequestedCertTTL == 0 {
			t.Errorf("defaults not applied: %+v", opts)
		}
	})
}

func TestLoadBootstrapKubeconfig_EmptyReturnsNil(t *testing.T) {
	bc, err := loadBootstrapKubeconfig("")
	if err != nil {
		t.Fatalf("empty path should not error: %v", err)
	}
	if bc != nil {
		t.Errorf("empty path should return nil; got %+v", bc)
	}
}

func TestLoadBootstrapKubeconfig_BadPath(t *testing.T) {
	_, err := loadBootstrapKubeconfig("/nonexistent/kapro-bootstrap.yaml")
	if err == nil {
		t.Fatal("expected error for nonexistent path")
	}
}

func TestHeartbeatLeaseName(t *testing.T) {
	if heartbeatLeaseName("de-prod-01") != "kapro-heartbeat-de-prod-01" {
		t.Errorf("unexpected lease name: %q", heartbeatLeaseName("de-prod-01"))
	}
}

// helpers
func startsWithBytes(s, prefix []byte) bool {
	if len(s) < len(prefix) {
		return false
	}
	for i := range prefix {
		if s[i] != prefix[i] {
			return false
		}
	}
	return true
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// Sanity check on PEM tooling so a regression in the test helper is obvious.
func TestPEMRoundTrip(t *testing.T) {
	block := &pem.Block{Type: "TEST", Bytes: []byte("hello")}
	encoded := pem.EncodeToMemory(block)
	decoded, _ := pem.Decode(encoded)
	if decoded == nil || string(decoded.Bytes) != "hello" {
		t.Errorf("PEM round-trip broken: %+v", decoded)
	}
}
