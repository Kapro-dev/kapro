package controller_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"testing"
	"time"

	certificatesv1 "k8s.io/api/certificates/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	certificatesv1client "k8s.io/client-go/kubernetes/typed/certificates/v1"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
	"kapro.io/kapro/internal/controller"
)

const kaproSystem = "kapro-system"

// fakeCertV1Client records UpdateApproval calls.
type fakeCertV1Client struct {
	certificatesv1client.CertificatesV1Interface
	csrClient *fakeCSRClientRec
}

func (f *fakeCertV1Client) CertificateSigningRequests() certificatesv1client.CertificateSigningRequestInterface {
	return f.csrClient
}

type fakeCSRClientRec struct {
	certificatesv1client.CertificateSigningRequestInterface
	lastApproval *certificatesv1.CertificateSigningRequest
	lastDenial   *certificatesv1.CertificateSigningRequest
}

func (f *fakeCSRClientRec) UpdateApproval(_ context.Context, _ string,
	csr *certificatesv1.CertificateSigningRequest,
	_ metav1.UpdateOptions) (*certificatesv1.CertificateSigningRequest, error) {
	for _, c := range csr.Status.Conditions {
		if c.Type == certificatesv1.CertificateApproved {
			f.lastApproval = csr.DeepCopy()
		} else if c.Type == certificatesv1.CertificateDenied {
			f.lastDenial = csr.DeepCopy()
		}
	}
	return csr, nil
}

// makeCSRPEM generates a real PEM-encoded CertificateRequest with the given subject.
func makeCSRPEM(t *testing.T, cn string, org []string) []byte {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	template := &x509.CertificateRequest{
		Subject: pkix.Name{
			CommonName:   cn,
			Organization: org,
		},
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, template, priv)
	if err != nil {
		t.Fatalf("create CSR DER: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})
}

// buildScheme returns a scheme with Kapro + certificates API groups registered.
func buildCSRScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := kaprov1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("AddToScheme kapro: %v", err)
	}
	if err := certificatesv1.AddToScheme(s); err != nil {
		t.Fatalf("AddToScheme certs: %v", err)
	}
	if err := corev1.AddToScheme(s); err != nil {
		t.Fatalf("AddToScheme core: %v", err)
	}
	if err := rbacv1.AddToScheme(s); err != nil {
		t.Fatalf("AddToScheme rbac: %v", err)
	}
	return s
}

// newReconciler creates a CSRApprovalReconciler backed by a fake client and a fake cert client.
func newCSRReconciler(scheme *runtime.Scheme, objs ...runtime.Object) (
	*controller.CSRApprovalReconciler, *fakeCSRClientRec) {

	clientObjs := make([]runtime.Object, 0, len(objs))
	clientObjs = append(clientObjs, objs...)
	fc := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kaprov1alpha1.BootstrapToken{}, &kaprov1alpha1.ManagedCluster{}).
		WithRuntimeObjects(clientObjs...).
		Build()
	fcc := &fakeCSRClientRec{}
	r := &controller.CSRApprovalReconciler{
		Client:     fc,
		CertClient: &fakeCertV1Client{csrClient: fcc},
		Recorder:   record.NewFakeRecorder(32),
	}
	return r, fcc
}

func reconcileCSR(t *testing.T, r *controller.CSRApprovalReconciler, csrName string) ctrl.Result {
	t.Helper()
	res, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: csrName},
	})
	if err != nil {
		t.Fatalf("Reconcile returned unexpected error: %v", err)
	}
	return res
}

// ---- helpers to build test objects ------------------------------------------

func validBootstrapToken(clusterName string) *kaprov1alpha1.BootstrapToken {
	return &kaprov1alpha1.BootstrapToken{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "bt-" + clusterName,
			Namespace: kaproSystem,
		},
		Spec: kaprov1alpha1.BootstrapTokenSpec{
			ClusterName: clusterName,
			TokenHash:   "aabbccddaabbccddaabbccddaabbccddaabbccddaabbccddaabbccddaabbccdd",
			ExpiresAt:   metav1.NewTime(time.Now().Add(24 * time.Hour)),
		},
		Status: kaprov1alpha1.BootstrapTokenStatus{Used: false},
	}
}

func expiredBootstrapToken(clusterName string) *kaprov1alpha1.BootstrapToken {
	bt := validBootstrapToken(clusterName)
	bt.Name = "bt-expired-" + clusterName
	bt.Spec.ExpiresAt = metav1.NewTime(time.Now().Add(-1 * time.Hour))
	return bt
}

func usedBootstrapToken(clusterName string) *kaprov1alpha1.BootstrapToken {
	bt := validBootstrapToken(clusterName)
	bt.Name = "bt-used-" + clusterName
	now := metav1.Now()
	bt.Status.Used = true
	bt.Status.UsedAt = &now
	return bt
}

func bootstrapCSR(t *testing.T, name, clusterName string) *certificatesv1.CertificateSigningRequest {
	t.Helper()
	csrPEM := makeCSRPEM(t, "kapro-cluster:"+clusterName, []string{"kapro:cluster-controllers"})
	return &certificatesv1.CertificateSigningRequest{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: certificatesv1.CertificateSigningRequestSpec{
			Request:    csrPEM,
			SignerName: "kubernetes.io/kube-apiserver-client",
			// Bootstrap SA is the requester.
			Username: "system:serviceaccount:" + kaproSystem + ":kapro-bootstrap-" + clusterName,
			Usages:   []certificatesv1.KeyUsage{certificatesv1.UsageClientAuth},
		},
	}
}

func renewalCSR(t *testing.T, name, clusterName string) *certificatesv1.CertificateSigningRequest {
	t.Helper()
	csrPEM := makeCSRPEM(t, "kapro-cluster:"+clusterName, []string{"kapro:cluster-controllers"})
	return &certificatesv1.CertificateSigningRequest{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: certificatesv1.CertificateSigningRequestSpec{
			Request:    csrPEM,
			SignerName: "kubernetes.io/kube-apiserver-client",
			// Renewal: requester is the cluster user identity from the previous cert.
			Username: "kapro-cluster:" + clusterName,
			Usages:   []certificatesv1.KeyUsage{certificatesv1.UsageClientAuth},
		},
	}
}

func managedCluster(name string) *kaprov1alpha1.ManagedCluster {
	return &kaprov1alpha1.ManagedCluster{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       kaprov1alpha1.ManagedClusterSpec{EnvironmentRef: name},
	}
}

// ---- tests ------------------------------------------------------------------

func TestCSRApproval_SkipsWrongSigner(t *testing.T) {
	scheme := buildCSRScheme(t)
	csrPEM := makeCSRPEM(t, "kapro-cluster:test-cluster", []string{"kapro:cluster-controllers"})
	csr := &certificatesv1.CertificateSigningRequest{
		ObjectMeta: metav1.ObjectMeta{Name: "wrong-signer"},
		Spec: certificatesv1.CertificateSigningRequestSpec{
			Request:    csrPEM,
			SignerName: "kubernetes.io/kubelet-serving", // wrong signer
			Username:   "system:serviceaccount:" + kaproSystem + ":kapro-bootstrap-test-cluster",
			Usages:     []certificatesv1.KeyUsage{certificatesv1.UsageClientAuth},
		},
	}
	r, fcc := newCSRReconciler(scheme, csr)
	reconcileCSR(t, r, "wrong-signer")

	if fcc.lastApproval != nil || fcc.lastDenial != nil {
		t.Error("CSR with wrong signer should be silently skipped — no approval or denial")
	}
}

func TestCSRApproval_DeniesWrongOrganization(t *testing.T) {
	scheme := buildCSRScheme(t)
	// Attacker submits O=system:masters trying for privilege escalation.
	csrPEM := makeCSRPEM(t, "kapro-cluster:evil", []string{"system:masters"})
	csr := &certificatesv1.CertificateSigningRequest{
		ObjectMeta: metav1.ObjectMeta{Name: "priv-esc"},
		Spec: certificatesv1.CertificateSigningRequestSpec{
			Request:    csrPEM,
			SignerName: "kubernetes.io/kube-apiserver-client",
			Username:   "system:serviceaccount:" + kaproSystem + ":kapro-bootstrap-evil",
			Usages:     []certificatesv1.KeyUsage{certificatesv1.UsageClientAuth},
		},
	}
	r, fcc := newCSRReconciler(scheme, csr)
	reconcileCSR(t, r, "priv-esc")

	// Wrong org → isKaproCSR returns false → silent skip (not denied, just ignored).
	if fcc.lastApproval != nil {
		t.Error("CSR with wrong org should never be approved")
	}
}

func TestCSRApproval_DeniesExtraUsages(t *testing.T) {
	scheme := buildCSRScheme(t)
	csrPEM := makeCSRPEM(t, "kapro-cluster:cluster1", []string{"kapro:cluster-controllers"})
	csr := &certificatesv1.CertificateSigningRequest{
		ObjectMeta: metav1.ObjectMeta{Name: "extra-usages"},
		Spec: certificatesv1.CertificateSigningRequestSpec{
			Request:    csrPEM,
			SignerName: "kubernetes.io/kube-apiserver-client",
			Username:   "system:serviceaccount:" + kaproSystem + ":kapro-bootstrap-cluster1",
			// Extra server auth usage — must be rejected.
			Usages: []certificatesv1.KeyUsage{certificatesv1.UsageClientAuth, certificatesv1.UsageServerAuth},
		},
	}
	r, fcc := newCSRReconciler(scheme, csr)
	reconcileCSR(t, r, "extra-usages")

	if fcc.lastApproval != nil {
		t.Error("CSR with extra usages (server auth) should never be approved")
	}
}

func TestCSRApproval_BootstrapHappyPath(t *testing.T) {
	const clusterName = "prod-eu"
	scheme := buildCSRScheme(t)
	bt := validBootstrapToken(clusterName)
	csr := bootstrapCSR(t, "csr-boot-prod-eu", clusterName)

	r, fcc := newCSRReconciler(scheme, bt, csr)
	reconcileCSR(t, r, "csr-boot-prod-eu")

	if fcc.lastApproval == nil {
		t.Fatal("expected bootstrap CSR to be approved")
	}
	if fcc.lastDenial != nil {
		t.Error("bootstrap CSR should not be denied")
	}

	// Verify ManagedCluster was created.
	mc := &kaprov1alpha1.ManagedCluster{}
	if err := r.Get(context.Background(), types.NamespacedName{Name: clusterName}, mc); err != nil {
		t.Errorf("ManagedCluster %q should have been created: %v", clusterName, err)
	}

	// Verify BootstrapToken was marked used.
	var latestBT kaprov1alpha1.BootstrapToken
	if err := r.Get(context.Background(), types.NamespacedName{
		Name: bt.Name, Namespace: kaproSystem}, &latestBT); err != nil {
		t.Fatalf("get BootstrapToken: %v", err)
	}
	if !latestBT.Status.Used {
		t.Error("BootstrapToken should be marked used after bootstrap")
	}
}

func TestCSRApproval_BootstrapExpiredToken(t *testing.T) {
	const clusterName = "prod-us"
	scheme := buildCSRScheme(t)
	bt := expiredBootstrapToken(clusterName)
	csr := bootstrapCSR(t, "csr-expired", clusterName)

	r, fcc := newCSRReconciler(scheme, bt, csr)
	reconcileCSR(t, r, "csr-expired")

	if fcc.lastApproval != nil {
		t.Error("CSR with expired token should not be approved")
	}
	if fcc.lastDenial == nil {
		t.Error("CSR with expired token should be denied")
	}
}

func TestCSRApproval_BootstrapUsedToken(t *testing.T) {
	const clusterName = "prod-as"
	scheme := buildCSRScheme(t)
	bt := usedBootstrapToken(clusterName)
	csr := bootstrapCSR(t, "csr-used-token", clusterName)

	r, fcc := newCSRReconciler(scheme, bt, csr)
	reconcileCSR(t, r, "csr-used-token")

	if fcc.lastApproval != nil {
		t.Error("replayed token (already used) should not be approved")
	}
	if fcc.lastDenial == nil {
		t.Error("replayed token should be denied")
	}
}

func TestCSRApproval_RenewalHappyPath(t *testing.T) {
	const clusterName = "staging-eu"
	scheme := buildCSRScheme(t)
	mc := managedCluster(clusterName)
	csr := renewalCSR(t, "csr-renew-staging", clusterName)

	r, fcc := newCSRReconciler(scheme, mc, csr)
	reconcileCSR(t, r, "csr-renew-staging")

	if fcc.lastApproval == nil {
		t.Fatal("expected renewal CSR to be approved")
	}
	if fcc.lastDenial != nil {
		t.Error("renewal CSR should not be denied")
	}
}

func TestCSRApproval_RenewalMissingManagedCluster(t *testing.T) {
	const clusterName = "ghost-cluster"
	scheme := buildCSRScheme(t)
	// No ManagedCluster in the fake client.
	csr := renewalCSR(t, "csr-renew-ghost", clusterName)

	r, fcc := newCSRReconciler(scheme, csr)
	reconcileCSR(t, r, "csr-renew-ghost")

	if fcc.lastApproval != nil {
		t.Error("renewal from unregistered cluster should not be approved")
	}
	if fcc.lastDenial == nil {
		t.Error("renewal from unregistered cluster should be denied")
	}
}

func TestCSRApproval_AlreadyApproved_Idempotent(t *testing.T) {
	const clusterName = "already-approved"
	scheme := buildCSRScheme(t)
	csrPEM := makeCSRPEM(t, "kapro-cluster:"+clusterName, []string{"kapro:cluster-controllers"})
	csr := &certificatesv1.CertificateSigningRequest{
		ObjectMeta: metav1.ObjectMeta{Name: "csr-already-approved"},
		Spec: certificatesv1.CertificateSigningRequestSpec{
			Request:    csrPEM,
			SignerName: "kubernetes.io/kube-apiserver-client",
			Username:   "kapro-cluster:" + clusterName,
			Usages:     []certificatesv1.KeyUsage{certificatesv1.UsageClientAuth},
		},
		Status: certificatesv1.CertificateSigningRequestStatus{
			Conditions: []certificatesv1.CertificateSigningRequestCondition{
				{Type: certificatesv1.CertificateApproved, Status: corev1.ConditionTrue},
			},
		},
	}
	r, fcc := newCSRReconciler(scheme, csr)
	reconcileCSR(t, r, "csr-already-approved")

	// Should be a no-op — cert client must NOT be called again.
	if fcc.lastApproval != nil {
		t.Error("already-approved CSR should be skipped, not re-approved")
	}
}

func TestCSRApproval_MissingCSR_ReturnsNil(t *testing.T) {
	scheme := buildCSRScheme(t)
	r, fcc := newCSRReconciler(scheme)
	// Reconcile with a name that doesn't exist — should return nil (NotFound is ignored).
	reconcileCSR(t, r, "does-not-exist")
	if fcc.lastApproval != nil || fcc.lastDenial != nil {
		t.Error("missing CSR should be a clean no-op")
	}
}

func TestCSRApproval_BootstrapSAMismatch(t *testing.T) {
	// Security: bootstrap SA for cluster-a must NOT be able to register as cluster-b.
	const clusterA = "cluster-a"
	const clusterB = "cluster-b"
	scheme := buildCSRScheme(t)

	// BootstrapToken exists for cluster-b.
	btB := validBootstrapToken(clusterB)

	// CSR claims CN=kapro-cluster:cluster-b but is signed by cluster-a's bootstrap SA.
	csrPEM := makeCSRPEM(t, "kapro-cluster:"+clusterB, []string{"kapro:cluster-controllers"})
	csr := &certificatesv1.CertificateSigningRequest{
		ObjectMeta: metav1.ObjectMeta{Name: "csr-sa-mismatch"},
		Spec: certificatesv1.CertificateSigningRequestSpec{
			Request:    csrPEM,
			SignerName: "kubernetes.io/kube-apiserver-client",
			// cluster-a's bootstrap SA requesting cluster-b's identity.
			Username: "system:serviceaccount:" + kaproSystem + ":kapro-bootstrap-" + clusterA,
			Usages:   []certificatesv1.KeyUsage{certificatesv1.UsageClientAuth},
		},
	}
	r, fcc := newCSRReconciler(scheme, btB, csr)
	reconcileCSR(t, r, "csr-sa-mismatch")

	if fcc.lastApproval != nil {
		t.Error("SA for cluster-a must not be allowed to register as cluster-b")
	}
	if fcc.lastDenial == nil {
		t.Error("SA/cluster mismatch must result in an explicit denial")
	}
}

func TestCSRApproval_BoundCSRRetryIsIdempotent(t *testing.T) {
	// If the CSR approval call fails transiently after the token is marked used,
	// the next reconcile must still approve (not deny) the same CSR.
	const clusterName = "retry-cluster"
	const csrName = "csr-retry"
	scheme := buildCSRScheme(t)

	// Token already marked used, with BoundCSRName pointing at this CSR.
	now := metav1.Now()
	bt := &kaprov1alpha1.BootstrapToken{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "bt-retry",
			Namespace: kaproSystem,
		},
		Spec: kaprov1alpha1.BootstrapTokenSpec{
			ClusterName: clusterName,
			TokenHash:   "aabbccddaabbccddaabbccddaabbccddaabbccddaabbccddaabbccddaabbccdd",
			ExpiresAt:   metav1.NewTime(time.Now().Add(24 * time.Hour)),
		},
		Status: kaprov1alpha1.BootstrapTokenStatus{
			Used:         true,
			UsedAt:       &now,
			BoundCSRName: csrName, // bound to this exact CSR
		},
	}
	csr := bootstrapCSR(t, csrName, clusterName)

	r, fcc := newCSRReconciler(scheme, bt, csr)
	reconcileCSR(t, r, csrName)

	if fcc.lastApproval == nil {
		t.Error("retry of already-bound CSR should be approved, not denied")
	}
	if fcc.lastDenial != nil {
		t.Error("retry of already-bound CSR should not be denied")
	}
}
