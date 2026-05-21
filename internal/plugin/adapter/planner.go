package adapter

import (
	"context"
	"fmt"
	"time"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
	"kapro.io/kapro/pkg/planner"
	kpiv1alpha1 "kapro.io/kapro/spec/kpi/v1alpha1"

	"google.golang.org/grpc"
)

// PlannerAdapter adapts a KPI gRPC plugin to pkg/planner.Plugin.
type PlannerAdapter struct {
	name       string
	endpoint   string
	client     kpiv1alpha1.PlannerServiceClient
	timeout    time.Duration
	parameters map[string]string
	conn       *grpc.ClientConn
}

type plannerCycleResult struct {
	byTarget map[string]*kpiv1alpha1.PlannedTarget
}

// NewPlannerAdapter returns a planner adapter backed by a KPI client.
func NewPlannerAdapter(reg kaprov1alpha2.Plugin, client kpiv1alpha1.PlannerServiceClient) (*PlannerAdapter, error) {
	if reg.Spec.Type != kaprov1alpha2.PluginTypePlanner {
		return nil, fmt.Errorf("plugin %q is %q, expected %q", reg.Name, reg.Spec.Type, kaprov1alpha2.PluginTypePlanner)
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
		endpoint:   reg.Spec.Endpoint,
		client:     client,
		timeout:    timeout,
		parameters: copyParameters(reg.Spec.Parameters),
	}, nil
}

// Close closes the underlying plugin connection when this adapter owns one.
func (p *PlannerAdapter) Close() error {
	if p == nil || p.conn == nil {
		return nil
	}
	return p.conn.Close()
}

func (p *PlannerAdapter) Name() string {
	return p.name
}

// PreFilter calls the external planner once per planning cycle and stores the
// response for Filter and Score.
func (p *PlannerAdapter) PreFilter(ctx context.Context, state *planner.CycleState, req planner.Request, targets []kaprov1alpha2.Cluster) *planner.Status {
	start := time.Now()
	result := "success"
	defer func() { observeRuntimeCall(kaprov1alpha2.PluginTypePlanner, p.name, "Plan", result, start) }()

	rpcCtx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	resp, err := p.client.Plan(rpcCtx, &kpiv1alpha1.PlanRequest{
		PromotionRun:  promotionrunName(req.PromotionRun),
		Plan: req.PromotionPlanRefName,
		Stage:         req.Stage.Name,
		Version:       promotionrunVersion(req.PromotionRun),
		Strategy:      stageStrategy(req.Stage),
		Targets:       plannerTargets(targets),
		Parameters:    copyParameters(p.parameters),
	})
	if err != nil {
		result = "error"
		return planner.NewStatusReason(planner.Error, "PlannerRPCFailed",
			fmt.Sprintf("planner plugin %q Plan RPC to %q failed: %v", p.name, p.endpoint, err))
	}
	byTarget := make(map[string]*kpiv1alpha1.PlannedTarget, len(resp.GetTargets()))
	for _, target := range resp.GetTargets() {
		if target == nil || target.GetName() == "" {
			continue
		}
		byTarget[target.GetName()] = target
	}
	state.Write(p.stateKey(), plannerCycleResult{byTarget: byTarget})
	return nil
}

// Filter skips or defers targets when the external planner says they should
// not be bound in this cycle.
func (p *PlannerAdapter) Filter(_ context.Context, state *planner.CycleState, _ planner.Request, target kaprov1alpha2.Cluster) *planner.Status {
	planned, ok := p.plannedTarget(state, target.Name)
	if !ok {
		return nil
	}
	switch planned.GetDecision() {
	case kpiv1alpha1.PlanningDecision_PLANNING_DECISION_SKIP:
		return planner.NewStatusReason(planner.Skip, reasonOrDefault(planned.GetReason(), "PluginSkipped"), planned.GetMessage())
	case kpiv1alpha1.PlanningDecision_PLANNING_DECISION_DEFER:
		return planner.NewStatusReason(planner.Skip, reasonOrDefault(planned.GetReason(), "PluginDeferred"), planned.GetMessage())
	default:
		return nil
	}
}

// Score applies the external planner score for included targets.
func (p *PlannerAdapter) Score(_ context.Context, state *planner.CycleState, _ planner.Request, target kaprov1alpha2.Cluster) (int64, *planner.Status) {
	planned, ok := p.plannedTarget(state, target.Name)
	if !ok {
		return 0, nil
	}
	return planned.GetScore(), nil
}

func (p *PlannerAdapter) plannedTarget(state *planner.CycleState, target string) (*kpiv1alpha1.PlannedTarget, bool) {
	value, ok := state.Read(p.stateKey())
	if !ok {
		return nil, false
	}
	cycle, ok := value.(plannerCycleResult)
	if !ok {
		return nil, false
	}
	planned, ok := cycle.byTarget[target]
	return planned, ok
}

func (p *PlannerAdapter) stateKey() string {
	return "plugin.planner." + p.name
}

func promotionrunName(promotionrun *kaprov1alpha2.PromotionRun) string {
	if promotionrun == nil {
		return ""
	}
	return promotionrun.Name
}

func promotionrunVersion(promotionrun *kaprov1alpha2.PromotionRun) string {
	if promotionrun == nil {
		return ""
	}
	return promotionrun.Spec.Version
}

func stageStrategy(stage kaprov1alpha2.Stage) *kpiv1alpha1.StageStrategy {
	if stage.Strategy == nil {
		return nil
	}
	return &kpiv1alpha1.StageStrategy{
		MaxParallel:    stage.Strategy.MaxParallel,
		MaxUnavailable: stage.Strategy.MaxUnavailable,
	}
}

func plannerTargets(targets []kaprov1alpha2.Cluster) []*kpiv1alpha1.Target {
	out := make([]*kpiv1alpha1.Target, 0, len(targets))
	for _, target := range targets {
		out = append(out, &kpiv1alpha1.Target{
			Name:               target.Name,
			Labels:             copyParameters(target.Labels),
			Ready:              targetReady(target),
			ActivePromotionRun: target.Status.ActivePromotionRun,
		})
	}
	return out
}

func targetReady(target kaprov1alpha2.Cluster) bool {
	ready := apimeta.FindStatusCondition(target.Status.Conditions, "Ready")
	return ready == nil || ready.Status != metav1.ConditionFalse
}

func reasonOrDefault(reason, fallback string) string {
	if reason != "" {
		return reason
	}
	return fallback
}
