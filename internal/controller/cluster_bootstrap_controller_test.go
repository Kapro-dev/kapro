package controller

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
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
)

// makeTestCSR is a small helper used across bootstrap-controller tests to
// build a valid Kapro-shaped CSR (correct signer, CN, O, usages) with a
// caller-supplied Username so we can exercise the bootstrap-SA name check.
func makeTestCSR(t *testing.T, cn string, orgs []string, username string) *certificatesv1.CertificateSigningRequest {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: cn, Organization: orgs},
	}, key)
	if err != nil {
		t.Fatalf("create CSR: %v", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})
	return &certificatesv1.CertificateSigningRequest{
		ObjectMeta: metav1.ObjectMeta{Name: "test-csr"},
		Spec: certificatesv1.CertificateSigningRequestSpec{
			SignerName: csrSigner,
			Request:    pemBytes,
			Username:   username,
			Usages:     []certificatesv1.KeyUsage{certificatesv1.UsageClientAuth},
		},
	}
}

// newScheme returns a runtime.Scheme that knows the Kapro types + the
// Kubernetes built-ins the bootstrap reconciler touches.
func newBootstrapScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := kaprov1alpha2.AddToScheme(s); err != nil {
		t.Fatalf("kapro AddToScheme: %v", err)
	}
	if err := certificatesv1.AddToScheme(s); err != nil {
		t.Fatalf("certificates AddToScheme: %v", err)
	}
	if err := rbacv1.AddToScheme(s); err != nil {
		t.Fatalf("rbac AddToScheme: %v", err)
	}
	if err := corev1.AddToScheme(s); err != nil {
		t.Fatalf("core AddToScheme: %v", err)
	}
	return s
}

// newReconciler builds a reconciler backed by a fake client preloaded with
// the supplied objects. KubeClient and CertClient are left nil — they are
// only required for paths under test that actually call them.
func newBootstrapReconciler(t *testing.T, objs ...client.Object) (*ClusterBootstrapReconciler, client.Client) {
	t.Helper()
	scheme := newBootstrapScheme(t)
	fc := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		WithStatusSubresource(&kaprov1alpha2.Cluster{}).
		Build()
	return &ClusterBootstrapReconciler{
		Client:       fc,
		Scheme:       scheme,
		Recorder:     record.NewFakeRecorder(16),
		HubAPIURL:    "https://hub.example.com:6443",
		HubCAData:    []byte("test-ca"),
		PodNamespace: "kapro-system",
	}, fc
}

func TestComputeExpiresAt_DefaultsTo24h(t *testing.T) {
	created := metav1.NewTime(time.Now().Add(-1 * time.Hour))
	fc := &kaprov1alpha2.Cluster{
		ObjectMeta: metav1.ObjectMeta{Name: "de-prod-01", CreationTimestamp: created},
		Spec: kaprov1alpha2.ClusterSpec{
			Bootstrap: &kaprov1alpha2.ClusterBootstrapSpec{},
		},
	}
	r, c := newBootstrapReconciler(t, fc)
	ctx := context.Background()

	mutated, err := r.computeExpiresAt(ctx, fc)
	if err != nil {
		t.Fatalf("computeExpiresAt: %v", err)
	}
	if !mutated {
		t.Fatal("expected mutation on first observation")
	}

	got := &kaprov1alpha2.Cluster{}
	if err := c.Get(ctx, client.ObjectKey{Name: "de-prod-01"}, got); err != nil {
		t.Fatalf("re-fetch: %v", err)
	}
	if got.Spec.Bootstrap.ExpiresAt == nil {
		t.Fatal("ExpiresAt was not set")
	}
	want := created.Add(defaultBootstrapTTL).Truncate(time.Second)
	if !got.Spec.Bootstrap.ExpiresAt.Time.Truncate(time.Second).Equal(want) {
		t.Errorf("ExpiresAt = %v, want %v", got.Spec.Bootstrap.ExpiresAt, want)
	}
}

func TestComputeExpiresAt_HonoursTTL(t *testing.T) {
	created := metav1.NewTime(time.Now().Add(-30 * time.Minute))
	fc := &kaprov1alpha2.Cluster{
		ObjectMeta: metav1.ObjectMeta{Name: "de-prod-01", CreationTimestamp: created},
		Spec: kaprov1alpha2.ClusterSpec{
			Bootstrap: &kaprov1alpha2.ClusterBootstrapSpec{TTL: "6h"},
		},
	}
	r, c := newBootstrapReconciler(t, fc)

	if _, err := r.computeExpiresAt(context.Background(), fc); err != nil {
		t.Fatalf("computeExpiresAt: %v", err)
	}
	got := &kaprov1alpha2.Cluster{}
	_ = c.Get(context.Background(), client.ObjectKey{Name: "de-prod-01"}, got)
	if got.Spec.Bootstrap.ExpiresAt == nil {
		t.Fatal("ExpiresAt nil")
	}
	want := created.Add(6 * time.Hour).Truncate(time.Second)
	if !got.Spec.Bootstrap.ExpiresAt.Time.Truncate(time.Second).Equal(want) {
		t.Errorf("ExpiresAt = %v, want %v", got.Spec.Bootstrap.ExpiresAt.Time, want)
	}
}

func TestComputeExpiresAt_CapsAtMax(t *testing.T) {
	created := metav1.NewTime(time.Now())
	fc := &kaprov1alpha2.Cluster{
		ObjectMeta: metav1.ObjectMeta{Name: "de-prod-01", CreationTimestamp: created},
		Spec: kaprov1alpha2.ClusterSpec{
			Bootstrap: &kaprov1alpha2.ClusterBootstrapSpec{TTL: "720h"}, // 30 days, way over max
		},
	}
	r, c := newBootstrapReconciler(t, fc)

	if _, err := r.computeExpiresAt(context.Background(), fc); err != nil {
		t.Fatalf("computeExpiresAt: %v", err)
	}
	got := &kaprov1alpha2.Cluster{}
	_ = c.Get(context.Background(), client.ObjectKey{Name: "de-prod-01"}, got)
	max := created.Add(bootstrapMaxTTL).Truncate(time.Second)
	if !got.Spec.Bootstrap.ExpiresAt.Time.Truncate(time.Second).Equal(max) {
		t.Errorf("ExpiresAt = %v, want capped at %v", got.Spec.Bootstrap.ExpiresAt.Time, max)
	}
}

func TestComputeExpiresAt_RejectsInvalidTTL(t *testing.T) {
	fc := &kaprov1alpha2.Cluster{
		ObjectMeta: metav1.ObjectMeta{Name: "de-prod-01"},
		Spec: kaprov1alpha2.ClusterSpec{
			Bootstrap: &kaprov1alpha2.ClusterBootstrapSpec{TTL: "garbage"},
		},
	}
	r, _ := newBootstrapReconciler(t, fc)

	mutated, err := r.computeExpiresAt(context.Background(), fc)
	if err == nil {
		t.Fatal("expected error for invalid TTL")
	}
	if mutated {
		t.Fatal("should not mutate on invalid TTL")
	}
}

func TestComputeExpiresAt_RejectsZeroTTL(t *testing.T) {
	fc := &kaprov1alpha2.Cluster{
		ObjectMeta: metav1.ObjectMeta{Name: "de-prod-01"},
		Spec: kaprov1alpha2.ClusterSpec{
			Bootstrap: &kaprov1alpha2.ClusterBootstrapSpec{TTL: "0s"},
		},
	}
	r, _ := newBootstrapReconciler(t, fc)

	if _, err := r.computeExpiresAt(context.Background(), fc); err == nil {
		t.Fatal("expected error for zero TTL")
	}
}

func TestComputeExpiresAt_NoopWhenAlreadySet(t *testing.T) {
	already := metav1.NewTime(time.Now().Add(2 * time.Hour))
	fc := &kaprov1alpha2.Cluster{
		ObjectMeta: metav1.ObjectMeta{Name: "de-prod-01"},
		Spec: kaprov1alpha2.ClusterSpec{
			Bootstrap: &kaprov1alpha2.ClusterBootstrapSpec{ExpiresAt: &already},
		},
	}
	r, _ := newBootstrapReconciler(t, fc)

	mutated, err := r.computeExpiresAt(context.Background(), fc)
	if err != nil {
		t.Fatalf("computeExpiresAt: %v", err)
	}
	if mutated {
		t.Fatal("should not mutate when ExpiresAt already set")
	}
}

func TestExpired(t *testing.T) {
	past := metav1.NewTime(time.Now().Add(-1 * time.Hour))
	future := metav1.NewTime(time.Now().Add(1 * time.Hour))
	cases := []struct {
		name string
		fc   *kaprov1alpha2.Cluster
		want bool
	}{
		{
			name: "past expiresAt + unused = expired",
			fc: &kaprov1alpha2.Cluster{
				Spec: kaprov1alpha2.ClusterSpec{Bootstrap: &kaprov1alpha2.ClusterBootstrapSpec{ExpiresAt: &past}},
			},
			want: true,
		},
		{
			name: "future expiresAt = not expired",
			fc: &kaprov1alpha2.Cluster{
				Spec: kaprov1alpha2.ClusterSpec{Bootstrap: &kaprov1alpha2.ClusterBootstrapSpec{ExpiresAt: &future}},
			},
			want: false,
		},
		{
			name: "past expiresAt + used = NOT expired (already registered)",
			fc: &kaprov1alpha2.Cluster{
				Spec: kaprov1alpha2.ClusterSpec{Bootstrap: &kaprov1alpha2.ClusterBootstrapSpec{ExpiresAt: &past}},
				Status: kaprov1alpha2.ClusterStatus{
					Bootstrap: &kaprov1alpha2.ClusterBootstrapStatus{Used: true},
				},
			},
			want: false,
		},
		{
			name: "no expiresAt set = not expired",
			fc: &kaprov1alpha2.Cluster{
				Spec: kaprov1alpha2.ClusterSpec{Bootstrap: &kaprov1alpha2.ClusterBootstrapSpec{}},
			},
			want: false,
		},
	}
	r := &ClusterBootstrapReconciler{}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := r.expired(c.fc); got != c.want {
				t.Errorf("expired = %v, want %v", got, c.want)
			}
		})
	}
}

func TestHandleSuspended_SetsReconcilingFalseAndClearsStalled(t *testing.T) {
	fc := &kaprov1alpha2.Cluster{
		ObjectMeta: metav1.ObjectMeta{Name: "de-prod-01"},
		Spec:       kaprov1alpha2.ClusterSpec{Suspend: true},
		Status: kaprov1alpha2.ClusterStatus{
			Conditions: []metav1.Condition{
				{Type: kaprov1alpha2.ConditionTypeStalled, Status: metav1.ConditionTrue, Reason: "BootstrapExpired"},
			},
		},
	}
	r, c := newBootstrapReconciler(t, fc)

	if _, err := r.handleSuspended(context.Background(), fc); err != nil {
		t.Fatalf("handleSuspended: %v", err)
	}
	got := &kaprov1alpha2.Cluster{}
	_ = c.Get(context.Background(), client.ObjectKey{Name: "de-prod-01"}, got)

	if cond := apimeta.FindStatusCondition(got.Status.Conditions, kaprov1alpha2.ConditionTypeReconciling); cond == nil ||
		cond.Status != metav1.ConditionFalse || cond.Reason != "Suspended" {
		t.Errorf("Reconciling condition = %+v; want False/Suspended", cond)
	}
	if cond := apimeta.FindStatusCondition(got.Status.Conditions, kaprov1alpha2.ConditionTypeStalled); cond != nil {
		t.Errorf("Stalled condition should be cleared; got %+v", cond)
	}
}

func TestReconcile_NoBootstrapSpec_NoOp(t *testing.T) {
	fc := &kaprov1alpha2.Cluster{
		ObjectMeta: metav1.ObjectMeta{Name: "imported-from-gcp-fleet"},
		Spec:       kaprov1alpha2.ClusterSpec{}, // no Bootstrap
	}
	r, _ := newBootstrapReconciler(t, fc)

	res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKey{Name: fc.Name}})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !res.IsZero() {
		t.Errorf("Reconcile should be no-op without bootstrap spec; got %+v", res)
	}
}

// TestReconcile_NoBootstrapSpec_StaleFinalizer covers the edge case where
// spec.bootstrap was set (and a finalizer added), then removed without
// deleting the FleetCluster. The reconciler must drop the now-orphan
// finalizer so the resource stays deletable.
func TestReconcile_NoBootstrapSpec_StaleFinalizer(t *testing.T) {
	fc := &kaprov1alpha2.Cluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "de-prod-01",
			Finalizers: []string{kaprov1alpha2.ClusterFinalizer},
		},
		Spec: kaprov1alpha2.ClusterSpec{}, // bootstrap removed
	}
	r, c := newBootstrapReconciler(t, fc)

	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKey{Name: fc.Name}})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	got := &kaprov1alpha2.Cluster{}
	if err := c.Get(context.Background(), client.ObjectKey{Name: fc.Name}, got); err != nil {
		t.Fatalf("re-fetch: %v", err)
	}
	if containsString(got.Finalizers, kaprov1alpha2.ClusterFinalizer) {
		t.Errorf("stale finalizer should be dropped; got %v", got.Finalizers)
	}
}

func TestReconcile_AddsFinalizer(t *testing.T) {
	fc := &kaprov1alpha2.Cluster{
		ObjectMeta: metav1.ObjectMeta{Name: "de-prod-01"},
		Spec: kaprov1alpha2.ClusterSpec{
			Bootstrap: &kaprov1alpha2.ClusterBootstrapSpec{TTL: "1h"},
		},
	}
	r, c := newBootstrapReconciler(t, fc)

	res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKey{Name: fc.Name}})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if res.RequeueAfter == 0 {
		t.Error("expected RequeueAfter to be set after adding finalizer")
	}
	got := &kaprov1alpha2.Cluster{}
	_ = c.Get(context.Background(), client.ObjectKey{Name: fc.Name}, got)
	if !containsString(got.Finalizers, kaprov1alpha2.ClusterFinalizer) {
		t.Errorf("finalizer not added; got %v", got.Finalizers)
	}
}

func TestReconcile_VaultBootstrapMaterialFailsClosed(t *testing.T) {
	future := metav1.NewTime(time.Now().Add(1 * time.Hour))
	fc := &kaprov1alpha2.Cluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "de-prod-01",
			Finalizers: []string{kaprov1alpha2.ClusterFinalizer},
		},
		Spec: kaprov1alpha2.ClusterSpec{
			Bootstrap: &kaprov1alpha2.ClusterBootstrapSpec{
				ExpiresAt: &future,
				MaterialSource: &kaprov1alpha2.ClusterBootstrapMaterialSource{
					Type: kaprov1alpha2.ClusterBootstrapMaterialVault,
					Vault: &kaprov1alpha2.VaultBootstrapMaterialSource{
						Path: "kapro/bootstrap/de-prod-01",
					},
				},
			},
		},
	}
	r, c := newBootstrapReconciler(t, fc)

	res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKey{Name: fc.Name}})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !res.IsZero() {
		t.Errorf("Reconcile should stop without requeue for disabled Vault bootstrap; got %+v", res)
	}

	got := &kaprov1alpha2.Cluster{}
	if err := c.Get(context.Background(), client.ObjectKey{Name: fc.Name}, got); err != nil {
		t.Fatalf("re-fetch Cluster: %v", err)
	}
	if got.Status.Bootstrap != nil && got.Status.Bootstrap.IssuedBootstrapKubeconfig != "" {
		t.Fatalf("Vault bootstrap must not fall back to Kubernetes Secret, got status.bootstrap=%+v", got.Status.Bootstrap)
	}
	if cond := apimeta.FindStatusCondition(got.Status.Conditions, kaprov1alpha2.ConditionTypeStalled); cond == nil ||
		cond.Status != metav1.ConditionTrue || cond.Reason != "BootstrapVaultDisabled" {
		t.Fatalf("Stalled condition = %+v; want True/BootstrapVaultDisabled", cond)
	}

	secret := &corev1.Secret{}
	err = c.Get(context.Background(), client.ObjectKey{Namespace: "kapro-system", Name: "kapro-bootstrap-kubeconfig-de-prod-01"}, secret)
	if err == nil {
		t.Fatal("Vault bootstrap path created a Kubernetes Secret fallback")
	}
}

// TestReconcile_CrashRecovery_ApprovesPendingCSR verifies the END-TO-END
// crash-recovery path through the public Reconcile entry point: when the
// controller previously marked status.bootstrap.Used=true with
// BoundCSRName=csr-X but crashed before UpdateApproval landed, the next
// Reconcile MUST re-process csr-X and approve it.
//
// This was broken in the originally-shipped PR-2: Reconcile Phase 3
// short-circuited to handleRegistered as soon as Used==true, so
// processCSRsForCluster was never reached and csr-X stayed pending
// forever (Copilot review on PR #57 caught this). The fix moved CSR
// processing to a phase that runs regardless of Used.
//
// TestProcessCSRsForCluster_RecoversFromCrashMidApprove (below) only
// exercises processCSRsForCluster directly and does NOT detect the
// Reconcile-level regression; this test is the higher-fidelity guard.
func TestReconcile_CrashRecovery_ApprovesPendingCSR(t *testing.T) {
	clusterName := "de-prod-01"
	bootstrapSA := "system:serviceaccount:kapro-system:kapro-bootstrap-" + clusterName

	csr := makeTestCSR(t, csrCNPrefix+clusterName, []string{csrOrganization}, bootstrapSA)
	csr.Name = "csr-crashed-mid-approve"
	// Pending: no Approved/Denied conditions yet.

	usedAt := metav1.Now()
	fc := &kaprov1alpha2.Cluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:       clusterName,
			Finalizers: []string{kaprov1alpha2.ClusterFinalizer},
		},
		Spec: kaprov1alpha2.ClusterSpec{
			Bootstrap: &kaprov1alpha2.ClusterBootstrapSpec{
				ExpiresAt: &metav1.Time{Time: time.Now().Add(1 * time.Hour)},
			},
		},
		Status: kaprov1alpha2.ClusterStatus{
			Bootstrap: &kaprov1alpha2.ClusterBootstrapStatus{
				Used:                true,
				UsedAt:              &usedAt,
				IssuedCredentialFor: clusterName,
				BoundCSRName:        csr.Name,
			},
		},
	}

	r, _ := newBootstrapReconciler(t, fc, csr)
	fakeCertClient := k8sfake.NewClientset(csr)
	r.CertClient = fakeCertClient.CertificatesV1()

	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKey{Name: fc.Name}}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	got, err := fakeCertClient.CertificatesV1().CertificateSigningRequests().Get(context.Background(), csr.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("re-fetch CSR: %v", err)
	}
	if !isCSRApproved(got) {
		t.Fatalf("Reconcile must re-approve a previously-bound pending CSR; status=%+v", got.Status)
	}
}

// TestProcessCSRsForCluster_RecoversFromCrashMidApprove verifies that a CSR
// which was marked as the BoundCSRName but never reached Approved status
// (e.g., controller crashed between markBootstrapUsed and approveCSR) gets
// re-processed on the next reconcile rather than being skipped forever.
func TestProcessCSRsForCluster_RecoversFromCrashMidApprove(t *testing.T) {
	// Spoke SA name + cluster name match the bootstrap convention.
	clusterName := "de-prod-01"
	bootstrapSA := "system:serviceaccount:kapro-system:kapro-bootstrap-" + clusterName

	// CSR already exists, properly signed, but never approved. Its name matches
	// the previously-bound CSR in FleetCluster.status.bootstrap.BoundCSRName.
	csr := makeTestCSR(t, csrCNPrefix+clusterName, []string{csrOrganization}, bootstrapSA)
	csr.Name = "csr-pending-mid-crash"

	// FleetCluster is in the "Used but never approved" state.
	usedAt := metav1.Now()
	fc := &kaprov1alpha2.Cluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:       clusterName,
			Finalizers: []string{kaprov1alpha2.ClusterFinalizer},
		},
		Spec: kaprov1alpha2.ClusterSpec{
			Bootstrap: &kaprov1alpha2.ClusterBootstrapSpec{
				ExpiresAt: &metav1.Time{Time: time.Now().Add(1 * time.Hour)},
			},
		},
		Status: kaprov1alpha2.ClusterStatus{
			Bootstrap: &kaprov1alpha2.ClusterBootstrapStatus{
				Used:                true,
				UsedAt:              &usedAt,
				IssuedCredentialFor: clusterName,
				BoundCSRName:        csr.Name,
			},
		},
	}

	// The controller-runtime client (used by r.List for CSRs) and the typed
	// CertClient (used by UpdateApproval) must BOTH see the CSR.
	r, _ := newBootstrapReconciler(t, fc, csr)
	fakeClient := k8sfake.NewClientset(csr)
	r.CertClient = fakeClient.CertificatesV1()

	res, err := r.processCSRsForCluster(context.Background(), fc)
	if err != nil {
		t.Fatalf("processCSRsForCluster: %v", err)
	}
	if res.RequeueAfter == 0 {
		t.Fatal("expected requeue after re-processing crashed CSR")
	}

	got, err := fakeClient.CertificatesV1().CertificateSigningRequests().Get(context.Background(), csr.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("re-fetch CSR: %v", err)
	}
	if !isCSRApproved(got) {
		t.Fatalf("CSR should have been approved on retry; status=%+v", got.Status)
	}
}

// TestProcessCSRsForCluster_SkipsFinalizedCSRs verifies that approved/denied
// CSRs are NOT reprocessed even if their CN matches the cluster.
func TestProcessCSRsForCluster_SkipsFinalizedCSRs(t *testing.T) {
	clusterName := "de-prod-01"
	bootstrapSA := "system:serviceaccount:kapro-system:kapro-bootstrap-" + clusterName

	approvedCSR := makeTestCSR(t, csrCNPrefix+clusterName, []string{csrOrganization}, bootstrapSA)
	approvedCSR.Name = "csr-already-approved"
	approvedCSR.Status.Conditions = []certificatesv1.CertificateSigningRequestCondition{
		{Type: certificatesv1.CertificateApproved, Status: "True"},
	}
	deniedCSR := makeTestCSR(t, csrCNPrefix+clusterName, []string{csrOrganization}, bootstrapSA)
	deniedCSR.Name = "csr-denied"
	deniedCSR.Status.Conditions = []certificatesv1.CertificateSigningRequestCondition{
		{Type: certificatesv1.CertificateDenied, Status: "True"},
	}

	fc := &kaprov1alpha2.Cluster{
		ObjectMeta: metav1.ObjectMeta{Name: clusterName},
		Spec: kaprov1alpha2.ClusterSpec{
			Bootstrap: &kaprov1alpha2.ClusterBootstrapSpec{
				ExpiresAt: &metav1.Time{Time: time.Now().Add(1 * time.Hour)},
			},
		},
	}

	r, _ := newBootstrapReconciler(t, fc, approvedCSR, deniedCSR)
	r.CertClient = k8sfake.NewClientset(approvedCSR, deniedCSR).CertificatesV1()

	res, err := r.processCSRsForCluster(context.Background(), fc)
	if err != nil {
		t.Fatalf("processCSRsForCluster: %v", err)
	}
	if res.RequeueAfter != 0 {
		t.Errorf("expected no requeue when all CSRs are finalized; got %+v", res)
	}
}

func TestMatchesFleetCluster_RejectsWrongSA(t *testing.T) {
	r, _ := newBootstrapReconciler(t)
	r.PodNamespace = "kapro-system"

	// CSR username doesn't match the expected bootstrap SA for this cluster.
	csr := makeTestCSR(t, "kapro-cluster:de-prod-01", []string{csrOrganization},
		"system:serviceaccount:kapro-system:kapro-bootstrap-fr-prod-99") // wrong cluster
	fc := &kaprov1alpha2.Cluster{ObjectMeta: metav1.ObjectMeta{Name: "de-prod-01"}}
	if r.matchesFleetCluster(csr, fc, "kapro-cluster:de-prod-01") {
		t.Error("CSR with mismatching bootstrap SA must not match")
	}
}

func TestMatchesFleetCluster_AcceptsCorrectSA(t *testing.T) {
	r, _ := newBootstrapReconciler(t)
	r.PodNamespace = "kapro-system"

	csr := makeTestCSR(t, "kapro-cluster:de-prod-01", []string{csrOrganization},
		"system:serviceaccount:kapro-system:kapro-bootstrap-de-prod-01")
	fc := &kaprov1alpha2.Cluster{ObjectMeta: metav1.ObjectMeta{Name: "de-prod-01"}}
	if !r.matchesFleetCluster(csr, fc, "kapro-cluster:de-prod-01") {
		t.Error("CSR with correct CN + SA must match")
	}
}

func TestProcessCSRsForCluster_ApprovesRenewalCSR(t *testing.T) {
	clusterName := "de-prod-01"
	csr := makeTestCSR(t, csrCNPrefix+clusterName, []string{csrOrganization}, csrCNPrefix+clusterName)
	csr.Name = "csr-renewal"
	csr.Labels = map[string]string{csrFleetClusterLabel: clusterName}

	usedAt := metav1.Now()
	fc := &kaprov1alpha2.Cluster{
		ObjectMeta: metav1.ObjectMeta{Name: clusterName},
		Spec: kaprov1alpha2.ClusterSpec{
			Bootstrap: &kaprov1alpha2.ClusterBootstrapSpec{
				ExpiresAt: &metav1.Time{Time: time.Now().Add(1 * time.Hour)},
			},
		},
		Status: kaprov1alpha2.ClusterStatus{
			Bootstrap: &kaprov1alpha2.ClusterBootstrapStatus{
				Used:                true,
				UsedAt:              &usedAt,
				IssuedCredentialFor: clusterName,
				BoundCSRName:        "csr-initial-bootstrap",
			},
		},
	}
	r, c := newBootstrapReconciler(t, fc, csr)
	fakeClient := k8sfake.NewClientset(csr)
	r.CertClient = fakeClient.CertificatesV1()

	res, err := r.processCSRsForCluster(context.Background(), fc)
	if err != nil {
		t.Fatalf("processCSRsForCluster: %v", err)
	}
	if res.RequeueAfter == 0 {
		t.Fatal("expected requeue after approving renewal CSR")
	}

	gotCSR, err := fakeClient.CertificatesV1().CertificateSigningRequests().Get(context.Background(), csr.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("re-fetch CSR: %v", err)
	}
	if !isCSRApproved(gotCSR) {
		t.Fatalf("renewal CSR should be approved; status=%+v", gotCSR.Status)
	}
	gotCluster := &kaprov1alpha2.Cluster{}
	if err := c.Get(context.Background(), client.ObjectKey{Name: clusterName}, gotCluster); err != nil {
		t.Fatalf("re-fetch Cluster: %v", err)
	}
	if gotCluster.Status.Bootstrap == nil || gotCluster.Status.Bootstrap.BoundCSRName != "csr-initial-bootstrap" {
		t.Fatalf("renewal must not replace bootstrap BoundCSRName; status=%+v", gotCluster.Status.Bootstrap)
	}
}

func TestProcessCSRsForCluster_RejectsMisleadingCSRLabel(t *testing.T) {
	clusterName := "de-prod-01"
	csr := makeTestCSR(t, csrCNPrefix+clusterName, []string{csrOrganization}, csrCNPrefix+"fr-prod-99")
	csr.Name = "csr-misleading-label"
	csr.Labels = map[string]string{csrFleetClusterLabel: clusterName}
	fc := &kaprov1alpha2.Cluster{
		ObjectMeta: metav1.ObjectMeta{Name: clusterName},
		Status: kaprov1alpha2.ClusterStatus{
			Bootstrap: &kaprov1alpha2.ClusterBootstrapStatus{Used: true},
		},
	}
	r, _ := newBootstrapReconciler(t, fc, csr)
	fakeClient := k8sfake.NewClientset(csr)
	r.CertClient = fakeClient.CertificatesV1()

	res, err := r.processCSRsForCluster(context.Background(), fc)
	if err != nil {
		t.Fatalf("processCSRsForCluster: %v", err)
	}
	if !res.IsZero() {
		t.Fatalf("expected no requeue for CSR rejected by authoritative username classification, got %+v", res)
	}
	gotCSR, err := fakeClient.CertificatesV1().CertificateSigningRequests().Get(context.Background(), csr.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("re-fetch CSR: %v", err)
	}
	if isCSRApproved(gotCSR) || isCSRDenied(gotCSR) {
		t.Fatalf("misleading label must not make CSR actionable; status=%+v", gotCSR.Status)
	}
}

func TestListCSRsForCluster_UsesLabeledAndLegacyUnlabeledCandidates(t *testing.T) {
	clusterName := "de-prod-01"
	labeled := makeTestCSR(t, csrCNPrefix+clusterName, []string{csrOrganization}, csrCNPrefix+clusterName)
	labeled.Name = "csr-labeled"
	labeled.Labels = map[string]string{csrFleetClusterLabel: clusterName}
	legacy := makeTestCSR(t, csrCNPrefix+clusterName, []string{csrOrganization}, csrCNPrefix+clusterName)
	legacy.Name = "csr-legacy"
	otherLabeled := makeTestCSR(t, csrCNPrefix+"fr-prod-99", []string{csrOrganization}, csrCNPrefix+"fr-prod-99")
	otherLabeled.Name = "csr-other"
	otherLabeled.Labels = map[string]string{csrFleetClusterLabel: "fr-prod-99"}
	fc := &kaprov1alpha2.Cluster{ObjectMeta: metav1.ObjectMeta{Name: clusterName}}
	r, _ := newBootstrapReconciler(t, fc, labeled, legacy, otherLabeled)

	got, err := r.listCSRsForCluster(context.Background(), fc)
	if err != nil {
		t.Fatalf("listCSRsForCluster: %v", err)
	}
	names := map[string]bool{}
	for _, csr := range got {
		names[csr.Name] = true
	}
	if !names["csr-labeled"] || !names["csr-legacy"] {
		t.Fatalf("expected labeled and legacy CSRs, got %v", names)
	}
	if names["csr-other"] {
		t.Fatalf("other cluster's labeled CSR must be excluded, got %v", names)
	}
}

func TestProcessCSRsForCluster_DeniesRenewalBeforeRegistration(t *testing.T) {
	clusterName := "de-prod-01"
	csr := makeTestCSR(t, csrCNPrefix+clusterName, []string{csrOrganization}, csrCNPrefix+clusterName)
	csr.Name = "csr-renewal-before-registration"

	fc := &kaprov1alpha2.Cluster{
		ObjectMeta: metav1.ObjectMeta{Name: clusterName},
		Spec: kaprov1alpha2.ClusterSpec{
			Bootstrap: &kaprov1alpha2.ClusterBootstrapSpec{
				ExpiresAt: &metav1.Time{Time: time.Now().Add(1 * time.Hour)},
			},
		},
	}
	r, _ := newBootstrapReconciler(t, fc, csr)
	fakeClient := k8sfake.NewClientset(csr)
	r.CertClient = fakeClient.CertificatesV1()

	res, err := r.processCSRsForCluster(context.Background(), fc)
	if err != nil {
		t.Fatalf("processCSRsForCluster: %v", err)
	}
	if !res.IsZero() {
		t.Fatalf("expected no requeue after denying invalid renewal CSR, got %+v", res)
	}

	gotCSR, err := fakeClient.CertificatesV1().CertificateSigningRequests().Get(context.Background(), csr.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("re-fetch CSR: %v", err)
	}
	if !isCSRDenied(gotCSR) {
		t.Fatalf("renewal CSR before registration should be denied; status=%+v", gotCSR.Status)
	}
}

func TestMatchesFleetCluster_AcceptsRenewalAfterRegistration(t *testing.T) {
	r, _ := newBootstrapReconciler(t)
	clusterName := "de-prod-01"
	csr := makeTestCSR(t, csrCNPrefix+clusterName, []string{csrOrganization}, csrCNPrefix+clusterName)
	fc := &kaprov1alpha2.Cluster{
		ObjectMeta: metav1.ObjectMeta{Name: clusterName},
		Status: kaprov1alpha2.ClusterStatus{
			Bootstrap: &kaprov1alpha2.ClusterBootstrapStatus{Used: true},
		},
	}

	if !r.matchesFleetCluster(csr, fc, csrCNPrefix+clusterName) {
		t.Fatal("registered cluster renewal CSR should match")
	}
}

func TestMatchesFleetCluster_RejectsRenewalForWrongIdentity(t *testing.T) {
	r, _ := newBootstrapReconciler(t)
	clusterName := "de-prod-01"
	csr := makeTestCSR(t, csrCNPrefix+clusterName, []string{csrOrganization}, csrCNPrefix+"fr-prod-99")
	fc := &kaprov1alpha2.Cluster{
		ObjectMeta: metav1.ObjectMeta{Name: clusterName},
		Status: kaprov1alpha2.ClusterStatus{
			Bootstrap: &kaprov1alpha2.ClusterBootstrapStatus{Used: true},
		},
	}

	if r.matchesFleetCluster(csr, fc, csrCNPrefix+clusterName) {
		t.Fatal("renewal CSR with a different cluster username must not match")
	}
}
