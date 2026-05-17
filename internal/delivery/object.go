package delivery

import (
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// Object is a typed wrapper around unstructured.Unstructured that carries
// the source-of-truth path (for diagnostics) and the parsed GVK so callers
// don't need to recompute it on every apply pass.
type Object struct {
	U      *unstructured.Unstructured
	GVK    schema.GroupVersionKind
	Source string // optional: artifact-relative path that produced this object
}

// Key returns a stable identifier for the object suitable for map keys and
// deduplication.
func (o *Object) Key() string {
	if o == nil || o.U == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%s/%s/%s/%s",
		o.GVK.GroupVersion().String(), o.GVK.Kind, o.U.GetNamespace(), o.U.GetName())
}

// FromUnstructured builds an Object from an already-typed unstructured. The
// GVK is read off the object. Returns nil if u is nil or has no Kind.
func FromUnstructured(u *unstructured.Unstructured, source string) *Object {
	if u == nil {
		return nil
	}
	gvk := u.GroupVersionKind()
	if gvk.Kind == "" {
		return nil
	}
	return &Object{U: u, GVK: gvk, Source: source}
}
