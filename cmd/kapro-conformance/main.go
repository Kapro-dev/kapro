package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	argocdsubstratev1alpha1 "kapro.io/kapro/api/substrate/argocd/v1alpha1"
	fluxsubstratev1alpha1 "kapro.io/kapro/api/substrate/flux/v1alpha1"
	kubernetessubstratev1alpha1 "kapro.io/kapro/api/substrate/kubernetes/v1alpha1"
	kaprov1alpha2 "kapro.io/kapro/api/v1alpha2"
	"kapro.io/kapro/conformance"
	actuatorconformance "kapro.io/kapro/conformance/actuator"
	gateconformance "kapro.io/kapro/conformance/gate"
	plannerconformance "kapro.io/kapro/conformance/planner"
	providerconformance "kapro.io/kapro/conformance/provider"
	substrateconformance "kapro.io/kapro/conformance/substrate"
	ksisubstrate "kapro.io/kapro/pkg/kapro/substrate"
	"kapro.io/kapro/pkg/spokeprovider"
	kaiv1alpha1 "kapro.io/kapro/spec/kai/v1alpha1"
	kgiv1alpha1 "kapro.io/kapro/spec/kgi/v1alpha1"
	kpiv1alpha1 "kapro.io/kapro/spec/kpi/v1alpha1"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

var errConformanceFailed = errors.New("conformance failed")

type options struct {
	endpoint   string
	timeout    time.Duration
	params     keyValues
	tls        bool
	caFile     string
	serverName string
	output     string
}

type optionSet struct {
	endpoint        bool
	requireEndpoint bool
	params          bool
	tls             bool
}

type keyValues map[string]string

func (v *keyValues) String() string {
	if v == nil || len(*v) == 0 {
		return ""
	}
	pairs := make([]string, 0, len(*v))
	for key, value := range *v {
		pairs = append(pairs, key+"="+value)
	}
	return strings.Join(pairs, ",")
}

func (v *keyValues) Set(raw string) error {
	key, value, ok := strings.Cut(raw, "=")
	if !ok || strings.TrimSpace(key) == "" {
		return fmt.Errorf("parameter must be key=value")
	}
	if *v == nil {
		*v = make(map[string]string)
	}
	(*v)[strings.TrimSpace(key)] = value
	return nil
}

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		if !errors.Is(err, errConformanceFailed) {
			fmt.Fprintln(os.Stderr, err)
		}
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		usage(stderr)
		return flag.ErrHelp
	}
	switch args[0] {
	case "all":
		return runAll(args[1:], stdout)
	case "actuator":
		return runActuator(args[1:], stdout)
	case "gate":
		return runGate(args[1:], stdout)
	case "planner":
		return runPlanner(args[1:], stdout)
	case "provider":
		return runProvider(args[1:], stdout)
	case "substrate":
		return runSubstrate(args[1:], stdout)
	case "-h", "--help", "help":
		usage(stdout)
		return nil
	default:
		usage(stderr)
		return fmt.Errorf("unknown conformance suite %q", args[0])
	}
}

func runAll(args []string, out io.Writer) error {
	opts, err := parseOptions("all", args, optionSet{}, out)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), opts.timeout)
	defer cancel()

	reports := []suiteReport{
		suiteReportFromConformance("reference", actuatorconformance.Check(ctx, referenceActuatorClient{}, actuatorconformance.DefaultScenario())),
		suiteReportFromConformance("reference", gateconformance.Check(ctx, referenceGateClient{}, gateconformance.DefaultScenario())),
		suiteReportFromConformance("reference", plannerconformance.Check(ctx, referencePlannerClient{}, plannerconformance.DefaultScenario())),
		suiteReportFromConformance("reference", providerconformance.Check(ctx, referenceProvider{}, providerconformance.DefaultScenario())),
	}
	reports = append(reports, referenceSubstrateReports(ctx)...)
	return printReports(out, opts, reports)
}

func runActuator(args []string, out io.Writer) error {
	opts, err := parseOptions("actuator", args, endpointOptionSet(), out)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	conn, err := dial(opts)
	if err != nil {
		return err
	}
	defer conn.Close()

	scenario := actuatorconformance.DefaultScenario()
	mergeParams(scenario.Apply.Parameters, opts.params)
	mergeParams(scenario.IsConverged.Parameters, opts.params)
	mergeParams(scenario.Rollback.Parameters, opts.params)
	scenario.Timeout = opts.timeout

	ctx, cancel := context.WithTimeout(context.Background(), opts.timeout)
	defer cancel()
	report := actuatorconformance.Check(ctx, kaiv1alpha1.NewActuatorServiceClient(conn), scenario)
	return printReports(out, opts, []suiteReport{suiteReportFromConformance(opts.endpoint, report)})
}

func runGate(args []string, out io.Writer) error {
	opts, err := parseOptions("gate", args, endpointOptionSet(), out)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	conn, err := dial(opts)
	if err != nil {
		return err
	}
	defer conn.Close()

	scenario := gateconformance.DefaultScenario()
	mergeParams(scenario.Evaluate.Parameters, opts.params)
	scenario.Timeout = opts.timeout

	ctx, cancel := context.WithTimeout(context.Background(), opts.timeout)
	defer cancel()
	report := gateconformance.Check(ctx, kgiv1alpha1.NewGateServiceClient(conn), scenario)
	return printReports(out, opts, []suiteReport{suiteReportFromConformance(opts.endpoint, report)})
}

func runPlanner(args []string, out io.Writer) error {
	opts, err := parseOptions("planner", args, endpointOptionSet(), out)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	conn, err := dial(opts)
	if err != nil {
		return err
	}
	defer conn.Close()

	scenario := plannerconformance.DefaultScenario()
	mergeParams(scenario.Plan.Parameters, opts.params)
	scenario.Timeout = opts.timeout

	ctx, cancel := context.WithTimeout(context.Background(), opts.timeout)
	defer cancel()
	report := plannerconformance.Check(ctx, kpiv1alpha1.NewPlannerServiceClient(conn), scenario)
	return printReports(out, opts, []suiteReport{suiteReportFromConformance(opts.endpoint, report)})
}

func runProvider(args []string, out io.Writer) error {
	opts, err := parseOptions("provider", args, optionSet{}, out)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), opts.timeout)
	defer cancel()
	report := providerconformance.Check(ctx, referenceProvider{}, providerconformance.DefaultScenario())
	return printReports(out, opts, []suiteReport{suiteReportFromConformance("reference", report)})
}

func runSubstrate(args []string, out io.Writer) error {
	opts, err := parseOptions("substrate", args, optionSet{}, out)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), opts.timeout)
	defer cancel()
	return printReports(out, opts, referenceSubstrateReports(ctx))
}

func endpointOptionSet() optionSet {
	return optionSet{
		endpoint:        true,
		requireEndpoint: true,
		params:          true,
		tls:             true,
	}
}

func parseOptions(suite string, args []string, optsFor optionSet, out io.Writer) (options, error) {
	opts := options{timeout: 10 * time.Second}
	fs := flag.NewFlagSet("kapro-conformance "+suite, flag.ContinueOnError)
	fs.SetOutput(out)
	fs.DurationVar(&opts.timeout, "timeout", opts.timeout, "overall conformance timeout")
	fs.StringVar(&opts.output, "o", "", "Output format: json")
	fs.StringVar(&opts.output, "output", "", "Output format: json")
	if optsFor.endpoint {
		fs.StringVar(&opts.endpoint, "endpoint", "", "gRPC target, for example localhost:9090 or dns:///plugin.namespace.svc:9090")
	}
	if optsFor.params {
		fs.Var(&opts.params, "param", "scenario parameter as key=value; repeat for multiple parameters")
	}
	if optsFor.tls {
		fs.BoolVar(&opts.tls, "tls", false, "use TLS for the gRPC connection")
		fs.StringVar(&opts.caFile, "ca-file", "", "CA bundle for TLS server verification")
		fs.StringVar(&opts.serverName, "server-name", "", "TLS server name override")
	}
	if err := fs.Parse(args); err != nil {
		return options{}, err
	}
	if optsFor.requireEndpoint && opts.endpoint == "" {
		return options{}, fmt.Errorf("--endpoint is required")
	}
	if opts.timeout <= 0 {
		return options{}, fmt.Errorf("--timeout must be positive")
	}
	if opts.output != "" && opts.output != "json" {
		return options{}, fmt.Errorf("--output must be json when set")
	}
	if opts.endpoint != "" {
		opts.endpoint = normalizeEndpoint(opts.endpoint)
	}
	return opts, nil
}

func dial(opts options) (*grpc.ClientConn, error) {
	transport := grpc.WithTransportCredentials(insecure.NewCredentials())
	if opts.tls {
		creds, err := tlsCredentials(opts)
		if err != nil {
			return nil, err
		}
		transport = grpc.WithTransportCredentials(creds)
	}
	conn, err := grpc.NewClient(opts.endpoint, transport)
	if err != nil {
		return nil, fmt.Errorf("create gRPC client for %s: %w", opts.endpoint, err)
	}
	return conn, nil
}

func tlsCredentials(opts options) (credentials.TransportCredentials, error) {
	if opts.caFile != "" {
		return credentials.NewClientTLSFromFile(opts.caFile, opts.serverName)
	}
	return credentials.NewTLS(&tls.Config{
		MinVersion: tls.VersionTLS12,
		ServerName: opts.serverName,
	}), nil
}

func normalizeEndpoint(endpoint string) string {
	if strings.HasPrefix(endpoint, ":") {
		return "localhost" + endpoint
	}
	return endpoint
}

func mergeParams(dst map[string]string, src map[string]string) {
	for key, value := range src {
		dst[key] = value
	}
}

type suiteReport struct {
	Suite    string        `json:"suite"`
	Target   string        `json:"target"`
	Passed   bool          `json:"passed"`
	Total    int           `json:"total"`
	Failures int           `json:"failures"`
	Results  []checkResult `json:"results"`
}

type checkResult struct {
	Name    string `json:"name"`
	Passed  bool   `json:"passed"`
	Message string `json:"message,omitempty"`
}

func suiteReportFromConformance(target string, report conformance.Report) suiteReport {
	out := suiteReport{
		Suite:  report.Suite,
		Target: target,
		Passed: report.Passed(),
		Total:  len(report.Results),
	}
	for _, result := range report.Results {
		if !result.Passed {
			out.Failures++
		}
		out.Results = append(out.Results, checkResult{
			Name:    result.Name,
			Passed:  result.Passed,
			Message: result.Message,
		})
	}
	return out
}

func printReports(out io.Writer, opts options, reports []suiteReport) error {
	if opts.output == "json" {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		if err := enc.Encode(reports); err != nil {
			return err
		}
		if reportsPassed(reports) {
			return nil
		}
		return errConformanceFailed
	}
	for _, report := range reports {
		fmt.Fprintf(out, "%s conformance against %s\n", report.Suite, report.Target)
		for _, result := range report.Results {
			if result.Passed {
				fmt.Fprintf(out, "PASS %s\n", result.Name)
				continue
			}
			fmt.Fprintf(out, "FAIL %s: %s\n", result.Name, result.Message)
		}
		if report.Passed {
			fmt.Fprintf(out, "PASS %d checks\n", report.Total)
		} else {
			fmt.Fprintf(out, "FAIL %d/%d checks\n", report.Failures, report.Total)
		}
		fmt.Fprintln(out)
	}
	if reportsPassed(reports) {
		return nil
	}
	return errConformanceFailed
}

func reportsPassed(reports []suiteReport) bool {
	if len(reports) == 0 {
		return false
	}
	for _, report := range reports {
		if !report.Passed {
			return false
		}
	}
	return true
}

func usage(out io.Writer) {
	fmt.Fprintln(out, `Usage:
  kapro-conformance all [-o json]
  kapro-conformance actuator --endpoint localhost:9090 [--param key=value]
  kapro-conformance gate --endpoint localhost:9090 [--param key=value]
  kapro-conformance planner --endpoint localhost:9090 [--param key=value]
  kapro-conformance provider [-o json]
  kapro-conformance substrate [-o json]

Common flags:
  --timeout      overall conformance timeout, default 10s
  -o, --output   output format: json

Live endpoint suite flags:
  --endpoint     gRPC target, for example localhost:9090 or dns:///plugin.svc:9090
  --param        scenario parameter as key=value; repeat for multiple parameters
  --tls          use TLS instead of plaintext gRPC
  --ca-file      CA bundle for TLS server verification
  --server-name  TLS server name override

Notes:
  all and provider run local reference suites. KSP provider conformance is a Go
  SDK contract; KSI substrate conformance is a Go SDK contract over typed
  substrate classes. Use conformance/provider or conformance/substrate from
  custom provider/substrate tests for production implementations.`)
}

type referenceActuatorClient struct{}

func (referenceActuatorClient) GetCapabilities(context.Context, *kaiv1alpha1.GetCapabilitiesRequest, ...grpc.CallOption) (*kaiv1alpha1.GetCapabilitiesResponse, error) {
	return &kaiv1alpha1.GetCapabilitiesResponse{
		ContractVersion: "v1alpha1",
		PluginVersion:   "reference",
		Capabilities:    []string{kaiv1alpha1.CapabilityApply, kaiv1alpha1.CapabilityConvergence, kaiv1alpha1.CapabilityRollback},
	}, nil
}

func (referenceActuatorClient) Apply(ctx context.Context, _ *kaiv1alpha1.ApplyRequest, _ ...grpc.CallOption) (*kaiv1alpha1.ApplyResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &kaiv1alpha1.ApplyResponse{Accepted: true, Message: "accepted"}, nil
}

func (referenceActuatorClient) IsConverged(context.Context, *kaiv1alpha1.IsConvergedRequest, ...grpc.CallOption) (*kaiv1alpha1.IsConvergedResponse, error) {
	return &kaiv1alpha1.IsConvergedResponse{Converged: true, Message: "converged"}, nil
}

func (referenceActuatorClient) Rollback(context.Context, *kaiv1alpha1.RollbackRequest, ...grpc.CallOption) (*kaiv1alpha1.RollbackResponse, error) {
	return &kaiv1alpha1.RollbackResponse{Accepted: true, Message: "rolled back"}, nil
}

type referenceGateClient struct{}

func (referenceGateClient) GetCapabilities(context.Context, *kgiv1alpha1.GetCapabilitiesRequest, ...grpc.CallOption) (*kgiv1alpha1.GetCapabilitiesResponse, error) {
	return &kgiv1alpha1.GetCapabilitiesResponse{
		ContractVersion: "v1alpha1",
		PluginVersion:   "reference",
		Capabilities:    []string{"evaluate"},
	}, nil
}

func (referenceGateClient) Evaluate(context.Context, *kgiv1alpha1.EvaluateRequest, ...grpc.CallOption) (*kgiv1alpha1.EvaluateResponse, error) {
	return &kgiv1alpha1.EvaluateResponse{Phase: kgiv1alpha1.GatePhase_GATE_PHASE_PASSED, Message: "passed"}, nil
}

type referencePlannerClient struct{}

func (referencePlannerClient) GetCapabilities(context.Context, *kpiv1alpha1.GetCapabilitiesRequest, ...grpc.CallOption) (*kpiv1alpha1.GetCapabilitiesResponse, error) {
	return &kpiv1alpha1.GetCapabilitiesResponse{
		ContractVersion: "v1alpha1",
		PluginVersion:   "reference",
		Capabilities:    []string{"filter", "score"},
	}, nil
}

func (referencePlannerClient) Plan(ctx context.Context, req *kpiv1alpha1.PlanRequest, _ ...grpc.CallOption) (*kpiv1alpha1.PlanResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	resp := &kpiv1alpha1.PlanResponse{Targets: make([]*kpiv1alpha1.PlannedTarget, 0, len(req.GetTargets()))}
	for _, target := range req.GetTargets() {
		decision := kpiv1alpha1.PlanningDecision_PLANNING_DECISION_INCLUDE
		if !target.GetReady() {
			decision = kpiv1alpha1.PlanningDecision_PLANNING_DECISION_DEFER
		}
		resp.Targets = append(resp.Targets, &kpiv1alpha1.PlannedTarget{
			Name:     target.GetName(),
			Decision: decision,
			Score:    100,
			Reason:   "conformance",
			Message:  "planned",
		})
	}
	return resp, nil
}

type referenceProvider struct{}

func (referenceProvider) Driver() kaprov1alpha2.BackendDriver {
	return kaprov1alpha2.BackendDriverOCI
}

func (referenceProvider) Capabilities() spokeprovider.Capabilities {
	return spokeprovider.Capabilities{
		Driver:            kaprov1alpha2.BackendDriverOCI,
		SupportsReconcile: true,
		SupportsObserve:   true,
	}
}

func (referenceProvider) Reconcile(context.Context, spokeprovider.ReconcileRequest) spokeprovider.ReconcileResult {
	return spokeprovider.ReconcileResult{Phase: kaprov1alpha2.DeliveryPhaseConverged}
}

type referenceSubstrate struct {
	className string
	mode      kaprov1alpha2.ExecutionMode
}

func (s referenceSubstrate) Validate(_ context.Context, req *ksisubstrate.ValidateRequest) (*ksisubstrate.ValidateResult, error) {
	if req == nil {
		return &ksisubstrate.ValidateResult{Valid: false, Reason: "RequestMissing", Message: "request is required"}, nil
	}
	if req.Config == nil {
		return &ksisubstrate.ValidateResult{Valid: false, Reason: "ConfigMissing", Message: "config is required"}, nil
	}
	if req.Backend == nil || req.Backend.Spec.ClassRef == nil || req.Backend.Spec.ClassRef.Name != s.className {
		return &ksisubstrate.ValidateResult{Valid: false, Reason: "ClassMismatch", Message: "backend classRef does not match reference substrate"}, nil
	}
	return &ksisubstrate.ValidateResult{Valid: true, Reason: "Valid", Message: "config accepted"}, nil
}

func (s referenceSubstrate) Apply(_ context.Context, req *ksisubstrate.ApplyRequest) (*ksisubstrate.ApplyResult, error) {
	if req == nil {
		return nil, fmt.Errorf("request is nil")
	}
	return &ksisubstrate.ApplyResult{
		Accepted:       true,
		Applied:        len(req.DesiredVersions),
		Reason:         "Accepted",
		Message:        "reference substrate accepted desired versions",
		BackendObjects: referenceBackendObjects(req.DesiredVersions, kaprov1alpha2.DeliveryPhaseApplying),
	}, nil
}

func (s referenceSubstrate) Observe(_ context.Context, req *ksisubstrate.ObserveRequest) (*ksisubstrate.ObserveResult, error) {
	if req == nil {
		return nil, fmt.Errorf("request is nil")
	}
	return &ksisubstrate.ObserveResult{
		Converged:      true,
		Phase:          kaprov1alpha2.DeliveryPhaseConverged,
		Reason:         "Converged",
		Message:        "reference substrate reports convergence",
		BackendObjects: referenceBackendObjects(req.DesiredVersions, kaprov1alpha2.DeliveryPhaseConverged),
	}, nil
}

func (s referenceSubstrate) Capabilities(context.Context) (*ksisubstrate.Capabilities, error) {
	return &ksisubstrate.Capabilities{
		ContractVersion:         ksisubstrate.ContractVersionV1Alpha1,
		SupportedExecutionModes: []kaprov1alpha2.ExecutionMode{s.mode},
		Capabilities: kaprov1alpha2.SubstrateCapabilities{
			Operations: &kaprov1alpha2.SubstrateOperationCapabilities{
				Apply:   true,
				Observe: true,
				DryRun:  true,
			},
			Staging: &kaprov1alpha2.SubstrateStagingCapabilities{},
		},
	}, nil
}

func referenceSubstrateReports(ctx context.Context) []suiteReport {
	type reference struct {
		name   string
		mode   kaprov1alpha2.ExecutionMode
		config runtime.Object
	}
	refs := []reference{
		{name: "kubernetes-apply", mode: kaprov1alpha2.ExecutionModeHubPush, config: &kubernetessubstratev1alpha1.KubernetesApplyConfig{}},
		{name: "argo-cd", mode: kaprov1alpha2.ExecutionModeHubPush, config: &argocdsubstratev1alpha1.ArgoCDSubstrateConfig{}},
		{name: "flux", mode: kaprov1alpha2.ExecutionModeSpokePull, config: &fluxsubstratev1alpha1.FluxSubstrateConfig{}},
	}
	reports := make([]suiteReport, 0, len(refs))
	for _, ref := range refs {
		scenario := referenceSubstrateScenario(ref.name, ref.config)
		report := substrateconformance.Check(ctx, referenceSubstrate{className: ref.name, mode: ref.mode}, scenario)
		reports = append(reports, suiteReportFromConformance(ref.name, report))
	}
	return reports
}

func referenceSubstrateScenario(className string, config runtime.Object) substrateconformance.Scenario {
	scenario := substrateconformance.DefaultScenario()
	class := &kaprov1alpha2.SubstrateClass{ObjectMeta: metav1.ObjectMeta{Name: className}}
	backend := &kaprov1alpha2.Backend{
		ObjectMeta: metav1.ObjectMeta{Name: className},
		Spec: kaprov1alpha2.BackendSpec{
			ClassRef: &kaprov1alpha2.SubstrateClassReference{Name: className},
			ConfigRef: &kaprov1alpha2.SubstrateObjectReference{
				APIVersion: "substrate.kapro.io/v1alpha1",
				Kind:       "ReferenceConfig",
				Name:       className,
			},
		},
	}
	scenario.Validate.Class = class
	scenario.Validate.Backend = backend
	scenario.Validate.Config = config
	scenario.MissingConfigValidate.Class = class
	scenario.MissingConfigValidate.Backend = backend
	scenario.Apply.Class = class
	scenario.Apply.Backend = backend
	scenario.Apply.Config = config
	scenario.Observe.Class = class
	scenario.Observe.Backend = backend
	scenario.Observe.Config = config
	scenario.RequiredOperations = []string{"apply", "observe", "dryRun"}
	return scenario
}

func referenceBackendObjects(versions map[string]string, phase kaprov1alpha2.DeliveryPhase) []kaprov1alpha2.BackendObjectStatus {
	objects := make([]kaprov1alpha2.BackendObjectStatus, 0, len(versions))
	for app, version := range versions {
		objects = append(objects, kaprov1alpha2.BackendObjectStatus{
			Kind:           "Reference",
			Name:           app,
			DesiredVersion: version,
			CurrentVersion: version,
			Phase:          string(phase),
		})
	}
	return objects
}
