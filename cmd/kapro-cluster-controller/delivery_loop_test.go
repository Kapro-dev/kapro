package main

import (
	"context"
	"errors"
	"testing"
	"time"
	"unicode/utf8"

	promtestutil "github.com/prometheus/client_golang/prometheus/testutil"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kaprov1alpha1 "kapro.io/kapro/api/kapro/v1alpha1"
	"kapro.io/kapro/pkg/spokeprovider"
)

// scriptedProvider records its Reconcile inputs and returns a fixed result
// keyed by app. Unknown apps yield a Failed result with a sentinel error.
type scriptedProvider struct {
	driver  kaprov1alpha1.SubstrateDriver
	results map[string]spokeprovider.ReconcileResult
	calls   []spokeprovider.ReconcileRequest
}

func (s *scriptedProvider) Driver() kaprov1alpha1.SubstrateDriver { return s.driver }
func (s *scriptedProvider) Capabilities() spokeprovider.Capabilities {
	return spokeprovider.Capabilities{
		Driver:            s.driver,
		SupportsReconcile: true,
		SupportsObserve:   true,
		SupportsApply:     true,
	}
}
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

func newDeliveryFC(name string, desired map[string]string, suspend bool, substrate string) *kaprov1alpha1.Cluster {
	fc := &kaprov1alpha1.Cluster{}
	fc.Name = name
	fc.Spec.DesiredVersions = desired
	fc.Spec.Suspend = suspend
	fc.Spec.Delivery = kaprov1alpha1.DeliverySpec{
		Mode:         kaprov1alpha1.DeliveryModePull,
		SubstrateRef: substrate,
	}
	return fc
}

func newDeliveryBP(name string, driver kaprov1alpha1.SubstrateDriver) *kaprov1alpha1.Substrate {
	bp := &kaprov1alpha1.Substrate{}
	bp.Name = name
	bp.Spec.Substrate = &kaprov1alpha1.SubstrateImplementationSpec{Kind: string(driver), Actuator: string(driver)}
	bp.Spec.Execution = &kaprov1alpha1.SubstrateExecutionSpec{Mode: kaprov1alpha1.ExecutionModeSpokePull}
	return bp
}

func deliveryHub(t *testing.T, fc *kaprov1alpha1.Cluster, bp *kaprov1alpha1.Substrate) client.Client {
	t.Helper()
	scheme := newStatusScheme(t)
	b := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&kaprov1alpha1.Cluster{})
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
	bp := newDeliveryBP("oci-default", kaprov1alpha1.SubstrateDriverOCI)
	hub := deliveryHub(t, fc, bp)

	provider := &scriptedProvider{
		driver: kaprov1alpha1.SubstrateDriverOCI,
		results: map[string]spokeprovider.ReconcileResult{
			"api": {Phase: kaprov1alpha1.DeliveryPhaseConverged, ObservedDigest: "sha256:aaa", AppliedObjects: 5, Format: "raw-yaml"},
			"web": {Phase: kaprov1alpha1.DeliveryPhaseConverged, ObservedDigest: "sha256:bbb", AppliedObjects: 3, Format: "helm"},
		},
	}
	reg := spokeprovider.NewRegistry()
	if err := reg.Register(kaprov1alpha1.SubstrateDriverOCI, provider); err != nil {
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

	var got kaprov1alpha1.Cluster
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

func TestDeliveryLoop_RecordsDeliveryMetrics(t *testing.T) {
	fc := newDeliveryFC("metrics-c1", map[string]string{"api": "1.0"}, false, "oci-default")
	bp := newDeliveryBP("oci-default", kaprov1alpha1.SubstrateDriverOCI)
	hub := deliveryHub(t, fc, bp)

	provider := &scriptedProvider{
		driver: kaprov1alpha1.SubstrateDriverOCI,
		results: map[string]spokeprovider.ReconcileResult{
			"api": {Phase: kaprov1alpha1.DeliveryPhaseConverged},
		},
	}
	reg := spokeprovider.NewRegistry()
	_ = reg.Register(kaprov1alpha1.SubstrateDriverOCI, provider)
	l := &deliveryLoop{
		Hub:         newHubClientFromStatic(hub),
		ClusterName: "metrics-c1",
		Registry:    reg,
	}
	labels := []string{"metrics-c1", "oci", string(kaprov1alpha1.DeliveryPhaseConverged), "success"}
	before := promtestutil.ToFloat64(spokeDeliveryReconciles.WithLabelValues(labels...))

	if err := l.tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}

	if got := promtestutil.ToFloat64(spokeDeliveryReconciles.WithLabelValues(labels...)) - before; got != 1 {
		t.Fatalf("spoke delivery counter delta=%v, want 1", got)
	}
}

func TestDeliveryLoop_EmitsDeliverySpans(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	defer otel.SetTracerProvider(previous)

	fc := newDeliveryFC("trace-c1", map[string]string{"api": "1.0"}, false, "oci-default")
	bp := newDeliveryBP("oci-default", kaprov1alpha1.SubstrateDriverOCI)
	hub := deliveryHub(t, fc, bp)

	providerImpl := &scriptedProvider{
		driver: kaprov1alpha1.SubstrateDriverOCI,
		results: map[string]spokeprovider.ReconcileResult{
			"api": {
				Phase:          kaprov1alpha1.DeliveryPhaseConverged,
				ObservedDigest: "sha256:aaa",
				AppliedObjects: 2,
				Format:         "raw-yaml",
			},
		},
	}
	reg := spokeprovider.NewRegistry()
	_ = reg.Register(kaprov1alpha1.SubstrateDriverOCI, providerImpl)
	l := &deliveryLoop{
		Hub:         newHubClientFromStatic(hub),
		ClusterName: "trace-c1",
		Registry:    reg,
	}

	if err := l.tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}

	tick := findSpan(t, recorder.Ended(), "kapro.spoke.delivery.tick")
	tickAttrs := deliverySpanAttributes(tick)
	for key, want := range map[string]string{
		"kapro.cluster":                "trace-c1",
		"kapro.delivery.substrate_ref": "oci-default",
	} {
		if got := tickAttrs[key].AsString(); got != want {
			t.Fatalf("tick attribute %s = %q, want %q (all attrs %#v)", key, got, want, tickAttrs)
		}
	}
	if got := tickAttrs["kapro.desired_version_count"].AsInt64(); got != 1 {
		t.Fatalf("desired_version_count = %d, want 1", got)
	}
	if got := tickAttrs["kapro.spoke.delivery.status_write"].AsBool(); !got {
		t.Fatalf("status_write = false, want true")
	}

	reconcile := findSpan(t, recorder.Ended(), "kapro.spoke.delivery.reconcile")
	reconcileAttrs := deliverySpanAttributes(reconcile)
	for key, want := range map[string]string{
		"kapro.cluster":                  "trace-c1",
		"kapro.app_key":                  "api",
		"kapro.version":                  "1.0",
		"kapro.delivery.substrate_ref":   "oci-default",
		"kapro.delivery.substrate":       "oci-default",
		"kapro.delivery.driver":          string(kaprov1alpha1.SubstrateDriverOCI),
		"kapro.delivery.phase":           string(kaprov1alpha1.DeliveryPhaseConverged),
		"kapro.delivery.result":          "success",
		"kapro.delivery.format":          "raw-yaml",
		"kapro.delivery.observed_digest": "sha256:aaa",
	} {
		if got := reconcileAttrs[key].AsString(); got != want {
			t.Fatalf("reconcile attribute %s = %q, want %q (all attrs %#v)", key, got, want, reconcileAttrs)
		}
	}
	if got := reconcileAttrs["kapro.delivery.applied_objects"].AsInt64(); got != 2 {
		t.Fatalf("applied_objects = %d, want 2", got)
	}
}

func TestDeliveryLoop_FailedDeliverySpanIsError(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	defer otel.SetTracerProvider(previous)

	fc := newDeliveryFC("trace-fail", map[string]string{"api": "1.0"}, false, "oci-default")
	bp := newDeliveryBP("oci-default", kaprov1alpha1.SubstrateDriverOCI)
	hub := deliveryHub(t, fc, bp)

	providerImpl := &scriptedProvider{
		driver: kaprov1alpha1.SubstrateDriverOCI,
		results: map[string]spokeprovider.ReconcileResult{
			"api": {Phase: kaprov1alpha1.DeliveryPhaseFailed, Err: errors.New("registry 503")},
		},
	}
	reg := spokeprovider.NewRegistry()
	_ = reg.Register(kaprov1alpha1.SubstrateDriverOCI, providerImpl)
	l := &deliveryLoop{
		Hub:         newHubClientFromStatic(hub),
		ClusterName: "trace-fail",
		Registry:    reg,
	}

	if err := l.tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}

	reconcile := findSpan(t, recorder.Ended(), "kapro.spoke.delivery.reconcile")
	if reconcile.Status().Code != codes.Error {
		t.Fatalf("reconcile status = %v, want error", reconcile.Status())
	}
	attrs := deliverySpanAttributes(reconcile)
	if got := attrs["kapro.delivery.result"].AsString(); got != "error" {
		t.Fatalf("delivery result = %q, want error", got)
	}
}

func TestDeliveryLoop_FailedDoesNotAdvanceCurrentVersions(t *testing.T) {
	fc := newDeliveryFC("c1", map[string]string{"api": "1.0"}, false, "oci-default")
	fc.Status.CurrentVersions = map[string]string{"api": "0.9"}
	bp := newDeliveryBP("oci-default", kaprov1alpha1.SubstrateDriverOCI)
	hub := deliveryHub(t, fc, bp)

	provider := &scriptedProvider{
		driver: kaprov1alpha1.SubstrateDriverOCI,
		results: map[string]spokeprovider.ReconcileResult{
			"api": {Phase: kaprov1alpha1.DeliveryPhaseFailed, Err: errors.New("registry 503")},
		},
	}
	reg := spokeprovider.NewRegistry()
	_ = reg.Register(kaprov1alpha1.SubstrateDriverOCI, provider)
	l := &deliveryLoop{
		Hub:         newHubClientFromStatic(hub),
		ClusterName: "c1",
		Registry:    reg,
	}

	if err := l.tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}

	var got kaprov1alpha1.Cluster
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

func TestDeliveryLoop_WritesStagingDiagnostics(t *testing.T) {
	fc := newDeliveryFC("c1", map[string]string{"api": "1.0"}, false, "oci-default")
	fc.Status.CurrentVersions = map[string]string{"api": "0.9"}
	fc.Spec.Delivery.Staging = &kaprov1alpha1.DeliveryStagingSpec{
		Type:          kaprov1alpha1.DeliveryStagingTwoPhase,
		FailurePolicy: kaprov1alpha1.DeliveryStagingFailureAbort,
	}
	bp := newDeliveryBP("oci-default", kaprov1alpha1.SubstrateDriverOCI)
	hub := deliveryHub(t, fc, bp)

	provider := &scriptedProvider{
		driver: kaprov1alpha1.SubstrateDriverOCI,
		results: map[string]spokeprovider.ReconcileResult{
			"api": {
				Phase: kaprov1alpha1.DeliveryPhaseFailed,
				Staging: &kaprov1alpha1.DeliveryStagingStatus{
					StagedObjects:        3,
					StagingFailedObjects: 1,
					FailurePhase:         kaprov1alpha1.DeliveryPhaseStaging,
				},
				Err: errors.New("staging failed for api"),
			},
		},
	}
	reg := spokeprovider.NewRegistry()
	_ = reg.Register(kaprov1alpha1.SubstrateDriverOCI, provider)
	l := &deliveryLoop{
		Hub:         newHubClientFromStatic(hub),
		ClusterName: "c1",
		Registry:    reg,
	}

	if err := l.tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}

	var got kaprov1alpha1.Cluster
	if err := hub.Get(context.Background(), client.ObjectKey{Name: "c1"}, &got); err != nil {
		t.Fatalf("re-get: %v", err)
	}
	if got.Status.CurrentVersions["api"] != "0.9" {
		t.Fatalf("currentVersions[api] = %q, want unchanged 0.9", got.Status.CurrentVersions["api"])
	}
	entry := got.Status.Delivery["api"]
	if entry.Staging == nil {
		t.Fatal("status.delivery[api].staging not written")
	}
	if entry.Staging.Type != kaprov1alpha1.DeliveryStagingTwoPhase {
		t.Fatalf("staging.type = %q, want TwoPhase", entry.Staging.Type)
	}
	if entry.Staging.FailurePolicy != kaprov1alpha1.DeliveryStagingFailureAbort {
		t.Fatalf("staging.failurePolicy = %q, want Abort", entry.Staging.FailurePolicy)
	}
	if entry.Staging.StagedObjects != 3 || entry.Staging.StagingFailedObjects != 1 {
		t.Fatalf("staging counts = %+v, want staged=3 failed=1", entry.Staging)
	}
	if entry.Staging.FailurePhase != kaprov1alpha1.DeliveryPhaseStaging {
		t.Fatalf("failurePhase = %q, want Staging", entry.Staging.FailurePhase)
	}
}

func TestDeliveryLoop_SuspendedWritesSkipped(t *testing.T) {
	fc := newDeliveryFC("c1", map[string]string{"api": "1.0", "web": "2.0"}, true, "oci-default")
	fc.Status.CurrentVersions = map[string]string{"api": "0.9"}
	bp := newDeliveryBP("oci-default", kaprov1alpha1.SubstrateDriverOCI)
	hub := deliveryHub(t, fc, bp)

	provider := &scriptedProvider{driver: kaprov1alpha1.SubstrateDriverOCI}
	reg := spokeprovider.NewRegistry()
	_ = reg.Register(kaprov1alpha1.SubstrateDriverOCI, provider)
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

	var got kaprov1alpha1.Cluster
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
	bp := newDeliveryBP("external-1", kaprov1alpha1.SubstrateDriverExternal)
	hub := deliveryHub(t, fc, bp)

	// Registry has only oci, not external. Resolve will fail.
	reg := spokeprovider.NewRegistry()
	_ = reg.Register(kaprov1alpha1.SubstrateDriverOCI, &scriptedProvider{driver: kaprov1alpha1.SubstrateDriverOCI})
	l := &deliveryLoop{
		Hub:         newHubClientFromStatic(hub),
		ClusterName: "c1",
		Registry:    reg,
	}

	if err := l.tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	var got kaprov1alpha1.Cluster
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

func TestDeliveryLoop_ClassRefFluxResolvesProvider(t *testing.T) {
	fc := newDeliveryFC("c1", map[string]string{"api": "1.0"}, false, "team-flux")
	bp := &kaprov1alpha1.Substrate{
		ObjectMeta: metav1.ObjectMeta{Name: "team-flux"},
		Spec: kaprov1alpha1.SubstrateSpec{
			ClassRef: &kaprov1alpha1.SubstrateClassReference{Name: "flux"},
			ConfigRef: &kaprov1alpha1.SubstrateObjectReference{
				APIVersion: "flux.substrate.kapro.io/v1alpha1",
				Kind:       "FluxSubstrateConfig",
				Name:       "team-flux",
			},
			Execution: &kaprov1alpha1.SubstrateExecutionSpec{Mode: kaprov1alpha1.ExecutionModeSpokePull},
		},
		Status: kaprov1alpha1.SubstrateStatus{Ready: true},
	}
	hub := deliveryHub(t, fc, bp)

	provider := &scriptedProvider{
		driver: kaprov1alpha1.SubstrateDriverFlux,
		results: map[string]spokeprovider.ReconcileResult{
			"api": {Phase: kaprov1alpha1.DeliveryPhaseConverged},
		},
	}
	reg := spokeprovider.NewRegistry()
	if err := reg.Register(kaprov1alpha1.SubstrateDriverFlux, provider); err != nil {
		t.Fatalf("Register: %v", err)
	}
	l := &deliveryLoop{
		Hub:         newHubClientFromStatic(hub),
		ClusterName: "c1",
		Registry:    reg,
	}

	if err := l.tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if len(provider.calls) != 1 {
		t.Fatalf("provider called %d times, want 1", len(provider.calls))
	}
	if provider.calls[0].SubstrateProfile.Name != "team-flux" {
		t.Fatalf("substrate profile = %q", provider.calls[0].SubstrateProfile.Name)
	}
}

func TestDeliveryLoop_MissingSubstrateFails(t *testing.T) {
	fc := newDeliveryFC("c1", map[string]string{"api": "1.0"}, false, "nonexistent")
	hub := deliveryHub(t, fc, nil)

	reg := spokeprovider.NewRegistry()
	_ = reg.Register(kaprov1alpha1.SubstrateDriverOCI, &scriptedProvider{driver: kaprov1alpha1.SubstrateDriverOCI})
	l := &deliveryLoop{
		Hub:         newHubClientFromStatic(hub),
		ClusterName: "c1",
		Registry:    reg,
	}

	if err := l.tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	var got kaprov1alpha1.Cluster
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
	bp := newDeliveryBP("oci-default", kaprov1alpha1.SubstrateDriverOCI)
	hub := deliveryHub(t, fc, bp)

	provider := &scriptedProvider{driver: kaprov1alpha1.SubstrateDriverOCI}
	reg := spokeprovider.NewRegistry()
	_ = reg.Register(kaprov1alpha1.SubstrateDriverOCI, provider)
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

	var got kaprov1alpha1.Cluster
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
	bp := newDeliveryBP("oci-default", kaprov1alpha1.SubstrateDriverOCI)
	bp.Spec.Parameters = map[string]string{"repository": "r.io/x", "tag": "PROFILE-default"}
	hub := deliveryHub(t, fc, bp)

	provider := &scriptedProvider{
		driver: kaprov1alpha1.SubstrateDriverOCI,
		results: map[string]spokeprovider.ReconcileResult{
			"api": {Phase: kaprov1alpha1.DeliveryPhaseConverged},
		},
	}
	reg := spokeprovider.NewRegistry()
	_ = reg.Register(kaprov1alpha1.SubstrateDriverOCI, provider)
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

func TestDeliveryLoop_HubRuntimeProfileSkipsSpokeDelivery(t *testing.T) {
	fc := newDeliveryFC("c1", map[string]string{"api": "1.0"}, false, "flux-hub")
	bp := newDeliveryBP("flux-hub", kaprov1alpha1.SubstrateDriverFlux)
	bp.Spec.Execution.Mode = kaprov1alpha1.ExecutionModeHubPush
	hub := deliveryHub(t, fc, bp)

	// Even though no flux provider is registered, the loop must NOT reach
	// the registry-resolve step for a hub-only profile — the hub-side
	// actuator owns delivery.
	provider := &scriptedProvider{driver: kaprov1alpha1.SubstrateDriverOCI}
	reg := spokeprovider.NewRegistry()
	_ = reg.Register(kaprov1alpha1.SubstrateDriverOCI, provider)
	l := &deliveryLoop{
		Hub:         newHubClientFromStatic(hub),
		ClusterName: "c1",
		Registry:    reg,
	}

	if err := l.tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if len(provider.calls) != 0 {
		t.Fatalf("provider should not be invoked for Runtime=Hub profile")
	}

	var got kaprov1alpha1.Cluster
	if err := hub.Get(context.Background(), client.ObjectKey{Name: "c1"}, &got); err != nil {
		t.Fatalf("re-get: %v", err)
	}
	entry := got.Status.Delivery["api"]
	if entry.Phase != kaprov1alpha1.DeliveryPhaseSkipped {
		t.Fatalf("phase = %q, want Skipped", entry.Phase)
	}
	if entry.LastError == "" {
		t.Fatalf("expected lastError to explain why spoke skipped")
	}
}

func TestDeliveryLoop_BothRuntimeProfileRunsOnSpoke(t *testing.T) {
	fc := newDeliveryFC("c1", map[string]string{"api": "1.0"}, false, "oci-both")
	bp := newDeliveryBP("oci-both", kaprov1alpha1.SubstrateDriverOCI)
	bp.Spec.Execution.Mode = kaprov1alpha1.ExecutionModeSpokePull
	hub := deliveryHub(t, fc, bp)

	provider := &scriptedProvider{
		driver: kaprov1alpha1.SubstrateDriverOCI,
		results: map[string]spokeprovider.ReconcileResult{
			"api": {Phase: kaprov1alpha1.DeliveryPhaseConverged},
		},
	}
	reg := spokeprovider.NewRegistry()
	_ = reg.Register(kaprov1alpha1.SubstrateDriverOCI, provider)
	l := &deliveryLoop{
		Hub:         newHubClientFromStatic(hub),
		ClusterName: "c1",
		Registry:    reg,
	}

	if err := l.tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if len(provider.calls) != 1 {
		t.Fatalf("Runtime=Both should run the spoke provider; got %d calls", len(provider.calls))
	}
}

func TestDeliveryLoop_BackfillsLastAttemptedAtWhenProviderOmitsIt(t *testing.T) {
	fc := newDeliveryFC("c1", map[string]string{"api": "1.0"}, false, "oci-default")
	bp := newDeliveryBP("oci-default", kaprov1alpha1.SubstrateDriverOCI)
	hub := deliveryHub(t, fc, bp)

	// A provider that returns a successful result but forgets to stamp
	// LastAttemptedAt — the loop must fill it from its own clock so SRE
	// dashboards still get a per-app timestamp.
	provider := &scriptedProvider{
		driver: kaprov1alpha1.SubstrateDriverOCI,
		results: map[string]spokeprovider.ReconcileResult{
			"api": {Phase: kaprov1alpha1.DeliveryPhaseConverged}, // no LastAttemptedAt
		},
	}
	reg := spokeprovider.NewRegistry()
	_ = reg.Register(kaprov1alpha1.SubstrateDriverOCI, provider)
	l := &deliveryLoop{
		Hub:         newHubClientFromStatic(hub),
		ClusterName: "c1",
		Registry:    reg,
		Now:         func() time.Time { return time.Unix(1700000777, 0) },
	}

	if err := l.tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	var got kaprov1alpha1.Cluster
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
	spec := kaprov1alpha1.ClusterSpec{
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
	spec := kaprov1alpha1.ClusterSpec{
		DesiredVersion:  "old",
		DesiredAppKey:   "api",
		DesiredVersions: map[string]string{"api": "new"},
	}
	if got := mergedDesiredVersions(spec)["api"]; got != "new" {
		t.Fatalf("map should win: got %q", got)
	}
}

func findSpan(t *testing.T, spans []sdktrace.ReadOnlySpan, name string) sdktrace.ReadOnlySpan {
	t.Helper()
	for _, span := range spans {
		if span.Name() == name {
			return span
		}
	}
	t.Fatalf("span %q not found in %d spans", name, len(spans))
	return nil
}

func deliverySpanAttributes(span sdktrace.ReadOnlySpan) map[string]attribute.Value {
	attrs := map[string]attribute.Value{}
	for _, attr := range span.Attributes() {
		attrs[string(attr.Key)] = attr.Value
	}
	return attrs
}
