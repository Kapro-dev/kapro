package controller

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"testing"

	certificatesv1 "k8s.io/api/certificates/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
)

func TestContainsString(t *testing.T) {
	cases := []struct {
		s    []string
		want string
		got  bool
	}{
		{nil, "x", false},
		{[]string{}, "x", false},
		{[]string{"a", "b", "c"}, "b", true},
		{[]string{"a", "b", "c"}, "z", false},
		{[]string{""}, "", true},
	}
	for _, c := range cases {
		if got := containsString(c.s, c.want); got != c.got {
			t.Errorf("containsString(%v, %q) = %v, want %v", c.s, c.want, got, c.got)
		}
	}
}

func TestRemoveString(t *testing.T) {
	cases := []struct {
		in   []string
		drop string
		want []string
	}{
		{nil, "x", nil},
		{[]string{"a", "b", "c"}, "b", []string{"a", "c"}},
		{[]string{"a", "b", "c"}, "z", []string{"a", "b", "c"}},
		{[]string{"a", "a", "a"}, "a", []string{}},
	}
	for _, c := range cases {
		got := removeString(append([]string(nil), c.in...), c.drop)
		if len(got) != len(c.want) {
			t.Fatalf("removeString(%v, %q) len=%d, want %d (got=%v)", c.in, c.drop, len(got), len(c.want), got)
		}
		for i, v := range got {
			if v != c.want[i] {
				t.Fatalf("removeString(%v, %q)[%d] = %q, want %q", c.in, c.drop, i, v, c.want[i])
			}
		}
	}
}

func TestSameFinalizers(t *testing.T) {
	cases := []struct {
		a, b []string
		want bool
	}{
		{nil, nil, true},
		{nil, []string{}, true},
		{[]string{"a"}, []string{"a"}, true},
		{[]string{"a", "b"}, []string{"b", "a"}, true},
		{[]string{"a"}, []string{"b"}, false},
		{[]string{"a", "b"}, []string{"a"}, false},
	}
	for _, c := range cases {
		if got := sameFinalizers(c.a, c.b); got != c.want {
			t.Errorf("sameFinalizers(%v, %v) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestLabelsEqual(t *testing.T) {
	if !labelsEqual(nil, nil) {
		t.Error("nil/nil should be equal")
	}
	if !labelsEqual(map[string]string{}, nil) {
		t.Error("empty/nil should be equal")
	}
	if labelsEqual(map[string]string{"a": "1"}, nil) {
		t.Error("nonempty/nil should not be equal")
	}
	if !labelsEqual(map[string]string{"a": "1", "b": "2"}, map[string]string{"a": "1", "b": "2"}) {
		t.Error("matching maps should be equal")
	}
	if labelsEqual(map[string]string{"a": "1"}, map[string]string{"a": "2"}) {
		t.Error("differing values should not be equal")
	}
}

func TestManagedResourceLabels(t *testing.T) {
	l := managedResourceLabels("de-prod-01", "cluster-controller-rbac")
	if l["app.kubernetes.io/managed-by"] != kaproManagedBy {
		t.Errorf("missing managed-by: %v", l)
	}
	if l["app.kubernetes.io/component"] != "cluster-controller-rbac" {
		t.Errorf("missing component: %v", l)
	}
	if l["kapro.io/fleetcluster"] != "de-prod-01" {
		t.Errorf("missing fleetcluster label: %v", l)
	}
}

func TestBootstrapStatusEqual(t *testing.T) {
	cases := []struct {
		name string
		a, b *kaprov1alpha1.FleetClusterBootstrapStatus
		want bool
	}{
		{"both nil", nil, nil, true},
		{"one nil", nil, &kaprov1alpha1.FleetClusterBootstrapStatus{}, false},
		{"empty match", &kaprov1alpha1.FleetClusterBootstrapStatus{}, &kaprov1alpha1.FleetClusterBootstrapStatus{}, true},
		{
			"used diff",
			&kaprov1alpha1.FleetClusterBootstrapStatus{Used: true},
			&kaprov1alpha1.FleetClusterBootstrapStatus{Used: false},
			false,
		},
		{
			"deeply equal",
			&kaprov1alpha1.FleetClusterBootstrapStatus{
				Used:                true,
				IssuedCredentialFor: "de-prod-01",
				BoundCSRName:        "csr-abc",
			},
			&kaprov1alpha1.FleetClusterBootstrapStatus{
				Used:                true,
				IssuedCredentialFor: "de-prod-01",
				BoundCSRName:        "csr-abc",
			},
			true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := bootstrapStatusEqual(c.a, c.b); got != c.want {
				t.Errorf("bootstrapStatusEqual = %v, want %v", got, c.want)
			}
		})
	}
}

func TestStartsWith(t *testing.T) {
	if !startsWith("kapro-cluster:de-prod-01", csrCNPrefix) {
		t.Error("expected prefix match")
	}
	if startsWith("kapro-clust", csrCNPrefix) {
		t.Error("substring shorter than prefix should not match")
	}
	if startsWith("other:de-prod-01", csrCNPrefix) {
		t.Error("differing prefix should not match")
	}
}

func TestHasOnlyClientAuthUsage(t *testing.T) {
	cases := []struct {
		name   string
		usages []certificatesv1.KeyUsage
		want   bool
	}{
		{"empty", nil, false},
		{"only client auth", []certificatesv1.KeyUsage{certificatesv1.UsageClientAuth}, true},
		{
			"client + server",
			[]certificatesv1.KeyUsage{certificatesv1.UsageClientAuth, certificatesv1.UsageServerAuth},
			false,
		},
		{
			"only server",
			[]certificatesv1.KeyUsage{certificatesv1.UsageServerAuth},
			false,
		},
		{
			"client + digital signature",
			[]certificatesv1.KeyUsage{certificatesv1.UsageClientAuth, certificatesv1.UsageDigitalSignature},
			false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := hasOnlyClientAuthUsage(c.usages); got != c.want {
				t.Errorf("hasOnlyClientAuthUsage = %v, want %v", got, c.want)
			}
		})
	}
}

func TestIsCSRApprovedDenied(t *testing.T) {
	approved := &certificatesv1.CertificateSigningRequest{Status: certificatesv1.CertificateSigningRequestStatus{
		Conditions: []certificatesv1.CertificateSigningRequestCondition{
			{Type: certificatesv1.CertificateApproved, Status: corev1.ConditionTrue},
		},
	}}
	denied := &certificatesv1.CertificateSigningRequest{Status: certificatesv1.CertificateSigningRequestStatus{
		Conditions: []certificatesv1.CertificateSigningRequestCondition{
			{Type: certificatesv1.CertificateDenied, Status: corev1.ConditionTrue},
		},
	}}
	pending := &certificatesv1.CertificateSigningRequest{}

	if !isCSRApproved(approved) {
		t.Error("approved CSR should be detected")
	}
	if isCSRApproved(denied) {
		t.Error("denied CSR is not approved")
	}
	if isCSRApproved(pending) {
		t.Error("pending CSR is not approved")
	}

	if !isCSRDenied(denied) {
		t.Error("denied CSR should be detected")
	}
	if isCSRDenied(approved) {
		t.Error("approved CSR is not denied")
	}
	if isCSRDenied(pending) {
		t.Error("pending CSR is not denied")
	}
}

func TestIsKaproCSR(t *testing.T) {
	makeCSR := func(t *testing.T, signerName, cn string, orgs []string, usages []certificatesv1.KeyUsage) *certificatesv1.CertificateSigningRequest {
		t.Helper()
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatalf("generate key: %v", err)
		}
		tmpl := &x509.CertificateRequest{
			Subject: pkix.Name{CommonName: cn, Organization: orgs},
		}
		der, err := x509.CreateCertificateRequest(rand.Reader, tmpl, key)
		if err != nil {
			t.Fatalf("create CSR: %v", err)
		}
		pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})
		return &certificatesv1.CertificateSigningRequest{
			ObjectMeta: metav1.ObjectMeta{Name: "test-csr"},
			Spec: certificatesv1.CertificateSigningRequestSpec{
				SignerName: signerName,
				Request:    pemBytes,
				Usages:     usages,
			},
		}
	}

	t.Run("valid kapro CSR", func(t *testing.T) {
		csr := makeCSR(t, csrSigner, "kapro-cluster:de-prod-01",
			[]string{csrOrganization},
			[]certificatesv1.KeyUsage{certificatesv1.UsageClientAuth})
		if !isKaproCSR(csr) {
			t.Error("valid kapro CSR should be detected")
		}
	})

	t.Run("wrong signer", func(t *testing.T) {
		csr := makeCSR(t, "kubernetes.io/kubelet-serving", "kapro-cluster:de-prod-01",
			[]string{csrOrganization},
			[]certificatesv1.KeyUsage{certificatesv1.UsageClientAuth})
		if isKaproCSR(csr) {
			t.Error("non-kapro signer should be rejected")
		}
	})

	t.Run("wrong CN prefix", func(t *testing.T) {
		csr := makeCSR(t, csrSigner, "other:de-prod-01",
			[]string{csrOrganization},
			[]certificatesv1.KeyUsage{certificatesv1.UsageClientAuth})
		if isKaproCSR(csr) {
			t.Error("non-kapro CN should be rejected")
		}
	})

	t.Run("system:masters escalation attempt", func(t *testing.T) {
		csr := makeCSR(t, csrSigner, "kapro-cluster:de-prod-01",
			[]string{csrOrganization, "system:masters"},
			[]certificatesv1.KeyUsage{certificatesv1.UsageClientAuth})
		if isKaproCSR(csr) {
			t.Error("CSR with extra Organization (system:masters) MUST be rejected")
		}
	})

	t.Run("missing organization", func(t *testing.T) {
		csr := makeCSR(t, csrSigner, "kapro-cluster:de-prod-01", nil,
			[]certificatesv1.KeyUsage{certificatesv1.UsageClientAuth})
		if isKaproCSR(csr) {
			t.Error("CSR without correct O must be rejected")
		}
	})

	t.Run("wrong organization", func(t *testing.T) {
		csr := makeCSR(t, csrSigner, "kapro-cluster:de-prod-01",
			[]string{"wrong:org"},
			[]certificatesv1.KeyUsage{certificatesv1.UsageClientAuth})
		if isKaproCSR(csr) {
			t.Error("CSR with wrong O must be rejected")
		}
	})

	t.Run("server auth usage rejected", func(t *testing.T) {
		csr := makeCSR(t, csrSigner, "kapro-cluster:de-prod-01",
			[]string{csrOrganization},
			[]certificatesv1.KeyUsage{certificatesv1.UsageClientAuth, certificatesv1.UsageServerAuth})
		if isKaproCSR(csr) {
			t.Error("CSR requesting server auth must be rejected")
		}
	})

	t.Run("malformed request PEM", func(t *testing.T) {
		csr := &certificatesv1.CertificateSigningRequest{
			Spec: certificatesv1.CertificateSigningRequestSpec{
				SignerName: csrSigner,
				Request:    []byte("not pem"),
				Usages:     []certificatesv1.KeyUsage{certificatesv1.UsageClientAuth},
			},
		}
		if isKaproCSR(csr) {
			t.Error("malformed CSR should be rejected")
		}
	})

	t.Run("nil CSR", func(t *testing.T) {
		if isKaproCSR(nil) {
			t.Error("nil CSR should be rejected")
		}
	})
}

func TestShouldProvision(t *testing.T) {
	cases := []struct {
		name string
		fc   *kaprov1alpha1.FleetCluster
		want bool
	}{
		{
			name: "no bootstrap spec",
			fc:   &kaprov1alpha1.FleetCluster{},
			want: false,
		},
		{
			name: "no status yet",
			fc: &kaprov1alpha1.FleetCluster{
				Spec: kaprov1alpha1.FleetClusterSpec{Bootstrap: &kaprov1alpha1.FleetClusterBootstrapSpec{}},
			},
			want: true,
		},
		{
			name: "already used",
			fc: &kaprov1alpha1.FleetCluster{
				Spec: kaprov1alpha1.FleetClusterSpec{Bootstrap: &kaprov1alpha1.FleetClusterBootstrapSpec{}},
				Status: kaprov1alpha1.FleetClusterStatus{
					Bootstrap: &kaprov1alpha1.FleetClusterBootstrapStatus{Used: true},
				},
			},
			want: false,
		},
		{
			name: "kubeconfig already issued",
			fc: &kaprov1alpha1.FleetCluster{
				Spec: kaprov1alpha1.FleetClusterSpec{Bootstrap: &kaprov1alpha1.FleetClusterBootstrapSpec{}},
				Status: kaprov1alpha1.FleetClusterStatus{
					Bootstrap: &kaprov1alpha1.FleetClusterBootstrapStatus{
						IssuedBootstrapKubeconfig: "kapro-bootstrap-kubeconfig-de-prod-01",
					},
				},
			},
			want: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := shouldProvision(c.fc); got != c.want {
				t.Errorf("shouldProvision = %v, want %v", got, c.want)
			}
		})
	}
}

func TestBuildBootstrapKubeconfig(t *testing.T) {
	t.Run("happy path", func(t *testing.T) {
		out, err := buildBootstrapKubeconfig("https://hub.example.com:6443", []byte("ca-bytes"), "tok-xyz", "de-prod-01", "kapro-bootstrap-de-prod-01")
		if err != nil {
			t.Fatalf("buildBootstrapKubeconfig: %v", err)
		}
		if len(out) == 0 {
			t.Fatal("empty kubeconfig bytes")
		}
		// Sanity: rendered kubeconfig should contain key markers.
		s := string(out)
		for _, want := range []string{"https://hub.example.com:6443", "kapro-bootstrap-de-prod-01", "current-context: bootstrap"} {
			if !contains(s, want) {
				t.Errorf("rendered kubeconfig missing %q\nfull:\n%s", want, s)
			}
		}
	})

	t.Run("empty hub URL is an error", func(t *testing.T) {
		_, err := buildBootstrapKubeconfig("", []byte("ca"), "tok", "c", "sa")
		if err == nil {
			t.Fatal("expected error for empty hub URL")
		}
	})
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
