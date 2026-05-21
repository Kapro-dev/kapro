package conversion

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
)

func TestConvertIdentityRoundTripsEachKind(t *testing.T) {
	for _, kind := range sortedSupportedKinds() {
		t.Run(kind, func(t *testing.T) {
			raw := []byte(`{"apiVersion":"kapro.io/v1alpha2","kind":"` + kind + `","metadata":{"name":"sample"}}`)
			got, err := ConvertIdentity("kapro.io/v1alpha2", []runtime.RawExtension{{Raw: raw}})
			if err != nil {
				t.Fatalf("ConvertIdentity: %v", err)
			}
			if len(got) != 1 {
				t.Fatalf("converted object count=%d, want 1", len(got))
			}
			if !bytes.Equal(got[0].Raw, raw) {
				t.Fatalf("identity conversion changed raw object\ngot:  %s\nwant: %s", got[0].Raw, raw)
			}
		})
	}
}

func TestIdentityHandlerReturnsSuccessfulConversionReview(t *testing.T) {
	raw := []byte(`{"apiVersion":"kapro.io/v1alpha2","kind":"Promotion","metadata":{"name":"checkout"}}`)
	review := apiextensionsv1.ConversionReview{
		TypeMeta: metav1.TypeMeta{
			APIVersion: apiextensionsv1.SchemeGroupVersion.String(),
			Kind:       "ConversionReview",
		},
		Request: &apiextensionsv1.ConversionRequest{
			UID:               types.UID("req-1"),
			DesiredAPIVersion: "kapro.io/v1alpha2",
			Objects:           []runtime.RawExtension{{Raw: raw}},
		},
	}
	body, err := json.Marshal(review)
	if err != nil {
		t.Fatalf("marshal review: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/convert", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	NewIdentityHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	out := apiextensionsv1.ConversionReview{}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if out.Response == nil {
		t.Fatal("missing response")
	}
	if out.Response.UID != review.Request.UID {
		t.Fatalf("response UID=%q, want %q", out.Response.UID, review.Request.UID)
	}
	if out.Response.Result.Status != metav1.StatusSuccess {
		t.Fatalf("result=%s message=%s", out.Response.Result.Status, out.Response.Result.Message)
	}
	if len(out.Response.ConvertedObjects) != 1 || !bytes.Equal(out.Response.ConvertedObjects[0].Raw, raw) {
		t.Fatalf("converted objects changed: %#v", out.Response.ConvertedObjects)
	}
}

func TestConvertIdentityRejectsLegacyAPIVersion(t *testing.T) {
	raw := []byte(`{"apiVersion":"kapro.io/v1alpha1","kind":"Promotion","metadata":{"name":"checkout"}}`)
	if _, err := ConvertIdentity("kapro.io/v1alpha2", []runtime.RawExtension{{Raw: raw}}); err == nil {
		t.Fatal("expected legacy apiVersion rejection")
	}
}

func TestIdentityHandlerRequiresPost(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/convert", nil)
	rec := httptest.NewRecorder()

	NewIdentityHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

func sortedSupportedKinds() []string {
	kinds := make([]string, 0, len(supportedKinds))
	for kind := range supportedKinds {
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)
	return kinds
}
