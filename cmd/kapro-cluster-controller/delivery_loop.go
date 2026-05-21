package main

import (
	"context"
	"fmt"
	"sort"
	"time"
	"unicode/utf8"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
	"kapro.io/kapro/pkg/spokeprovider"
)

// deliveryLoop watches FleetCluster.spec.desiredVersions and dispatches each
// (appKey, version) tuple through the spoke Provider registry once per tick.
//
// One Get + one Status().Patch per tick — no per-app round-trips — so a
// fleet of N clusters with M apps scales as O(N) hub-side requests, not
// O(N*M). Per-app Provider.Reconcile errors are isolated; one failure does
// not abort the rest of the tick.
//
// The loop is the SINGLE writer of status.delivery and status.currentVersions
// on this cluster's FleetCluster — same RBAC owner as status.lastHeartbeat.
type deliveryLoop struct {
	Hub         *HubClient
	ClusterName string
	Interval    time.Duration

	// Registry resolves Provider implementations by BackendProfile.spec.driver.
	Registry *spokeprovider.Registry

	// Now is injected so tests can stamp deterministic timestamps.
	Now func() time.Time

	// MaxLastErrorBytes truncates LastError before writing, per the API
	// contract that bounds the status object size. Defaults to 4096.
	MaxLastErrorBytes int
}

const defaultDeliveryInterval = 30 * time.Second
const defaultMaxLastErrorBytes = 4096

// Run drives the delivery loop until ctx is cancelled. Errors from
// individual ticks are logged and never propagated — the loop is best-effort
// and the next tick retries from scratch.
func (l *deliveryLoop) Run(ctx context.Context) {
	logger := log.Log.WithName("delivery").WithValues("cluster", l.ClusterName)
	if l.Interval <= 0 {
		l.Interval = defaultDeliveryInterval
	}
	logger.Info("delivery loop starting", "interval", l.Interval)
	if err := l.tick(ctx); err != nil {
		logger.Error(err, "first delivery tick failed")
	}
	t := time.NewTicker(l.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := l.tick(ctx); err != nil {
				logger.Error(err, "delivery tick failed")
			}
		}
	}
}

// tick performs one reconcile pass over all desiredVersions on this cluster's
// FleetCluster. Returns an error only when the hub round-trip fails outright;
// per-app reconcile failures are written into status and not returned.
func (l *deliveryLoop) tick(ctx context.Context) error {
	tctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	hub, err := l.Hub.Client()
	if err != nil {
		return err
	}

	fc := &kaprov1alpha2.Cluster{}
	if err := hub.Get(tctx, client.ObjectKey{Name: l.ClusterName}, fc); err != nil {
		return fmt.Errorf("get FleetCluster %q: %w", l.ClusterName, err)
	}

	desired := mergedDesiredVersions(fc.Spec)

	// Suspend short-circuit: write Skipped for every app and return.
	if fc.Spec.Suspend {
		l.writeSuspended(tctx, hub, fc, desired)
		return nil
	}

	if len(desired) == 0 {
		// Nothing to do; do not touch status. A previously-populated
		// status.delivery is preserved so SREs still see the last attempt
		// after a deliberate rollback to empty desiredVersions.
		return nil
	}

	// Resolve the backend profile once per tick.
	profile, profErr := l.resolveBackendProfile(tctx, hub, fc.Spec.Delivery.BackendRef)

	results := make(map[string]spokeprovider.ReconcileResult, len(desired))
	for _, appKey := range sortedKeys(desired) {
		version := desired[appKey]
		res := l.reconcileOne(tctx, fc, profile, profErr, appKey, version)
		results[appKey] = res
	}

	return l.writeStatus(tctx, hub, fc, results, desired)
}

// reconcileOne resolves the right provider and runs one Reconcile call.
// Always returns a populated ReconcileResult — never panics, never returns
// an error to the caller.
func (l *deliveryLoop) reconcileOne(
	ctx context.Context,
	fc *kaprov1alpha2.Cluster,
	profile *kaprov1alpha2.Backend,
	profErr error,
	appKey, version string,
) spokeprovider.ReconcileResult {
	out := spokeprovider.ReconcileResult{LastAttemptedAt: l.now()}
	if profErr != nil {
		out.Phase = kaprov1alpha2.DeliveryPhaseFailed
		out.Err = profErr
		return out
	}
	if profile == nil {
		out.Phase = kaprov1alpha2.DeliveryPhaseFailed
		out.Err = fmt.Errorf("BackendProfile %q not found", fc.Spec.Delivery.BackendRef)
		return out
	}
	// Runtime gating: if this BackendProfile is hub-only, the hub-side
	// actuator owns delivery (it patches backend-native objects on the
	// hub, e.g. Flux OCIRepository.tag) and the spoke MUST stay out of the
	// way. Surface Skipped so SREs see why the spoke didn't act.
	if profile.Spec.Runtime == kaprov1alpha2.BackendRuntimeHub {
		out.Phase = kaprov1alpha2.DeliveryPhaseSkipped
		out.Err = fmt.Errorf("BackendProfile %q runtime is Hub; spoke delivery is a no-op", profile.Name)
		return out
	}
	if l.Registry == nil {
		out.Phase = kaprov1alpha2.DeliveryPhaseFailed
		out.Err = fmt.Errorf("delivery loop has no provider registry")
		return out
	}
	provider, err := l.Registry.Resolve(profile.Spec.Driver)
	if err != nil {
		out.Phase = kaprov1alpha2.DeliveryPhaseFailed
		out.Err = err
		return out
	}
	params := mergeParameters(profile.Spec.Parameters, fc.Spec.Delivery.Parameters)
	res := provider.Reconcile(ctx, spokeprovider.ReconcileRequest{
		Cluster:        fc,
		AppKey:         appKey,
		DesiredVersion: version,
		BackendProfile: profile,
		Parameters:     params,
	})
	// Backfill LastAttemptedAt if the Provider implementation forgot to set
	// it — SREs rely on this timestamp to answer "is the spoke alive?"
	// independently of whether ObservedDigest has populated yet.
	if res.LastAttemptedAt.IsZero() {
		res.LastAttemptedAt = l.now()
	}
	return res
}

// resolveBackendProfile reads the cluster-scoped BackendProfile referenced by
// fc.spec.delivery.backendRef. Returns a configuration error (not a wrapped
// IsNotFound) when the ref is missing/empty so per-app status carries a
// stable human-readable message.
func (l *deliveryLoop) resolveBackendProfile(ctx context.Context, hub client.Client, name string) (*kaprov1alpha2.Backend, error) {
	if name == "" {
		return nil, fmt.Errorf("FleetCluster.spec.delivery.backendRef is empty")
	}
	bp := &kaprov1alpha2.Backend{}
	if err := hub.Get(ctx, client.ObjectKey{Name: name}, bp); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("BackendProfile %q not found", name)
		}
		return nil, fmt.Errorf("get BackendProfile %q: %w", name, err)
	}
	return bp, nil
}

// writeStatus folds per-app ReconcileResults into a single Status().Patch.
// status.currentVersions[app] advances only when Phase==Converged.
// status.delivery[app] is always overwritten with the latest result so SREs
// see the most recent attempt's phase + lastError.
func (l *deliveryLoop) writeStatus(
	ctx context.Context,
	hub client.Client,
	fc *kaprov1alpha2.Cluster,
	results map[string]spokeprovider.ReconcileResult,
	desired map[string]string,
) error {
	patch := client.MergeFrom(fc.DeepCopy())

	if fc.Status.Delivery == nil {
		fc.Status.Delivery = map[string]kaprov1alpha2.FleetClusterDeliveryStatus{}
	}
	if fc.Status.CurrentVersions == nil {
		fc.Status.CurrentVersions = map[string]string{}
	}

	for appKey, res := range results {
		entry := kaprov1alpha2.FleetClusterDeliveryStatus{
			Phase:          res.Phase,
			DesiredVersion: desired[appKey],
			ObservedDigest: res.ObservedDigest,
			AppliedObjects: res.AppliedObjects,
			Format:         res.Format,
		}
		if !res.LastAttemptedAt.IsZero() {
			t := metav1.NewTime(res.LastAttemptedAt)
			entry.LastAttemptedAt = &t
		}
		if !res.LastAppliedAt.IsZero() {
			t := metav1.NewTime(res.LastAppliedAt)
			entry.LastAppliedAt = &t
		}
		if res.Err != nil {
			entry.LastError = truncateError(res.Err.Error(), l.maxLastErrorBytes())
		}
		fc.Status.Delivery[appKey] = entry

		if res.Phase == kaprov1alpha2.DeliveryPhaseConverged {
			fc.Status.CurrentVersions[appKey] = entry.DesiredVersion
		}
	}

	if err := hub.Status().Patch(ctx, fc, patch); err != nil {
		if apierrors.IsForbidden(err) {
			return fmt.Errorf("per-cluster RBAC missing for status patch: %w", err)
		}
		return fmt.Errorf("patch FleetCluster delivery status: %w", err)
	}
	return nil
}

// writeSuspended marks every desired app as Skipped without touching
// currentVersions — a suspended cluster preserves whatever was last
// converged, so resuming does not pretend the spoke has rolled back.
func (l *deliveryLoop) writeSuspended(
	ctx context.Context,
	hub client.Client,
	fc *kaprov1alpha2.Cluster,
	desired map[string]string,
) {
	if len(desired) == 0 {
		return
	}
	results := make(map[string]spokeprovider.ReconcileResult, len(desired))
	for appKey := range desired {
		results[appKey] = spokeprovider.ReconcileResult{
			Phase:           kaprov1alpha2.DeliveryPhaseSkipped,
			LastAttemptedAt: l.now(),
		}
	}
	if err := l.writeStatus(ctx, hub, fc, results, desired); err != nil {
		log.FromContext(ctx).Error(err, "write suspended delivery status")
	}
}

// mergedDesiredVersions returns spec.desiredVersions augmented with the
// legacy single-app pair (spec.desiredVersion + spec.desiredAppKey). The
// map form wins on collision since it is the modern field.
func mergedDesiredVersions(spec kaprov1alpha2.ClusterSpec) map[string]string {
	out := make(map[string]string, len(spec.DesiredVersions)+1)
	if spec.DesiredVersion != "" {
		key := spec.DesiredAppKey
		if key == "" {
			key = "default"
		}
		out[key] = spec.DesiredVersion
	}
	for k, v := range spec.DesiredVersions {
		if v == "" {
			continue
		}
		out[k] = v
	}
	return out
}

// mergeParameters overlays cluster params on top of profile params (cluster wins).
func mergeParameters(profile, cluster map[string]string) map[string]string {
	out := make(map[string]string, len(profile)+len(cluster))
	for k, v := range profile {
		out[k] = v
	}
	for k, v := range cluster {
		out[k] = v
	}
	return out
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// truncateError caps an error string at max BYTES, suffixing "…". It
// guards against splitting a multi-byte UTF-8 rune across the cut point so
// the resulting string is always valid UTF-8 (the apiserver rejects status
// fields with invalid UTF-8).
func truncateError(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

func (l *deliveryLoop) now() time.Time {
	if l.Now != nil {
		return l.Now()
	}
	return time.Now()
}

func (l *deliveryLoop) maxLastErrorBytes() int {
	if l.MaxLastErrorBytes > 0 {
		return l.MaxLastErrorBytes
	}
	return defaultMaxLastErrorBytes
}
