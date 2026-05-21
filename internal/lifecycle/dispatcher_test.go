package lifecycle

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
)

// newTestDispatcher builds a Dispatcher backed by a fake client with the
// given seed objects. The HTTP client points at an in-process test server
// and the SSRF guard is bypassed (loopback is normally forbidden).
func newTestDispatcher(t *testing.T, objs ...client.Object) (*Dispatcher, client.Client, *record.FakeRecorder) {
	t.Helper()
	t.Setenv(allowInsecureEnv, "1") // enable http:// + loopback for httptest
	scheme := runtime.NewScheme()
	if err := kaprov1alpha2.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		WithStatusSubresource(&kaprov1alpha2.Promotion{}).
		Build()
	rec := record.NewFakeRecorder(32)
	d := &Dispatcher{
		Client:     c,
		Recorder:   rec,
		HTTPClient: &http.Client{Timeout: 5 * time.Second},
		Namespace:  "kapro-system",
		Now:        time.Now,
		rootCtx:    context.Background(),
		inflight:   make(map[string]struct{}),
	}
	return d, c, rec
}

func newTestPromotion(name string, handlers ...kaprov1alpha2.PromotionLifecycleHandler) *kaprov1alpha2.Promotion {
	return &kaprov1alpha2.Promotion{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: kaprov1alpha2.PromotionSpec{
			FleetRef: "checkout",
			Version:  "v1.2.3",
			Lifecycle: &kaprov1alpha2.PromotionLifecycleSpec{
				Handlers: handlers,
			},
		},
		Status: kaprov1alpha2.PromotionStatus{
			ActiveAttemptRef: &kaprov1alpha2.PromotionAttemptRef{Name: name + "-att-1"},
		},
	}
}

// TestWebhookFiresOnceOnPhaseTransition verifies the happy path:
// a 2xx response yields one HTTP call and a Succeeded status entry.
func TestWebhookFiresOnceOnPhaseTransition(t *testing.T) {
	var calls int32
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		body, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	p := newTestPromotion("checkout", kaprov1alpha2.PromotionLifecycleHandler{
		Name: "slack",
		On:   []kaprov1alpha2.PromotionPhase{kaprov1alpha2.PromotionPhaseSucceeded},
		Webhook: &kaprov1alpha2.PromotionLifecycleWebhook{
			URL: srv.URL,
		},
	})
	d, c, _ := newTestDispatcher(t, p)

	d.OnPhaseTransition(context.Background(), p,
		kaprov1alpha2.PromotionPhaseProgressing, kaprov1alpha2.PromotionPhaseSucceeded)
	d.Wait()

	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("calls = %d, want 1", got)
	}

	// CloudEvents payload must match the stable vocabulary contract — same
	// envelope as the operator-level sink (built via pkg/events.Render).
	var env map[string]any
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if env["specversion"] != "1.0" {
		t.Fatalf("specversion = %v, want 1.0", env["specversion"])
	}
	if env["type"] != "kapro.io/promotion.succeeded" {
		t.Fatalf("type = %v, want kapro.io/promotion.succeeded", env["type"])
	}
	if env["subject"] != "checkout" {
		t.Fatalf("subject = %v, want checkout", env["subject"])
	}
	data := env["data"].(map[string]any)
	if data["phase"] != "Succeeded" || data["previousPhase"] != "Progressing" {
		t.Fatalf("data = %+v, want phase=Succeeded prev=Progressing", data)
	}

	// Status is updated with a Succeeded result.
	var got kaprov1alpha2.Promotion
	if err := c.Get(context.Background(), client.ObjectKey{Name: p.Name}, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Status.LifecycleHandlerResults) != 1 {
		t.Fatalf("results = %d, want 1", len(got.Status.LifecycleHandlerResults))
	}
	res := got.Status.LifecycleHandlerResults[0]
	if res.Result != resultSucceeded || res.Attempts != 1 || res.HTTPStatus != http.StatusNoContent {
		t.Fatalf("result = %+v", res)
	}
}

// TestWebhookRetriesTransient5xx confirms transient 5xx responses retry
// up to MaxRetries+1 attempts and the final outcome is Failed.
func TestWebhookRetriesTransient5xx(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	maxRetries := int32(2)
	p := newTestPromotion("checkout", kaprov1alpha2.PromotionLifecycleHandler{
		Name:    "retrying",
		On:      []kaprov1alpha2.PromotionPhase{kaprov1alpha2.PromotionPhaseFailed},
		Webhook: &kaprov1alpha2.PromotionLifecycleWebhook{URL: srv.URL},
		// Tight per-handler budget so the test isn't slow.
		Timeout:    &metav1.Duration{Duration: 5 * time.Second},
		MaxRetries: &maxRetries,
	})
	d, c, _ := newTestDispatcher(t, p)

	// Override base backoff so the test runs in ~ms, not seconds. The
	// backoffBase const is fine in prod; we just want a fast test.
	overrideBackoff(t, 5*time.Millisecond)

	d.OnPhaseTransition(context.Background(), p,
		kaprov1alpha2.PromotionPhaseProgressing, kaprov1alpha2.PromotionPhaseFailed)
	d.Wait()

	if got := atomic.LoadInt32(&calls); got != maxRetries+1 {
		t.Fatalf("calls = %d, want %d (1 initial + %d retries)", got, maxRetries+1, maxRetries)
	}

	var got kaprov1alpha2.Promotion
	if err := c.Get(context.Background(), client.ObjectKey{Name: p.Name}, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status.LifecycleHandlerResults[0].Result != resultFailed {
		t.Fatalf("result = %q, want Failed", got.Status.LifecycleHandlerResults[0].Result)
	}
}

// TestWebhookDoesNotRetry4xx confirms permanent client errors short-
// circuit retries.
func TestWebhookDoesNotRetry4xx(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	maxRetries := int32(3)
	p := newTestPromotion("checkout", kaprov1alpha2.PromotionLifecycleHandler{
		Name:       "no-retry",
		On:         []kaprov1alpha2.PromotionPhase{kaprov1alpha2.PromotionPhaseSucceeded},
		Webhook:    &kaprov1alpha2.PromotionLifecycleWebhook{URL: srv.URL},
		MaxRetries: &maxRetries,
	})
	d, _, _ := newTestDispatcher(t, p)

	d.OnPhaseTransition(context.Background(), p,
		kaprov1alpha2.PromotionPhaseProgressing, kaprov1alpha2.PromotionPhaseSucceeded)
	d.Wait()

	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("calls = %d, want 1 (4xx must not retry)", got)
	}
}

// TestIdempotencyOnRefire verifies the dispatcher skips handlers already
// recorded with a terminal Result for the same (handler, phase, attempt)
// tuple. This is what makes at-least-once delivery safe across restarts.
func TestIdempotencyOnRefire(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	p := newTestPromotion("checkout", kaprov1alpha2.PromotionLifecycleHandler{
		Name:    "idempotent",
		On:      []kaprov1alpha2.PromotionPhase{kaprov1alpha2.PromotionPhaseSucceeded},
		Webhook: &kaprov1alpha2.PromotionLifecycleWebhook{URL: srv.URL},
	})
	d, c, _ := newTestDispatcher(t, p)

	d.OnPhaseTransition(context.Background(), p,
		kaprov1alpha2.PromotionPhaseProgressing, kaprov1alpha2.PromotionPhaseSucceeded)
	d.Wait()

	// Refetch and replay — the status now records the prior success, so
	// the second OnPhaseTransition should be a no-op.
	var refetched kaprov1alpha2.Promotion
	if err := c.Get(context.Background(), client.ObjectKey{Name: p.Name}, &refetched); err != nil {
		t.Fatal(err)
	}
	// Carry the spec.lifecycle through (fake client preserves it; sanity).
	d.OnPhaseTransition(context.Background(), &refetched,
		kaprov1alpha2.PromotionPhaseProgressing, kaprov1alpha2.PromotionPhaseSucceeded)
	d.Wait()

	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("calls = %d, want 1 (second fire must be idempotent)", got)
	}
}

// TestEventHandlerEmitsKubernetesEvent verifies the Event handler kind
// produces a Kubernetes Event with substituted message fields and never
// makes an HTTP call.
func TestEventHandlerEmitsKubernetesEvent(t *testing.T) {
	p := newTestPromotion("checkout", kaprov1alpha2.PromotionLifecycleHandler{
		Name: "shout",
		On:   []kaprov1alpha2.PromotionPhase{kaprov1alpha2.PromotionPhaseSucceeded},
		Event: &kaprov1alpha2.PromotionLifecycleEvent{
			Reason:  "PromotedToProd",
			Message: "{{.Name}} promoted to prod at version {{.Version}}",
			Type:    "Normal",
		},
	})
	d, c, rec := newTestDispatcher(t, p)

	d.OnPhaseTransition(context.Background(), p,
		kaprov1alpha2.PromotionPhaseProgressing, kaprov1alpha2.PromotionPhaseSucceeded)
	d.Wait()

	// Expect: one "LifecycleHookFired" and one user-defined "PromotedToProd".
	gotEvents := drainEvents(rec)
	wantContains := []string{"PromotedToProd", "promoted to prod at version v1.2.3", "LifecycleHookFired"}
	for _, want := range wantContains {
		if !containsSubstring(gotEvents, want) {
			t.Fatalf("missing event containing %q; got=%v", want, gotEvents)
		}
	}

	var got kaprov1alpha2.Promotion
	if err := c.Get(context.Background(), client.ObjectKey{Name: p.Name}, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status.LifecycleHandlerResults[0].Kind != kindEvent {
		t.Fatalf("Kind = %q, want Event", got.Status.LifecycleHandlerResults[0].Kind)
	}
}

// TestNoCrossPhaseFiring verifies a handler keyed on phase=Succeeded does
// not fire on a Failed transition.
func TestNoCrossPhaseFiring(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
	}))
	defer srv.Close()

	p := newTestPromotion("checkout", kaprov1alpha2.PromotionLifecycleHandler{
		Name:    "success-only",
		On:      []kaprov1alpha2.PromotionPhase{kaprov1alpha2.PromotionPhaseSucceeded},
		Webhook: &kaprov1alpha2.PromotionLifecycleWebhook{URL: srv.URL},
	})
	d, _, _ := newTestDispatcher(t, p)

	d.OnPhaseTransition(context.Background(), p,
		kaprov1alpha2.PromotionPhaseProgressing, kaprov1alpha2.PromotionPhaseFailed)
	d.Wait()

	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Fatalf("calls = %d, want 0", got)
	}
}

// TestSecretAuthHeaderInjected verifies the dispatcher reads the auth
// header value from the referenced Secret and sets it on the request.
func TestSecretAuthHeaderInjected(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "slack-hook", Namespace: "kapro-system"},
		Data:       map[string][]byte{"token": []byte("Bearer xoxb-abc")},
	}
	p := newTestPromotion("checkout", kaprov1alpha2.PromotionLifecycleHandler{
		Name: "authd",
		On:   []kaprov1alpha2.PromotionPhase{kaprov1alpha2.PromotionPhaseSucceeded},
		Webhook: &kaprov1alpha2.PromotionLifecycleWebhook{
			URL: srv.URL,
			AuthHeader: &kaprov1alpha2.PromotionLifecycleAuthHeader{
				Name:       "Authorization",
				SecretName: "slack-hook",
				SecretKey:  "token",
			},
		},
	})
	d, _, _ := newTestDispatcher(t, p, secret)

	d.OnPhaseTransition(context.Background(), p,
		kaprov1alpha2.PromotionPhaseProgressing, kaprov1alpha2.PromotionPhaseSucceeded)
	d.Wait()

	if gotAuth != "Bearer xoxb-abc" {
		t.Fatalf("Authorization header = %q, want %q", gotAuth, "Bearer xoxb-abc")
	}
}

// TestStatusBoundedAtMax verifies the lifecycleHandlerResults list does
// not grow unbounded.
func TestStatusBoundedAtMax(t *testing.T) {
	var list []kaprov1alpha2.PromotionLifecycleHandlerResult
	for i := range kaprov1alpha2.MaxLifecycleHandlerResults + 10 {
		r := kaprov1alpha2.PromotionLifecycleHandlerResult{
			Name:  "h",
			Phase: kaprov1alpha2.PromotionPhaseSucceeded,
			// Unique attempt names keep upsertLifecycleResult from collapsing
			// entries into a single slot.
			AttemptName: time.Now().Format(time.RFC3339Nano) + "-" + string(rune('A'+i%26)),
			Result:      resultSucceeded,
			FiredAt:     metav1.NewTime(time.Now().Add(time.Duration(i) * time.Millisecond)),
		}
		list = upsertLifecycleResult(list, r)
	}
	if len(list) != kaprov1alpha2.MaxLifecycleHandlerResults {
		t.Fatalf("len = %d, want %d", len(list), kaprov1alpha2.MaxLifecycleHandlerResults)
	}
}

// TestX509ErrorShortCircuitsRetries verifies that an unrecoverable TLS
// certificate verification failure (x509.UnknownAuthorityError) is
// classified as permanent so the dispatcher does not retry it.
func TestX509ErrorShortCircuitsRetries(t *testing.T) {
	// Server with a self-signed cert that the default RootCAs will not
	// trust; the client transport here uses the system pool, so the
	// handshake will fail with x509.UnknownAuthorityError.
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	maxRetries := int32(5)
	p := newTestPromotion("checkout", kaprov1alpha2.PromotionLifecycleHandler{
		Name:       "cert-broken",
		On:         []kaprov1alpha2.PromotionPhase{kaprov1alpha2.PromotionPhaseSucceeded},
		Webhook:    &kaprov1alpha2.PromotionLifecycleWebhook{URL: srv.URL}, // httptest.NewTLSServer URL is https://
		MaxRetries: &maxRetries,
	})
	// Build a dispatcher that does NOT trust the test server's cert.
	d, c, _ := newTestDispatcher(t, p)
	d.HTTPClient = &http.Client{} // default transport, no test-server cert pool

	overrideBackoff(t, time.Millisecond)
	start := time.Now()
	d.OnPhaseTransition(context.Background(), p,
		kaprov1alpha2.PromotionPhaseProgressing, kaprov1alpha2.PromotionPhaseSucceeded)
	d.Wait()
	elapsed := time.Since(start)

	// A retrying dispatcher would loop maxRetries+1 = 6 times. We assert
	// the run completes well under the time required for that, AND we
	// check the recorded attempts to confirm short-circuit.
	if elapsed > 2*time.Second {
		t.Fatalf("dispatcher took %v; x509 should short-circuit retries", elapsed)
	}
	var got kaprov1alpha2.Promotion
	if err := c.Get(context.Background(), client.ObjectKey{Name: p.Name}, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Status.LifecycleHandlerResults) != 1 {
		t.Fatalf("results = %d, want 1", len(got.Status.LifecycleHandlerResults))
	}
	res := got.Status.LifecycleHandlerResults[0]
	if res.Result != resultFailed {
		t.Fatalf("result = %q, want Failed", res.Result)
	}
	if res.Attempts != 1 {
		t.Fatalf("attempts = %d, want 1 (cert error must not retry)", res.Attempts)
	}
}

// TestBothKindsSetRecordedAsFailed verifies a misconfigured handler with
// both spec.webhook and spec.event set is recorded as Failed with a clear
// message instead of silently picking one.
func TestBothKindsSetRecordedAsFailed(t *testing.T) {
	p := newTestPromotion("checkout", kaprov1alpha2.PromotionLifecycleHandler{
		Name: "ambiguous",
		On:   []kaprov1alpha2.PromotionPhase{kaprov1alpha2.PromotionPhaseSucceeded},
		Webhook: &kaprov1alpha2.PromotionLifecycleWebhook{
			URL: "https://example.com/hook",
		},
		Event: &kaprov1alpha2.PromotionLifecycleEvent{
			Reason: "Shout",
		},
	})
	d, c, _ := newTestDispatcher(t, p)

	d.OnPhaseTransition(context.Background(), p,
		kaprov1alpha2.PromotionPhaseProgressing, kaprov1alpha2.PromotionPhaseSucceeded)
	d.Wait()

	var got kaprov1alpha2.Promotion
	if err := c.Get(context.Background(), client.ObjectKey{Name: p.Name}, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Status.LifecycleHandlerResults) != 1 {
		t.Fatalf("results = %d, want 1", len(got.Status.LifecycleHandlerResults))
	}
	res := got.Status.LifecycleHandlerResults[0]
	if res.Result != resultFailed || res.Kind != "Misconfigured" {
		t.Fatalf("result = %+v, want Result=Failed Kind=Misconfigured", res)
	}
	if !containsString(res.Message, "both spec.webhook and spec.event") {
		t.Fatalf("message = %q, want guidance about both kinds set", res.Message)
	}
}

// TestPerHandlerTimeoutHonoredAbove30s verifies that the dispatcher's
// per-handler timeout (set on the handler spec, up to 5m) is not silently
// capped by the default HTTP client. The default client must have no
// Timeout; the per-request context is the only deadline.
func TestPerHandlerTimeoutHonoredAbove30s(t *testing.T) {
	c := defaultHTTPClient()
	if c.Timeout != 0 {
		t.Fatalf("defaultHTTPClient().Timeout = %v, want 0 (per-request ctx is the only deadline)", c.Timeout)
	}
}

// TestNonHTTPSWebhookRejected confirms the SSRF / cleartext guard kicks
// in when the operator has NOT opted in to insecure webhooks.
func TestNonHTTPSWebhookRejected(t *testing.T) {
	// Re-disable the env var that newTestDispatcher set, then bypass that
	// helper to keep the safety check in place.
	t.Setenv(allowInsecureEnv, "")

	_, err := validateWebhookURL("http://example.com/hook", false)
	if err == nil {
		t.Fatal("expected http:// to be rejected; got nil")
	}
	if _, err := validateWebhookURL("https://example.com/hook", false); err != nil {
		t.Fatalf("https:// must be allowed; got %v", err)
	}
}

// drainEvents reads all queued events from a FakeRecorder without blocking.
func drainEvents(rec *record.FakeRecorder) []string {
	var out []string
	for {
		select {
		case e := <-rec.Events:
			out = append(out, e)
		default:
			return out
		}
	}
}

func containsSubstring(haystack []string, needle string) bool {
	for _, s := range haystack {
		if containsString(s, needle) {
			return true
		}
	}
	return false
}

func containsString(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	// Naive search keeps the test file dependency-free.
	if len(sub) == 0 {
		return 0
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// overrideBackoff swaps the package-level backoffBase for the duration of
// a test. Tests do not run in parallel, so direct mutation is safe.
func overrideBackoff(t *testing.T, d time.Duration) {
	prev := backoffBase
	backoffBase = d
	t.Cleanup(func() { backoffBase = prev })
}
