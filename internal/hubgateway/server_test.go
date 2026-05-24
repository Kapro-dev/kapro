package hubgateway

import (
	"bytes"
	"context"
	"encoding/json"
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
)

func TestGraphIncludesSubstrates(t *testing.T) {
	c := testClient(t,
		&kaprov1alpha1.Substrate{
			ObjectMeta: metav1.ObjectMeta{Name: "flux"},
			Spec:       hubGatewaySubstrateSpec("flux"),
		},
	)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/graph", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	rec := httptest.NewRecorder()

	(&Server{Client: c, BearerToken: []byte("test-token")}).Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"substrates"`) {
		t.Fatalf("graph response missing substrates: %s", rec.Body.String())
	}
}

func TestCreatePromotion(t *testing.T) {
	c := testClient(t)
	body := bytes.NewBufferString(`{"name":"checkout-1","fleetRef":"checkout","version":"1.2.3","plans":[{"name":"main","plan":"checkout"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/promotions", body)
	req.Header.Set("Authorization", "Bearer test-token")
	rec := httptest.NewRecorder()

	(&Server{Client: c, BearerToken: []byte("test-token")}).Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var promotion kaprov1alpha1.Promotion
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
		&kaproruntimev1alpha1.Target{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "target-a",
				Labels: map[string]string{"team": "checkout"},
			},
			Status: kaprov1alpha1.TargetStatus{
				TargetExecutionState: kaprov1alpha1.TargetExecutionState{Phase: kaprov1alpha1.TargetPhaseApplying},
			},
		},
		&kaproruntimev1alpha1.Target{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "target-b",
				Labels: map[string]string{"team": "checkout"},
			},
			Status: kaprov1alpha1.TargetStatus{
				TargetExecutionState: kaprov1alpha1.TargetExecutionState{Phase: kaprov1alpha1.TargetPhaseConverged},
			},
		},
		&kaproruntimev1alpha1.Target{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "target-c",
				Labels: map[string]string{"team": "payments"},
			},
			Status: kaprov1alpha1.TargetStatus{
				TargetExecutionState: kaprov1alpha1.TargetExecutionState{Phase: kaprov1alpha1.TargetPhaseApplying},
			},
		},
		&kaprov1alpha1.Cluster{
			ObjectMeta: metav1.ObjectMeta{Name: "cluster-a", Labels: map[string]string{"team": "checkout"}},
			Status:     kaprov1alpha1.ClusterStatus{Phase: kaprov1alpha1.ClusterPhaseConverged},
		},
	)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/graph?resource=targets&labelSelector=team%3Dcheckout&phase=Applying&limit=1", nil)
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
	if len(graph.Targets) != 1 {
		t.Fatalf("targets=%d, want 1; body=%s", len(graph.Targets), rec.Body.String())
	}
	if graph.Targets[0].Status.Phase != kaprov1alpha1.TargetPhaseApplying {
		t.Fatalf("phase=%q, want Applying", graph.Targets[0].Status.Phase)
	}
	if len(graph.Clusters) != 0 {
		t.Fatalf("clusters=%d, want 0 when resource=targets", len(graph.Clusters))
	}
	if graph.Page.Resource != "targets" || graph.Page.Limit != 1 || graph.Page.Counts["targets"] != 1 {
		t.Fatalf("unexpected page metadata: %+v", graph.Page)
	}
}

func TestGraphPhaseFilterScansPastFirstLimitedPage(t *testing.T) {
	c := testClient(t,
		&kaproruntimev1alpha1.Target{
			ObjectMeta: metav1.ObjectMeta{Name: "target-a"},
			Status: kaprov1alpha1.TargetStatus{
				TargetExecutionState: kaprov1alpha1.TargetExecutionState{Phase: kaprov1alpha1.TargetPhaseConverged},
			},
		},
		&kaproruntimev1alpha1.Target{
			ObjectMeta: metav1.ObjectMeta{Name: "target-b"},
			Status: kaprov1alpha1.TargetStatus{
				TargetExecutionState: kaprov1alpha1.TargetExecutionState{Phase: kaprov1alpha1.TargetPhaseFailed},
			},
		},
		&kaproruntimev1alpha1.Target{
			ObjectMeta: metav1.ObjectMeta{Name: "target-c"},
			Status: kaprov1alpha1.TargetStatus{
				TargetExecutionState: kaprov1alpha1.TargetExecutionState{Phase: kaprov1alpha1.TargetPhaseApplying},
			},
		},
	)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/graph?resource=targets&phase=Applying&limit=1", nil)
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
	if len(graph.Targets) != 1 {
		t.Fatalf("targets=%d, want 1; body=%s", len(graph.Targets), rec.Body.String())
	}
	if graph.Targets[0].Name != "target-c" {
		t.Fatalf("targets[0].name=%q, want target-c", graph.Targets[0].Name)
	}
}

func TestGraphMarksLimitedResponsesAsTruncated(t *testing.T) {
	c := testClient(t,
		&kaprov1alpha1.Substrate{
			ObjectMeta: metav1.ObjectMeta{Name: "flux"},
			Spec:       hubGatewaySubstrateSpec("flux"),
		},
		&kaprov1alpha1.Substrate{
			ObjectMeta: metav1.ObjectMeta{Name: "argo"},
			Spec:       hubGatewaySubstrateSpec("argo"),
		},
	)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/graph?resource=substrates&limit=1", nil)
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
	if len(graph.Substrates) != 1 {
		t.Fatalf("substrates=%d, want 1", len(graph.Substrates))
	}
	if !graph.Page.Truncated {
		t.Fatalf("page not marked truncated: %+v", graph.Page)
	}
}

func hubGatewaySubstrateSpec(kind string) kaprov1alpha1.SubstrateSpec {
	return kaprov1alpha1.SubstrateSpec{
		Substrate: &kaprov1alpha1.SubstrateImplementationSpec{Kind: kind, Actuator: kind},
		Execution: &kaprov1alpha1.SubstrateExecutionSpec{Mode: kaprov1alpha1.ExecutionModeSpokePull},
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
	body := bytes.NewBufferString(`{"name":"checkout-1","fleetRef":"checkout","version":"1.2.3","plans":[{"name":"main","plan":"checkout"}],"extra":true}`)
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
	if err := kaprov1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := kaproruntimev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
}
