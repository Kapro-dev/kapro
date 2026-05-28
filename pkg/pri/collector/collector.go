// Package collector maps Kapro runtime objects into PRI v0.1 documents.
package collector

import (
	"context"
	"fmt"
	"sort"

	kaprov1alpha1 "kapro.io/kapro/api/kapro/v1alpha1"
	kaproruntimev1alpha1 "kapro.io/kapro/api/kaproruntime/v1alpha1"
	"kapro.io/kapro/pkg/pri"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	labelPromotion = "kapro.io/promotion"
	labelUnit      = kaprov1alpha1.LabelUnit

	annotationSourceAPIVersion = "kapro.io/pri.source-api-version"
	annotationSourceKind       = "kapro.io/pri.source-kind"
	annotationKaproRun         = "kapro.io/pri.kapro-promotionrun"
)

// Bundle is the portable PRI view collected from one Kapro PromotionRun.
type Bundle struct {
	Promotion    *pri.Promotion
	PromotionRun *pri.PromotionRun
	Evidence     []pri.Evidence
}

// Documents returns a deterministic document list suitable for encoding.
func (b Bundle) Documents() []any {
	var docs []any
	if b.Promotion != nil {
		docs = append(docs, b.Promotion)
	}
	if b.PromotionRun != nil {
		docs = append(docs, b.PromotionRun)
	}
	for i := range b.Evidence {
		docs = append(docs, &b.Evidence[i])
	}
	return docs
}

// Collector reads Kapro runtime API objects and emits PRI documents.
type Collector struct {
	Client client.Client
}

func New(c client.Client) Collector {
	return Collector{Client: c}
}

// CollectPromotionRun returns PRI documents for one Kapro PromotionRun and its
// child Target objects. The collector is emission-mode: it does not mutate the
// cluster and does not require Kapro to store PRI-native objects.
func (c Collector) CollectPromotionRun(ctx context.Context, name string) (Bundle, error) {
	var run kaproruntimev1alpha1.PromotionRun
	if err := c.Client.Get(ctx, client.ObjectKey{Name: name}, &run); err != nil {
		return Bundle{}, fmt.Errorf("get PromotionRun %q: %w", name, err)
	}

	targets, err := c.listTargets(ctx, run.Name)
	if err != nil {
		return Bundle{}, err
	}

	bundle := Bundle{
		PromotionRun: FromKaproPromotionRun(&run, targets),
		Evidence:     EvidenceFromAuditTrail(&run),
	}
	if promotion := PromotionFromKaproPromotionRun(&run, targets); promotion != nil {
		bundle.Promotion = promotion
	}
	return bundle, nil
}

func (c Collector) listTargets(ctx context.Context, runName string) ([]kaproruntimev1alpha1.Target, error) {
	var list kaproruntimev1alpha1.TargetList
	if err := c.Client.List(ctx, &list); err != nil {
		return nil, fmt.Errorf("list Targets for PromotionRun %q: %w", runName, err)
	}
	out := make([]kaproruntimev1alpha1.Target, 0, len(list.Items))
	for i := range list.Items {
		target := list.Items[i]
		if target.Spec.PromotionRunRef == runName {
			out = append(out, target)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Spec.Target != out[j].Spec.Target {
			return out[i].Spec.Target < out[j].Spec.Target
		}
		if out[i].Spec.PlanRef != out[j].Spec.PlanRef {
			return out[i].Spec.PlanRef < out[j].Spec.PlanRef
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// FromKaproPromotionRun maps Kapro's runtime state to PRI's portable runtime
// phase model while preserving the native phase in implementationPhase.
func FromKaproPromotionRun(run *kaproruntimev1alpha1.PromotionRun, targets []kaproruntimev1alpha1.Target) *pri.PromotionRun {
	promotionRef := promotionRefForRun(run)
	out := &pri.PromotionRun{
		TypeMeta: pri.TypeMeta{APIVersion: pri.APIVersion, Kind: pri.KindPromotionRun},
		Metadata: pri.Metadata{
			Name:        run.Name,
			Labels:      copyStringMap(run.Labels),
			Annotations: sourceAnnotations(run.APIVersion, run.Kind, run.Name, run.Annotations),
		},
		Spec: pri.PromotionRunSpec{
			PromotionRef: promotionRef,
		},
		Status: pri.PromotionRunStatus{
			Phase:               mapRunPhase(run.Status.Phase),
			ImplementationPhase: string(run.Status.Phase),
			StartedAt:           run.Status.StartedAt,
			CompletedAt:         run.Status.CompletedAt,
			TargetResults:       mapTargetResults(targets),
		},
	}
	if out.Status.ImplementationPhase == out.Status.Phase {
		out.Status.ImplementationPhase = ""
	}
	if run.Status.StartedAt != "" {
		out.Status.Attempts = []pri.Attempt{{
			ID:                  "1",
			Phase:               out.Status.Phase,
			ImplementationPhase: string(run.Status.Phase),
			StartedAt:           run.Status.StartedAt,
			CompletedAt:         run.Status.CompletedAt,
		}}
		if out.Status.Attempts[0].ImplementationPhase == out.Status.Attempts[0].Phase {
			out.Status.Attempts[0].ImplementationPhase = ""
		}
	}
	return out
}

// PromotionFromKaproPromotionRun synthesizes a PRI Promotion intent from a
// Kapro runtime record when enough target information is available.
func PromotionFromKaproPromotionRun(run *kaproruntimev1alpha1.PromotionRun, targets []kaproruntimev1alpha1.Target) *pri.Promotion {
	targetDocs := promotionTargets(run, targets)
	if len(targetDocs) == 0 {
		return nil
	}
	unit := firstNonEmpty(run.Spec.DeliveryUnitRef, run.Labels[labelUnit], promotionRefForRun(run), run.Name)
	promotion := &pri.Promotion{
		TypeMeta: pri.TypeMeta{APIVersion: pri.APIVersion, Kind: pri.KindPromotion},
		Metadata: pri.Metadata{
			Name:        promotionRefForRun(run),
			Labels:      copyStringMap(run.Labels),
			Annotations: sourceAnnotations(run.APIVersion, run.Kind, run.Name, run.Annotations),
		},
		Spec: pri.PromotionSpec{
			Unit:      unit,
			Artifacts: promotionArtifacts(unit, run),
			Targets:   targetDocs,
		},
	}
	if len(run.Spec.Plans) > 0 && run.Spec.Plans[0].Plan != "" {
		promotion.Spec.Plan = &pri.Plan{Ref: run.Spec.Plans[0].Plan}
	}
	return promotion
}

func promotionArtifacts(unit string, run *kaproruntimev1alpha1.PromotionRun) []pri.Artifact {
	if len(run.Spec.Versions) > 0 {
		keys := make([]string, 0, len(run.Spec.Versions))
		for key := range run.Spec.Versions {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		out := make([]pri.Artifact, 0, len(keys))
		for _, key := range keys {
			out = append(out, pri.Artifact{Name: key, Version: run.Spec.Versions[key]})
		}
		return out
	}
	return []pri.Artifact{{
		Name:    unit,
		Version: firstNonEmpty(run.Status.ResolvedVersion, run.Spec.Version),
	}}
}

func promotionTargets(run *kaproruntimev1alpha1.PromotionRun, targets []kaproruntimev1alpha1.Target) []pri.Target {
	seen := map[string]struct{}{}
	var names []string
	for _, target := range targets {
		name := firstNonEmpty(target.Spec.Target, target.Status.Target)
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	if len(names) == 0 && run.Spec.Scope != nil {
		for _, name := range run.Spec.Scope.Targets {
			if name == "" {
				continue
			}
			if _, ok := seen[name]; ok {
				continue
			}
			seen[name] = struct{}{}
			names = append(names, name)
		}
	}
	sort.Strings(names)
	out := make([]pri.Target, 0, len(names))
	for _, name := range names {
		out = append(out, pri.Target{Name: name})
	}
	return out
}

func mapTargetResults(targets []kaproruntimev1alpha1.Target) []pri.TargetResult {
	out := make([]pri.TargetResult, 0, len(targets))
	for _, target := range targets {
		name := firstNonEmpty(target.Spec.Target, target.Status.Target)
		if name == "" {
			continue
		}
		native := firstNonEmpty(string(target.Status.Phase), string(target.Spec.CancelledPhase))
		out = append(out, pri.TargetResult{
			Target:              name,
			Phase:               mapTargetPhase(target.Status.Phase),
			ImplementationPhase: native,
		})
		last := &out[len(out)-1]
		if last.ImplementationPhase == last.Phase {
			last.ImplementationPhase = ""
		}
	}
	return out
}

func EvidenceFromAuditTrail(run *kaproruntimev1alpha1.PromotionRun) []pri.Evidence {
	out := make([]pri.Evidence, 0, len(run.Status.AuditTrail))
	for i, entry := range run.Status.AuditTrail {
		name := fmt.Sprintf("audit-%d", i)
		out = append(out, pri.Evidence{
			TypeMeta: pri.TypeMeta{APIVersion: pri.APIVersion, Kind: pri.KindEvidence},
			Metadata: pri.Metadata{
				Name:        name,
				Labels:      copyStringMap(run.Labels),
				Annotations: sourceAnnotations(run.APIVersion, run.Kind, run.Name, nil),
			},
			Spec: pri.EvidenceSpec{
				Type:        "audit",
				URI:         fmt.Sprintf("urn:kapro:audit:%s:%d", run.Name, i),
				SubjectRefs: []string{run.Name, promotionRefForRun(run)},
			},
		})
		if entry.Artifact != "" {
			out[len(out)-1].Metadata.Annotations["kapro.io/artifact"] = entry.Artifact
		}
		if entry.CompletedAt != "" {
			out[len(out)-1].Metadata.Annotations["kapro.io/completed-at"] = entry.CompletedAt
		}
	}
	return out
}

func mapRunPhase(phase kaprov1alpha1.PromotionRunPhase) string {
	switch phase {
	case "", kaprov1alpha1.PromotionRunPhasePending:
		return "Pending"
	case kaprov1alpha1.PromotionRunPhaseProgressing:
		return "Running"
	case kaprov1alpha1.PromotionRunPhaseComplete:
		return "Succeeded"
	case kaprov1alpha1.PromotionRunPhaseFailed:
		return "Failed"
	case kaprov1alpha1.PromotionRunPhaseSuperseded:
		return "Cancelled"
	default:
		return "Failed"
	}
}

func mapTargetPhase(phase kaprov1alpha1.TargetPhase) string {
	switch phase {
	case "", kaprov1alpha1.TargetPhasePending:
		return "Pending"
	case kaprov1alpha1.TargetPhaseApplying:
		return "Delivering"
	case kaprov1alpha1.TargetPhaseVerification,
		kaprov1alpha1.TargetPhaseHealthCheck,
		kaprov1alpha1.TargetPhaseSoaking,
		kaprov1alpha1.TargetPhaseMetricsCheck,
		kaprov1alpha1.TargetPhaseWaitingApproval:
		return "Verifying"
	case kaprov1alpha1.TargetPhaseConverged:
		return "Succeeded"
	case kaprov1alpha1.TargetPhaseFailed:
		return "Failed"
	case kaprov1alpha1.TargetPhaseSkipped:
		return "Skipped"
	default:
		return "Failed"
	}
}

func promotionRefForRun(run *kaproruntimev1alpha1.PromotionRun) string {
	if run.Labels != nil {
		if value := run.Labels[labelPromotion]; value != "" {
			return value
		}
	}
	for _, owner := range run.OwnerReferences {
		if owner.Kind == "Promotion" && owner.Name != "" {
			return owner.Name
		}
	}
	return run.Name
}

func sourceAnnotations(apiVersion, kind, name string, existing map[string]string) map[string]string {
	out := copyStringMap(existing)
	if out == nil {
		out = map[string]string{}
	}
	if apiVersion != "" {
		out[annotationSourceAPIVersion] = apiVersion
	}
	if kind != "" {
		out[annotationSourceKind] = kind
	}
	if name != "" {
		out[annotationKaproRun] = name
	}
	return out
}

func copyStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
