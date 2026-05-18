package controller

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// defaultStatusUpdateRetries is the bound on conflict-retry loops around
// Status().Update calls. At 500-cluster scale, heartbeat / target status
// writes synchronise within sub-second windows and routinely race with
// the controller-runtime workqueue's own requeue. Without retry, the
// reconciler bubbles the 409 up and the workqueue exponential-backs-off,
// which inflates p99 status-write latency unnecessarily. 5 retries is
// enough to cover the typical contention window (each retry refetches);
// beyond that, surface the conflict so the workqueue can take over.
const defaultStatusUpdateRetries = 5

// StatusUpdateWithRetry runs Status().Update in a loop, refetching the
// object and re-applying mutate on each apierrors.IsConflict response.
// mutate receives the freshly-fetched copy and must populate the desired
// status fields onto it (typically a small functional patch — set Phase,
// append a Condition, write a counter). The function returns nil only
// when the Status().Update successfully lands.
//
// Non-conflict errors are returned unwrapped on the first occurrence.
// After defaultStatusUpdateRetries unsuccessful conflict retries, the
// last conflict error is returned so the caller (workqueue) sees it
// and reschedules with backoff.
//
// Use this on hot Status().Update sites where conflict rates are
// expected to be > 0.1% (heartbeat, target FSM transitions, AgentPolicy
// counter increments). Cold sites (one-shot bootstrap, deletion) can
// continue to call Status().Update directly.
func StatusUpdateWithRetry[T client.Object](
	ctx context.Context,
	c client.Client,
	obj T,
	mutate func(T) error,
) error {
	key := client.ObjectKeyFromObject(obj)
	var lastConflict error
	for attempt := 0; attempt < defaultStatusUpdateRetries; attempt++ {
		if attempt > 0 {
			if err := c.Get(ctx, key, obj); err != nil {
				return fmt.Errorf("refetch %T %s for status retry: %w", obj, key, err)
			}
		}
		if err := mutate(obj); err != nil {
			return err
		}
		if err := c.Status().Update(ctx, obj); err != nil {
			if apierrors.IsConflict(err) {
				lastConflict = err
				continue
			}
			return err
		}
		return nil
	}
	return fmt.Errorf("status update for %T %s lost %d conflict races: %w",
		obj, key, defaultStatusUpdateRetries, lastConflict)
}
