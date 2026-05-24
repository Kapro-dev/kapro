package actuator

import (
	"context"
	"errors"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kaprov1alpha1 "kapro.io/kapro/api/kapro/v1alpha1"
)

// ErrTwoPhaseUnsupported is the canonical error returned by actuators that do
// not implement the optional two-phase staging extension.
var ErrTwoPhaseUnsupported = errors.New("two-phase staging not supported by this actuator")

// TwoPhaseStaging is an optional actuator extension for substrates that can stage
// a write, commit it later, or discard it without mutating live state.
//
// Prepare must validate the requested desired versions without committing live
// workload changes. Commit makes a prepared handle live. Discard releases a
// prepared handle without committing it. Implementations should make all three
// methods idempotent for the same handle ID.
type TwoPhaseStaging interface {
	Prepare(ctx context.Context, req StageRequest) (StageHandle, error)
	Commit(ctx context.Context, handle StageHandle) (CommitResult, error)
	Discard(ctx context.Context, handle StageHandle) error
}

// StageRequest carries the inputs required to prepare a staged delivery.
type StageRequest struct {
	Cluster         *kaprov1alpha1.Cluster
	DesiredVersions map[string]string
	DryRun          bool
}

// StageHandle is an opaque substrate-issued reference to prepared work.
type StageHandle struct {
	ID        string
	Substrate kaprov1alpha1.SubstrateDriver
	AppKeys   []string
	Expiry    metav1.Time
}

// CommitResult summarizes a two-phase commit attempt.
type CommitResult struct {
	Applied int
	Phase   kaprov1alpha1.DeliveryPhase
}

// AsTwoPhase returns an actuator's optional two-phase staging extension.
func AsTwoPhase(a Actuator) (TwoPhaseStaging, bool) {
	staging, ok := a.(TwoPhaseStaging)
	return staging, ok
}
