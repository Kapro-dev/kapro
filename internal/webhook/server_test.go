package webhook

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	kaproruntimev1alpha1 "kapro.io/kapro/api/kaproruntime/v1alpha1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kaprov1alpha1 "kapro.io/kapro/api/kapro/v1alpha1"
	"kapro.io/kapro/internal/webhook/token"
)

func webhookTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := kaprov1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}
	if err := kaproruntimev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add runtime scheme: %v", err)
	}
	return scheme
}

func TestHandleStatus_RequiresPromotionRunInOperatorNamespace(t *testing.T) {
	scheme := webhookTestScheme(t)
	target := &kaproruntimev1alpha1.Target{
		ObjectMeta: metav1.ObjectMeta{Name: "rel-wave-prod-cluster-a"},
		Spec: kaprov1alpha1.TargetSpec{
			PromotionRunRef: "rel-1",
			Target:          "cluster-a",
			Version:         "repo@sha256:abc",
		},
		Status: kaprov1alpha1.TargetStatus{
			TargetExecutionState: kaprov1alpha1.TargetExecutionState{Phase: kaprov1alpha1.TargetPhaseWaitingApproval},
		},
	}
	s := &Server{
		Client:            fake.NewClientBuilder().WithScheme(scheme).WithObjects(target).Build(),
		OperatorNamespace: "kapro-system",
	}

	req := httptest.NewRequest(http.MethodGet, "/status/"+target.Name, nil)
	req.SetPathValue("name", target.Name)
	rec := httptest.NewRecorder()

	s.handleStatus(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 when owning promotionrun is absent in operator namespace, got %d", rec.Code)
	}
}

func TestHandleReject_TargetPromotionRunMismatchRejected(t *testing.T) {
	scheme := webhookTestScheme(t)
	promotionrun := &kaproruntimev1alpha1.PromotionRun{
		ObjectMeta: metav1.ObjectMeta{Name: "rel-1", Namespace: "default", UID: "uid-1"},
	}
	target := &kaproruntimev1alpha1.Target{
		ObjectMeta: metav1.ObjectMeta{Name: "rel-1-deadbeef"},
		Spec: kaprov1alpha1.TargetSpec{
			PromotionRunRef: "other-promotionrun",
			Target:          "cluster-a",
		},
	}
	s := &Server{
		Client:      fake.NewClientBuilder().WithScheme(scheme).WithObjects(promotionrun, target).Build(),
		TokenSecret: []byte("secret"),
	}
	claims := token.Claims{
		Action:       "reject",
		Namespace:    "default",
		PromotionRun: "rel-1",
		Target:       "cluster-a",
		UID:          "uid-1/" + target.Name,
		JTI:          "reject-jti",
		Exp:          1 << 62,
	}
	tokenStr, err := token.Sign(claims, s.TokenSecret)
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/reject/"+target.Name, nil)
	req.Header.Set("Authorization", "Bearer "+tokenStr)
	req.SetPathValue("name", target.Name)
	rec := httptest.NewRecorder()

	s.handleReject(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 on target/promotionrun mismatch, got %d", rec.Code)
	}
}

func TestApprovalWebhookRejectsQueryToken(t *testing.T) {
	s := &Server{TokenSecret: []byte("secret")}
	tokenStr, err := token.Sign(token.Claims{Action: "approve", JTI: "query-jti", Exp: 1 << 62}, s.TokenSecret)
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/approve/target-a?token="+tokenStr, nil)

	if _, err := s.verifyToken(req, "approve"); err == nil || !strings.Contains(err.Error(), "not query strings") {
		t.Fatalf("expected query token rejection, got %v", err)
	}
}

func TestApprovalWebhookRendersFragmentBackedDecisionPage(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodGet, "/approve/target-a", nil)
	rec := httptest.NewRecorder()

	s.handleApprove(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 decision page, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "window.location.hash") || !strings.Contains(body, `Authorization": "Bearer "`) {
		t.Fatalf("decision page does not read fragment and POST bearer token:\n%s", body)
	}
}

func TestHandleApproveRejectsReplayToken(t *testing.T) {
	scheme := webhookTestScheme(t)
	promotionrun := &kaproruntimev1alpha1.PromotionRun{
		ObjectMeta: metav1.ObjectMeta{Name: "rel-1", Namespace: "default", UID: "uid-1"},
	}
	target := &kaproruntimev1alpha1.Target{
		ObjectMeta: metav1.ObjectMeta{Name: "rel-1-wave-prod-cluster-a"},
		Spec: kaprov1alpha1.TargetSpec{
			PromotionRunRef: "rel-1",
			Target:          "cluster-a",
			Version:         "v1",
		},
	}
	s := &Server{
		Client:      fake.NewClientBuilder().WithScheme(scheme).WithObjects(promotionrun, target).Build(),
		TokenSecret: []byte("secret"),
	}
	tokenStr := signApprovalTestToken(t, s.TokenSecret, token.Claims{
		SyncName:     target.Name,
		Action:       "approve",
		Namespace:    "default",
		PromotionRun: "rel-1",
		Target:       "cluster-a",
		Version:      "v1",
		UID:          "uid-1/" + target.Name,
		JTI:          "approve-jti",
		Exp:          1 << 62,
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/approve/"+target.Name, nil)
	req.Header.Set("Authorization", "Bearer "+tokenStr)
	req.SetPathValue("name", target.Name)
	s.handleApprove(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("first approve code = %d, body=%s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/approve/"+target.Name, nil)
	req.Header.Set("Authorization", "Bearer "+tokenStr)
	req.SetPathValue("name", target.Name)
	s.handleApprove(rec, req)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "token_already_redeemed") {
		t.Fatalf("replay code/body = %d/%s, want token_already_redeemed conflict", rec.Code, rec.Body.String())
	}

	var got kaproruntimev1alpha1.Target
	if err := s.Client.Get(context.Background(), client.ObjectKey{Name: target.Name}, &got); err != nil {
		t.Fatal(err)
	}
	if got.Annotations[approvalDecisionTokenJTIAnnotation] != "approve-jti" {
		t.Fatalf("redeemed token annotation = %q", got.Annotations[approvalDecisionTokenJTIAnnotation])
	}
}

func TestHandleRejectBlockedAfterApproveDecisionClaim(t *testing.T) {
	scheme := webhookTestScheme(t)
	promotionrun := &kaproruntimev1alpha1.PromotionRun{
		ObjectMeta: metav1.ObjectMeta{Name: "rel-1", Namespace: "default", UID: "uid-1"},
	}
	target := &kaproruntimev1alpha1.Target{
		ObjectMeta: metav1.ObjectMeta{Name: "rel-1-wave-prod-cluster-a"},
		Spec: kaprov1alpha1.TargetSpec{
			PromotionRunRef: "rel-1",
			Target:          "cluster-a",
			Version:         "v1",
		},
	}
	s := &Server{
		Client:      fake.NewClientBuilder().WithScheme(scheme).WithObjects(promotionrun, target).Build(),
		TokenSecret: []byte("secret"),
	}
	approveToken := signApprovalTestToken(t, s.TokenSecret, token.Claims{
		SyncName:     target.Name,
		Action:       "approve",
		Namespace:    "default",
		PromotionRun: "rel-1",
		Target:       "cluster-a",
		Version:      "v1",
		UID:          "uid-1/" + target.Name,
		JTI:          "approve-jti",
		Exp:          1 << 62,
	})
	rejectToken := signApprovalTestToken(t, s.TokenSecret, token.Claims{
		SyncName:     target.Name,
		Action:       "reject",
		Namespace:    "default",
		PromotionRun: "rel-1",
		Target:       "cluster-a",
		Version:      "v1",
		UID:          "uid-1/" + target.Name,
		JTI:          "reject-jti",
		Exp:          1 << 62,
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/approve/"+target.Name, nil)
	req.Header.Set("Authorization", "Bearer "+approveToken)
	req.SetPathValue("name", target.Name)
	s.handleApprove(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("approve code = %d, body=%s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/reject/"+target.Name, nil)
	req.Header.Set("Authorization", "Bearer "+rejectToken)
	req.SetPathValue("name", target.Name)
	s.handleReject(rec, req)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "token_already_redeemed") {
		t.Fatalf("reject-after-approve code/body = %d/%s, want token_already_redeemed conflict", rec.Code, rec.Body.String())
	}
}

func signApprovalTestToken(t *testing.T, secret []byte, claims token.Claims) string {
	t.Helper()
	tokenStr, err := token.Sign(claims, secret)
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return tokenStr
}
