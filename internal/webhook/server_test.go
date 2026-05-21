package webhook

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
	"kapro.io/kapro/internal/webhook/token"
)

func webhookTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := kaprov1alpha2.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}
	return scheme
}

func TestHandleStatus_RequiresPromotionRunInOperatorNamespace(t *testing.T) {
	scheme := webhookTestScheme(t)
	target := &kaprov1alpha2.Target{
		ObjectMeta: metav1.ObjectMeta{Name: "rel-wave-prod-cluster-a"},
		Spec: kaprov1alpha2.TargetSpec{
			PromotionRunRef: "rel-1",
			Target:          "cluster-a",
			Version:         "repo@sha256:abc",
		},
		Status: kaprov1alpha2.TargetStatus{
			TargetStatus: kaprov1alpha2.TargetStatus{Phase: kaprov1alpha2.TargetPhaseWaitingApproval},
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
	promotionrun := &kaprov1alpha2.PromotionRun{
		ObjectMeta: metav1.ObjectMeta{Name: "rel-1", Namespace: "default", UID: "uid-1"},
	}
	target := &kaprov1alpha2.Target{
		ObjectMeta: metav1.ObjectMeta{Name: "rel-1-deadbeef"},
		Spec: kaprov1alpha2.TargetSpec{
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
		Exp:          1 << 62,
	}
	tokenStr, err := token.Sign(claims, s.TokenSecret)
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/reject/"+target.Name+"?token="+url.QueryEscape(tokenStr), nil)
	req.SetPathValue("name", target.Name)
	rec := httptest.NewRecorder()

	s.handleReject(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 on target/promotionrun mismatch, got %d", rec.Code)
	}
}
