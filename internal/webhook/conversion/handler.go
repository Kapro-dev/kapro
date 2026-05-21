package conversion

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
)

const maxConversionReviewBytes = 1 << 20

var supportedKinds = map[string]struct{}{
	"Approval":        {},
	"Backend":         {},
	"Cluster":         {},
	"ClusterTemplate": {},
	"Fleet":           {},
	"Plan":            {},
	"Plugin":          {},
	"Policy":          {},
	"Promotion":       {},
	"PromotionRun":    {},
	"Source":          {},
	"Target":          {},
	"Trigger":         {},
}

// NewIdentityHandler returns the ADR-0011 conversion webhook scaffold.
//
// v1alpha2 is currently the only served version, so the handler intentionally
// performs identity conversion only. Future API versions should replace this
// dispatcher with explicit per-kind conversion functions.
func NewIdentityHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		review := apiextensionsv1.ConversionReview{}
		if err := json.NewDecoder(io.LimitReader(r.Body, maxConversionReviewBytes)).Decode(&review); err != nil {
			http.Error(w, "invalid ConversionReview", http.StatusBadRequest)
			return
		}
		if review.Request == nil {
			http.Error(w, "missing ConversionReview request", http.StatusBadRequest)
			return
		}

		response := &apiextensionsv1.ConversionResponse{
			UID: review.Request.UID,
			Result: metav1.Status{
				Status: metav1.StatusSuccess,
			},
		}
		objects, err := ConvertIdentity(review.Request.DesiredAPIVersion, review.Request.Objects)
		if err != nil {
			response.Result = metav1.Status{
				Status:  metav1.StatusFailure,
				Message: err.Error(),
			}
		} else {
			response.ConvertedObjects = objects
		}

		out := apiextensionsv1.ConversionReview{
			TypeMeta: metav1.TypeMeta{
				APIVersion: apiextensionsv1.SchemeGroupVersion.String(),
				Kind:       "ConversionReview",
			},
			Response: response,
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(out); err != nil {
			http.Error(w, "write ConversionReview response", http.StatusInternalServerError)
		}
	})
}

// ConvertIdentity validates that every object is already a supported v1alpha2
// Kapro object and returns the original raw payload unchanged.
func ConvertIdentity(apiVersion string, objects []runtime.RawExtension) ([]runtime.RawExtension, error) {
	servedAPIVersion := kaprov1alpha2.GroupVersion.String()
	if apiVersion != servedAPIVersion {
		return nil, fmt.Errorf("unsupported desired apiVersion %q; only %s is served", apiVersion, servedAPIVersion)
	}
	converted := make([]runtime.RawExtension, 0, len(objects))
	for i, obj := range objects {
		meta := metav1.TypeMeta{}
		if err := json.Unmarshal(obj.Raw, &meta); err != nil {
			return nil, fmt.Errorf("object %d: decode type metadata: %w", i, err)
		}
		if meta.APIVersion != kaprov1alpha2.GroupVersion.String() {
			return nil, fmt.Errorf("object %d: unsupported apiVersion %q", i, meta.APIVersion)
		}
		if _, ok := supportedKinds[meta.Kind]; !ok {
			return nil, fmt.Errorf("object %d: unsupported kind %q", i, meta.Kind)
		}
		converted = append(converted, runtime.RawExtension{Raw: obj.Raw})
	}
	return converted, nil
}
