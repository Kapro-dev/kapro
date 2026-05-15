package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	kgiv1alpha1 "kapro.io/kapro/spec/kgi/v1alpha1"

	"google.golang.org/grpc"
)

const (
	contractVersion = "v1alpha1"
	pluginVersion   = "0.1.0"

	defaultListenAddr = ":9090"
	defaultOperator   = "lte"
	defaultProvider   = "static"
)

type sloGateServer struct {
	kgiv1alpha1.UnimplementedGateServiceServer

	httpClient *http.Client
}

func (s *sloGateServer) GetCapabilities(context.Context, *kgiv1alpha1.GetCapabilitiesRequest) (*kgiv1alpha1.GetCapabilitiesResponse, error) {
	return &kgiv1alpha1.GetCapabilitiesResponse{
		ContractVersion: contractVersion,
		PluginVersion:   pluginVersion,
		Capabilities: []string{
			"slo.static-threshold",
			"slo.prometheus-instant-query",
			"slo.operators.lt-lte-gt-gte-eq",
		},
	}, nil
}

func (s *sloGateServer) Evaluate(ctx context.Context, req *kgiv1alpha1.EvaluateRequest) (*kgiv1alpha1.EvaluateResponse, error) {
	params := req.GetParameters()
	threshold, err := parseRequiredFloat(params, "threshold")
	if err != nil {
		return inconclusive(err.Error()), nil
	}
	operator := firstNonEmpty(params["operator"], defaultOperator)
	value, phase, message, err := s.value(ctx, params)
	if err != nil {
		return nil, err
	}
	if phase != kgiv1alpha1.GatePhase_GATE_PHASE_UNSPECIFIED {
		return &kgiv1alpha1.EvaluateResponse{Phase: phase, Message: message}, nil
	}
	passed, err := compare(value, threshold, operator)
	if err != nil {
		return inconclusive(err.Error()), nil
	}
	metric := firstNonEmpty(params["metric"], req.GetGate(), "slo")
	resultMessage := fmt.Sprintf("%s value %.6g %s threshold %.6g", metric, value, operator, threshold)
	if passed {
		return &kgiv1alpha1.EvaluateResponse{Phase: kgiv1alpha1.GatePhase_GATE_PHASE_PASSED, Message: resultMessage}, nil
	}
	return &kgiv1alpha1.EvaluateResponse{Phase: kgiv1alpha1.GatePhase_GATE_PHASE_FAILED, Message: resultMessage}, nil
}

func (s *sloGateServer) value(ctx context.Context, params map[string]string) (float64, kgiv1alpha1.GatePhase, string, error) {
	provider := strings.ToLower(firstNonEmpty(params["provider"], defaultProvider))
	switch provider {
	case "static":
		value, err := parseRequiredFloat(params, "value")
		if err != nil {
			return 0, kgiv1alpha1.GatePhase_GATE_PHASE_INCONCLUSIVE, err.Error(), nil
		}
		return value, kgiv1alpha1.GatePhase_GATE_PHASE_UNSPECIFIED, "", nil
	case "prometheus":
		if firstNonEmpty(params["prometheusURL"], params["url"]) == "" || strings.TrimSpace(params["query"]) == "" {
			return 0, kgiv1alpha1.GatePhase_GATE_PHASE_INCONCLUSIVE, "prometheusURL and query are required for prometheus provider", nil
		}
		value, ok, err := s.queryPrometheus(ctx, params)
		if err != nil {
			return 0, kgiv1alpha1.GatePhase_GATE_PHASE_UNSPECIFIED, "", err
		}
		if !ok {
			return 0, kgiv1alpha1.GatePhase_GATE_PHASE_RUNNING, "metric value is not available yet", nil
		}
		return value, kgiv1alpha1.GatePhase_GATE_PHASE_UNSPECIFIED, "", nil
	default:
		return 0, kgiv1alpha1.GatePhase_GATE_PHASE_INCONCLUSIVE, fmt.Sprintf("unsupported provider %q", provider), nil
	}
}

func (s *sloGateServer) queryPrometheus(ctx context.Context, params map[string]string) (float64, bool, error) {
	baseURL := firstNonEmpty(params["prometheusURL"], params["url"])
	query := params["query"]
	if baseURL == "" || query == "" {
		return 0, false, fmt.Errorf("prometheusURL and query are required for prometheus provider")
	}
	endpoint, err := url.Parse(strings.TrimRight(baseURL, "/") + "/api/v1/query")
	if err != nil {
		return 0, false, fmt.Errorf("parse prometheusURL: %w", err)
	}
	q := endpoint.Query()
	q.Set("query", query)
	endpoint.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return 0, false, fmt.Errorf("create prometheus request: %w", err)
	}
	resp, err := s.http().Do(req)
	if err != nil {
		return 0, false, fmt.Errorf("query prometheus: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return 0, false, fmt.Errorf("prometheus returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var result prometheusQueryResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return 0, false, fmt.Errorf("decode prometheus response: %w", err)
	}
	if result.Status != "success" {
		if result.Error != "" {
			return 0, false, fmt.Errorf("prometheus query status %q: %s: %s", result.Status, result.ErrorType, result.Error)
		}
		return 0, false, fmt.Errorf("prometheus query status %q", result.Status)
	}
	return prometheusValue(result)
}

func (s *sloGateServer) http() *http.Client {
	if s.httpClient != nil {
		return s.httpClient
	}
	return &http.Client{Timeout: 10 * time.Second}
}

type prometheusQueryResponse struct {
	Status    string `json:"status"`
	ErrorType string `json:"errorType,omitempty"`
	Error     string `json:"error,omitempty"`
	Data      struct {
		ResultType string          `json:"resultType"`
		Result     json.RawMessage `json:"result"`
	} `json:"data"`
}

type prometheusResult struct {
	Value []any `json:"value"`
}

func prometheusValue(resp prometheusQueryResponse) (float64, bool, error) {
	switch resp.Data.ResultType {
	case "vector":
		var results []prometheusResult
		if err := json.Unmarshal(resp.Data.Result, &results); err != nil {
			return 0, false, fmt.Errorf("decode prometheus vector result: %w", err)
		}
		if len(results) == 0 {
			return 0, false, nil
		}
		if len(results) > 1 {
			return 0, false, fmt.Errorf("prometheus query returned %d series; expected exactly one series", len(results))
		}
		return parsePrometheusSampleValue(results[0].Value)
	case "scalar":
		var value []any
		if err := json.Unmarshal(resp.Data.Result, &value); err != nil {
			return 0, false, fmt.Errorf("decode prometheus scalar result: %w", err)
		}
		return parsePrometheusSampleValue(value)
	default:
		return 0, false, fmt.Errorf("unsupported prometheus resultType %q", resp.Data.ResultType)
	}
}

func parsePrometheusSampleValue(value []any) (float64, bool, error) {
	if len(value) < 2 {
		return 0, false, fmt.Errorf("prometheus result value is missing")
	}
	raw, ok := value[1].(string)
	if !ok {
		return 0, false, fmt.Errorf("prometheus result value is not a string")
	}
	parsed, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, false, fmt.Errorf("parse prometheus value: %w", err)
	}
	if math.IsNaN(parsed) || math.IsInf(parsed, 0) {
		return 0, false, fmt.Errorf("prometheus value must be finite")
	}
	return parsed, true, nil
}

func parseRequiredFloat(params map[string]string, key string) (float64, error) {
	raw := strings.TrimSpace(params[key])
	if raw == "" {
		return 0, fmt.Errorf("%s is required", key)
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", key, err)
	}
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, fmt.Errorf("%s must be finite", key)
	}
	return value, nil
}

func compare(value, threshold float64, operator string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(operator)) {
	case "lt", "<":
		return value < threshold, nil
	case "lte", "<=":
		return value <= threshold, nil
	case "gt", ">":
		return value > threshold, nil
	case "gte", ">=":
		return value >= threshold, nil
	case "eq", "==":
		return value == threshold, nil
	default:
		return false, fmt.Errorf("unsupported operator %q", operator)
	}
}

func inconclusive(message string) *kgiv1alpha1.EvaluateResponse {
	return &kgiv1alpha1.EvaluateResponse{
		Phase:   kgiv1alpha1.GatePhase_GATE_PHASE_INCONCLUSIVE,
		Message: message,
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
	kgiv1alpha1.RegisterGateServiceServer(grpcServer, &sloGateServer{})
	log.Printf("slo gate plugin listening on %s", *listenAddr)
	if err := grpcServer.Serve(listener); err != nil {
		log.Fatalf("serve grpc: %v", err)
	}
}
