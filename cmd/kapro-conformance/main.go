package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"kapro.io/kapro/conformance"
	actuatorconformance "kapro.io/kapro/conformance/actuator"
	gateconformance "kapro.io/kapro/conformance/gate"
	plannerconformance "kapro.io/kapro/conformance/planner"
	kaiv1alpha1 "kapro.io/kapro/spec/kai/v1alpha1"
	kgiv1alpha1 "kapro.io/kapro/spec/kgi/v1alpha1"
	kpiv1alpha1 "kapro.io/kapro/spec/kpi/v1alpha1"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

var errConformanceFailed = errors.New("conformance failed")

type options struct {
	endpoint   string
	timeout    time.Duration
	params     keyValues
	tls        bool
	caFile     string
	serverName string
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
	case "actuator":
		return runActuator(args[1:], stdout)
	case "gate":
		return runGate(args[1:], stdout)
	case "planner":
		return runPlanner(args[1:], stdout)
	case "-h", "--help", "help":
		usage(stdout)
		return nil
	default:
		usage(stderr)
		return fmt.Errorf("unknown conformance suite %q", args[0])
	}
}

func runActuator(args []string, out io.Writer) error {
	opts, err := parseOptions("actuator", args)
	if err != nil {
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
	return printReport(out, opts.endpoint, report)
}

func runGate(args []string, out io.Writer) error {
	opts, err := parseOptions("gate", args)
	if err != nil {
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
	return printReport(out, opts.endpoint, report)
}

func runPlanner(args []string, out io.Writer) error {
	opts, err := parseOptions("planner", args)
	if err != nil {
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
	return printReport(out, opts.endpoint, report)
}

func parseOptions(suite string, args []string) (options, error) {
	opts := options{timeout: 10 * time.Second}
	fs := flag.NewFlagSet("kapro-conformance "+suite, flag.ContinueOnError)
	fs.StringVar(&opts.endpoint, "endpoint", "", "gRPC target, for example localhost:9090 or dns:///plugin.namespace.svc:9090")
	fs.DurationVar(&opts.timeout, "timeout", opts.timeout, "overall conformance timeout")
	fs.Var(&opts.params, "param", "scenario parameter as key=value; repeat for multiple parameters")
	fs.BoolVar(&opts.tls, "tls", false, "use TLS for the gRPC connection")
	fs.StringVar(&opts.caFile, "ca-file", "", "CA bundle for TLS server verification")
	fs.StringVar(&opts.serverName, "server-name", "", "TLS server name override")
	if err := fs.Parse(args); err != nil {
		return options{}, err
	}
	if opts.endpoint == "" {
		return options{}, fmt.Errorf("--endpoint is required")
	}
	if opts.timeout <= 0 {
		return options{}, fmt.Errorf("--timeout must be positive")
	}
	opts.endpoint = normalizeEndpoint(opts.endpoint)
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

func printReport(out io.Writer, endpoint string, report conformance.Report) error {
	fmt.Fprintf(out, "%s conformance against %s\n", report.Suite, endpoint)
	for _, result := range report.Results {
		if result.Passed {
			fmt.Fprintf(out, "PASS %s\n", result.Name)
			continue
		}
		fmt.Fprintf(out, "FAIL %s: %s\n", result.Name, result.Message)
	}
	if report.Passed() {
		fmt.Fprintf(out, "PASS %d checks\n", len(report.Results))
		return nil
	}
	fmt.Fprintf(out, "FAIL %d/%d checks\n", len(report.Failed()), len(report.Results))
	return errConformanceFailed
}

func usage(out io.Writer) {
	fmt.Fprintln(out, `Usage:
  kapro-conformance actuator --endpoint localhost:9090 [--param key=value]
  kapro-conformance gate --endpoint localhost:9090 [--param key=value]
  kapro-conformance planner --endpoint localhost:9090 [--param key=value]

Common flags:
  --endpoint     gRPC target, for example localhost:9090 or dns:///plugin.svc:9090
  --timeout      overall conformance timeout, default 10s
  --param        scenario parameter as key=value; repeat for multiple parameters
  --tls          use TLS instead of plaintext gRPC
  --ca-file      CA bundle for TLS server verification
  --server-name  TLS server name override`)
}
