// Package statuspatch provides a generic diff-based status patcher that skips
// redundant writes. At 1000+ clusters, avoiding no-op status patches reduces
// hub API load significantly.
package statuspatch

import (
	"context"
	"reflect"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

// PatchStatusIfChanged compares old and new status using reflect.DeepEqual.
// If they differ, it patches the object's status subresource using MergeFrom.
// Returns (true, nil) when a write occurred, (false, nil) when skipped.
func PatchStatusIfChanged[T client.Object](
	ctx context.Context,
	c client.Client,
	obj T,
	patch client.Patch,
	oldStatus, newStatus any,
) (bool, error) {
	if reflect.DeepEqual(oldStatus, newStatus) {
		return false, nil
	}
	if err := c.Status().Patch(ctx, obj, patch); err != nil {
		return false, err
	}
	return true, nil
}
