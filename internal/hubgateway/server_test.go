package hubgateway

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
)

func TestGraphIncludesBackendProfiles(t *testing.T) {
	c := testClient(t,
		&kaprov1alpha1.BackendProfile{
			ObjectMeta: metav1.ObjectMeta{Name: "flux"},
			Spec:       kaprov1alpha1.BackendProfileSpec{Driver: kaprov1alpha1.BackendDriverFlux},
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

func TestCreateRelease(t *testing.T) {
	c := testClient(t)
	body := bytes.NewBufferString(`{"name":"checkout-1","version":"1.2.3","pipelines":[{"name":"main","pipeline":"checkout"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/releases", body)
	req.Header.Set("Authorization", "Bearer test-token")
	rec := httptest.NewRecorder()

	(&Server{Client: c, BearerToken: []byte("test-token")}).Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var release kaprov1alpha1.Release
	if err := c.Get(context.Background(), client.ObjectKey{Name: "checkout-1"}, &release); err != nil {
		t.Fatalf("release not created: %v", err)
	}
	if release.Spec.Version != "1.2.3" {
		t.Fatalf("version=%s", release.Spec.Version)
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

func TestCreateReleaseRejectsUnknownFields(t *testing.T) {
	c := testClient(t)
	body := bytes.NewBufferString(`{"name":"checkout-1","version":"1.2.3","pipelines":[{"name":"main","pipeline":"checkout"}],"extra":true}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/releases", body)
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
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
}
