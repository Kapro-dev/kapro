package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"sort"
	"strconv"
	"strings"

	kpiv1alpha1 "kapro.io/kapro/spec/kpi/v1alpha1"

	"google.golang.org/grpc"
)

const (
	contractVersion = "v1alpha1"
	pluginVersion   = "0.1.0"

	defaultListenAddr = ":9090"
)

type capacityPlannerServer struct {
	kpiv1alpha1.UnimplementedPlannerServiceServer
}

type plannedTarget struct {
	name     string
	region   string
	capacity int64
	target   *kpiv1alpha1.PlannedTarget
}

func (s *capacityPlannerServer) GetCapabilities(context.Context, *kpiv1alpha1.GetCapabilitiesRequest) (*kpiv1alpha1.GetCapabilitiesResponse, error) {
	return &kpiv1alpha1.GetCapabilitiesResponse{
		ContractVersion: contractVersion,
		PluginVersion:   pluginVersion,
		Capabilities: []string{
			"filter",
			"score",
			"order",
			"defer",
		},
	}, nil
}

func (s *capacityPlannerServer) Plan(ctx context.Context, req *kpiv1alpha1.PlanRequest) (*kpiv1alpha1.PlanResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(req.GetTargets()) == 0 {
		return &kpiv1alpha1.PlanResponse{}, nil
	}
	params := req.GetParameters()
	minCapacity, err := parseOptionalInt(params["minAvailableCapacityPercent"], 0)
	if err != nil {
		return nil, err
	}
	labelFilters := requiredLabels(params)
	planned := make([]plannedTarget, 0, len(req.GetTargets()))
	for _, target := range orderedTargets(req.GetTargets()) {
		planned = append(planned, s.planTarget(target, minCapacity, labelFilters))
	}
	sortPlanned(planned)
	applyMaxParallel(planned, req.GetStrategy().GetMaxParallel())
	sortPlanned(planned)
	out := make([]*kpiv1alpha1.PlannedTarget, 0, len(planned))
	for _, item := range planned {
		out = append(out, item.target)
	}
	return &kpiv1alpha1.PlanResponse{Targets: out}, nil
}

func sortPlanned(planned []plannedTarget) {
	sort.SliceStable(planned, func(i, j int) bool {
		left, right := planned[i], planned[j]
		if decisionRank(left.target.GetDecision()) != decisionRank(right.target.GetDecision()) {
			return decisionRank(left.target.GetDecision()) < decisionRank(right.target.GetDecision())
		}
		if left.capacity != right.capacity {
			return left.capacity > right.capacity
		}
		if left.region != right.region {
			return left.region < right.region
		}
		return left.name < right.name
	})
}

func applyMaxParallel(planned []plannedTarget, maxParallel int32) {
	if maxParallel <= 0 {
		return
	}
	included := int32(0)
	for _, item := range planned {
		if item.target.GetDecision() != kpiv1alpha1.PlanningDecision_PLANNING_DECISION_INCLUDE {
			continue
		}
		included++
		if included <= maxParallel {
			continue
		}
		item.target.Decision = kpiv1alpha1.PlanningDecision_PLANNING_DECISION_DEFER
		item.target.Reason = "MaxParallelLimit"
		item.target.Message = fmt.Sprintf("deferred by maxParallel=%d", maxParallel)
	}
}

func (s *capacityPlannerServer) planTarget(target *kpiv1alpha1.Target, minCapacity int64, labelFilters map[string]string) plannedTarget {
	labels := target.GetLabels()
	capacity, capacityErr := capacityPercent(labels)
	item := plannedTarget{
		name:     target.GetName(),
		region:   firstNonEmpty(labels["region"], labels["topology.kubernetes.io/region"]),
		capacity: capacity,
		target: &kpiv1alpha1.PlannedTarget{
			Name:  target.GetName(),
			Score: capacity,
		},
	}
	if !target.GetReady() {
		item.target.Decision = kpiv1alpha1.PlanningDecision_PLANNING_DECISION_SKIP
		item.target.Reason = "TargetNotReady"
		item.target.Message = "target is not ready"
		return item
	}
	if target.GetActiveRelease() != "" {
		item.target.Decision = kpiv1alpha1.PlanningDecision_PLANNING_DECISION_DEFER
		item.target.Reason = "ActiveRelease"
		item.target.Message = fmt.Sprintf("target already has active release %q", target.GetActiveRelease())
		return item
	}
	for _, key := range sortedLabelKeys(labelFilters) {
		want := labelFilters[key]
		got, ok := labels[key]
		if !ok {
			item.target.Decision = kpiv1alpha1.PlanningDecision_PLANNING_DECISION_SKIP
			item.target.Reason = "RequiredLabelMismatch"
			item.target.Message = fmt.Sprintf("label %s is missing, want %q", key, want)
			return item
		}
		if got != want {
			item.target.Decision = kpiv1alpha1.PlanningDecision_PLANNING_DECISION_SKIP
			item.target.Reason = "RequiredLabelMismatch"
			item.target.Message = fmt.Sprintf("label %s=%q, want %q", key, got, want)
			return item
		}
	}
	if capacityErr != nil {
		item.target.Decision = kpiv1alpha1.PlanningDecision_PLANNING_DECISION_SKIP
		item.target.Reason = "InvalidCapacity"
		item.target.Message = capacityErr.Error()
		return item
	}
	if capacity < minCapacity {
		item.target.Decision = kpiv1alpha1.PlanningDecision_PLANNING_DECISION_DEFER
		item.target.Reason = "InsufficientCapacity"
		item.target.Message = fmt.Sprintf("available capacity %d%% below minimum %d%%", capacity, minCapacity)
		return item
	}
	item.target.Decision = kpiv1alpha1.PlanningDecision_PLANNING_DECISION_INCLUDE
	item.target.Reason = "Eligible"
	item.target.Message = fmt.Sprintf("available capacity %d%%", capacity)
	return item
}

func orderedTargets(targets []*kpiv1alpha1.Target) []*kpiv1alpha1.Target {
	out := append([]*kpiv1alpha1.Target(nil), targets...)
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].GetName() < out[j].GetName()
	})
	return out
}

func capacityPercent(labels map[string]string) (int64, error) {
	for _, key := range []string{"kapro.io/available-capacity-percent", "availableCapacityPercent", "capacity"} {
		if raw := strings.TrimSpace(labels[key]); raw != "" {
			value, err := strconv.ParseInt(raw, 10, 64)
			if err != nil {
				return 0, fmt.Errorf("capacity label %s=%q is not an integer percentage", key, raw)
			}
			if value < 0 {
				return 0, nil
			}
			if value > 100 {
				return 100, nil
			}
			return value, nil
		}
	}
	return 100, nil
}

func parseOptionalInt(raw string, defaultValue int64) (int64, error) {
	if strings.TrimSpace(raw) == "" {
		return defaultValue, nil
	}
	value, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse minAvailableCapacityPercent: %w", err)
	}
	if value < 0 || value > 100 {
		return 0, fmt.Errorf("minAvailableCapacityPercent must be between 0 and 100")
	}
	return value, nil
}

func requiredLabels(params map[string]string) map[string]string {
	out := map[string]string{}
	for key, value := range params {
		if strings.HasPrefix(key, "requiredLabel.") {
			labelKey := strings.TrimPrefix(key, "requiredLabel.")
			if labelKey != "" {
				out[labelKey] = value
			}
		}
	}
	return out
}

func sortedLabelKeys(labels map[string]string) []string {
	keys := make([]string, 0, len(labels))
	for key := range labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func decisionRank(decision kpiv1alpha1.PlanningDecision) int {
	switch decision {
	case kpiv1alpha1.PlanningDecision_PLANNING_DECISION_INCLUDE:
		return 0
	case kpiv1alpha1.PlanningDecision_PLANNING_DECISION_DEFER:
		return 1
	case kpiv1alpha1.PlanningDecision_PLANNING_DECISION_SKIP:
		return 2
	default:
		return 3
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func main() {
	listenAddr := flag.String("listen", defaultListenAddr, "gRPC listen address")
	flag.Parse()

	listener, err := net.Listen("tcp", *listenAddr)
	if err != nil {
		log.Fatalf("listen on %s: %v", *listenAddr, err)
	}
	grpcServer := grpc.NewServer()
	kpiv1alpha1.RegisterPlannerServiceServer(grpcServer, &capacityPlannerServer{})
	log.Printf("capacity planner plugin listening on %s", *listenAddr)
	if err := grpcServer.Serve(listener); err != nil {
		log.Fatalf("serve grpc: %v", err)
	}
}
