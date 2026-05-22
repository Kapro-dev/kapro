// Package controller — PromotionRun retention / garbage-collection reconciler.
//
// Implements ADR-0015 (PromotionRun retention). PromotionRun objects are
// immutable execution records; at ~10 promotions/day with 5 attempts per
// promotion, an active Kapro install accumulates ~18k objects per year.
// Without bounded retention the operator and apiserver pay etcd, watch
// fan-out, and List() cost forever.
//
// Trigger: watches the parent Promotion. On every Promotion reconcile event
// the GC controller lists the Promotion's child PromotionRuns and prunes
// terminal attempts beyond the cap, oldest first within each outcome bucket.
//
// Retention policy (see ADR-0015):
//
//   - NEVER delete a non-terminal PromotionRun. Active attempts are the live
//     execution record; deletion would orphan child Targets and Promotion
//     state aggregation.
//   - Always retain at least DefaultMinRetainedPerOutcome of each terminal
//     outcome (Complete / Failed / Superseded). The most recent failure is
//     usually the one an operator is debugging — never auto-prune it just
//     because Successes filled the cap.
//   - Total retained per Promotion (active + terminal) is bounded by
//     DefaultMaxRetainedPerPromotion. Excess terminal attempts beyond the
//     cap AND beyond the per-outcome floor are deleted oldest first.
//
// Tier B opt-in (ADR-0010). The controller name is `promotionrun-gc`. It is
// intentionally NOT in the default `controllers:` list — adopters must opt
// in. Reasoning: deletion is destructive; for first-touch users running
// `kubectl get promotionruns` is the audit trail. Mature deployments opt in.
//
// Failure mode: a delete that returns NotFound or Forbidden is logged and
// skipped — the controller never retries indefinitely. A delete that
// returns a transient error is retried at the standard backoff. The
// controller is idempotent; a re-run is always safe.
package controller

import (
	"context"
	"fmt"
	"sort"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
)

// PromotionRunGCReconciler enforces ADR-0015 retention on PromotionRun
// children of each Promotion.
type PromotionRunGCReconciler struct {
	client.Client
	Recorder record.EventRecorder
	Scheme   *runtime.Scheme

	// MaxRetainedPerPromotion overrides the default total cap. Zero means
	// use kaprov1alpha2.DefaultMaxRetainedPerPromotion.
	MaxRetainedPerPromotion int
	// MinRetainedPerOutcome overrides the default per-outcome floor. Zero
	// means use kaprov1alpha2.DefaultMinRetainedPerOutcome.
	MinRetainedPerOutcome int
}

// +kubebuilder:rbac:groups=kapro.io,resources=promotions,verbs=get;list;watch
// +kubebuilder:rbac:groups=kapro.io,resources=promotionruns,verbs=get;list;watch;delete

func (r *PromotionRunGCReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("promotion", req.Name)

	var promotion kaprov1alpha2.Promotion
	if err := r.Get(ctx, req.NamespacedName, &promotion); err != nil {
		// Promotion deletion cascades to PromotionRuns via ownerReferences
		// (set in PromotionReconciler.stampAttempt). No work for us.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	var runs kaprov1alpha2.PromotionRunList
	if err := r.List(ctx, &runs, client.MatchingLabels{promotionOwnerLabel: promotion.Name}); err != nil {
		return ctrl.Result{}, fmt.Errorf("list child PromotionRuns: %w", err)
	}

	victims := r.selectVictims(runs.Items)
	if len(victims) == 0 {
		return ctrl.Result{}, nil
	}

	logger.Info("pruning terminal PromotionRun attempts beyond retention cap",
		"total", len(runs.Items),
		"toDelete", len(victims),
		"maxRetained", r.maxRetained(),
		"minPerOutcome", r.minPerOutcome(),
	)

	var deleted int
	for i := range victims {
		v := &victims[i]
		if err := r.Delete(ctx, v); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			if apierrors.IsForbidden(err) {
				logger.Info("skipping delete: forbidden", "promotionrun", v.Name)
				continue
			}
			// Transient — return error so controller-runtime retries.
			return ctrl.Result{}, fmt.Errorf("delete PromotionRun %s: %w", v.Name, err)
		}
		deleted++
		r.Recorder.Eventf(&promotion, "Normal", "AttemptPruned",
			"Pruned terminal PromotionRun %s (phase=%s, age=%s) per ADR-0015 retention",
			v.Name, v.Status.Phase, time.Since(v.CreationTimestamp.Time).Round(time.Second))
	}

	if deleted > 0 {
		logger.Info("retention pass complete", "deleted", deleted)
	}
	return ctrl.Result{}, nil
}

// selectVictims returns the PromotionRuns to delete to bring this
// Promotion's child set within the retention cap. Active (non-terminal)
// runs are NEVER selected. Per-outcome floor is honoured.
func (r *PromotionRunGCReconciler) selectVictims(runs []kaprov1alpha2.PromotionRun) []kaprov1alpha2.PromotionRun {
	if len(runs) == 0 {
		return nil
	}

	maxTotal := r.maxRetained()
	minPerOutcome := r.minPerOutcome()

	// Bucket by terminal vs non-terminal first; non-terminal exempt.
	var active []kaprov1alpha2.PromotionRun
	bucketByPhase := map[kaprov1alpha2.PromotionRunPhase][]kaprov1alpha2.PromotionRun{}
	for i := range runs {
		run := runs[i]
		if !run.Status.Phase.IsTerminal() {
			active = append(active, run)
			continue
		}
		bucketByPhase[run.Status.Phase] = append(bucketByPhase[run.Status.Phase], run)
	}

	// Within each terminal bucket, sort by CreationTimestamp ascending so
	// the OLDEST are candidates for deletion. Resolution-tie-break: name.
	for phase, bucket := range bucketByPhase {
		sort.Slice(bucket, func(i, j int) bool {
			a, b := bucket[i].CreationTimestamp, bucket[j].CreationTimestamp
			if a.Equal(&b) {
				return bucket[i].Name < bucket[j].Name
			}
			return a.Before(&b)
		})
		bucketByPhase[phase] = bucket
	}

	totalTerminal := 0
	for _, bucket := range bucketByPhase {
		totalTerminal += len(bucket)
	}

	// Budget = total cap minus active count. Active runs always survive,
	// so they consume budget. If active alone exceeds the cap, no
	// terminal pruning happens (defensive — an operator with 50+ active
	// attempts has a more serious problem than retention).
	budget := max(maxTotal-len(active), 0)
	if totalTerminal <= budget {
		return nil
	}

	// We need to delete (totalTerminal - budget) attempts. Each terminal
	// bucket contributes its excess-above-floor first; if that's not
	// enough, walk the buckets oldest-first beyond the floor.
	excess := totalTerminal - budget
	var victims []kaprov1alpha2.PromotionRun

	// Pass 1: each bucket donates everything above its per-outcome floor.
	// Donations are taken from the OLDEST end of the bucket.
	for phase, bucket := range bucketByPhase {
		if len(bucket) <= minPerOutcome {
			continue
		}
		donate := min(len(bucket)-minPerOutcome, excess)
		victims = append(victims, bucket[:donate]...)
		bucketByPhase[phase] = bucket[donate:]
		excess -= donate
		if excess == 0 {
			return victims
		}
	}

	// Pass 2: still over budget even after each bucket trimmed to its
	// floor. We must violate the floor — take from whichever bucket has
	// the oldest remaining entry until we hit budget. This path happens
	// only when (numTerminalOutcomes * minPerOutcome) + activeCount >
	// maxTotal, which is a configuration choice the operator made.
	type cursor struct {
		phase kaprov1alpha2.PromotionRunPhase
		idx   int
	}
	allCursors := make([]cursor, 0, 3)
	for phase, bucket := range bucketByPhase {
		if len(bucket) > 0 {
			allCursors = append(allCursors, cursor{phase: phase, idx: 0})
		}
	}
	for excess > 0 && len(allCursors) > 0 {
		// Pick the cursor whose current head is the oldest.
		oldest := 0
		for i := 1; i < len(allCursors); i++ {
			lhs := bucketByPhase[allCursors[i].phase][allCursors[i].idx].CreationTimestamp
			rhs := bucketByPhase[allCursors[oldest].phase][allCursors[oldest].idx].CreationTimestamp
			if lhs.Before(&rhs) {
				oldest = i
			}
		}
		chosen := allCursors[oldest]
		victims = append(victims, bucketByPhase[chosen.phase][chosen.idx])
		allCursors[oldest].idx++
		excess--
		if allCursors[oldest].idx >= len(bucketByPhase[chosen.phase]) {
			// Bucket exhausted; remove cursor.
			allCursors = append(allCursors[:oldest], allCursors[oldest+1:]...)
		}
	}
	return victims
}

func (r *PromotionRunGCReconciler) maxRetained() int {
	if r.MaxRetainedPerPromotion > 0 {
		return r.MaxRetainedPerPromotion
	}
	return kaprov1alpha2.DefaultMaxRetainedPerPromotion
}

func (r *PromotionRunGCReconciler) minPerOutcome() int {
	if r.MinRetainedPerOutcome > 0 {
		return r.MinRetainedPerOutcome
	}
	return kaprov1alpha2.DefaultMinRetainedPerOutcome
}

// SetupWithManager registers the reconciler on the parent Promotion. We
// don't watch PromotionRuns directly — a Promotion event is enough to
// re-evaluate that Promotion's whole child set, and per-attempt deletions
// from a sibling PromotionRunReconciler call would create a noisy
// reconcile storm.
func (r *PromotionRunGCReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("promotionrun-gc").
		For(&kaprov1alpha2.Promotion{}).
		Complete(r)
}

// Predicate placeholder so future contributors can scope events without
// touching SetupWithManager. We accept all Promotion events today — the
// reconcile is cheap (one List, possibly one delete batch).
var _ = metav1.IsControlledBy
