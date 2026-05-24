package substrate

import (
	"context"
	"strconv"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	kaprov1alpha1 "kapro.io/kapro/api/kapro/v1alpha1"
	"kapro.io/kapro/pkg/kapro/substrate"
)

func TestRun(t *testing.T) {
	Run(t, fakeSubstrate{}, validScenario())
}

func TestCheckReportsMissingCapabilities(t *testing.T) {
	report := Check(context.Background(), missingOperationSubstrate{}, validScenario())
	if report.Passed() {
		t.Fatalf("Check passed for substrate with missing operations: %#v", report)
	}
	for _, result := range report.Failed() {
		if result.Name == "CapabilitiesReportSupportedContract" &&
			strings.Contains(result.Message, "missing required operations") {
			return
		}
	}
	t.Fatalf("missing operation failure not reported: %#v", report.Failed())
}

func TestCheckReportsRequestMutation(t *testing.T) {
	report := Check(context.Background(), mutatingSubstrate{}, validScenario())
	if report.Passed() {
		t.Fatalf("Check passed for mutating substrate: %#v", report)
	}
	for _, result := range report.Failed() {
		if result.Name == "ApplyDoesNotMutateRequest" {
			return
		}
	}
	t.Fatalf("request mutation failure not reported: %#v", report.Failed())
}

func TestCheckReportsRequestObjectMutation(t *testing.T) {
	report := Check(context.Background(), mutatingObjectSubstrate{}, validScenario())
	if report.Passed() {
		t.Fatalf("Check passed for object-mutating substrate: %#v", report)
	}
	for _, result := range report.Failed() {
		if result.Name == "ApplyDoesNotMutateRequest" {
			return
		}
	}
	t.Fatalf("request object mutation failure not reported: %#v", report.Failed())
}

func TestCheckReportsNonIdempotentApplySubstrateObjects(t *testing.T) {
	report := Check(context.Background(), &changingApplyObjectsSubstrate{}, validScenario())
	if report.Passed() {
		t.Fatalf("Check passed for non-idempotent apply objects: %#v", report)
	}
	for _, result := range report.Failed() {
		if result.Name == "ApplyIsIdempotent" {
			return
		}
	}
	t.Fatalf("non-idempotent apply object failure not reported: %#v", report.Failed())
}

func TestCheckReportsNonDeterministicObserveSubstrateObjects(t *testing.T) {
	report := Check(context.Background(), &changingObserveObjectsSubstrate{}, validScenario())
	if report.Passed() {
		t.Fatalf("Check passed for non-deterministic observe objects: %#v", report)
	}
	for _, result := range report.Failed() {
		if result.Name == "ObserveIsDeterministic" {
			return
		}
	}
	t.Fatalf("non-deterministic observe object failure not reported: %#v", report.Failed())
}

func validScenario() Scenario {
	config := &runtime.Unknown{Raw: []byte(`{"kind":"Config"}`)}
	scenario := DefaultScenario()
	scenario.Validate.Config = config
	scenario.Apply.Config = config
	scenario.Observe.Config = config
	return scenario
}

type fakeSubstrate struct{}

func (fakeSubstrate) Validate(_ context.Context, req *substrate.ValidateRequest) (*substrate.ValidateResult, error) {
	if req == nil || req.Config == nil {
		return &substrate.ValidateResult{Valid: false, Reason: "ConfigMissing", Message: "config is required"}, nil
	}
	return &substrate.ValidateResult{Valid: true, Reason: "Valid", Message: "config accepted"}, nil
}

func (fakeSubstrate) Apply(_ context.Context, _ *substrate.ApplyRequest) (*substrate.ApplyResult, error) {
	return &substrate.ApplyResult{Accepted: true, Applied: 1, Reason: "Applied", Message: "applied"}, nil
}

func (fakeSubstrate) Observe(_ context.Context, _ *substrate.ObserveRequest) (*substrate.ObserveResult, error) {
	return &substrate.ObserveResult{Converged: true, Phase: kaprov1alpha1.DeliveryPhaseConverged, Reason: "Converged", Message: "ready"}, nil
}

func (fakeSubstrate) Capabilities(context.Context) (*substrate.Capabilities, error) {
	return &substrate.Capabilities{
		ContractVersion:         substrate.ContractVersionV1Alpha1,
		SupportedExecutionModes: []kaprov1alpha1.ExecutionMode{kaprov1alpha1.ExecutionModeHubPush},
		Capabilities: kaprov1alpha1.SubstrateCapabilities{
			Operations: &kaprov1alpha1.SubstrateOperationCapabilities{
				Apply:   true,
				Observe: true,
				DryRun:  true,
			},
			Staging: &kaprov1alpha1.SubstrateStagingCapabilities{},
		},
	}, nil
}

type missingOperationSubstrate struct {
	fakeSubstrate
}

func (missingOperationSubstrate) Capabilities(context.Context) (*substrate.Capabilities, error) {
	return &substrate.Capabilities{
		ContractVersion:         substrate.ContractVersionV1Alpha1,
		SupportedExecutionModes: []kaprov1alpha1.ExecutionMode{kaprov1alpha1.ExecutionModeHubPush},
		Capabilities: kaprov1alpha1.SubstrateCapabilities{
			Operations: &kaprov1alpha1.SubstrateOperationCapabilities{Apply: true},
		},
	}, nil
}

type mutatingSubstrate struct {
	fakeSubstrate
}

func (mutatingSubstrate) Apply(_ context.Context, req *substrate.ApplyRequest) (*substrate.ApplyResult, error) {
	if req.Parameters == nil {
		req.Parameters = map[string]string{}
	}
	req.Parameters["mutated"] = metav1.Now().String()
	return &substrate.ApplyResult{Accepted: true, Applied: 1, Reason: "Applied", Message: "applied"}, nil
}

type mutatingObjectSubstrate struct {
	fakeSubstrate
}

func (mutatingObjectSubstrate) Apply(_ context.Context, req *substrate.ApplyRequest) (*substrate.ApplyResult, error) {
	req.Substrate.Spec.Parameters = map[string]string{"mutated": "true"}
	return &substrate.ApplyResult{Accepted: true, Applied: 1, Reason: "Applied", Message: "applied"}, nil
}

type changingApplyObjectsSubstrate struct {
	fakeSubstrate
	n int
}

func (s *changingApplyObjectsSubstrate) Apply(_ context.Context, _ *substrate.ApplyRequest) (*substrate.ApplyResult, error) {
	s.n++
	return &substrate.ApplyResult{
		Accepted: true,
		Applied:  1,
		Reason:   "Applied",
		Message:  "applied",
		SubstrateObjects: []kaprov1alpha1.SubstrateObjectStatus{{
			Kind:           "Deployment",
			Name:           "checkout",
			CurrentVersion: "v1.0." + strconv.Itoa(s.n),
			Phase:          string(kaprov1alpha1.DeliveryPhaseApplying),
		}},
	}, nil
}

type changingObserveObjectsSubstrate struct {
	fakeSubstrate
	n int
}

func (s *changingObserveObjectsSubstrate) Observe(_ context.Context, _ *substrate.ObserveRequest) (*substrate.ObserveResult, error) {
	s.n++
	return &substrate.ObserveResult{
		Converged: true,
		Phase:     kaprov1alpha1.DeliveryPhaseConverged,
		Reason:    "Converged",
		Message:   "ready",
		SubstrateObjects: []kaprov1alpha1.SubstrateObjectStatus{{
			Kind:           "Deployment",
			Name:           "checkout",
			CurrentVersion: "v1.0." + strconv.Itoa(s.n),
			Phase:          string(kaprov1alpha1.DeliveryPhaseConverged),
		}},
	}, nil
}
