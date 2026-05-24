package controller

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"reflect"
	"testing"
	"time"

	authv1 "k8s.io/api/authentication/v1"
	certificatesv1 "k8s.io/api/certificates/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	kaprov1alpha1 "kapro.io/kapro/api/kapro/v1alpha1"
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

func TestBuildClusterRoleUsesNamedVerbsOnly(t *testing.T) {
	role := buildClusterRole("kapro:cluster-controller:de-prod-01", "de-prod-01")

	clusterRule := findPolicyRule(t, role.Rules, "kapro.io", "clusters")
	if got, want := clusterRule.ResourceNames, []string{"de-prod-01"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("cluster resourceNames = %v, want %v", got, want)
	}
	assertVerbs(t, clusterRule.Verbs, []string{"get", "patch", "update"})

	statusRule := findPolicyRule(t, role.Rules, "kapro.io", "clusters/status")
	assertVerbs(t, statusRule.Verbs, []string{"get", "patch", "update"})

	csrRule := findPolicyRule(t, role.Rules, "certificates.k8s.io", "certificatesigningrequests")
	assertVerbs(t, csrRule.Verbs, []string{"create", "get"})
}

func findPolicyRule(t *testing.T, rules []rbacv1.PolicyRule, apiGroup, resource string) rbacv1.PolicyRule {
	t.Helper()
	for _, rule := range rules {
		if containsString(rule.APIGroups, apiGroup) && containsString(rule.Resources, resource) {
			return rule
		}
	}
	t.Fatalf("policy rule for %s/%s not found: %#v", apiGroup, resource, rules)
	return rbacv1.PolicyRule{}
}

func assertVerbs(t *testing.T, got, want []string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("verbs = %v, want %v", got, want)
	}
	for _, forbidden := range []string{"list", "watch"} {
		if containsString(got, forbidden) {
			t.Fatalf("verbs must not include %q: %v", forbidden, got)
		}
	}
}

func TestBootstrapStatusEqual(t *testing.T) {
	// Distinct *metav1.Time allocations with the same instant. The previous
	// implementation used `*a == *b` and would report these as unequal
	// because Go struct equality compares pointer addresses, not pointed-to
	// values. The fix unpacks each pointer field and compares semantics.
	instant := time.Date(2026, 5, 17, 10, 0, 0, 0, time.UTC)
	timeA := metav1.NewTime(instant)
	timeB := metav1.NewTime(instant) // different allocation, same time

	cases := []struct {
		name string
		a, b *kaprov1alpha1.ClusterBootstrapStatus
		want bool
	}{
		{"both nil", nil, nil, true},
		{"one nil", nil, &kaprov1alpha1.ClusterBootstrapStatus{}, false},
		{"empty match", &kaprov1alpha1.ClusterBootstrapStatus{}, &kaprov1alpha1.ClusterBootstrapStatus{}, true},
		{
			"used diff",
			&kaprov1alpha1.ClusterBootstrapStatus{Used: true},
			&kaprov1alpha1.ClusterBootstrapStatus{Used: false},
			false,
		},
		{
			"deeply equal (no UsedAt)",
			&kaprov1alpha1.ClusterBootstrapStatus{
				Used:                true,
				IssuedCredentialFor: "de-prod-01",
				BoundCSRName:        "csr-abc",
			},
			&kaprov1alpha1.ClusterBootstrapStatus{
				Used:                true,
				IssuedCredentialFor: "de-prod-01",
				BoundCSRName:        "csr-abc",
			},
			true,
		},
		{
			// REGRESSION GUARD: this case failed with the previous `*a == *b`
			// implementation because timeA and timeB are distinct heap
			// allocations holding the same instant. The semantic-compare fix
			// makes this case pass.
			"semantically equal UsedAt with distinct pointer addresses",
			&kaprov1alpha1.ClusterBootstrapStatus{Used: true, UsedAt: &timeA},
			&kaprov1alpha1.ClusterBootstrapStatus{Used: true, UsedAt: &timeB},
			true,
		},
		{
			"genuinely different UsedAt",
			&kaprov1alpha1.ClusterBootstrapStatus{Used: true, UsedAt: &timeA},
			&kaprov1alpha1.ClusterBootstrapStatus{Used: true, UsedAt: func() *metav1.Time {
				t := metav1.NewTime(instant.Add(time.Hour))
				return &t
			}()},
			false,
		},
		{
			"one UsedAt nil, the other set",
			&kaprov1alpha1.ClusterBootstrapStatus{Used: true, UsedAt: nil},
			&kaprov1alpha1.ClusterBootstrapStatus{Used: true, UsedAt: &timeA},
			false,
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
		{
			// Defence-in-depth: even a duplicate ClientAuth must not pass.
			"duplicate client auth",
			[]certificatesv1.KeyUsage{certificatesv1.UsageClientAuth, certificatesv1.UsageClientAuth},
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
		fc   *kaprov1alpha1.Cluster
		want bool
	}{
		{
			name: "no bootstrap spec",
			fc:   &kaprov1alpha1.Cluster{},
			want: false,
		},
		{
			name: "no status yet",
			fc: &kaprov1alpha1.Cluster{
				Spec: kaprov1alpha1.ClusterSpec{Bootstrap: &kaprov1alpha1.ClusterBootstrapSpec{}},
			},
			want: true,
		},
		{
			name: "already used",
			fc: &kaprov1alpha1.Cluster{
				Spec: kaprov1alpha1.ClusterSpec{Bootstrap: &kaprov1alpha1.ClusterBootstrapSpec{}},
				Status: kaprov1alpha1.ClusterStatus{
					Bootstrap: &kaprov1alpha1.ClusterBootstrapStatus{Used: true},
				},
			},
			want: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// Spec-only branches don't need a client; nil is fine — the only
			// path that calls r.Get is the Secret-lookup branch, which is
			// exercised separately by TestShouldProvision_SecretBased.
			r := &ClusterBootstrapReconciler{PodNamespace: "kapro-system"}
			if got := r.shouldProvision(context.Background(), c.fc); got != c.want {
				t.Errorf("shouldProvision = %v, want %v", got, c.want)
			}
		})
	}
}

// TestShouldProvision_SecretBased exercises the token-freshness branch:
// when status.bootstrap.IssuedBootstrapKubeconfig points at a Secret, the
// hub re-issues iff the Secret is missing, lacks the
// `kapro.io/bootstrap-expires-at` annotation, holds a malformed annotation,
// or is within 15 minutes of expiry. This guards against spec.bootstrap.ttl
// being significantly longer than the TokenRequest TTL.
func TestShouldProvision_SecretBased(t *testing.T) {
	secretName := "kapro-bootstrap-kubeconfig-de-prod-01"
	fc := &kaprov1alpha1.Cluster{
		ObjectMeta: metav1.ObjectMeta{Name: "de-prod-01"},
		Spec:       kaprov1alpha1.ClusterSpec{Bootstrap: &kaprov1alpha1.ClusterBootstrapSpec{}},
		Status: kaprov1alpha1.ClusterStatus{
			Bootstrap: &kaprov1alpha1.ClusterBootstrapStatus{
				IssuedBootstrapKubeconfig: secretName,
			},
		},
	}

	mkSecret := func(annotation string) *corev1.Secret {
		s := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      secretName,
				Namespace: "kapro-system",
			},
		}
		if annotation != "" {
			s.Annotations = map[string]string{"kapro.io/bootstrap-expires-at": annotation}
		}
		return s
	}

	cases := []struct {
		name    string
		secret  *corev1.Secret
		want    bool
		because string
	}{
		{"missing secret", nil, true, "Secret deleted ⇒ re-issue"},
		{"no annotation", mkSecret(""), true, "missing expiry annotation ⇒ re-issue"},
		{"malformed annotation", mkSecret("not-a-timestamp"), true, "unparseable annotation ⇒ re-issue"},
		{"fresh token", mkSecret(time.Now().Add(45 * time.Minute).Format(time.RFC3339)), false, "fresh token within TTL ⇒ skip"},
		{"expiring within leeway", mkSecret(time.Now().Add(5 * time.Minute).Format(time.RFC3339)), true, "< 15m to expiry ⇒ re-issue"},
		{"already expired", mkSecret(time.Now().Add(-1 * time.Minute).Format(time.RFC3339)), true, "negative remaining ⇒ re-issue"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var r *ClusterBootstrapReconciler
			if c.secret == nil {
				r, _ = newBootstrapReconciler(t)
			} else {
				r, _ = newBootstrapReconciler(t, c.secret)
			}
			if got := r.shouldProvision(context.Background(), fc); got != c.want {
				t.Errorf("shouldProvision = %v, want %v (%s)", got, c.want, c.because)
			}
		})
	}
}

func TestEnsureBootstrapProvisionedRequestsDefaultAPIServerAudience(t *testing.T) {
	fc := &kaprov1alpha1.Cluster{
		ObjectMeta: metav1.ObjectMeta{Name: "de-prod-01"},
		Spec:       kaprov1alpha1.ClusterSpec{Bootstrap: &kaprov1alpha1.ClusterBootstrapSpec{}},
	}
	r, c := newBootstrapReconciler(t, fc)

	var captured *authv1.TokenRequest
	tokenExpiry := metav1.NewTime(time.Now().Add(bootstrapTokenLifetime))
	kubeClient := k8sfake.NewClientset()
	kubeClient.Fake.PrependReactor("create", "serviceaccounts", func(action k8stesting.Action) (bool, runtime.Object, error) {
		if action.GetSubresource() != "token" {
			return false, nil, nil
		}
		create, ok := action.(k8stesting.CreateAction)
		if !ok {
			t.Fatalf("token action type = %T, want CreateAction", action)
		}
		request, ok := create.GetObject().(*authv1.TokenRequest)
		if !ok {
			t.Fatalf("token action object type = %T, want *TokenRequest", create.GetObject())
		}
		captured = request.DeepCopy()
		response := request.DeepCopy()
		response.Status.Token = "bootstrap-token"
		response.Status.ExpirationTimestamp = tokenExpiry
		return true, response, nil
	})
	r.KubeClient = kubeClient

	res, err := r.ensureBootstrapProvisioned(context.Background(), fc)
	if err != nil {
		t.Fatalf("ensureBootstrapProvisioned: %v", err)
	}
	if res.RequeueAfter == 0 {
		t.Fatal("expected requeue after bootstrap material is issued")
	}
	if captured == nil {
		t.Fatal("TokenRequest was not captured")
	}
	if len(captured.Spec.Audiences) != 0 {
		t.Fatalf("bootstrap TokenRequest audiences = %v, want empty so kube-apiserver defaults apply", captured.Spec.Audiences)
	}
	if captured.Spec.ExpirationSeconds == nil || *captured.Spec.ExpirationSeconds != int64(bootstrapTokenLifetime.Seconds()) {
		t.Fatalf("expirationSeconds = %v, want %d", captured.Spec.ExpirationSeconds, int64(bootstrapTokenLifetime.Seconds()))
	}

	secret := &corev1.Secret{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "kapro-system", Name: "kapro-bootstrap-kubeconfig-de-prod-01"}, secret); err != nil {
		t.Fatalf("bootstrap kubeconfig Secret was not written: %v", err)
	}
	if got := secret.Annotations["kapro.io/bootstrap-expires-at"]; got != tokenExpiry.Format(time.RFC3339) {
		t.Fatalf("bootstrap expiry annotation = %q, want %q", got, tokenExpiry.Format(time.RFC3339))
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
		for _, want := range []string{"https://hub.example.com:6443", "kapro-bootstrap-de-prod-01", "current-context: kapro-de-prod-01"} {
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
