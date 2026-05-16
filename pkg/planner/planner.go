// Package planner defines scheduler-style extension points for release target planning.
package planner

import (
	"context"
	"fmt"
	"sort"
	"sync"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
)

const (
	// MaxNodeScore matches the Kubernetes scheduler scoring scale.
	MaxNodeScore int64 = 100
	// MinNodeScore matches the Kubernetes scheduler scoring scale.
	MinNodeScore int64 = 0
)

// Code is the normalized result code returned by planner plugins.
type Code string

const (
	Success Code = "Success"
	Error   Code = "Error"
	Skip    Code = "Skip"
)

// Status is the normalized result of one planner plugin phase.
type Status struct {
	Code    Code
	Reason  string
	Message string
}

// IsSuccess returns true when the status permits planning to continue.
func (s *Status) IsSuccess() bool {
	return s == nil || s.Code == "" || s.Code == Success || s.Code == Skip
}

// AsError converts a non-success status into an error.
func (s *Status) AsError(plugin, phase string) error {
	if s == nil || s.IsSuccess() {
		return nil
	}
	if s.Message == "" {
		return fmt.Errorf("planner plugin %q failed at %s", plugin, phase)
	}
	return fmt.Errorf("planner plugin %q failed at %s: %s", plugin, phase, s.Message)
}

// NewStatus returns a planner status.
func NewStatus(code Code, message string) *Status {
	return &Status{Code: code, Message: message}
}

// NewStatusReason returns a planner status with a machine-readable reason.
func NewStatusReason(code Code, reason, message string) *Status {
	return &Status{Code: code, Reason: reason, Message: message}
}

// CycleState carries per-planning-cycle data shared between plugin phases.
type CycleState struct {
	values map[string]any
}

// NewCycleState returns an empty cycle state.
func NewCycleState() *CycleState {
	return &CycleState{values: make(map[string]any)}
}

// Write stores a value for later plugin phases in the same planning cycle.
func (s *CycleState) Write(key string, value any) {
	if s.values == nil {
		s.values = make(map[string]any)
	}
	s.values[key] = value
}

// Read returns a value written earlier in the planning cycle.
func (s *CycleState) Read(key string) (any, bool) {
	if s == nil {
		return nil, false
	}
	v, ok := s.values[key]
	return v, ok
}

// Request is the immutable planning context for one Release/Pipeline/Stage expansion.
type Request struct {
	Release         *kaprov1alpha1.Release
	PipelineRefName string
	Pipeline        *kaprov1alpha1.Pipeline
	Stage           kaprov1alpha1.Stage
}

// Decision records one non-default planner decision for operator visibility.
type Decision struct {
	Target  string
	Plugin  string
	Phase   string
	Reason  string
	Message string
}

// Result is the output of one planning cycle.
type Result struct {
	Targets   []kaprov1alpha1.MemberCluster
	Decisions []Decision
}

// Plugin is the base identity contract implemented by all planner plugins.
type Plugin interface {
	Name() string
}

// PreFilterPlugin runs once before target filtering.
type PreFilterPlugin interface {
	Plugin
	PreFilter(ctx context.Context, state *CycleState, req Request, targets []kaprov1alpha1.MemberCluster) *Status
}

// FilterPlugin decides whether one target remains eligible for this stage.
type FilterPlugin interface {
	Plugin
	Filter(ctx context.Context, state *CycleState, req Request, target kaprov1alpha1.MemberCluster) *Status
}

// ScorePlugin assigns a score to one eligible target.
type ScorePlugin interface {
	Plugin
	Score(ctx context.Context, state *CycleState, req Request, target kaprov1alpha1.MemberCluster) (int64, *Status)
}

// NormalizeScorePlugin can normalize scores after all targets have been scored.
type NormalizeScorePlugin interface {
	Plugin
	NormalizeScore(ctx context.Context, state *CycleState, req Request, scores NodeScoreList) *Status
}

// ReservePlugin reserves one selected target before the controller binds it by creating a ReleaseTarget.
type ReservePlugin interface {
	Plugin
	Reserve(ctx context.Context, state *CycleState, req Request, target kaprov1alpha1.MemberCluster) *Status
}

// PermitPlugin is the final admission hook before a target is bound into the release plan.
type PermitPlugin interface {
	Plugin
	Permit(ctx context.Context, state *CycleState, req Request, target kaprov1alpha1.MemberCluster) *Status
}

// NodeScore mirrors the Kubernetes scheduler score shape for one target.
type NodeScore struct {
	Name  string
	Score int64
}

// NodeScoreList is a list of target scores.
type NodeScoreList []NodeScore

// Framework runs planner plugins in scheduler-style phases.
type Framework struct {
	mu      sync.RWMutex
	plugins []Plugin
}

// NewFramework returns a planner framework with plugins in execution order.
func NewFramework(plugins ...Plugin) *Framework {
	return &Framework{plugins: append([]Plugin(nil), plugins...)}
}

// Upsert adds or replaces a plugin by name and returns the previous plugin,
// when one existed. It is safe to call while planning is running; each planning
// cycle uses a snapshot of the plugin list.
func (f *Framework) Upsert(plugin Plugin) Plugin {
	if f == nil || plugin == nil {
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, existing := range f.plugins {
		if existing.Name() == plugin.Name() {
			old := existing
			f.plugins[i] = plugin
			return old
		}
	}
	f.plugins = append(f.plugins, plugin)
	return nil
}

// Unregister removes a plugin by name and returns the previous plugin, when
// one existed.
func (f *Framework) Unregister(name string) (Plugin, bool) {
	if f == nil {
		return nil, false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, existing := range f.plugins {
		if existing.Name() == name {
			old := existing
			f.plugins = append(f.plugins[:i], f.plugins[i+1:]...)
			return old, true
		}
	}
	return nil, false
}

func (f *Framework) snapshot() []Plugin {
	if f == nil {
		return nil
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	return append([]Plugin(nil), f.plugins...)
}

// Plan returns the eligible targets in deterministic execution order.
func (f *Framework) Plan(ctx context.Context, req Request, targets []kaprov1alpha1.MemberCluster) ([]kaprov1alpha1.MemberCluster, error) {
	result, err := f.PlanWithResult(ctx, req, targets)
	if err != nil {
		return nil, err
	}
	return result.Targets, nil
}

// PlanWithResult returns eligible targets and planner decisions for observability.
func (f *Framework) PlanWithResult(ctx context.Context, req Request, targets []kaprov1alpha1.MemberCluster) (Result, error) {
	if f == nil {
		f = NewFramework()
	}
	plugins := f.snapshot()
	result := Result{}
	state := NewCycleState()
	targets = append([]kaprov1alpha1.MemberCluster(nil), targets...)

	for _, plugin := range plugins {
		preFilter, ok := plugin.(PreFilterPlugin)
		if !ok {
			continue
		}
		if err := preFilter.PreFilter(ctx, state, req, targets).AsError(plugin.Name(), "PreFilter"); err != nil {
			return result, err
		}
	}

	filtered := make([]kaprov1alpha1.MemberCluster, 0, len(targets))
	for _, target := range targets {
		allowed := true
		for _, plugin := range plugins {
			filter, ok := plugin.(FilterPlugin)
			if !ok {
				continue
			}
			status := filter.Filter(ctx, state, req, target)
			if err := status.AsError(plugin.Name(), "Filter"); err != nil {
				return result, err
			}
			if status != nil && status.Code == Skip {
				result.Decisions = append(result.Decisions, decisionFromStatus(target.Name, plugin.Name(), "Filter", status))
				allowed = false
				break
			}
		}
		if allowed {
			filtered = append(filtered, target)
		}
	}

	scores := scoreTargets(ctx, plugins, state, req, filtered)
	if err := normalizeScores(ctx, plugins, state, req, scores); err != nil {
		return result, err
	}
	sort.SliceStable(filtered, func(i, j int) bool {
		left := scores[filtered[i].Name]
		right := scores[filtered[j].Name]
		if left == right {
			return filtered[i].Name < filtered[j].Name
		}
		return left > right
	})

	planned := make([]kaprov1alpha1.MemberCluster, 0, len(filtered))
	for _, target := range filtered {
		if err := runReserve(ctx, plugins, state, req, target); err != nil {
			return result, err
		}
		if allowed, decision, err := runPermit(ctx, plugins, state, req, target); err != nil {
			return result, err
		} else if !allowed {
			result.Decisions = append(result.Decisions, decision)
			continue
		}
		planned = append(planned, target)
	}
	result.Targets = planned
	return result, nil
}

func scoreTargets(ctx context.Context, plugins []Plugin, state *CycleState, req Request, targets []kaprov1alpha1.MemberCluster) map[string]int64 {
	scores := make(map[string]int64, len(targets))
	for _, target := range targets {
		var total int64
		for _, plugin := range plugins {
			scorer, ok := plugin.(ScorePlugin)
			if !ok {
				continue
			}
			score, status := scorer.Score(ctx, state, req, target)
			if status != nil && !status.IsSuccess() {
				continue
			}
			total += clampScore(score)
		}
		scores[target.Name] = total
	}
	return scores
}

func normalizeScores(ctx context.Context, plugins []Plugin, state *CycleState, req Request, scores map[string]int64) error {
	scoreList := make(NodeScoreList, 0, len(scores))
	for name, score := range scores {
		scoreList = append(scoreList, NodeScore{Name: name, Score: score})
	}
	sort.Slice(scoreList, func(i, j int) bool { return scoreList[i].Name < scoreList[j].Name })
	for _, plugin := range plugins {
		normalizer, ok := plugin.(NormalizeScorePlugin)
		if !ok {
			continue
		}
		if err := normalizer.NormalizeScore(ctx, state, req, scoreList).AsError(plugin.Name(), "NormalizeScore"); err != nil {
			return err
		}
	}
	for _, score := range scoreList {
		scores[score.Name] = clampScore(score.Score)
	}
	return nil
}

func runReserve(ctx context.Context, plugins []Plugin, state *CycleState, req Request, target kaprov1alpha1.MemberCluster) error {
	for _, plugin := range plugins {
		reserve, ok := plugin.(ReservePlugin)
		if !ok {
			continue
		}
		if err := reserve.Reserve(ctx, state, req, target).AsError(plugin.Name(), "Reserve"); err != nil {
			return err
		}
	}
	return nil
}

func runPermit(ctx context.Context, plugins []Plugin, state *CycleState, req Request, target kaprov1alpha1.MemberCluster) (bool, Decision, error) {
	for _, plugin := range plugins {
		permit, ok := plugin.(PermitPlugin)
		if !ok {
			continue
		}
		status := permit.Permit(ctx, state, req, target)
		if err := status.AsError(plugin.Name(), "Permit"); err != nil {
			return false, Decision{}, err
		}
		if status != nil && status.Code == Skip {
			return false, decisionFromStatus(target.Name, plugin.Name(), "Permit", status), nil
		}
	}
	return true, Decision{}, nil
}

func clampScore(score int64) int64 {
	if score < MinNodeScore {
		return MinNodeScore
	}
	if score > MaxNodeScore {
		return MaxNodeScore
	}
	return score
}

func decisionFromStatus(target, plugin, phase string, status *Status) Decision {
	if status == nil {
		return Decision{Target: target, Plugin: plugin, Phase: phase}
	}
	reason := status.Reason
	if reason == "" {
		reason = string(status.Code)
	}
	return Decision{
		Target:  target,
		Plugin:  plugin,
		Phase:   phase,
		Reason:  reason,
		Message: status.Message,
	}
}
