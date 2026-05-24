// Package decisiontrace creates durable controller decision audit records.
package decisiontrace

import (
	"context"
	"fmt"
	"hash"
	"hash/fnv"
	"strings"
	"unicode/utf8"

	kaproruntimev1alpha1 "kapro.io/kapro/api/kaproruntime/v1alpha1"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	defaultMaxMessageRunes = 512
	defaultMaxEvidence     = 16
	defaultMaxDetailRunes  = 256

	tracerName = "kapro.io/kapro/internal/decisiontrace"
)

// Emitter writes DecisionTrace objects. Callers decide whether to surface the
// returned error; promotion logic should log and continue.
type Emitter struct {
	Client          client.Client
	Signer          Signer
	MaxMessageRunes int
	MaxEvidence     int
	MaxDetailRunes  int
}

// Emit creates one bounded DecisionTrace object. Duplicate traces are treated
// as success so reconcilers can retry safely.
func (e Emitter) Emit(ctx context.Context, spec kaproruntimev1alpha1.DecisionTraceSpec) error {
	if e.Client == nil {
		return nil
	}
	spec = e.normalize(spec)
	ctx, span := otel.Tracer(tracerName).Start(ctx, "kapro.decisiontrace.emit",
		trace.WithAttributes(
			attribute.String("kapro.promotionrun", spec.PromotionRun),
			attribute.String("kapro.plan", spec.Plan),
			attribute.String("kapro.stage", spec.Stage),
			attribute.String("kapro.target", spec.Target),
			attribute.String("kapro.decisiontrace.event_type", string(spec.EventType)),
			attribute.String("kapro.decisiontrace.source", spec.Source),
			attribute.String("kapro.decisiontrace.phase", spec.Phase),
			attribute.String("kapro.decisiontrace.reason", spec.Reason),
		),
	)
	defer span.End()

	err := e.emit(ctx, spec)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	return err
}

func (e Emitter) emit(ctx context.Context, spec kaproruntimev1alpha1.DecisionTraceSpec) error {
	if spec.PromotionRun == "" {
		return fmt.Errorf("decision trace promotionRun is required")
	}
	if spec.EventType == "" {
		return fmt.Errorf("decision trace eventType is required")
	}
	if spec.Source == "" {
		return fmt.Errorf("decision trace source is required")
	}
	decisionTrace := &kaproruntimev1alpha1.DecisionTrace{
		ObjectMeta: metav1.ObjectMeta{
			Name: nameFor(spec),
			Labels: map[string]string{
				"kapro.io/promotionrun": spec.PromotionRun,
				"kapro.io/event-type":   strings.ToLower(string(spec.EventType)),
			},
		},
		Spec: spec,
	}
	if err := e.Client.Create(ctx, decisionTrace); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return err
		}
		return e.signExisting(ctx, decisionTrace.Name)
	}
	return e.signExisting(ctx, decisionTrace.Name)
}

func (e Emitter) signExisting(ctx context.Context, name string) error {
	if e.Signer == nil {
		return nil
	}
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var trace kaproruntimev1alpha1.DecisionTrace
		if err := e.Client.Get(ctx, client.ObjectKey{Name: name}, &trace); err != nil {
			return err
		}
		if trace.Status.Signed {
			return nil
		}
		sig, err := e.Signer.SignDecisionTrace(ctx, trace.Spec)
		if err != nil {
			return err
		}
		before := trace.DeepCopy()
		trace.Status = statusForSignature(sig)
		return e.Client.Status().Patch(ctx, &trace, client.MergeFrom(before))
	})
}

func (e Emitter) normalize(spec kaproruntimev1alpha1.DecisionTraceSpec) kaproruntimev1alpha1.DecisionTraceSpec {
	if spec.Time.IsZero() {
		spec.Time = metav1.Now()
	}
	maxMessage := e.MaxMessageRunes
	if maxMessage <= 0 {
		maxMessage = defaultMaxMessageRunes
	}
	maxEvidence := e.MaxEvidence
	if maxEvidence <= 0 {
		maxEvidence = defaultMaxEvidence
	}
	maxDetail := e.MaxDetailRunes
	if maxDetail <= 0 {
		maxDetail = defaultMaxDetailRunes
	}
	spec.Message = truncateRunes(spec.Message, maxMessage)
	if len(spec.Evidence) > maxEvidence {
		spec.Evidence = append([]kaproruntimev1alpha1.DecisionTraceEvidence(nil), spec.Evidence[:maxEvidence]...)
	}
	for i := range spec.Evidence {
		spec.Evidence[i].Type = truncateRunes(spec.Evidence[i].Type, maxDetail)
		spec.Evidence[i].Source = truncateRunes(spec.Evidence[i].Source, maxDetail)
		for key, value := range spec.Evidence[i].Detail {
			spec.Evidence[i].Detail[key] = truncateRunes(value, maxDetail)
		}
	}
	return spec
}

func nameFor(spec kaproruntimev1alpha1.DecisionTraceSpec) string {
	h := fnv.New64a()
	writeHash(h, spec.PromotionRun)
	writeHash(h, spec.Plan)
	writeHash(h, spec.Stage)
	writeHash(h, spec.Target)
	writeHash(h, string(spec.EventType))
	writeHash(h, spec.Source)
	writeHash(h, spec.Phase)
	writeHash(h, spec.Reason)
	writeHash(h, spec.Message)
	return fmt.Sprintf("dtrace-%016x", h.Sum64())
}

func writeHash(h hash.Hash, value string) {
	_, _ = h.Write([]byte(value))
	_, _ = h.Write([]byte{0})
}

func truncateRunes(value string, maxRunes int) string {
	if maxRunes <= 0 || utf8.RuneCountInString(value) <= maxRunes {
		return value
	}
	var b strings.Builder
	b.Grow(len(value))
	for i, r := range value {
		if i >= len(value) {
			break
		}
		if maxRunes == 0 {
			break
		}
		b.WriteRune(r)
		maxRunes--
	}
	return b.String()
}
