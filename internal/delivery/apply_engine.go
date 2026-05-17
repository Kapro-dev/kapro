package delivery

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// FieldManager is the Server-Side Apply field manager Kapro uses on the
// spoke cluster. Distinct from the hub's "kapro-operator" so SSA conflicts
// are diagnosable in audit logs.
const FieldManager = "kapro-cluster-controller"

// ApplyEngine is the two-phase server-side apply orchestrator.
//
// Phase 1 (Stage): each object is server-side-applied with DryRun=All. The
// API server runs admission webhooks and schema validation but no object is
// persisted. If ANY dry-run fails, the whole batch is aborted before any
// commit. This guarantees "all or nothing" in the validation sense — same
// invariant Sveltos's pullmode provides.
//
// Phase 2 (Commit): each object is server-side-applied for real. We only
// reach this phase when every dry-run succeeded. Per-object commit failures
// are surfaced as a multierror but the engine does NOT attempt to undo
// already-committed objects — partial commit is preferable to a destructive
// rollback that may delete unrelated workloads. The caller's status loop
// reports lastError and lets the next reconcile retry.
//
// Caller is responsible for:
//   - Setting Force=true if it wants to overwrite competing field managers.
//     Off by default to preserve user edits made in-cluster.
//   - Sorting objects upstream when ordering matters; the engine preserves
//     the slice order it received.
type ApplyEngine struct {
	// Client is a controller-runtime client wired to the spoke API server.
	Client client.Client
	// Force enables conflict resolution on SSA. Defaults to false — Kapro
	// prefers to surface "another field manager owns this field" loudly
	// over silently overwriting user input.
	Force bool
}

// ApplyResult summarises one Apply call.
type ApplyResult struct {
	// Staged is the count of objects whose dry-run validated.
	Staged int
	// Committed is the count of objects whose commit succeeded.
	Committed int
	// StagingErrors collects per-object dry-run failures. Non-empty means
	// no commits happened.
	StagingErrors []ObjectError
	// CommitErrors collects per-object commit failures. Non-empty means the
	// dry-run pass succeeded but at least one commit failed afterwards.
	CommitErrors []ObjectError
}

// Succeeded returns true when every object dry-ran AND committed cleanly.
func (r ApplyResult) Succeeded() bool {
	return len(r.StagingErrors) == 0 && len(r.CommitErrors) == 0 && r.Committed > 0
}

// Err returns the combined staging/commit error, or nil on success / empty input.
func (r ApplyResult) Err() error {
	if len(r.StagingErrors) > 0 {
		return fmt.Errorf("staging failed for %d/%d objects: %s",
			len(r.StagingErrors), r.Staged+len(r.StagingErrors), summariseErrors(r.StagingErrors))
	}
	if len(r.CommitErrors) > 0 {
		return fmt.Errorf("commit failed for %d/%d objects: %s",
			len(r.CommitErrors), r.Committed+len(r.CommitErrors), summariseErrors(r.CommitErrors))
	}
	return nil
}

// ObjectError pairs an object key with the error encountered.
type ObjectError struct {
	Key string
	Err error
}

// Apply runs the two-phase apply on objects in the supplied order.
// Empty input returns a zero-valued ApplyResult with no error — the caller
// is expected to treat zero-object renders as "nothing to do".
func (e *ApplyEngine) Apply(ctx context.Context, objects []*Object) (ApplyResult, error) {
	if e == nil || e.Client == nil {
		return ApplyResult{}, errors.New("ApplyEngine: nil client")
	}
	res := ApplyResult{}
	if len(objects) == 0 {
		return res, nil
	}

	// Phase 1: dry-run apply every object. Continue past per-object failures
	// so the caller sees the full set of validation problems in one shot —
	// rapid iteration UX, mirrors `kubectl apply --dry-run=server`.
	for _, obj := range objects {
		if obj == nil || obj.U == nil {
			continue
		}
		opts := []client.PatchOption{
			client.FieldOwner(FieldManager),
			client.DryRunAll,
		}
		if e.Force {
			opts = append(opts, client.ForceOwnership)
		}
		if err := e.Client.Patch(ctx, obj.U.DeepCopy(), client.Apply, opts...); err != nil {
			res.StagingErrors = append(res.StagingErrors, ObjectError{Key: obj.Key(), Err: err})
			continue
		}
		res.Staged++
	}
	if len(res.StagingErrors) > 0 {
		return res, res.Err()
	}

	// Phase 2: commit. We've already dry-run validated everything so commit
	// failures here are infrastructural (network blip, optimistic conflict).
	// We still proceed past individual failures so the caller can decide
	// whether to retry the residual or not.
	for _, obj := range objects {
		if obj == nil || obj.U == nil {
			continue
		}
		opts := []client.PatchOption{client.FieldOwner(FieldManager)}
		if e.Force {
			opts = append(opts, client.ForceOwnership)
		}
		toApply := obj.U.DeepCopy()
		if err := e.Client.Patch(ctx, toApply, client.Apply, opts...); err != nil {
			res.CommitErrors = append(res.CommitErrors, ObjectError{Key: obj.Key(), Err: err})
			continue
		}
		res.Committed++
	}
	if len(res.CommitErrors) > 0 {
		return res, res.Err()
	}
	return res, nil
}

// summariseErrors returns a compact, deterministic stringification of an
// ObjectError slice — first 3 errors verbatim, then "...and N more" when
// truncated. Keeps status messages bounded.
func summariseErrors(errs []ObjectError) string {
	sort.Slice(errs, func(i, j int) bool { return errs[i].Key < errs[j].Key })
	const maxShown = 3
	var parts []string
	for i, e := range errs {
		if i >= maxShown {
			parts = append(parts, fmt.Sprintf("...and %d more", len(errs)-maxShown))
			break
		}
		parts = append(parts, fmt.Sprintf("%s: %s", e.Key, errorBrief(e.Err)))
	}
	return strings.Join(parts, "; ")
}

// errorBrief shortens an apiserver error to its Reason+Message, dropping
// long-form status-object dumps that bloat the status field.
func errorBrief(err error) string {
	if err == nil {
		return ""
	}
	if s, ok := err.(apierrors.APIStatus); ok {
		st := s.Status()
		if st.Reason != "" {
			return fmt.Sprintf("%s: %s", st.Reason, st.Message)
		}
		return st.Message
	}
	return err.Error()
}

// Compile-time assert that *unstructured.Unstructured satisfies client.Object.
var _ client.Object = (*unstructured.Unstructured)(nil)
