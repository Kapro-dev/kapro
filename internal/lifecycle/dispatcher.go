// Package lifecycle dispatches user-declared Promotion lifecycle handlers
// (Webhook, Event) on coarse phase transitions. The dispatcher is
// fire-and-forget — handler failures are recorded but never block the
// Promotion FSM. Delivery semantics are at-least-once: handlers must be
// idempotent at the receiver, keyed by (handlerName, phase, attemptName)
// which are exposed in the CloudEvents payload.
package lifecycle

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
	"kapro.io/kapro/internal/metrics"
	"kapro.io/kapro/pkg/events"
)

const (
	defaultHandlerTimeout = 30 * time.Second
	maxHandlerTimeout     = 5 * time.Minute
	defaultMaxRetries     = 3
	cloudEventsSpec       = "1.0"
	resultSucceeded       = "Succeeded"
	resultFailed          = "Failed"
	kindWebhook           = "Webhook"
	kindEvent             = "Event"

	// allowInsecureEnv enables HTTP (cleartext) and SSRF-allowed webhook
	// URLs. Intended for in-cluster sinks (e.g. http://kafka-bridge.kafka)
	// and local development; production deployments should leave it unset.
	allowInsecureEnv = "KAPRO_LIFECYCLE_INSECURE_WEBHOOKS"
)

// backoffBase is the linear-backoff increment between webhook retries.
// Production cadence is 2s, 4s, 6s, .... It is a var (not a const) so tests
// can swap it for sub-second values to keep the test suite fast.
var backoffBase = 2 * time.Second

// Dispatcher fires lifecycle handlers asynchronously on Promotion phase
// transitions. A single Dispatcher is shared by the PromotionController;
// each call to Publish spawns one goroutine per matched handler plus,
// when configured, one goroutine for the operator-level CloudEvents sink.
type Dispatcher struct {
	Client     client.Client
	Recorder   record.EventRecorder
	HTTPClient *http.Client
	// Namespace is the operator's own namespace, used to resolve Secret
	// references from PromotionLifecycleAuthHeader.
	Namespace string
	// Sink is the optional operator-level CloudEvents subscriber. When
	// non-nil, every Publish call fans out to this single URL in
	// addition to the per-Promotion handlers. This is the canonical
	// integration point for Argo Events / Flux Notification Controller /
	// kube-event-exporter / Knative / any CloudEvents-aware system.
	Sink *Sink
	// Now is injected for deterministic tests; defaults to time.Now.
	Now func() time.Time
	// rootCtx is the long-lived context tied to manager shutdown. All
	// fire-and-forget goroutines derive their context from this so that
	// shutdown drains in-flight invocations cleanly.
	rootCtx context.Context
	// wg tracks in-flight goroutines so tests (and graceful shutdown) can
	// wait for completion.
	wg sync.WaitGroup
	// inflight deduplicates concurrent invocations of the same
	// (handler, phase, attempt) triple across reconciles. Without this,
	// two reconcile loops that both observe the same transition before
	// status is updated would each fire the same handler.
	inflight   map[string]struct{}
	inflightMu sync.Mutex
}

// NewDispatcher constructs a Dispatcher rooted at the given context (the
// manager's context). HTTPClient is nil-defaulted to a defaulted client
// with no client-level timeout (per-request context drives deadlines) and
// an SSRF-guarded transport.
func NewDispatcher(rootCtx context.Context, c client.Client, rec record.EventRecorder, namespace string) *Dispatcher {
	return &Dispatcher{
		Client:     c,
		Recorder:   rec,
		Namespace:  namespace,
		Now:        time.Now,
		rootCtx:    rootCtx,
		HTTPClient: defaultHTTPClient(),
		inflight:   make(map[string]struct{}),
	}
}

// WithSink attaches an operator-level CloudEvents sink. Returns the same
// dispatcher for chaining at the construction site.
func (d *Dispatcher) WithSink(s *Sink) *Dispatcher {
	d.Sink = s
	return d
}

// Wait blocks until all in-flight handler goroutines complete. Intended
// for tests and manager shutdown drains.
func (d *Dispatcher) Wait() { d.wg.Wait() }

// OnPhaseTransition is called from the PromotionController after a status
// patch that changed the coarse phase. It is a thin compatibility shim
// that builds a Kapro CloudEvents Event and calls Publish.
//
// Existing callers in the controller use this signature; new emitters
// (attempt stamped, attempt superseded) call Publish directly.
func (d *Dispatcher) OnPhaseTransition(ctx context.Context,
	promotion *kaprov1alpha1.Promotion,
	prevPhase, newPhase kaprov1alpha1.PromotionPhase,
) {
	if promotion == nil || newPhase == "" || newPhase == prevPhase {
		return
	}
	d.Publish(ctx, promotion, events.Event{
		Type:          eventTypeForPhase(newPhase),
		PromotionName: promotion.Name,
		PromotionUID:  string(promotion.UID),
		KaproRef:      promotion.Spec.KaproRef,
		Phase:         string(newPhase),
		PreviousPhase: string(prevPhase),
		Version:       promotion.Spec.Version,
		AttemptName:   activeAttemptName(promotion),
		Reason:        fmt.Sprintf("Promotion phase: %s -> %s", prevPhase, newPhase),
	})
}

// Publish dispatches a Kapro lifecycle event:
//
//  1. The operator-level Sink (when configured) receives the rendered
//     CloudEvents v1.0 envelope. This is the canonical subscription
//     path for Argo Events / Flux Notification Controller /
//     kube-event-exporter / Knative / any CloudEvents-aware system.
//  2. Per-Promotion lifecycle handlers (spec.lifecycle.handlers[]) that
//     nominate the event's phase fire asynchronously. Existing
//     idempotency on (name, phase, attempt) applies.
//
// Both paths run in independent goroutines: a sink delivery failure does
// not block per-Promotion handlers, and vice versa.
func (d *Dispatcher) Publish(_ context.Context, promotion *kaprov1alpha1.Promotion, ev events.Event) {
	if promotion == nil || ev.Type == "" {
		return
	}

	// 1) Operator-level sink fanout (fire-and-forget).
	if d.Sink != nil && d.Sink.URL != "" {
		d.wg.Add(1)
		go func() {
			defer d.wg.Done()
			d.Sink.Publish(d.rootCtx, d.Recorder, promotionRefForEvent(promotion), ev)
		}()
	}

	// 2) Per-Promotion handlers (phase-filtered, idempotent).
	if promotion.Spec.Lifecycle == nil {
		return
	}
	phase := kaprov1alpha1.PromotionPhase(ev.Phase)
	for i := range promotion.Spec.Lifecycle.Handlers {
		h := promotion.Spec.Lifecycle.Handlers[i]
		if !handlerMatchesPhase(&h, phase) {
			continue
		}
		if alreadyFinal(promotion, h.Name, phase, ev.AttemptName) {
			continue
		}
		key := inflightKey(promotion.Name, h.Name, phase, ev.AttemptName)
		if !d.tryReserveInflight(key) {
			continue
		}
		snap := snapshot{
			PromotionName: promotion.Name,
			PromotionUID:  string(promotion.UID),
			Generation:    promotion.Generation,
			Phase:         phase,
			PrevPhase:     kaprov1alpha1.PromotionPhase(ev.PreviousPhase),
			AttemptName:   ev.AttemptName,
			Version:       promotion.Spec.Version,
			KaproRef:      promotion.Spec.KaproRef,
			Handler:       h,
			InflightKey:   key,
		}
		d.wg.Add(1)
		go d.run(snap)
	}
}

// activeAttemptName picks the most relevant PromotionRun name for an
// event: the active attempt when one is in flight, otherwise the newest
// attempt in history. Empty when no attempt exists yet.
func activeAttemptName(p *kaprov1alpha1.Promotion) string {
	if p.Status.ActiveAttemptRef != nil {
		return p.Status.ActiveAttemptRef.Name
	}
	if len(p.Status.Attempts) > 0 {
		return p.Status.Attempts[0].Name
	}
	return ""
}

// eventTypeForPhase maps a coarse Promotion phase to its CloudEvents
// type from the stable pkg/events vocabulary.
func eventTypeForPhase(phase kaprov1alpha1.PromotionPhase) events.EventType {
	switch phase {
	case kaprov1alpha1.PromotionPhasePending:
		return events.EventPromotionCreated
	case kaprov1alpha1.PromotionPhaseProgressing:
		return events.EventPromotionProgressing
	case kaprov1alpha1.PromotionPhasePaused:
		return events.EventPromotionPaused
	case kaprov1alpha1.PromotionPhaseRestarting:
		return events.EventPromotionRestarting
	case kaprov1alpha1.PromotionPhaseSucceeded:
		return events.EventPromotionSucceeded
	case kaprov1alpha1.PromotionPhaseFailed:
		return events.EventPromotionFailed
	case kaprov1alpha1.PromotionPhaseRollingBack:
		return events.EventPromotionRollingBack
	case kaprov1alpha1.PromotionPhaseTerminating:
		return events.EventPromotionTerminating
	}
	// Unknown phase: still publish, with a synthetic type. This keeps the
	// sink unblocked when a new phase is added before this map is.
	return events.EventType("kapro.io/promotion." + string(phase))
}

// promotionRefForEvent returns a minimal *corev1.ObjectReference for the
// Sink's observability events (Kubernetes Events about sink delivery).
// The dispatcher already uses promotionRef for the handler goroutine; the
// sink uses *corev1.ObjectReference directly.
func promotionRefForEvent(p *kaprov1alpha1.Promotion) *corev1.ObjectReference {
	return &corev1.ObjectReference{
		APIVersion: kaprov1alpha1.GroupVersion.String(),
		Kind:       "Promotion",
		Name:       p.Name,
		UID:        p.UID,
	}
}

// snapshot is the immutable per-invocation state the goroutine reads.
type snapshot struct {
	PromotionName string
	PromotionUID  string
	Generation    int64
	Phase         kaprov1alpha1.PromotionPhase
	PrevPhase     kaprov1alpha1.PromotionPhase
	AttemptName   string
	Version       string
	KaproRef      string
	Handler       kaprov1alpha1.PromotionLifecycleHandler
	InflightKey   string
}

func (d *Dispatcher) run(snap snapshot) {
	defer d.wg.Done()
	defer d.releaseInflight(snap.InflightKey)

	timeout := handlerTimeout(snap.Handler.Timeout)
	ctx, cancel := context.WithTimeout(d.rootCtx, timeout)
	defer cancel()

	start := d.Now()
	kind := handlerKind(&snap.Handler)
	result := PromotionLifecycleResult{
		Name:        snap.Handler.Name,
		Phase:       snap.Phase,
		AttemptName: snap.AttemptName,
		Kind:        kind,
		FiredAt:     metav1.NewTime(start),
	}

	var err error
	switch kind {
	case kindWebhook:
		var status int
		var attempts int32
		status, attempts, err = d.fireWebhook(ctx, snap)
		result.HTTPStatus = int32(status)
		result.Attempts = attempts
	case kindEvent:
		err = d.fireEvent(snap)
		result.Attempts = 1
	default:
		// "Misconfigured" — both kinds set, or neither set. Record a
		// Failed result so the user sees a discoverable signal in
		// Promotion.status.lifecycleHandlerResults[].
		hasWebhook := snap.Handler.Webhook != nil
		hasEvent := snap.Handler.Event != nil
		switch {
		case hasWebhook && hasEvent:
			err = fmt.Errorf("handler %q sets both spec.webhook and spec.event; exactly one must be set", snap.Handler.Name)
		default:
			err = fmt.Errorf("handler %q sets neither spec.webhook nor spec.event; exactly one must be set", snap.Handler.Name)
		}
		result.Attempts = 1
	}

	duration := d.Now().Sub(start)
	result.DurationMs = duration.Milliseconds()

	if err != nil {
		result.Result = resultFailed
		result.Message = truncate(err.Error(), 256)
		metrics.LifecycleHookInvocations.WithLabelValues(kind, string(snap.Phase), "failed").Inc()
		d.Recorder.Eventf(d.promotionRef(snap), corev1.EventTypeWarning,
			"LifecycleHookFailed",
			"handler %q (%s) failed on phase %s after %dms: %s",
			snap.Handler.Name, kind, snap.Phase, result.DurationMs, result.Message)
	} else {
		result.Result = resultSucceeded
		result.Message = "ok"
		metrics.LifecycleHookInvocations.WithLabelValues(kind, string(snap.Phase), "succeeded").Inc()
		d.Recorder.Eventf(d.promotionRef(snap), corev1.EventTypeNormal,
			"LifecycleHookFired",
			"handler %q (%s) fired on phase %s in %dms",
			snap.Handler.Name, kind, snap.Phase, result.DurationMs)
	}
	metrics.LifecycleHookDuration.WithLabelValues(kind, string(snap.Phase)).Observe(duration.Seconds())

	if writeErr := d.recordResult(snap.PromotionName, result); writeErr != nil {
		logf.FromContext(d.rootCtx).Error(writeErr, "record lifecycle handler result",
			"promotion", snap.PromotionName,
			"handler", snap.Handler.Name,
			"phase", string(snap.Phase),
		)
	}
}

// promotionRef returns a minimal Promotion object suitable for use as the
// Recorder target. The Recorder only reads ObjectMeta and TypeMeta, so we
// don't need to fetch the live object.
func (d *Dispatcher) promotionRef(snap snapshot) *kaprov1alpha1.Promotion {
	return &kaprov1alpha1.Promotion{
		TypeMeta: metav1.TypeMeta{APIVersion: kaprov1alpha1.GroupVersion.String(), Kind: "Promotion"},
		ObjectMeta: metav1.ObjectMeta{
			Name:       snap.PromotionName,
			UID:        types.UID(snap.PromotionUID),
			Generation: snap.Generation,
		},
	}
}

// fireWebhook posts a CloudEvents v1.0 payload to the configured URL,
// retrying transient failures with linear backoff (2s, 4s, 6s, ...).
// Permanent failures (4xx, unrecoverable network errors) short-circuit.
func (d *Dispatcher) fireWebhook(ctx context.Context, snap snapshot) (int, int32, error) {
	wh := snap.Handler.Webhook
	if wh == nil {
		return 0, 0, errors.New("webhook handler missing spec.webhook")
	}
	allowInsecure := os.Getenv(allowInsecureEnv) == "1"
	parsedURL, err := validateWebhookURL(wh.URL, allowInsecure)
	if err != nil {
		return 0, 0, err
	}

	headers := make(http.Header)
	headers.Set("Content-Type", "application/cloudevents+json")
	if wh.AuthHeader != nil {
		val, err := d.resolveAuthHeader(ctx, wh.AuthHeader)
		if err != nil {
			return 0, 0, fmt.Errorf("resolve auth header: %w", err)
		}
		headers.Set(wh.AuthHeader.Name, val)
	}

	payload, err := buildCloudEvent(snap)
	if err != nil {
		return 0, 0, fmt.Errorf("build payload: %w", err)
	}

	maxRetries := handlerMaxRetries(snap.Handler.MaxRetries)
	var lastStatus int
	var lastErr error
	var madeAttempts int32

	for attempt := int32(1); attempt <= maxRetries+1; attempt++ {
		// Per-attempt timeout: split the handler timeout evenly across
		// retries so a slow endpoint can't exhaust the whole budget on
		// the first call.
		attemptCtx, cancel := context.WithTimeout(ctx, handlerTimeout(snap.Handler.Timeout)/time.Duration(maxRetries+1))
		status, err := d.doRequest(attemptCtx, parsedURL.String(), headers, payload)
		cancel()
		lastStatus = status
		lastErr = err
		madeAttempts = attempt

		if err == nil && status >= 200 && status < 300 {
			return status, attempt, nil
		}
		if isPermanentFailure(status, err) {
			break
		}
		if attempt > maxRetries {
			break
		}
		// Linear backoff; bail if rootCtx or per-handler ctx fires.
		select {
		case <-ctx.Done():
			return lastStatus, attempt, ctx.Err()
		case <-time.After(backoffBase * time.Duration(attempt)):
		}
	}
	if lastErr != nil {
		return lastStatus, madeAttempts, lastErr
	}
	return lastStatus, madeAttempts, fmt.Errorf("webhook returned non-2xx: HTTP %d", lastStatus)
}

func (d *Dispatcher) doRequest(ctx context.Context, rawURL string, headers http.Header, body []byte) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rawURL, bytes.NewReader(body))
	if err != nil {
		return 0, fmt.Errorf("build request: %w", err)
	}
	for k, vs := range headers {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	resp, err := d.HTTPClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode, nil
}

// fireEvent records a Kubernetes Event on the Promotion. Message fields
// support {{.Phase}}, {{.PreviousPhase}}, {{.Version}}, {{.Name}}, and
// {{.AttemptName}} substitution.
func (d *Dispatcher) fireEvent(snap snapshot) error {
	e := snap.Handler.Event
	if e == nil {
		return errors.New("event handler missing spec.event")
	}
	eventType := corev1.EventTypeNormal
	if strings.EqualFold(e.Type, "Warning") {
		eventType = corev1.EventTypeWarning
	}
	msg := substituteEventMessage(e.Message, snap)
	d.Recorder.Event(d.promotionRef(snap), eventType, e.Reason, msg)
	return nil
}

// resolveAuthHeader reads the auth header value from the referenced
// Secret in the operator's namespace.
func (d *Dispatcher) resolveAuthHeader(ctx context.Context, ref *kaprov1alpha1.PromotionLifecycleAuthHeader) (string, error) {
	if d.Namespace == "" {
		return "", errors.New("operator namespace unset; cannot resolve auth header Secret")
	}
	var secret corev1.Secret
	if err := d.Client.Get(ctx, client.ObjectKey{Namespace: d.Namespace, Name: ref.SecretName}, &secret); err != nil {
		return "", fmt.Errorf("get Secret %s/%s: %w", d.Namespace, ref.SecretName, err)
	}
	val, ok := secret.Data[ref.SecretKey]
	if !ok {
		return "", fmt.Errorf("secret %s/%s missing key %q", d.Namespace, ref.SecretName, ref.SecretKey)
	}
	return string(val), nil
}

// recordResult patches Promotion.status.lifecycleHandlerResults with a
// newest-first upsert keyed by (name, phase, attemptName). Bounded by
// MaxLifecycleHandlerResults. Uses StatusUpdate-with-retry semantics via
// a fresh Get loop to absorb concurrent reconciles.
func (d *Dispatcher) recordResult(promotionName string, r PromotionLifecycleResult) error {
	const maxAttempts = 5
	for range maxAttempts {
		var p kaprov1alpha1.Promotion
		if err := d.Client.Get(d.rootCtx, client.ObjectKey{Name: promotionName}, &p); err != nil {
			if apierrors.IsNotFound(err) {
				return nil
			}
			return err
		}
		patch := client.MergeFrom(p.DeepCopy())
		p.Status.LifecycleHandlerResults = upsertLifecycleResult(p.Status.LifecycleHandlerResults, r.ToAPI())
		if err := d.Client.Status().Patch(d.rootCtx, &p, patch); err != nil {
			if apierrors.IsConflict(err) {
				continue
			}
			return err
		}
		return nil
	}
	return errors.New("status patch conflict after retries")
}

func (d *Dispatcher) tryReserveInflight(key string) bool {
	d.inflightMu.Lock()
	defer d.inflightMu.Unlock()
	if _, exists := d.inflight[key]; exists {
		return false
	}
	d.inflight[key] = struct{}{}
	return true
}

func (d *Dispatcher) releaseInflight(key string) {
	d.inflightMu.Lock()
	delete(d.inflight, key)
	d.inflightMu.Unlock()
}

// ---- helpers --------------------------------------------------------------

func handlerMatchesPhase(h *kaprov1alpha1.PromotionLifecycleHandler, p kaprov1alpha1.PromotionPhase) bool {
	return slices.Contains(h.On, p)
}

// alreadyFinal returns true when the status already contains a terminal
// result for this (handler, phase, attempt) tuple. Used to dedup re-fires
// after a controller restart or reconcile loop on the same transition.
func alreadyFinal(p *kaprov1alpha1.Promotion, handlerName string, phase kaprov1alpha1.PromotionPhase, attempt string) bool {
	for _, r := range p.Status.LifecycleHandlerResults {
		if r.Name != handlerName || r.Phase != phase || r.AttemptName != attempt {
			continue
		}
		if r.Result == resultSucceeded || r.Result == resultFailed {
			return true
		}
	}
	return false
}

func handlerTimeout(d *metav1.Duration) time.Duration {
	if d == nil || d.Duration <= 0 {
		return defaultHandlerTimeout
	}
	if d.Duration > maxHandlerTimeout {
		return maxHandlerTimeout
	}
	return d.Duration
}

func handlerMaxRetries(n *int32) int32 {
	if n == nil {
		return defaultMaxRetries
	}
	if *n < 0 {
		return 0
	}
	if *n > 10 {
		return 10
	}
	return *n
}

// Kind returns the handler kind name. Exactly one of Webhook or Event
// must be set. When both are set or neither is set the dispatcher records
// "Misconfigured" so the status entry surfaces the user error instead of
// silently picking one. There is no in-tree admission validator yet; this
// is the only place the contract is enforced.
func handlerKind(h *kaprov1alpha1.PromotionLifecycleHandler) string {
	hasWebhook := h.Webhook != nil
	hasEvent := h.Event != nil
	switch {
	case hasWebhook && hasEvent:
		return "Misconfigured"
	case hasWebhook:
		return kindWebhook
	case hasEvent:
		return kindEvent
	default:
		return "Misconfigured"
	}
}

// PromotionLifecycleResult is the in-package representation used by the
// dispatcher before persistence. It mirrors the API type 1:1; the
// conversion is a single function so the field set stays in sync.
type PromotionLifecycleResult struct {
	Name        string
	Phase       kaprov1alpha1.PromotionPhase
	AttemptName string
	Kind        string
	Result      string
	HTTPStatus  int32
	Attempts    int32
	DurationMs  int64
	Message     string
	FiredAt     metav1.Time
}

// ToAPI converts the dispatcher's internal result shape to the API type.
func (r PromotionLifecycleResult) ToAPI() kaprov1alpha1.PromotionLifecycleHandlerResult {
	return kaprov1alpha1.PromotionLifecycleHandlerResult{
		Name:        r.Name,
		Phase:       r.Phase,
		AttemptName: r.AttemptName,
		Kind:        r.Kind,
		Result:      r.Result,
		HTTPStatus:  r.HTTPStatus,
		Attempts:    r.Attempts,
		DurationMs:  r.DurationMs,
		Message:     r.Message,
		FiredAt:     r.FiredAt,
	}
}

func upsertLifecycleResult(list []kaprov1alpha1.PromotionLifecycleHandlerResult, current kaprov1alpha1.PromotionLifecycleHandlerResult) []kaprov1alpha1.PromotionLifecycleHandlerResult {
	for i := range list {
		if list[i].Name == current.Name && list[i].Phase == current.Phase && list[i].AttemptName == current.AttemptName {
			list[i] = current
			// Re-sort newest-first so an updated entry floats to the top.
			sortByFiredAtDesc(list)
			return list
		}
	}
	out := append([]kaprov1alpha1.PromotionLifecycleHandlerResult{current}, list...)
	if len(out) > kaprov1alpha1.MaxLifecycleHandlerResults {
		out = out[:kaprov1alpha1.MaxLifecycleHandlerResults]
	}
	return out
}

func sortByFiredAtDesc(list []kaprov1alpha1.PromotionLifecycleHandlerResult) {
	sort.SliceStable(list, func(i, j int) bool {
		return list[i].FiredAt.After(list[j].FiredAt.Time)
	})
}

func inflightKey(promotion, handler string, phase kaprov1alpha1.PromotionPhase, attempt string) string {
	return promotion + "|" + handler + "|" + string(phase) + "|" + attempt
}

func validateWebhookURL(rawURL string, allowInsecure bool) (*url.URL, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("parse url: %w", err)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("webhook url missing host: %q", rawURL)
	}
	if u.Scheme != "https" && (u.Scheme != "http" || !allowInsecure) {
		return nil, fmt.Errorf("webhook url must be https (or set %s=1 for http): %q", allowInsecureEnv, rawURL)
	}
	return u, nil
}

// isPermanentFailure classifies a webhook attempt outcome as terminal
// (no retry) or transient (retry with backoff).
//
//	Terminal:
//	  - HTTP 4xx (except 408 Request Timeout, 425 Too Early, 429 Too
//	    Many Requests, which the receiver may recover from).
//	  - Context cancellation (the manager is shutting down).
//	  - TLS handshake / certificate verification failures — a retry
//	    against the same URL will fail the same way.
//	  - Malformed-URL errors from http.NewRequest / http.Client.Do.
//
//	Transient:
//	  - 5xx, network read/write errors, DNS hiccups, connection refused.
//	    Retried with linear backoff up to maxRetries.
func isPermanentFailure(status int, err error) bool {
	if status >= 400 && status < 500 {
		switch status {
		case http.StatusRequestTimeout, http.StatusTooEarly, http.StatusTooManyRequests:
			return false
		}
		return true
	}
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return true
	}
	// x509 certificate verification errors won't recover by retrying the
	// same URL. Check the structured types first.
	var certErr *x509.CertificateInvalidError
	if errors.As(err, &certErr) {
		return true
	}
	var hostErr *x509.HostnameError
	if errors.As(err, &hostErr) {
		return true
	}
	var unknownAuthErr x509.UnknownAuthorityError
	if errors.As(err, &unknownAuthErr) {
		return true
	}
	var tlsVerifyErr *tls.CertificateVerificationError
	if errors.As(err, &tlsVerifyErr) {
		return true
	}
	// http.Client.Do often returns TLS handshake failures wrapped through
	// *url.Error -> *net.OpError without preserving the structured cert
	// type at the leaf (the leaf may be a bare alert error). Fall back to
	// a substring check on "x509:" / "tls:" prefixes the stdlib emits so
	// we still classify those as permanent.
	msg := err.Error()
	if strings.Contains(msg, "x509:") || strings.Contains(msg, "tls:") {
		return true
	}
	// Malformed-URL / unsupported-scheme errors surface as *url.Error
	// with a non-network leaf — also permanent.
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Err != nil {
		var netOpErr *net.OpError
		if !errors.As(urlErr.Err, &netOpErr) {
			return true
		}
	}
	return false
}

func substituteEventMessage(tpl string, snap snapshot) string {
	if tpl == "" {
		return fmt.Sprintf("Promotion %s phase %s -> %s", snap.PromotionName, snap.PrevPhase, snap.Phase)
	}
	repl := strings.NewReplacer(
		"{{.Phase}}", string(snap.Phase),
		"{{.PreviousPhase}}", string(snap.PrevPhase),
		"{{.Version}}", snap.Version,
		"{{.Name}}", snap.PromotionName,
		"{{.AttemptName}}", snap.AttemptName,
	)
	return repl.Replace(tpl)
}

// buildCloudEvent produces a CloudEvents v1.0 JSON payload describing the
// transition. The CloudEvents standard ID is a fresh random hex string so
// receivers can dedupe their own way; idempotency at the Kapro layer is
// keyed on (handler, phase, attemptName) which appear in `data`.
func buildCloudEvent(snap snapshot) ([]byte, error) {
	id, err := randomID()
	if err != nil {
		return nil, err
	}
	envelope := map[string]any{
		"specversion":     cloudEventsSpec,
		"id":              id,
		"type":            "kapro.io/promotion." + strings.ToLower(string(snap.Phase)),
		"source":          "/apis/kapro.io/v1alpha1/promotions/" + snap.PromotionName,
		"time":            time.Now().UTC().Format(time.RFC3339),
		"datacontenttype": "application/json",
		"data": map[string]any{
			"promotion":     snap.PromotionName,
			"kaproRef":      snap.KaproRef,
			"phase":         string(snap.Phase),
			"previousPhase": string(snap.PrevPhase),
			"attemptName":   snap.AttemptName,
			"version":       snap.Version,
			"handler":       snap.Handler.Name,
		},
	}
	return json.Marshal(envelope)
}

func randomID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

// defaultHTTPClient is the package's default HTTP client for outbound
// webhook calls. It applies the same SSRF guard as the gate webhook so
// lifecycle webhooks cannot be aimed at private or metadata addresses
// unless the operator opts out via KAPRO_LIFECYCLE_INSECURE_WEBHOOKS=1.
//
// Note: the client has no Timeout. Per-handler timeouts (up to
// maxHandlerTimeout = 5m) are enforced via http.NewRequestWithContext on
// each attempt. Setting a client-level Timeout would silently cap user
// handler.timeout values above 30s.
func defaultHTTPClient() *http.Client {
	allowInsecure := os.Getenv(allowInsecureEnv) == "1"
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	if !allowInsecure {
		transport.DialContext = safeDial
	}
	return &http.Client{Transport: transport}
}

func isForbiddenIP(addr netip.Addr) bool {
	addr = addr.Unmap()
	return addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast() ||
		addr.IsUnspecified() || addr.IsMulticast() || addr.IsLinkLocalMulticast()
}

func safeDial(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("ssrf guard: parse addr %q: %w", addr, err)
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("ssrf guard: resolve %q: %w", host, err)
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("ssrf guard: no addresses for %q", host)
	}
	for _, ip := range ips {
		a, ok := netip.AddrFromSlice(ip.IP)
		if !ok {
			return nil, fmt.Errorf("ssrf guard: invalid IP %v", ip.IP)
		}
		if isForbiddenIP(a) {
			return nil, fmt.Errorf("ssrf guard: %q resolves to forbidden address %s", host, ip.IP)
		}
	}
	d := &net.Dialer{}
	var lastErr error
	for _, ip := range ips {
		conn, dialErr := d.DialContext(ctx, network, net.JoinHostPort(ip.IP.String(), port))
		if dialErr == nil {
			return conn, nil
		}
		lastErr = dialErr
	}
	return nil, lastErr
}
