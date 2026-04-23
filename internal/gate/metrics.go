package gate

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"text/template"
	"time"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
)

// MetricsGate evaluates a single PromQL expression against a Prometheus-compatible
// HTTP API. The query is expected to return a scalar or instant vector; the gate
// passes when the returned value is non-zero (i.e. the expression is truthy).
//
// For example, to assert checkout success rate > 99.5%:
//
//	query: 'sum(rate(checkout_success_total[5m])) / sum(rate(checkout_total[5m])) > 0.995'
//
// A non-zero scalar result means the condition holds; 0 (or an empty result)
// means the gate is blocked.
//
// The PrometheusURL is derived from MetricGate.Endpoint. When empty, the gate
// falls back to the in-cluster Prometheus default
// http://prometheus-operated.monitoring.svc:9090.
type MetricsGate struct {
	// HTTPClient is used for Prometheus API calls. Defaults to a 10-second
	// timeout client when nil.
	HTTPClient *http.Client
}

const defaultPrometheusURL = "http://prometheus-operated.monitoring.svc:9090"

func (g *MetricsGate) httpClient() *http.Client {
	if g.HTTPClient != nil {
		return g.HTTPClient
	}
	return &http.Client{Timeout: 10 * time.Second}
}

const defaultWindow   = "5m"
const defaultInterval = "30s"
const minInterval     = 10 * time.Second

// retryAfter returns the poll interval for a metric gate, clamped to minInterval.
func retryAfter(metric kaprov1alpha1.MetricGate) string {
	iv := strings.TrimSpace(metric.Interval)
	if iv == "" {
		return defaultInterval
	}
	d, err := time.ParseDuration(iv)
	if err != nil || d < minInterval {
		return defaultInterval
	}
	return iv
}

// resolveQuery substitutes {{.Window}} in the query template with the
// configured window (defaulting to defaultWindow).
func resolveQuery(metric kaprov1alpha1.MetricGate) (string, error) {
	w := strings.TrimSpace(metric.Window)
	if w == "" {
		w = defaultWindow
	}
	// Fast path: no template markers — return as-is.
	if !strings.Contains(metric.Query, "{{") {
		return metric.Query, nil
	}
	tmpl, err := template.New("query").Parse(metric.Query)
	if err != nil {
		return "", fmt.Errorf("parse query template: %w", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, map[string]string{"Window": w}); err != nil {
		return "", fmt.Errorf("execute query template: %w", err)
	}
	return buf.String(), nil
}

// Evaluate queries the Prometheus instant-query endpoint for the metric at
// req.MetricIndex. Returns Passed when the query yields a non-zero value.
// The poll interval is controlled by MetricGate.Interval (default 30s, min 10s).
// The query window is injected via {{.Window}} template substitution using MetricGate.Window.
func (g *MetricsGate) Evaluate(ctx context.Context, req Request) (Result, error) {
	if req.Policy == nil || req.MetricIndex >= len(req.Policy.Gate.Metrics) {
		return Result{Phase: kaprov1alpha1.GatePhasePassed, Message: "no metrics configured"}, nil
	}

	metric := req.Policy.Gate.Metrics[req.MetricIndex]
	interval := retryAfter(metric)

	baseURL := defaultPrometheusURL
	if metric.Endpoint != "" {
		baseURL = metric.Endpoint
	}

	query, err := resolveQuery(metric)
	if err != nil {
		return Result{
			Phase:      kaprov1alpha1.GatePhaseInconclusive,
			Message:    fmt.Sprintf("query template error: %v", err),
			RetryAfter: interval,
		}, nil
	}

	passed, val, err := g.queryInstant(ctx, baseURL, query)
	if err != nil {
		return Result{
			Phase:      kaprov1alpha1.GatePhaseInconclusive,
			Message:    fmt.Sprintf("prometheus query error: %v", err),
			RetryAfter: interval,
		}, nil // don't propagate — retry is safer than blocking the pipeline
	}

	if passed {
		return Result{
			Phase:   kaprov1alpha1.GatePhasePassed,
			Message: fmt.Sprintf("metric query passed (value=%.4f): %s", val, query),
		}, nil
	}

	return Result{
		Phase:      kaprov1alpha1.GatePhaseInconclusive,
		Message:    fmt.Sprintf("metric gate blocked (value=%.4f, interval=%s): %s", val, interval, query),
		RetryAfter: interval,
	}, nil
}

// prometheusInstantResponse is the subset of the Prometheus HTTP API v1 response
// needed to extract a scalar or instant-vector result.
type prometheusInstantResponse struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string          `json:"resultType"`
		Result     json.RawMessage `json:"result"` // type depends on resultType
	} `json:"data"`
}

// vectorResult is a single series entry for resultType=vector.
type vectorResult struct {
	Value []json.RawMessage `json:"value"` // [timestamp, "value"]
}

// queryInstant calls /api/v1/query and returns (passed, value, error).
// passed is true when the query returns a result with value > 0.
func (g *MetricsGate) queryInstant(ctx context.Context, baseURL, query string) (bool, float64, error) {
	endpoint := fmt.Sprintf("%s/api/v1/query", baseURL)

	params := url.Values{}
	params.Set("query", query)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		endpoint+"?"+params.Encode(), nil)
	if err != nil {
		return false, 0, fmt.Errorf("build request: %w", err)
	}

	resp, err := g.httpClient().Do(req)
	if err != nil {
		return false, 0, fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return false, 0, fmt.Errorf("read body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return false, 0, fmt.Errorf("prometheus returned %d: %s", resp.StatusCode, body)
	}

	var pr prometheusInstantResponse
	if err := json.Unmarshal(body, &pr); err != nil {
		return false, 0, fmt.Errorf("unmarshal: %w", err)
	}

	if pr.Status != "success" {
		return false, 0, fmt.Errorf("prometheus status: %s", pr.Status)
	}

	switch pr.Data.ResultType {
	case "vector":
		var results []vectorResult
		if err := json.Unmarshal(pr.Data.Result, &results); err != nil {
			return false, 0, fmt.Errorf("unmarshal vector: %w", err)
		}
		if len(results) == 0 {
			// Empty result → condition not satisfied
			return false, 0, nil
		}
		if len(results[0].Value) < 2 {
			return false, 0, fmt.Errorf("unexpected value array length")
		}
		val, err := parsePromValue(results[0].Value[1])
		if err != nil {
			return false, 0, err
		}
		return val != 0, val, nil

	case "scalar":
		// scalar result is [timestamp, "value"]
		var scalarPair []json.RawMessage
		if err := json.Unmarshal(pr.Data.Result, &scalarPair); err != nil {
			return false, 0, fmt.Errorf("unmarshal scalar: %w", err)
		}
		if len(scalarPair) < 2 {
			return false, 0, fmt.Errorf("unexpected scalar array length")
		}
		val, err := parsePromValue(scalarPair[1])
		if err != nil {
			return false, 0, err
		}
		return val != 0, val, nil

	default:
		return false, 0, fmt.Errorf("unsupported resultType: %s", pr.Data.ResultType)
	}
}

func parsePromValue(raw json.RawMessage) (float64, error) {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return 0, fmt.Errorf("parse prom value: %w", err)
	}
	return strconv.ParseFloat(s, 64)
}
