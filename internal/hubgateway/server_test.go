package hubgateway

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
)

func TestGraphIncludesBackendProfiles(t *testing.T) {
	c := testClient(t,
		&kaprov1alpha2.Backend{
			ObjectMeta: metav1.ObjectMeta{Name: "flux"},
			Spec:       kaprov1alpha2.BackendSpec{Driver: kaprov1alpha2.BackendDriverFlux},
		},
	)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/graph", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	rec := httptest.NewRecorder()

	(&Server{Client: c, BearerToken: []byte("test-token")}).Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"backendProfiles"`) {
		t.Fatalf("graph response missing backendProfiles: %s", rec.Body.String())
	}
}

func TestCreatePromotion(t *testing.T) {
	c := testClient(t)
	body := bytes.NewBufferString(`{"name":"checkout-1","fleetRef":"checkout","version":"1.2.3","promotionPlans":[{"name":"main","promotionplan":"checkout"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/promotions", body)
	req.Header.Set("Authorization", "Bearer test-token")
	rec := httptest.NewRecorder()

	(&Server{Client: c, BearerToken: []byte("test-token")}).Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var promotion kaprov1alpha2.Promotion
	if err := c.Get(context.Background(), client.ObjectKey{Name: "checkout-1"}, &promotion); err != nil {
		t.Fatalf("promotion not created: %v", err)
	}
	if promotion.Spec.FleetRef != "checkout" || promotion.Spec.Version != "1.2.3" {
		t.Fatalf("spec=%+v", promotion.Spec)
	}
}

func TestGatewayRequiresBearerToken(t *testing.T) {
	c := testClient(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/graph", nil)
	rec := httptest.NewRecorder()

	(&Server{Client: c, BearerToken: []byte("test-token")}).Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestGatewayRejectsWrongBearerToken(t *testing.T) {
	c := testClient(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/graph", nil)
	req.Header.Set("Authorization", "Bearer wrong-token")
	rec := httptest.NewRecorder()

	(&Server{Client: c, BearerToken: []byte("test-token")}).Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestGraphSupportsResourceLabelPhaseAndLimitFilters(t *testing.T) {
	c := testClient(t,
		&kaprov1alpha2.Target{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "target-a",
				Labels: map[string]string{"team": "checkout"},
			},
			Status: kaprov1alpha2.TargetStatus{
				TargetExecutionState: kaprov1alpha2.TargetExecutionState{Phase: kaprov1alpha2.TargetPhaseApplying},
			},
		},
		&kaprov1alpha2.Target{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "target-b",
				Labels: map[string]string{"team": "checkout"},
			},
			Status: kaprov1alpha2.TargetStatus{
				TargetExecutionState: kaprov1alpha2.TargetExecutionState{Phase: kaprov1alpha2.TargetPhaseConverged},
			},
		},
		&kaprov1alpha2.Target{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "target-c",
				Labels: map[string]string{"team": "payments"},
			},
			Status: kaprov1alpha2.TargetStatus{
				TargetExecutionState: kaprov1alpha2.TargetExecutionState{Phase: kaprov1alpha2.TargetPhaseApplying},
			},
		},
		&kaprov1alpha2.Cluster{
			ObjectMeta: metav1.ObjectMeta{Name: "cluster-a", Labels: map[string]string{"team": "checkout"}},
			Status:     kaprov1alpha2.ClusterStatus{Phase: kaprov1alpha2.ClusterPhaseConverged},
		},
	)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/graph?resource=promotiontargets&labelSelector=team%3Dcheckout&phase=Applying&limit=1", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	rec := httptest.NewRecorder()

	(&Server{Client: c, BearerToken: []byte("test-token")}).Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var graph GraphResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &graph); err != nil {
		t.Fatal(err)
	}
	if len(graph.PromotionTargets) != 1 {
		t.Fatalf("promotionTargets=%d, want 1; body=%s", len(graph.PromotionTargets), rec.Body.String())
	}
	if graph.PromotionTargets[0].Status.Phase != kaprov1alpha2.TargetPhaseApplying {
		t.Fatalf("phase=%q, want Applying", graph.PromotionTargets[0].Status.Phase)
	}
	if len(graph.FleetClusters) != 0 {
		t.Fatalf("fleetClusters=%d, want 0 when resource=promotiontargets", len(graph.FleetClusters))
	}
	if graph.Page.Resource != "promotiontargets" || graph.Page.Limit != 1 || graph.Page.Counts["promotiontargets"] != 1 {
		t.Fatalf("unexpected page metadata: %+v", graph.Page)
	}
}

func TestGraphPhaseFilterScansPastFirstLimitedPage(t *testing.T) {
	c := testClient(t,
		&kaprov1alpha2.Target{
			ObjectMeta: metav1.ObjectMeta{Name: "target-a"},
			Status: kaprov1alpha2.TargetStatus{
				TargetExecutionState: kaprov1alpha2.TargetExecutionState{Phase: kaprov1alpha2.TargetPhaseConverged},
			},
		},
		&kaprov1alpha2.Target{
			ObjectMeta: metav1.ObjectMeta{Name: "target-b"},
			Status: kaprov1alpha2.TargetStatus{
				TargetExecutionState: kaprov1alpha2.TargetExecutionState{Phase: kaprov1alpha2.TargetPhaseFailed},
			},
		},
		&kaprov1alpha2.Target{
			ObjectMeta: metav1.ObjectMeta{Name: "target-c"},
			Status: kaprov1alpha2.TargetStatus{
				TargetExecutionState: kaprov1alpha2.TargetExecutionState{Phase: kaprov1alpha2.TargetPhaseApplying},
			},
		},
	)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/graph?resource=promotiontargets&phase=Applying&limit=1", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	rec := httptest.NewRecorder()

	(&Server{Client: c, BearerToken: []byte("test-token")}).Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var graph GraphResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &graph); err != nil {
		t.Fatal(err)
	}
	if len(graph.PromotionTargets) != 1 {
		t.Fatalf("promotionTargets=%d, want 1; body=%s", len(graph.PromotionTargets), rec.Body.String())
	}
	if graph.PromotionTargets[0].Name != "target-c" {
		t.Fatalf("promotionTargets[0].name=%q, want target-c", graph.PromotionTargets[0].Name)
	}
}

func TestGraphMarksLimitedResponsesAsTruncated(t *testing.T) {
	c := testClient(t,
		&kaprov1alpha2.Backend{
			ObjectMeta: metav1.ObjectMeta{Name: "flux"},
			Spec:       kaprov1alpha2.BackendSpec{Driver: kaprov1alpha2.BackendDriverFlux},
		},
		&kaprov1alpha2.Backend{
			ObjectMeta: metav1.ObjectMeta{Name: "argo"},
			Spec:       kaprov1alpha2.BackendSpec{Driver: kaprov1alpha2.BackendDriverArgo},
		},
	)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/graph?resource=backendprofiles&limit=1", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	rec := httptest.NewRecorder()

	(&Server{Client: c, BearerToken: []byte("test-token")}).Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var graph GraphResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &graph); err != nil {
		t.Fatal(err)
	}
	if len(graph.BackendProfiles) != 1 {
		t.Fatalf("backendProfiles=%d, want 1", len(graph.BackendProfiles))
	}
	if !graph.Page.Truncated {
		t.Fatalf("page not marked truncated: %+v", graph.Page)
	}
}

func TestGraphRejectsInvalidLimit(t *testing.T) {
	c := testClient(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/graph?limit=0", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	rec := httptest.NewRecorder()

	(&Server{Client: c, BearerToken: []byte("test-token")}).Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestGraphRejectsUnknownResource(t *testing.T) {
	c := testClient(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/graph?resource=secrets", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	rec := httptest.NewRecorder()

	(&Server{Client: c, BearerToken: []byte("test-token")}).Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestCreatePromotionRejectsUnknownFields(t *testing.T) {
	c := testClient(t)
	body := bytes.NewBufferString(`{"name":"checkout-1","fleetRef":"checkout","version":"1.2.3","promotionPlans":[{"name":"main","promotionplan":"checkout"}],"extra":true}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/promotions", body)
	req.Header.Set("Authorization", "Bearer test-token")
	rec := httptest.NewRecorder()

	(&Server{Client: c, BearerToken: []byte("test-token")}).Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func testClient(t *testing.T, objects ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := kaprov1alpha2.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
}
