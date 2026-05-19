package lifecycle

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
	"kapro.io/kapro/pkg/events"
)

func newSinkTestDispatcher(t *testing.T, sinkURL string, objs ...*kaprov1alpha1.Promotion) (*Dispatcher, *Sink) {
	t.Helper()
	t.Setenv(allowInsecureEnv, "1")
	scheme := runtime.NewScheme()
	if err := kaprov1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	builder := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kaprov1alpha1.Promotion{})
	for _, p := range objs {
		builder = builder.WithObjects(p)
	}
	c := builder.Build()

	sink := &Sink{
		URL:        sinkURL,
		Timeout:    2 * time.Second,
		MaxRetries: 0,
		HTTPClient: &http.Client{Timeout: 2 * time.Second},
	}
	d := &Dispatcher{
		Client:     c,
		Recorder:   record.NewFakeRecorder(32),
		HTTPClient: &http.Client{Timeout: 2 * time.Second},
		Namespace:  "kapro-system",
		Sink:       sink,
		Now:        time.Now,
		rootCtx:    context.Background(),
		inflight:   make(map[string]struct{}),
	}
	return d, sink
}

// TestSinkReceivesCloudEventsForEveryTransition is the canary that proves
// the canonical CNCF integration point works: every phase transition
// produces exactly one CloudEvents v1.0 POST to the configured operator
// sink, with the right Kapro vocabulary type.
func TestSinkReceivesCloudEventsForEveryTransition(t *testing.T) {
	var calls int32
	var (
		bodiesMu sync.Mutex
		bodies   [][]byte
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		body, _ := io.ReadAll(r.Body)
		bodiesMu.Lock()
		bodies = append(bodies, body)
		bodiesMu.Unlock()
		if got := r.Header.Get("Content-Type"); got != "application/cloudevents+json" {
			t.Errorf("Content-Type = %q, want application/cloudevents+json", got)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	p := &kaprov1alpha1.Promotion{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout"},
		Spec: kaprov1alpha1.PromotionSpec{
			KaproRef: "checkout-fleet",
			Version:  "v1.2.3",
		},
	}
	d, _ := newSinkTestDispatcher(t, srv.URL, p)

	transitions := []struct{ prev, next kaprov1alpha1.PromotionPhase }{
		{"", kaprov1alpha1.PromotionPhasePending},
		{kaprov1alpha1.PromotionPhasePending, kaprov1alpha1.PromotionPhaseProgressing},
		{kaprov1alpha1.PromotionPhaseProgressing, kaprov1alpha1.PromotionPhaseSucceeded},
	}
	for _, tr := range transitions {
		d.OnPhaseTransition(context.Background(), p, tr.prev, tr.next)
	}
	d.Wait()

	if got := atomic.LoadInt32(&calls); got != int32(len(transitions)) {
		t.Fatalf("sink calls = %d, want %d", got, len(transitions))
	}

	// Goroutine ordering is non-deterministic — verify the *set* of event
	// types delivered, not the order. Every transition's CloudEvent type
	// must appear exactly once.
	bodiesMu.Lock()
	gotTypes := make(map[events.EventType]int, len(bodies))
	for _, body := range bodies {
		var env events.Envelope
		if err := json.Unmarshal(body, &env); err != nil {
			t.Fatalf("unmarshal envelope: %v", err)
		}
		if env.SpecVersion != "1.0" {
			t.Fatalf("specversion = %q, want 1.0", env.SpecVersion)
		}
		if env.Data.KaproRef != "checkout-fleet" {
			t.Fatalf("data.kaproRef = %q", env.Data.KaproRef)
		}
		gotTypes[env.Type]++
	}
	bodiesMu.Unlock()
	wantTypes := []events.EventType{
		events.EventPromotionCreated,     // "" -> Pending
		events.EventPromotionProgressing, // Pending -> Progressing
		events.EventPromotionSucceeded,   // Progressing -> Succeeded
	}
	for _, want := range wantTypes {
		if gotTypes[want] != 1 {
			t.Fatalf("event type %q delivered %d times, want 1 (gotTypes=%v)", want, gotTypes[want], gotTypes)
		}
	}
}

// TestSinkFailureDoesNotBlockPerPromotionHandlers verifies the
// canonical-vs-ergonomic isolation. A broken operator sink should never
// prevent the in-CRD spec.lifecycle.handlers[] from firing.
func TestSinkFailureDoesNotBlockPerPromotionHandlers(t *testing.T) {
	var handlerCalls int32
	handlerSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&handlerCalls, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer handlerSrv.Close()

	// Sink endpoint that always returns 500.
	var sinkCalls int32
	sinkSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&sinkCalls, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer sinkSrv.Close()

	p := &kaprov1alpha1.Promotion{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout"},
		Spec: kaprov1alpha1.PromotionSpec{
			KaproRef: "checkout-fleet",
			Version:  "v1.2.3",
			Lifecycle: &kaprov1alpha1.PromotionLifecycleSpec{
				Handlers: []kaprov1alpha1.PromotionLifecycleHandler{{
					Name: "team-channel",
					On:   []kaprov1alpha1.PromotionPhase{kaprov1alpha1.PromotionPhaseSucceeded},
					Webhook: &kaprov1alpha1.PromotionLifecycleWebhook{
						URL: handlerSrv.URL,
					},
				}},
			},
		},
	}
	d, _ := newSinkTestDispatcher(t, sinkSrv.URL, p)
	overrideBackoff(t, time.Millisecond)

	d.OnPhaseTransition(context.Background(), p,
		kaprov1alpha1.PromotionPhaseProgressing, kaprov1alpha1.PromotionPhaseSucceeded)
	d.Wait()

	if got := atomic.LoadInt32(&handlerCalls); got != 1 {
		t.Fatalf("per-Promotion handler called %d times, want 1 (sink failure must not block)", got)
	}
	if got := atomic.LoadInt32(&sinkCalls); got == 0 {
		t.Fatal("sink was not called at all")
	}
}

// TestSinkAuthHeaderInjected verifies the operator-level auth header
// (sourced via env at the Deployment level) is set on each sink POST.
func TestSinkAuthHeaderInjected(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("X-Operator-Token")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	p := &kaprov1alpha1.Promotion{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout"},
		Spec:       kaprov1alpha1.PromotionSpec{KaproRef: "k"},
	}
	d, sink := newSinkTestDispatcher(t, srv.URL, p)
	sink.AuthHeaderName = "X-Operator-Token"
	sink.AuthHeaderValue = "s3cret-token"

	d.OnPhaseTransition(context.Background(), p, "", kaprov1alpha1.PromotionPhasePending)
	d.Wait()

	if gotAuth != "s3cret-token" {
		t.Fatalf("auth header = %q, want %q", gotAuth, "s3cret-token")
	}
}

// TestSinkFromEnvParsing exercises every documented env var.
func TestSinkFromEnvParsing(t *testing.T) {
	t.Setenv(allowInsecureEnv, "1")
	t.Setenv(SinkEnvURL, "http://sink.example.com")
	t.Setenv(SinkEnvAuthHeaderName, "X-Token")
	t.Setenv(SinkEnvAuthHeaderValue, "abc123")
	t.Setenv(SinkEnvTimeout, "7s")
	t.Setenv(SinkEnvMaxRetries, "5")

	s, err := SinkFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if s == nil {
		t.Fatal("SinkFromEnv returned nil with URL set")
	}
	if s.URL != "http://sink.example.com" {
		t.Fatalf("URL = %q", s.URL)
	}
	if s.AuthHeaderName != "X-Token" || s.AuthHeaderValue != "abc123" {
		t.Fatalf("auth = %q / %q", s.AuthHeaderName, s.AuthHeaderValue)
	}
	if s.Timeout != 7*time.Second {
		t.Fatalf("Timeout = %v, want 7s", s.Timeout)
	}
	if s.MaxRetries != 5 {
		t.Fatalf("MaxRetries = %d, want 5", s.MaxRetries)
	}
}

// TestSinkFromEnvDisabledWhenURLUnset is the nil-safety contract for
// production operators that don't run a sink.
func TestSinkFromEnvDisabledWhenURLUnset(t *testing.T) {
	t.Setenv(SinkEnvURL, "")
	s, err := SinkFromEnv()
	if err != nil {
		t.Fatalf("err = %v, want nil when URL unset", err)
	}
	if s != nil {
		t.Fatalf("Sink = %+v, want nil when URL unset", s)
	}
}
