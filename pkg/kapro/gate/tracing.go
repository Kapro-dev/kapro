package gate

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

const tracerName = "kapro.io/kapro/pkg/kapro/gate"

// WithTracing wraps a Predicate so every Evaluate call emits an OpenTelemetry
// span with promotion identity and predicate outcome attributes.
func WithTracing(name string, p Predicate) Predicate {
	return PredicateFunc(func(ctx context.Context, req Request) (Result, error) {
		identity := requestIdentity(req)
		ctx, span := otel.Tracer(tracerName).Start(ctx, "kapro.predicate.evaluate",
			trace.WithAttributes(
				attribute.String("kapro.predicate.name", name),
				attribute.String("kapro.fleet", identity.fleet),
				attribute.String("kapro.promotion", identity.promotion),
				attribute.String("kapro.promotionrun", identity.promotionRun),
				attribute.String("kapro.plan", identity.plan),
				attribute.String("kapro.stage", identity.stage),
				attribute.String("kapro.target", identity.target),
				attribute.String("kapro.version", identity.version),
			),
		)
		defer span.End()

		result, err := p.Evaluate(ctx, req)
		span.SetAttributes(
			attribute.String("kapro.predicate.phase", string(result.Phase)),
			attribute.String("kapro.predicate.reason", result.Reason),
		)
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		} else if result.IsFailed() {
			span.SetStatus(codes.Error, result.Reason)
		}
		return result, err
	})
}

type identityFields struct {
	fleet        string
	promotion    string
	promotionRun string
	plan         string
	stage        string
	target       string
	version      string
}

func requestIdentity(req Request) identityFields {
	identity := identityFields{
		fleet:        req.Fleet,
		promotion:    req.Promotion,
		promotionRun: req.PromotionRun,
		plan:         req.Plan,
		stage:        req.Stage,
		target:       req.Target,
		version:      req.Version,
	}
	if req.Context == nil {
		return identity
	}
	if identity.promotionRun == "" {
		identity.promotionRun = req.Context.PromotionRunRef
	}
	if identity.plan == "" {
		identity.plan = req.Context.Plan
	}
	if identity.stage == "" {
		identity.stage = req.Context.Stage
	}
	if identity.target == "" {
		identity.target = req.Context.Target
	}
	if identity.version == "" {
		identity.version = req.Context.Version
	}
	return identity
}
