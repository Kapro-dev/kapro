// Package decisiontrace creates durable controller decision audit records.
package decisiontrace

import (
	"context"
	"fmt"
	"hash"
	"hash/fnv"
	"strings"
	"unicode/utf8"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
)

const (
	defaultMaxMessageRunes = 512
	defaultMaxEvidence     = 16
	defaultMaxDetailRunes  = 256
)

// Emitter writes DecisionTrace objects. Callers decide whether to surface the
// returned error; promotion logic should log and continue.
type Emitter struct {
	Client          client.Client
	MaxMessageRunes int
	MaxEvidence     int
	MaxDetailRunes  int
}

// Emit creates one bounded DecisionTrace object. Duplicate traces are treated
// as success so reconcilers can retry safely.
func (e Emitter) Emit(ctx context.Context, spec kaprov1alpha2.DecisionTraceSpec) error {
	if e.Client == nil {
		return nil
	}
	spec = e.normalize(spec)
	if spec.PromotionRun == "" {
		return fmt.Errorf("decision trace promotionRun is required")
	}
	if spec.EventType == "" {
		return fmt.Errorf("decision trace eventType is required")
	}
	if spec.Source == "" {
		return fmt.Errorf("decision trace source is required")
	}
	trace := &kaprov1alpha2.DecisionTrace{
		ObjectMeta: metav1.ObjectMeta{
			Name: nameFor(spec),
			Labels: map[string]string{
				"kapro.io/promotionrun": spec.PromotionRun,
				"kapro.io/event-type":   strings.ToLower(string(spec.EventType)),
			},
		},
		Spec: spec,
	}
	if err := e.Client.Create(ctx, trace); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	return nil
}

func (e Emitter) normalize(spec kaprov1alpha2.DecisionTraceSpec) kaprov1alpha2.DecisionTraceSpec {
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
		spec.Evidence = append([]kaprov1alpha2.DecisionTraceEvidence(nil), spec.Evidence[:maxEvidence]...)
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

func nameFor(spec kaprov1alpha2.DecisionTraceSpec) string {
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
