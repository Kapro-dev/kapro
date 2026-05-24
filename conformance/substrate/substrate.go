// Package substrate provides conformance checks for KSI substrate
// implementations.
package substrate

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"testing"
	"time"

	kaprov1alpha1 "kapro.io/kapro/api/kapro/v1alpha1"
	"kapro.io/kapro/conformance"
	ksisubstrate "kapro.io/kapro/pkg/kapro/substrate"
)

// Scenario contains the requests used by the substrate conformance harness.
type Scenario struct {
	Validate              *ksisubstrate.ValidateRequest
	MissingConfigValidate *ksisubstrate.ValidateRequest
	Apply                 *ksisubstrate.ApplyRequest
	Observe               *ksisubstrate.ObserveRequest
	RequiredOperations    []string
	Timeout               time.Duration
}

// DefaultScenario returns a minimal deterministic KSI scenario.
func DefaultScenario() Scenario {
	substrate := &kaprov1alpha1.Substrate{
		Spec: kaprov1alpha1.SubstrateSpec{
			ClassRef: &kaprov1alpha1.SubstrateClassReference{Name: "conformance"},
		},
	}
	cluster := &kaprov1alpha1.Cluster{}
	versions := map[string]string{"app": "v1.0.0"}
	envelope := ksisubstrate.RequestEnvelope{
		Substrate:  substrate,
		Cluster:    cluster,
		Parameters: map[string]string{"conformance": "true"},
	}
	return Scenario{
		Validate: &ksisubstrate.ValidateRequest{
			RequestEnvelope: envelope,
			DryRun:          true,
		},
		MissingConfigValidate: &ksisubstrate.ValidateRequest{
			RequestEnvelope: envelope,
			DryRun:          true,
		},
		Apply: &ksisubstrate.ApplyRequest{
			RequestEnvelope: envelope,
			DesiredVersions: cloneVersions(versions),
			DryRun:          true,
		},
		Observe: &ksisubstrate.ObserveRequest{
			RequestEnvelope: envelope,
			DesiredVersions: cloneVersions(versions),
		},
		RequiredOperations: []string{"apply", "observe"},
		Timeout:            10 * time.Second,
	}
}

// Run executes the substrate conformance checks and fails the test on the first
// failed check.
func Run(t *testing.T, s ksisubstrate.Substrate, scenario Scenario) {
	t.Helper()
	report := Check(context.Background(), s, scenario)
	for _, result := range report.Results {
		if !result.Passed {
			t.Fatalf("%s: %s", result.Name, result.Message)
		}
	}
}

// Check executes the substrate conformance checks and returns structured
// results for Go tests and future CLI wrappers.
func Check(ctx context.Context, s ksisubstrate.Substrate, scenario Scenario) conformance.Report {
	if ctx == nil {
		ctx = context.Background()
	}
	if scenario.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, scenario.Timeout)
		defer cancel()
	}
	scenario = normalizeScenario(scenario)
	return conformance.Report{
		Suite: "KSI substrate",
		Results: []conformance.Result{
			checkCapabilities(ctx, s, scenario),
			checkValidateValidConfig(ctx, s, scenario),
			checkValidateMissingConfig(ctx, s, scenario),
			checkOptionalCapabilityParity(s, scenario),
			checkApplyIdempotent(ctx, s, scenario),
			checkObserveDeterministic(ctx, s, scenario),
			checkApplyThenObserve(ctx, s, scenario),
			checkApplyDoesNotMutateRequest(ctx, s, scenario),
		},
	}
}

func normalizeScenario(scenario Scenario) Scenario {
	if scenario.Timeout == 0 {
		scenario.Timeout = 10 * time.Second
	}
	if scenario.Validate == nil {
		defaults := DefaultScenario()
		scenario.Validate = defaults.Validate
	}
	if scenario.MissingConfigValidate == nil {
		defaults := DefaultScenario()
		scenario.MissingConfigValidate = defaults.MissingConfigValidate
	}
	if scenario.Apply == nil {
		defaults := DefaultScenario()
		scenario.Apply = defaults.Apply
	}
	if scenario.Observe == nil {
		defaults := DefaultScenario()
		scenario.Observe = defaults.Observe
	}
	if len(scenario.RequiredOperations) == 0 {
		scenario.RequiredOperations = []string{"apply", "observe"}
	}
	return scenario
}

func checkCapabilities(ctx context.Context, s ksisubstrate.Substrate, scenario Scenario) conformance.Result {
	const name = "CapabilitiesReportSupportedContract"
	if s == nil {
		return conformance.Fail(name, "substrate is nil")
	}
	caps, err := s.Capabilities(ctx)
	if err != nil {
		return conformance.Fail(name, "Capabilities returned error: %v", err)
	}
	if caps == nil {
		return conformance.Fail(name, "Capabilities returned nil")
	}
	if caps.ContractVersion != ksisubstrate.ContractVersionV1Alpha1 {
		return conformance.Fail(name, "contract_version=%q, want %q", caps.ContractVersion, ksisubstrate.ContractVersionV1Alpha1)
	}
	if len(caps.SupportedExecutionModes) == 0 {
		return conformance.Fail(name, "supported execution modes is empty")
	}
	if caps.Capabilities.Operations == nil {
		return conformance.Fail(name, "operation capabilities are empty")
	}
	if missing := missingOperations(caps.Capabilities.Operations, scenario.RequiredOperations); len(missing) > 0 {
		return conformance.Fail(name, "missing required operations %v", missing)
	}
	return conformance.Pass(name)
}

func checkValidateValidConfig(ctx context.Context, s ksisubstrate.Substrate, scenario Scenario) conformance.Result {
	const name = "ValidateAcceptsValidConfig"
	res, err := s.Validate(ctx, scenario.Validate)
	if err != nil {
		return conformance.Fail(name, "Validate returned error: %v", err)
	}
	if res == nil {
		return conformance.Fail(name, "Validate returned nil")
	}
	if !res.Valid {
		return conformance.Fail(name, "Validate valid=false reason=%q message=%q", res.Reason, res.Message)
	}
	return conformance.Pass(name)
}

func checkValidateMissingConfig(ctx context.Context, s ksisubstrate.Substrate, scenario Scenario) conformance.Result {
	const name = "ValidateRejectsMissingConfigDeterministically"
	if scenario.MissingConfigValidate == nil {
		return conformance.Pass(name)
	}
	req := *scenario.MissingConfigValidate
	req.Config = nil
	res, err := s.Validate(ctx, &req)
	if err != nil {
		return conformance.Fail(name, "Validate returned error instead of deterministic invalid result: %v", err)
	}
	if res == nil {
		return conformance.Fail(name, "Validate returned nil")
	}
	if res.Valid {
		return conformance.Fail(name, "Validate accepted missing config")
	}
	if res.Reason == "" {
		return conformance.Fail(name, "invalid missing-config result must include a reason")
	}
	return conformance.Pass(name)
}

func checkOptionalCapabilityParity(s ksisubstrate.Substrate, scenario Scenario) conformance.Result {
	const name = "OptionalInterfacesMatchCapabilities"
	caps, err := s.Capabilities(context.Background())
	if err != nil || caps == nil {
		return conformance.Fail(name, "Capabilities unavailable: %v", err)
	}
	ops := caps.Capabilities.Operations
	if ops == nil {
		return conformance.Fail(name, "operation capabilities are empty")
	}
	if _, ok := s.(ksisubstrate.Rollbacker); ok && !ops.Rollback {
		return conformance.Fail(name, "implements Rollbacker but capabilities.rollback=false")
	}
	if _, ok := s.(ksisubstrate.Discoverer); ok && !ops.Discover {
		return conformance.Fail(name, "implements Discoverer but capabilities.discover=false")
	}
	if _, ok := s.(ksisubstrate.TwoPhaser); ok {
		if caps.Capabilities.Staging == nil || !caps.Capabilities.Staging.TwoPhase {
			return conformance.Fail(name, "implements TwoPhaser but capabilities.staging.twoPhase=false")
		}
	}
	_ = scenario
	return conformance.Pass(name)
}

func checkApplyIdempotent(ctx context.Context, s ksisubstrate.Substrate, scenario Scenario) conformance.Result {
	const name = "ApplyIsIdempotent"
	first, err := s.Apply(ctx, cloneApplyRequest(scenario.Apply))
	if err != nil {
		return conformance.Fail(name, "first Apply returned error: %v", err)
	}
	second, err := s.Apply(ctx, cloneApplyRequest(scenario.Apply))
	if err != nil {
		return conformance.Fail(name, "second Apply returned error: %v", err)
	}
	if !sameApplyShape(first, second) {
		return conformance.Fail(name, "first=%#v second=%#v", first, second)
	}
	return conformance.Pass(name)
}

func checkObserveDeterministic(ctx context.Context, s ksisubstrate.Substrate, scenario Scenario) conformance.Result {
	const name = "ObserveIsDeterministic"
	first, err := s.Observe(ctx, cloneObserveRequest(scenario.Observe))
	if err != nil {
		return conformance.Fail(name, "first Observe returned error: %v", err)
	}
	second, err := s.Observe(ctx, cloneObserveRequest(scenario.Observe))
	if err != nil {
		return conformance.Fail(name, "second Observe returned error: %v", err)
	}
	if !sameObserveShape(first, second) {
		return conformance.Fail(name, "first=%#v second=%#v", first, second)
	}
	return conformance.Pass(name)
}

func checkApplyThenObserve(ctx context.Context, s ksisubstrate.Substrate, scenario Scenario) conformance.Result {
	const name = "ApplyThenObserveReportsPhase"
	if _, err := s.Apply(ctx, cloneApplyRequest(scenario.Apply)); err != nil {
		return conformance.Fail(name, "Apply returned error: %v", err)
	}
	observed, err := s.Observe(ctx, cloneObserveRequest(scenario.Observe))
	if err != nil {
		return conformance.Fail(name, "Observe returned error: %v", err)
	}
	if observed == nil {
		return conformance.Fail(name, "Observe returned nil")
	}
	if observed.Phase == "" {
		return conformance.Fail(name, "observed phase is empty")
	}
	return conformance.Pass(name)
}

func checkApplyDoesNotMutateRequest(ctx context.Context, s ksisubstrate.Substrate, scenario Scenario) conformance.Result {
	const name = "ApplyDoesNotMutateRequest"
	req := cloneApplyRequest(scenario.Apply)
	before := cloneApplyRequest(req)
	if _, err := s.Apply(ctx, req); err != nil {
		return conformance.Fail(name, "Apply returned error: %v", err)
	}
	if !reflect.DeepEqual(before, req) {
		return conformance.Fail(name, "request mutated before=%#v after=%#v", before, req)
	}
	return conformance.Pass(name)
}

func missingOperations(ops *kaprov1alpha1.SubstrateOperationCapabilities, required []string) []string {
	var missing []string
	for _, op := range required {
		switch op {
		case "apply":
			if !ops.Apply {
				missing = append(missing, op)
			}
		case "observe":
			if !ops.Observe {
				missing = append(missing, op)
			}
		case "dryRun":
			if !ops.DryRun {
				missing = append(missing, op)
			}
		case "rollback":
			if !ops.Rollback {
				missing = append(missing, op)
			}
		case "discover":
			if !ops.Discover {
				missing = append(missing, op)
			}
		default:
			missing = append(missing, fmt.Sprintf("unknown:%s", op))
		}
	}
	return missing
}

func sameApplyShape(first, second *ksisubstrate.ApplyResult) bool {
	if first == nil || second == nil {
		return first == second
	}
	return first.Accepted == second.Accepted &&
		first.Applied == second.Applied &&
		first.Reason == second.Reason &&
		first.Message == second.Message &&
		reflect.DeepEqual(sortedSubstrateObjects(first.SubstrateObjects), sortedSubstrateObjects(second.SubstrateObjects))
}

func sameObserveShape(first, second *ksisubstrate.ObserveResult) bool {
	if first == nil || second == nil {
		return first == second
	}
	return first.Converged == second.Converged &&
		first.Phase == second.Phase &&
		first.Reason == second.Reason &&
		first.Message == second.Message &&
		reflect.DeepEqual(sortedSubstrateObjects(first.SubstrateObjects), sortedSubstrateObjects(second.SubstrateObjects))
}

func sortedSubstrateObjects(in []kaprov1alpha1.SubstrateObjectStatus) []kaprov1alpha1.SubstrateObjectStatus {
	out := append([]kaprov1alpha1.SubstrateObjectStatus(nil), in...)
	sort.SliceStable(out, func(i, j int) bool {
		return substrateObjectKey(out[i]) < substrateObjectKey(out[j])
	})
	return out
}

func substrateObjectKey(in kaprov1alpha1.SubstrateObjectStatus) string {
	return in.APIVersion + "\x00" +
		in.Kind + "\x00" +
		in.Namespace + "\x00" +
		in.Name + "\x00" +
		in.Unit + "\x00" +
		in.DesiredVersion + "\x00" +
		in.CurrentVersion + "\x00" +
		in.SyncStatus + "\x00" +
		in.HealthStatus + "\x00" +
		in.Phase + "\x00" +
		in.Message
}

func cloneApplyRequest(req *ksisubstrate.ApplyRequest) *ksisubstrate.ApplyRequest {
	if req == nil {
		return nil
	}
	out := *req
	out.RequestEnvelope = cloneEnvelope(req.RequestEnvelope)
	out.DesiredVersions = cloneVersions(req.DesiredVersions)
	return &out
}

func cloneObserveRequest(req *ksisubstrate.ObserveRequest) *ksisubstrate.ObserveRequest {
	if req == nil {
		return nil
	}
	out := *req
	out.RequestEnvelope = cloneEnvelope(req.RequestEnvelope)
	out.DesiredVersions = cloneVersions(req.DesiredVersions)
	return &out
}

func cloneEnvelope(in ksisubstrate.RequestEnvelope) ksisubstrate.RequestEnvelope {
	out := in
	if in.Class != nil {
		out.Class = in.Class.DeepCopy()
	}
	if in.Substrate != nil {
		out.Substrate = in.Substrate.DeepCopy()
	}
	if in.Config != nil {
		out.Config = in.Config.DeepCopyObject()
	}
	if in.Binding != nil {
		out.Binding = in.Binding.DeepCopyObject()
	}
	if in.Cluster != nil {
		out.Cluster = in.Cluster.DeepCopy()
	}
	if in.Credentials != nil {
		out.Credentials = in.Credentials.DeepCopy()
	}
	out.Parameters = cloneStringMap(in.Parameters)
	return out
}

func cloneVersions(in map[string]string) map[string]string {
	return cloneStringMap(in)
}

func cloneStringMap(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
