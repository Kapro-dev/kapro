package adapter

import (
	"context"
	"fmt"
	"time"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
	"kapro.io/kapro/pkg/planner"
	kpiv1alpha1 "kapro.io/kapro/spec/kpi/v1alpha1"

	"google.golang.org/grpc"
)

const plannerStatePrefix = "kapro.io/plugin/adapter/planner/"

// PlannerAdapter adapts a KPI gRPC plugin to pkg/planner.Plugin phases.
type PlannerAdapter struct {
	name       string
	client     kpiv1alpha1.PlannerServiceClient
	timeout    time.Duration
	parameters map[string]string
	conn       *grpc.ClientConn
}

type plannerCycleDecision struct {
	decision kpiv1alpha1.PlanningDecision
	score    int64
	reason   string
	message  string
}

// NewPlannerAdapter returns a planner adapter backed by a KPI client.
func NewPlannerAdapter(reg kaprov1alpha1.PluginRegistration, client kpiv1alpha1.PlannerServiceClient) (*PlannerAdapter, error) {
	if reg.Spec.Type != kaprov1alpha1.PluginTypePlanner {
		return nil, fmt.Errorf("plugin %q is %q, expected %q", reg.Name, reg.Spec.Type, kaprov1alpha1.PluginTypePlanner)
	}
	if client == nil {
		return nil, fmt.Errorf("planner plugin client is nil")
	}
	if err := validateRegistration(reg); err != nil {
		return nil, err
	}
	timeout, err := timeoutFor(reg)
	if err != nil {
		return nil, err
	}
	return &PlannerAdapter{
		name:       reg.Spec.Name,
		client:     client,
		timeout:    timeout,
		parameters: copyParameters(reg.Spec.Parameters),
	}, nil
}

func (p *PlannerAdapter) Name() string { return p.name }

// PreFilter asks the external planner for target decisions for this stage.
func (p *PlannerAdapter) PreFilter(ctx context.Context, state *planner.CycleState, req planner.Request, targets []kaprov1alpha1.MemberCluster) *planner.Status {
	rpcCtx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	resp, err := p.client.Plan(rpcCtx, p.planRequest(req, targets))
	if err != nil {
		return planner.NewStatus(planner.Error, fmt.Sprintf("plan via planner plugin %q: %v", p.name, err))
	}
	decisions, err := p.mapPlanResponse(resp, targets)
	if err != nil {
		return planner.NewStatus(planner.Error, err.Error())
	}
	state.Write(p.stateKey(), decisions)
	return nil
}

// Filter maps KPI skip/defer decisions into the existing framework's skip path.
func (p *PlannerAdapter) Filter(_ context.Context, state *planner.CycleState, _ planner.Request, target kaprov1alpha1.MemberCluster) *planner.Status {
	decision, ok, status := p.targetDecision(state, target.Name, "Filter")
	if status != nil || !ok {
		return status
	}
	switch decision.decision {
	case kpiv1alpha1.PlanningDecision_PLANNING_DECISION_INCLUDE:
		return nil
	case kpiv1alpha1.PlanningDecision_PLANNING_DECISION_SKIP:
		return planner.NewStatusReason(planner.Skip, defaultReason(decision.reason, "Skipped"), decision.message)
	case kpiv1alpha1.PlanningDecision_PLANNING_DECISION_DEFER:
		return planner.NewStatusReason(planner.Skip, defaultReason(decision.reason, "Deferred"), decision.message)
	default:
		return planner.NewStatus(planner.Error, fmt.Sprintf("planner plugin %q returned unsupported decision %s for target %q", p.name, decision.decision.String(), target.Name))
	}
}

// Score maps KPI scores into the existing framework ordering phase.
func (p *PlannerAdapter) Score(_ context.Context, state *planner.CycleState, _ planner.Request, target kaprov1alpha1.MemberCluster) (int64, *planner.Status) {
	decision, ok, status := p.targetDecision(state, target.Name, "Score")
	if status != nil || !ok {
		return 0, status
	}
	return decision.score, nil
}

func (p *PlannerAdapter) planRequest(req planner.Request, targets []kaprov1alpha1.MemberCluster) *kpiv1alpha1.PlanRequest {
	out := &kpiv1alpha1.PlanRequest{
		Release:    releaseName(req.Release),
		Pipeline:   req.PipelineRefName,
		Stage:      req.Stage.Name,
		Version:    releaseVersion(req.Release),
		Targets:    make([]*kpiv1alpha1.Target, 0, len(targets)),
		Parameters: copyParameters(p.parameters),
	}
	if req.Stage.Strategy != nil {
		out.Strategy = &kpiv1alpha1.StageStrategy{
			MaxParallel:    req.Stage.Strategy.MaxParallel,
			MaxUnavailable: req.Stage.Strategy.MaxUnavailable,
		}
	}
	for _, target := range targets {
		out.Targets = append(out.Targets, &kpiv1alpha1.Target{
			Name:          target.Name,
			Labels:        copyParameters(target.Labels),
			Ready:         targetReady(target),
			ActiveRelease: target.Status.ActiveRelease,
		})
	}
	return out
}

func (p *PlannerAdapter) mapPlanResponse(resp *kpiv1alpha1.PlanResponse, targets []kaprov1alpha1.MemberCluster) (map[string]plannerCycleDecision, error) {
	known := make(map[string]struct{}, len(targets))
	for _, target := range targets {
		known[target.Name] = struct{}{}
	}
	decisions := make(map[string]plannerCycleDecision, len(resp.GetTargets()))
	for _, target := range resp.GetTargets() {
		name := target.GetName()
		if name == "" {
			return nil, fmt.Errorf("planner plugin %q returned a decision without a target name", p.name)
		}
		if _, ok := known[name]; !ok {
			return nil, fmt.Errorf("planner plugin %q returned unknown target %q", p.name, name)
		}
		if _, exists := decisions[name]; exists {
			return nil, fmt.Errorf("planner plugin %q returned duplicate target %q", p.name, name)
		}
		switch target.GetDecision() {
		case kpiv1alpha1.PlanningDecision_PLANNING_DECISION_INCLUDE,
			kpiv1alpha1.PlanningDecision_PLANNING_DECISION_SKIP,
			kpiv1alpha1.PlanningDecision_PLANNING_DECISION_DEFER:
		default:
			return nil, fmt.Errorf("planner plugin %q returned unsupported decision %s for target %q", p.name, target.GetDecision().String(), name)
		}
		decisions[name] = plannerCycleDecision{
			decision: target.GetDecision(),
			score:    target.GetScore(),
			reason:   target.GetReason(),
			message:  target.GetMessage(),
		}
	}
	return decisions, nil
}

func (p *PlannerAdapter) targetDecision(state *planner.CycleState, target, phase string) (plannerCycleDecision, bool, *planner.Status) {
	raw, ok := state.Read(p.stateKey())
	if !ok {
		return plannerCycleDecision{}, false, planner.NewStatus(planner.Error, fmt.Sprintf("planner plugin %q has no plan state for %s", p.name, phase))
	}
	decisions, ok := raw.(map[string]plannerCycleDecision)
	if !ok {
		return plannerCycleDecision{}, false, planner.NewStatus(planner.Error, fmt.Sprintf("planner plugin %q has invalid plan state for %s", p.name, phase))
	}
	decision, ok := decisions[target]
	return decision, ok, nil
}

func (p *PlannerAdapter) stateKey() string {
	return plannerStatePrefix + p.name
}

func releaseName(release *kaprov1alpha1.Release) string {
	if release == nil {
		return ""
	}
	return release.Name
}

func releaseVersion(release *kaprov1alpha1.Release) string {
	if release == nil {
		return ""
	}
	if release.Status.ResolvedVersion != "" {
		return release.Status.ResolvedVersion
	}
	return release.Spec.Version
}

func targetReady(target kaprov1alpha1.MemberCluster) bool {
	ready := apimeta.FindStatusCondition(target.Status.Conditions, "Ready")
	return ready == nil || ready.Status != metav1.ConditionFalse
}

func defaultReason(reason, fallback string) string {
	if reason != "" {
		return reason
	}
	return fallback
}
