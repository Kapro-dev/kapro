package main

import (
	"context"
	"errors"
	"testing"
	"time"
	"unicode/utf8"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
	"kapro.io/kapro/pkg/spokeprovider"
)

// scriptedProvider records its Reconcile inputs and returns a fixed result
// keyed by app. Unknown apps yield a Failed result with a sentinel error.
type scriptedProvider struct {
	driver  kaprov1alpha1.BackendDriver
	results map[string]spokeprovider.ReconcileResult
	calls   []spokeprovider.ReconcileRequest
}

func (s *scriptedProvider) Driver() kaprov1alpha1.BackendDriver { return s.driver }
func (s *scriptedProvider) Reconcile(_ context.Context, req spokeprovider.ReconcileRequest) spokeprovider.ReconcileResult {
	s.calls = append(s.calls, req)
	if r, ok := s.results[req.AppKey]; ok {
		return r
	}
	return spokeprovider.ReconcileResult{
		Phase: kaprov1alpha1.DeliveryPhaseFailed,
		Err:   errors.New("scriptedProvider: no result for app " + req.AppKey),
	}
}

func newDeliveryFC(name string, desired map[string]string, suspend bool, backend string) *kaprov1alpha1.FleetCluster {
	fc := &kaprov1alpha1.FleetCluster{}
	fc.Name = name
	fc.Spec.DesiredVersions = desired
	fc.Spec.Suspend = suspend
	fc.Spec.Delivery = kaprov1alpha1.DeliverySpec{
		Mode:       kaprov1alpha1.DeliveryModePull,
		BackendRef: backend,
	}
	return fc
}

func newDeliveryBP(name string, driver kaprov1alpha1.BackendDriver) *kaprov1alpha1.BackendProfile {
	bp := &kaprov1alpha1.BackendProfile{}
	bp.Name = name
	bp.Spec.Driver = driver
	bp.Spec.Runtime = kaprov1alpha1.BackendRuntimeSpoke
	return bp
}

func deliveryHub(t *testing.T, fc *kaprov1alpha1.FleetCluster, bp *kaprov1alpha1.BackendProfile) client.Client {
	t.Helper()
	scheme := newStatusScheme(t)
	b := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&kaprov1alpha1.FleetCluster{})
	if fc != nil {
		b = b.WithObjects(fc)
	}
	if bp != nil {
		b = b.WithObjects(bp)
	}
	return b.Build()
}

func TestDeliveryLoop_ConvergedAdvancesCurrentVersions(t *testing.T) {
	fc := newDeliveryFC("c1", map[string]string{"api": "1.0", "web": "2.0"}, false, "oci-default")
	bp := newDeliveryBP("oci-default", kaprov1alpha1.BackendDriverOCI)
	hub := deliveryHub(t, fc, bp)

	provider := &scriptedProvider{
		driver: kaprov1alpha1.BackendDriverOCI,
		results: map[string]spokeprovider.ReconcileResult{
			"api": {Phase: kaprov1alpha1.DeliveryPhaseConverged, ObservedDigest: "sha256:aaa", AppliedObjects: 5, Format: "raw-yaml"},
			"web": {Phase: kaprov1alpha1.DeliveryPhaseConverged, ObservedDigest: "sha256:bbb", AppliedObjects: 3, Format: "helm"},
		},
	}
	reg := spokeprovider.NewRegistry()
	if err := reg.Register(kaprov1alpha1.BackendDriverOCI, provider); err != nil {
		t.Fatalf("Register: %v", err)
	}
	l := &deliveryLoop{
		Hub:         newHubClientFromStatic(hub),
		ClusterName: "c1",
		Registry:    reg,
		Now:         func() time.Time { return time.Unix(1700000000, 0) },
	}

	if err := l.tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}

	var got kaprov1alpha1.FleetCluster
	if err := hub.Get(context.Background(), client.ObjectKey{Name: "c1"}, &got); err != nil {
		t.Fatalf("re-get: %v", err)
	}
	if got.Status.CurrentVersions["api"] != "1.0" || got.Status.CurrentVersions["web"] != "2.0" {
		t.Fatalf("currentVersions = %v, want api=1.0 web=2.0", got.Status.CurrentVersions)
	}
	if got.Status.Delivery["api"].Phase != kaprov1alpha1.DeliveryPhaseConverged {
		t.Fatalf("api phase = %q", got.Status.Delivery["api"].Phase)
	}
	if got.Status.Delivery["api"].ObservedDigest != "sha256:aaa" {
		t.Fatalf("api digest = %q", got.Status.Delivery["api"].ObservedDigest)
	}
	if got.Status.Delivery["api"].AppliedObjects != 5 {
		t.Fatalf("api appliedObjects = %d", got.Status.Delivery["api"].AppliedObjects)
	}
	if len(provider.calls) != 2 {
		t.Fatalf("provider called %d times, want 2", len(provider.calls))
	}
}

func TestDeliveryLoop_FailedDoesNotAdvanceCurrentVersions(t *testing.T) {
	fc := newDeliveryFC("c1", map[string]string{"api": "1.0"}, false, "oci-default")
	fc.Status.CurrentVersions = map[string]string{"api": "0.9"}
	bp := newDeliveryBP("oci-default", kaprov1alpha1.BackendDriverOCI)
	hub := deliveryHub(t, fc, bp)

	provider := &scriptedProvider{
		driver: kaprov1alpha1.BackendDriverOCI,
		results: map[string]spokeprovider.ReconcileResult{
			"api": {Phase: kaprov1alpha1.DeliveryPhaseFailed, Err: errors.New("registry 503")},
		},
	}
	reg := spokeprovider.NewRegistry()
	_ = reg.Register(kaprov1alpha1.BackendDriverOCI, provider)
	l := &deliveryLoop{
		Hub:         newHubClientFromStatic(hub),
		ClusterName: "c1",
		Registry:    reg,
	}

	if err := l.tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}

	var got kaprov1alpha1.FleetCluster
	if err := hub.Get(context.Background(), client.ObjectKey{Name: "c1"}, &got); err != nil {
		t.Fatalf("re-get: %v", err)
	}
	if got.Status.CurrentVersions["api"] != "0.9" {
		t.Fatalf("currentVersions[api] = %q, want unchanged 0.9", got.Status.CurrentVersions["api"])
	}
	entry := got.Status.Delivery["api"]
	if entry.Phase != kaprov1alpha1.DeliveryPhaseFailed {
		t.Fatalf("phase = %q", entry.Phase)
	}
	if entry.LastError == "" || entry.LastError != "registry 503" {
		t.Fatalf("lastError = %q", entry.LastError)
	}
}

func TestDeliveryLoop_SuspendedWritesSkipped(t *testing.T) {
	fc := newDeliveryFC("c1", map[string]string{"api": "1.0", "web": "2.0"}, true, "oci-default")
	fc.Status.CurrentVersions = map[string]string{"api": "0.9"}
	bp := newDeliveryBP("oci-default", kaprov1alpha1.BackendDriverOCI)
	hub := deliveryHub(t, fc, bp)

	provider := &scriptedProvider{driver: kaprov1alpha1.BackendDriverOCI}
	reg := spokeprovider.NewRegistry()
	_ = reg.Register(kaprov1alpha1.BackendDriverOCI, provider)
	l := &deliveryLoop{
		Hub:         newHubClientFromStatic(hub),
		ClusterName: "c1",
		Registry:    reg,
		Now:         func() time.Time { return time.Unix(1700000000, 0) },
	}

	if err := l.tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if len(provider.calls) != 0 {
		t.Fatalf("provider called on suspended cluster: %v", provider.calls)
	}

	var got kaprov1alpha1.FleetCluster
	if err := hub.Get(context.Background(), client.ObjectKey{Name: "c1"}, &got); err != nil {
		t.Fatalf("re-get: %v", err)
	}
	for _, app := range []string{"api", "web"} {
		if got.Status.Delivery[app].Phase != kaprov1alpha1.DeliveryPhaseSkipped {
			t.Fatalf("%s phase = %q, want Skipped", app, got.Status.Delivery[app].Phase)
		}
	}
	if got.Status.CurrentVersions["api"] != "0.9" {
		t.Fatalf("currentVersions[api] = %q, want preserved 0.9", got.Status.CurrentVersions["api"])
	}
}

func TestDeliveryLoop_UnknownDriverFailsLoudly(t *testing.T) {
	fc := newDeliveryFC("c1", map[string]string{"api": "1.0"}, false, "external-1")
	bp := newDeliveryBP("external-1", kaprov1alpha1.BackendDriverExternal)
	hub := deliveryHub(t, fc, bp)

	// Registry has only oci, not external. Resolve will fail.
	reg := spokeprovider.NewRegistry()
	_ = reg.Register(kaprov1alpha1.BackendDriverOCI, &scriptedProvider{driver: kaprov1alpha1.BackendDriverOCI})
	l := &deliveryLoop{
		Hub:         newHubClientFromStatic(hub),
		ClusterName: "c1",
		Registry:    reg,
	}

	if err := l.tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	var got kaprov1alpha1.FleetCluster
	if err := hub.Get(context.Background(), client.ObjectKey{Name: "c1"}, &got); err != nil {
		t.Fatalf("re-get: %v", err)
	}
	entry := got.Status.Delivery["api"]
	if entry.Phase != kaprov1alpha1.DeliveryPhaseFailed {
		t.Fatalf("phase = %q", entry.Phase)
	}
	if entry.LastError == "" {
		t.Fatalf("expected lastError to be populated")
	}
}

func TestDeliveryLoop_MissingBackendProfileFails(t *testing.T) {
	fc := newDeliveryFC("c1", map[string]string{"api": "1.0"}, false, "nonexistent")
	hub := deliveryHub(t, fc, nil)

	reg := spokeprovider.NewRegistry()
	_ = reg.Register(kaprov1alpha1.BackendDriverOCI, &scriptedProvider{driver: kaprov1alpha1.BackendDriverOCI})
	l := &deliveryLoop{
		Hub:         newHubClientFromStatic(hub),
		ClusterName: "c1",
		Registry:    reg,
	}

	if err := l.tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	var got kaprov1alpha1.FleetCluster
	if err := hub.Get(context.Background(), client.ObjectKey{Name: "c1"}, &got); err != nil {
		t.Fatalf("re-get: %v", err)
	}
	entry := got.Status.Delivery["api"]
	if entry.Phase != kaprov1alpha1.DeliveryPhaseFailed {
		t.Fatalf("phase = %q", entry.Phase)
	}
	if entry.LastError == "" {
		t.Fatalf("expected lastError to be populated")
	}
}

func TestDeliveryLoop_EmptyDesiredVersionsIsNoOp(t *testing.T) {
	fc := newDeliveryFC("c1", nil, false, "oci-default")
	bp := newDeliveryBP("oci-default", kaprov1alpha1.BackendDriverOCI)
	hub := deliveryHub(t, fc, bp)

	provider := &scriptedProvider{driver: kaprov1alpha1.BackendDriverOCI}
	reg := spokeprovider.NewRegistry()
	_ = reg.Register(kaprov1alpha1.BackendDriverOCI, provider)
	l := &deliveryLoop{
		Hub:         newHubClientFromStatic(hub),
		ClusterName: "c1",
		Registry:    reg,
	}

	if err := l.tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if len(provider.calls) != 0 {
		t.Fatalf("provider should not be called with empty desiredVersions")
	}

	var got kaprov1alpha1.FleetCluster
	if err := hub.Get(context.Background(), client.ObjectKey{Name: "c1"}, &got); err != nil {
		t.Fatalf("re-get: %v", err)
	}
	if len(got.Status.Delivery) != 0 {
		t.Fatalf("status.delivery should remain empty, got %v", got.Status.Delivery)
	}
}

func TestDeliveryLoop_MergesProfileAndClusterParameters(t *testing.T) {
	fc := newDeliveryFC("c1", map[string]string{"api": "1.0"}, false, "oci-default")
	fc.Spec.Delivery.Parameters = map[string]string{"tag": "{version}", "extra": "from-cluster"}
	bp := newDeliveryBP("oci-default", kaprov1alpha1.BackendDriverOCI)
	bp.Spec.Parameters = map[string]string{"repository": "r.io/x", "tag": "PROFILE-default"}
	hub := deliveryHub(t, fc, bp)

	provider := &scriptedProvider{
		driver: kaprov1alpha1.BackendDriverOCI,
		results: map[string]spokeprovider.ReconcileResult{
			"api": {Phase: kaprov1alpha1.DeliveryPhaseConverged},
		},
	}
	reg := spokeprovider.NewRegistry()
	_ = reg.Register(kaprov1alpha1.BackendDriverOCI, provider)
	l := &deliveryLoop{
		Hub:         newHubClientFromStatic(hub),
		ClusterName: "c1",
		Registry:    reg,
	}

	if err := l.tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if len(provider.calls) != 1 {
		t.Fatalf("provider calls = %d, want 1", len(provider.calls))
	}
	params := provider.calls[0].Parameters
	if params["repository"] != "r.io/x" {
		t.Fatalf("repository = %q, want r.io/x (from profile)", params["repository"])
	}
	if params["tag"] != "{version}" {
		t.Fatalf("tag = %q, want {version} (cluster wins over profile)", params["tag"])
	}
	if params["extra"] != "from-cluster" {
		t.Fatalf("extra = %q, want from-cluster", params["extra"])
	}
}

func TestDeliveryLoop_TruncateError(t *testing.T) {
	long := make([]byte, 5000)
	for i := range long {
		long[i] = 'x'
	}
	got := truncateError(string(long), 100)
	// "…" is 3 bytes in UTF-8, so output is 100 prefix bytes + ellipsis.
	if len(got) != 103 {
		t.Fatalf("truncateError len = %d, want 103", len(got))
	}
	short := "hi"
	if truncateError(short, 100) != "hi" {
		t.Fatalf("short string should pass through")
	}
}

func TestDeliveryLoop_TruncateError_DoesNotSplitMultibyteRune(t *testing.T) {
	// Build a string where byte index `max` falls in the middle of a
	// 3-byte UTF-8 character (the ellipsis itself). Without UTF-8-aware
	// truncation the result would contain a half-rune and be rejected by
	// the apiserver. The "max" we pick is the byte index of the second
	// byte of the 4th "…" — guaranteed mid-rune.
	s := "aaaaaaaaaaaaa…………"
	// First 13 ASCII bytes + 4× 3-byte ellipsis. Pick max=14 → middle of
	// first ellipsis (bytes 13,14,15).
	got := truncateError(s, 14)
	if !utf8.ValidString(got) {
		t.Fatalf("truncateError produced invalid UTF-8: %q (% x)", got, []byte(got))
	}
	// Must have shrunk back to a rune boundary at byte 13 (the ASCII).
	if got != "aaaaaaaaaaaaa…" {
		t.Fatalf("expected to cut back to rune boundary, got %q", got)
	}
}

func TestDeliveryLoop_BackfillsLastAttemptedAtWhenProviderOmitsIt(t *testing.T) {
	fc := newDeliveryFC("c1", map[string]string{"api": "1.0"}, false, "oci-default")
	bp := newDeliveryBP("oci-default", kaprov1alpha1.BackendDriverOCI)
	hub := deliveryHub(t, fc, bp)

	// A provider that returns a successful result but forgets to stamp
	// LastAttemptedAt — the loop must fill it from its own clock so SRE
	// dashboards still get a per-app timestamp.
	provider := &scriptedProvider{
		driver: kaprov1alpha1.BackendDriverOCI,
		results: map[string]spokeprovider.ReconcileResult{
			"api": {Phase: kaprov1alpha1.DeliveryPhaseConverged}, // no LastAttemptedAt
		},
	}
	reg := spokeprovider.NewRegistry()
	_ = reg.Register(kaprov1alpha1.BackendDriverOCI, provider)
	l := &deliveryLoop{
		Hub:         newHubClientFromStatic(hub),
		ClusterName: "c1",
		Registry:    reg,
		Now:         func() time.Time { return time.Unix(1700000777, 0) },
	}

	if err := l.tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	var got kaprov1alpha1.FleetCluster
	if err := hub.Get(context.Background(), client.ObjectKey{Name: "c1"}, &got); err != nil {
		t.Fatalf("re-get: %v", err)
	}
	entry := got.Status.Delivery["api"]
	if entry.LastAttemptedAt == nil {
		t.Fatalf("LastAttemptedAt was not backfilled")
	}
	if !entry.LastAttemptedAt.Time.Equal(time.Unix(1700000777, 0)) {
		t.Fatalf("LastAttemptedAt = %v, want injected clock value", entry.LastAttemptedAt.Time)
	}
}

func TestMergedDesiredVersions_LegacyFieldsAndMap(t *testing.T) {
	spec := kaprov1alpha1.FleetClusterSpec{
		DesiredVersion: "legacy-1",
		DesiredAppKey:  "legacy",
		DesiredVersions: map[string]string{
			"api": "1.0",
			"web": "", // dropped
		},
	}
	got := mergedDesiredVersions(spec)
	if got["api"] != "1.0" {
		t.Fatalf("api = %q", got["api"])
	}
	if got["legacy"] != "legacy-1" {
		t.Fatalf("legacy = %q", got["legacy"])
	}
	if _, ok := got["web"]; ok {
		t.Fatalf("empty web should be dropped")
	}
}

func TestMergedDesiredVersions_MapWinsOverLegacy(t *testing.T) {
	spec := kaprov1alpha1.FleetClusterSpec{
		DesiredVersion:  "old",
		DesiredAppKey:   "api",
		DesiredVersions: map[string]string{"api": "new"},
	}
	if got := mergedDesiredVersions(spec)["api"]; got != "new" {
		t.Fatalf("map should win: got %q", got)
	}
}

// silence unused-helper warnings if a test is trimmed later
var _ = metav1.Now
