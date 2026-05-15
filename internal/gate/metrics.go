package gate

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"text/template"
	"time"

	kaprov1alpha1 "kapro.io/kapro/api/v1alpha1"
	"kapro.io/kapro/internal/gate/statistics"
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

const defaultWindow = "5m"
const defaultInterval = "30s"
const minInterval = 10 * time.Second
const defaultSequentialConfidence = 0.95

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

func metricWindow(metric kaprov1alpha1.MetricGate) string {
	w := strings.TrimSpace(metric.Window)
	if w == "" {
		return defaultWindow
	}
	return w
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
			Phase:   kaprov1alpha1.GatePhaseFailed,
			Message: fmt.Sprintf("query template error: %v", err),
			Evidence: []Evidence{metricEvidence(metric, query, "", 0, 0, "invalid query template",
				kaprov1alpha1.GatePhaseFailed)},
		}, nil
	}

	analysis := metricAnalysis(metric)
	switch analysis.Mode {
	case "baseline":
		return g.evaluateBaseline(ctx, baseURL, metric, analysis, query, interval)
	case "sequential":
		return g.evaluateSequential(ctx, baseURL, metric, analysis, query, interval)
	case "changePoint":
		return g.evaluateChangePoint(ctx, baseURL, metric, analysis, query, interval)
	case "score":
		return g.evaluateScore(ctx, baseURL, metric, analysis, query, interval)
	case "sloBurnRate", "threshold":
		return g.evaluateInstant(ctx, baseURL, metric, analysis, query, interval)
	default:
		return Result{
			Phase:   kaprov1alpha1.GatePhaseFailed,
			Message: fmt.Sprintf("unsupported metric analysis mode %q", analysis.Mode),
			Evidence: []Evidence{metricEvidence(metric, query, "", 0, 0, "unsupported analysis mode",
				kaprov1alpha1.GatePhaseFailed)},
		}, nil
	}
}

func (g *MetricsGate) evaluateInstant(ctx context.Context, baseURL string, metric kaprov1alpha1.MetricGate, analysis metricAnalysisConfig, query, interval string) (Result, error) {
	ok, val, err := g.queryInstant(ctx, baseURL, query)
	if err != nil {
		return Result{
			Phase:      kaprov1alpha1.GatePhaseInconclusive,
			Message:    fmt.Sprintf("prometheus query error: %v", err),
			RetryAfter: interval,
			Evidence: []Evidence{metricEvidence(metric, query, "", 0, 0, err.Error(),
				kaprov1alpha1.GatePhaseInconclusive)},
		}, nil // don't propagate — retry is safer than blocking the pipeline
	}
	if !ok {
		return noInstantDataResult(metric, query, "", interval, "prometheus query returned no series"), nil
	}

	threshold := analysis.threshold
	if !compare(val, threshold, analysis.comparator) {
		return Result{
			Phase:      kaprov1alpha1.GatePhaseInconclusive,
			Message:    fmt.Sprintf("metric gate blocked (mode=%s, value=%.4f, comparator=%s, threshold=%.4f, interval=%s): %s", analysis.Mode, val, analysis.comparator, threshold, interval, query),
			RetryAfter: interval,
			Evidence: []Evidence{metricEvidence(metric, query, "", val, 0,
				fmt.Sprintf("value %.4f did not satisfy %s %.4f", val, analysis.comparator, threshold),
				kaprov1alpha1.GatePhaseInconclusive)},
		}, nil
	}

	return Result{
		Phase:   kaprov1alpha1.GatePhasePassed,
		Message: fmt.Sprintf("metric query passed (mode=%s, value=%.4f, comparator=%s, threshold=%.4f): %s", analysis.Mode, val, analysis.comparator, threshold, query),
		Evidence: []Evidence{metricEvidence(metric, query, "", val, 0,
			fmt.Sprintf("value %.4f satisfied %s %.4f", val, analysis.comparator, threshold),
			kaprov1alpha1.GatePhasePassed)},
	}, nil
}

func (g *MetricsGate) evaluateBaseline(ctx context.Context, baseURL string, metric kaprov1alpha1.MetricGate, analysis metricAnalysisConfig, query, interval string) (Result, error) {
	if strings.TrimSpace(analysis.baselineQuery) == "" {
		return Result{
			Phase:   kaprov1alpha1.GatePhaseFailed,
			Message: "baseline metric analysis requires analysis.baselineQuery",
			Evidence: []Evidence{metricEvidence(metric, query, "", 0, 0, "missing baseline query",
				kaprov1alpha1.GatePhaseFailed)},
		}, nil
	}

	if analysis.baselineHealthQuery != "" {
		healthy, err := g.baselineHealthy(ctx, baseURL, analysis.baselineHealthQuery)
		if err != nil {
			return Result{
				Phase:      kaprov1alpha1.GatePhaseInconclusive,
				Message:    fmt.Sprintf("prometheus baseline health query error: %v", err),
				RetryAfter: interval,
				Evidence: []Evidence{metricEvidence(metric, query, analysis.baselineQuery, 0, 0, err.Error(),
					kaprov1alpha1.GatePhaseInconclusive)},
			}, nil
		}
		if !healthy {
			return Result{
				Phase:      kaprov1alpha1.GatePhaseInconclusive,
				Message:    "baseline is not healthy; refusing baseline comparison",
				RetryAfter: interval,
				Evidence: []Evidence{metricEvidenceWithStats(metric, query, analysis.baselineQuery, 0, 0, 1, nil,
					"baseline health query returned false", kaprov1alpha1.GatePhaseInconclusive, statEvidence{BaselineHealthy: ptrBool(false)})},
			}, nil
		}
	}

	ok, val, err := g.queryInstant(ctx, baseURL, query)
	if err != nil {
		return Result{
			Phase:      kaprov1alpha1.GatePhaseInconclusive,
			Message:    fmt.Sprintf("prometheus query error: %v", err),
			RetryAfter: interval,
			Evidence: []Evidence{metricEvidence(metric, query, analysis.baselineQuery, 0, 0, err.Error(),
				kaprov1alpha1.GatePhaseInconclusive)},
		}, nil
	}
	if !ok {
		return noInstantDataResult(metric, query, analysis.baselineQuery, interval, "prometheus query returned no series"), nil
	}
	ok, baseline, err := g.queryInstant(ctx, baseURL, analysis.baselineQuery)
	if err != nil {
		return Result{
			Phase:      kaprov1alpha1.GatePhaseInconclusive,
			Message:    fmt.Sprintf("prometheus baseline query error: %v", err),
			RetryAfter: interval,
			Evidence: []Evidence{metricEvidence(metric, query, analysis.baselineQuery, val, 0, err.Error(),
				kaprov1alpha1.GatePhaseInconclusive)},
		}, nil
	}
	if !ok {
		return noInstantDataResult(metric, query, analysis.baselineQuery, interval, "prometheus baseline query returned no series"), nil
	}
	if baseline <= 0 {
		return Result{
			Phase:      kaprov1alpha1.GatePhaseInconclusive,
			Message:    fmt.Sprintf("baseline metric is not positive (baseline=%.4f)", baseline),
			RetryAfter: interval,
			Evidence: []Evidence{metricEvidence(metric, query, analysis.baselineQuery, val, baseline,
				"baseline must be positive for ratio analysis", kaprov1alpha1.GatePhaseInconclusive)},
		}, nil
	}

	ratio := val / baseline
	if !compare(ratio, analysis.threshold, analysis.comparator) {
		return Result{
			Phase:      kaprov1alpha1.GatePhaseInconclusive,
			Message:    fmt.Sprintf("baseline metric blocked (value=%.4f, baseline=%.4f, ratio=%.4f, comparator=%s, threshold=%.4f)", val, baseline, ratio, analysis.comparator, analysis.threshold),
			RetryAfter: interval,
			Evidence: []Evidence{metricEvidence(metric, query, analysis.baselineQuery, ratio, baseline,
				fmt.Sprintf("ratio %.4f did not satisfy %s %.4f", ratio, analysis.comparator, analysis.threshold),
				kaprov1alpha1.GatePhaseInconclusive)},
		}, nil
	}

	return Result{
		Phase:   kaprov1alpha1.GatePhasePassed,
		Message: fmt.Sprintf("baseline metric passed (value=%.4f, baseline=%.4f, ratio=%.4f, comparator=%s, threshold=%.4f)", val, baseline, ratio, analysis.comparator, analysis.threshold),
		Evidence: []Evidence{metricEvidence(metric, query, analysis.baselineQuery, ratio, baseline,
			fmt.Sprintf("ratio %.4f satisfied %s %.4f", ratio, analysis.comparator, analysis.threshold),
			kaprov1alpha1.GatePhasePassed)},
	}, nil
}

func (g *MetricsGate) evaluateSequential(ctx context.Context, baseURL string, metric kaprov1alpha1.MetricGate, analysis metricAnalysisConfig, query, interval string) (Result, error) {
	values, err := g.queryRange(ctx, baseURL, query, metricWindow(metric), interval)
	if err != nil {
		return Result{
			Phase:      kaprov1alpha1.GatePhaseInconclusive,
			Message:    fmt.Sprintf("prometheus range query error: %v", err),
			RetryAfter: interval,
			Evidence: []Evidence{metricEvidence(metric, query, "", 0, 0, err.Error(),
				kaprov1alpha1.GatePhaseInconclusive)},
		}, nil
	}

	minSamples := analysis.minSamples
	if minSamples <= 0 {
		minSamples = 5
	}
	if int64(len(values)) < minSamples {
		return Result{
			Phase:      kaprov1alpha1.GatePhaseInconclusive,
			Message:    fmt.Sprintf("sequential metric analysis needs more samples (samples=%d, minSamples=%d)", len(values), minSamples),
			RetryAfter: interval,
			Evidence: []Evidence{metricEvidenceWithConfidence(metric, query, "", statistics.Mean(values), 0, len(values), nil,
				fmt.Sprintf("waiting for at least %d samples", minSamples), kaprov1alpha1.GatePhaseInconclusive)},
		}, nil
	}

	test := statistics.OneSample(values, analysis.threshold)
	confidence := test.Confidence
	if confidence < analysis.confidenceThreshold && (analysis.maxSamples <= 0 || int64(len(values)) < analysis.maxSamples) {
		return Result{
			Phase:      kaprov1alpha1.GatePhaseInconclusive,
			Message:    fmt.Sprintf("sequential metric analysis is not confident yet (mean=%.4f, pValue=%.4f, confidence=%.3f, required=%.3f)", test.Mean, test.PValue, confidence, analysis.confidenceThreshold),
			RetryAfter: interval,
			Evidence: []Evidence{metricEvidenceWithStats(metric, query, "", test.Mean, 0, len(values), &confidence,
				"confidence below threshold", kaprov1alpha1.GatePhaseInconclusive, statEvidence{
					Alpha: analysis.alpha, PValue: &test.PValue, EffectSize: test.EffectSize,
					DecisionRule: "one-sample threshold test",
				})},
		}, nil
	}

	phase := kaprov1alpha1.GatePhasePassed
	reason := fmt.Sprintf("mean %.4f satisfied %s %.4f with confidence %.3f", test.Mean, analysis.comparator, analysis.threshold, confidence)
	if !compare(test.Mean, analysis.threshold, analysis.comparator) {
		phase = kaprov1alpha1.GatePhaseFailed
		reason = fmt.Sprintf("mean %.4f did not satisfy %s %.4f with confidence %.3f", test.Mean, analysis.comparator, analysis.threshold, confidence)
	}
	result := Result{
		Phase:   phase,
		Message: fmt.Sprintf("sequential metric analysis %s (mean=%.4f, samples=%d, pValue=%.4f, confidence=%.3f)", strings.ToLower(string(phase)), test.Mean, len(values), test.PValue, confidence),
		Evidence: []Evidence{metricEvidenceWithStats(metric, query, "", test.Mean, 0, len(values), &confidence,
			reason, phase, statEvidence{
				Alpha: analysis.alpha, PValue: &test.PValue, EffectSize: test.EffectSize,
				DecisionRule: "one-sample threshold test",
			})},
	}
	if phase == kaprov1alpha1.GatePhaseFailed {
		result.RetryAfter = "0"
	}
	return result, nil
}

func (g *MetricsGate) evaluateChangePoint(ctx context.Context, baseURL string, metric kaprov1alpha1.MetricGate, analysis metricAnalysisConfig, query, interval string) (Result, error) {
	values, err := g.queryRange(ctx, baseURL, query, metricWindow(metric), interval)
	if err != nil {
		return Result{
			Phase:      kaprov1alpha1.GatePhaseInconclusive,
			Message:    fmt.Sprintf("prometheus range query error: %v", err),
			RetryAfter: interval,
			Evidence: []Evidence{metricEvidence(metric, query, "", 0, 0, err.Error(),
				kaprov1alpha1.GatePhaseInconclusive)},
		}, nil
	}
	minSamples := analysis.minSamples
	if minSamples <= 0 {
		minSamples = 8
	}
	if int64(len(values)) < minSamples {
		return Result{
			Phase:      kaprov1alpha1.GatePhaseInconclusive,
			Message:    fmt.Sprintf("change-point analysis needs more samples (samples=%d, minSamples=%d)", len(values), minSamples),
			RetryAfter: interval,
			Evidence: []Evidence{metricEvidenceWithStats(metric, query, "", statistics.Mean(values), 0, len(values), nil,
				"waiting for enough samples", kaprov1alpha1.GatePhaseInconclusive, statEvidence{
					Alpha: analysis.alpha, DecisionRule: "split-window change-point test",
				})},
		}, nil
	}
	test := statistics.ChangePoint(values)
	confidence := test.Confidence
	if test.PValue > analysis.alphaValue && (analysis.maxSamples <= 0 || int64(len(values)) < analysis.maxSamples) {
		return Result{
			Phase:      kaprov1alpha1.GatePhaseInconclusive,
			Message:    fmt.Sprintf("change-point analysis has no significant shift yet (pValue=%.4f, alpha=%.4f)", test.PValue, analysis.alphaValue),
			RetryAfter: interval,
			Evidence: []Evidence{metricEvidenceWithStats(metric, query, "", test.Mean, 0, len(values), &confidence,
				"no significant change point yet", kaprov1alpha1.GatePhaseInconclusive, statEvidence{
					Alpha: analysis.alpha, PValue: &test.PValue, EffectSize: test.EffectSize,
					DecisionRule: "split-window change-point test",
				})},
		}, nil
	}
	phase := kaprov1alpha1.GatePhasePassed
	reason := "significant change is not a regression for the configured comparator"
	if changeIsRegression(test.EffectSize, analysis.comparator) {
		phase = kaprov1alpha1.GatePhaseFailed
		reason = "significant change point indicates regression"
	}
	result := Result{
		Phase:   phase,
		Message: fmt.Sprintf("change-point metric analysis %s (mean=%.4f, samples=%d, pValue=%.4f, effectSize=%.4f)", strings.ToLower(string(phase)), test.Mean, len(values), test.PValue, test.EffectSize),
		Evidence: []Evidence{metricEvidenceWithStats(metric, query, "", test.Mean, 0, len(values), &confidence,
			reason, phase, statEvidence{
				Alpha: analysis.alpha, PValue: &test.PValue, EffectSize: test.EffectSize,
				DecisionRule: "split-window change-point test",
			})},
	}
	if phase == kaprov1alpha1.GatePhaseFailed {
		result.RetryAfter = "0"
	}
	return result, nil
}

func (g *MetricsGate) evaluateScore(ctx context.Context, baseURL string, metric kaprov1alpha1.MetricGate, analysis metricAnalysisConfig, query, interval string) (Result, error) {
	if analysis.baselineHealthQuery != "" {
		healthy, err := g.baselineHealthy(ctx, baseURL, analysis.baselineHealthQuery)
		if err != nil {
			return Result{
				Phase:      kaprov1alpha1.GatePhaseInconclusive,
				Message:    fmt.Sprintf("prometheus baseline health query error: %v", err),
				RetryAfter: interval,
				Evidence: []Evidence{metricEvidence(metric, query, analysis.baselineQuery, 0, 0, err.Error(),
					kaprov1alpha1.GatePhaseInconclusive)},
			}, nil
		}
		if !healthy {
			return Result{
				Phase:      kaprov1alpha1.GatePhaseInconclusive,
				Message:    "baseline is not healthy; refusing score analysis",
				RetryAfter: interval,
				Evidence: []Evidence{metricEvidenceWithStats(metric, query, analysis.baselineQuery, 0, 0, 1, nil,
					"baseline health query returned false", kaprov1alpha1.GatePhaseInconclusive, statEvidence{
						BaselineHealthy: ptrBool(false), DecisionRule: "baseline health precondition",
					})},
			}, nil
		}
	}
	ok, val, err := g.queryInstant(ctx, baseURL, query)
	if err != nil {
		return Result{
			Phase:      kaprov1alpha1.GatePhaseInconclusive,
			Message:    fmt.Sprintf("prometheus query error: %v", err),
			RetryAfter: interval,
			Evidence: []Evidence{metricEvidence(metric, query, "", 0, 0, err.Error(),
				kaprov1alpha1.GatePhaseInconclusive)},
		}, nil
	}
	if !ok {
		return noInstantDataResult(metric, query, analysis.baselineQuery, interval, "prometheus query returned no series"), nil
	}
	score := statistics.Score(val, analysis.threshold, analysis.comparator)
	if score < analysis.scoreThreshold {
		return Result{
			Phase:      kaprov1alpha1.GatePhaseInconclusive,
			Message:    fmt.Sprintf("metric score blocked (score=%.1f, required=%.1f, value=%.4f)", score, analysis.scoreThreshold, val),
			RetryAfter: interval,
			Evidence: []Evidence{metricEvidenceWithStats(metric, query, analysis.baselineQuery, val, 0, 1, nil,
				"score below threshold", kaprov1alpha1.GatePhaseInconclusive, statEvidence{
					Score: &score, DecisionRule: "single-metric canary score",
				})},
		}, nil
	}
	return Result{
		Phase:   kaprov1alpha1.GatePhasePassed,
		Message: fmt.Sprintf("metric score passed (score=%.1f, required=%.1f, value=%.4f)", score, analysis.scoreThreshold, val),
		Evidence: []Evidence{metricEvidenceWithStats(metric, query, analysis.baselineQuery, val, 0, 1, nil,
			"score satisfied threshold", kaprov1alpha1.GatePhasePassed, statEvidence{
				Score: &score, DecisionRule: "single-metric canary score",
			})},
	}, nil
}

func metricThreshold(metric kaprov1alpha1.MetricGate) float64 {
	if metric.Threshold == nil {
		return 0
	}
	return *metric.Threshold
}

type metricAnalysisConfig struct {
	Mode                string
	comparator          string
	threshold           float64
	baselineQuery       string
	baselineHealthQuery string
	minSamples          int64
	maxSamples          int64
	confidenceThreshold float64
	alpha               *float64
	alphaValue          float64
	scoreThreshold      float64
}

func metricAnalysis(metric kaprov1alpha1.MetricGate) metricAnalysisConfig {
	mode := "threshold"
	comparator := ""
	minSamples := int64(0)
	confidenceThreshold := defaultSequentialConfidence
	alphaValue := 0.05
	scoreThreshold := 95.0
	baselineQuery := ""
	baselineHealthQuery := ""
	maxSamples := int64(0)
	alpha := &alphaValue
	if metric.Analysis != nil {
		if metric.Analysis.Mode != "" {
			mode = metric.Analysis.Mode
		}
		comparator = metric.Analysis.Comparator
		baselineQuery = metric.Analysis.BaselineQuery
		baselineHealthQuery = metric.Analysis.BaselineHealthQuery
		minSamples = int64(metric.Analysis.MinSamples)
		maxSamples = int64(metric.Analysis.MaxSamples)
		if metric.Analysis.ConfidenceThreshold != nil {
			confidenceThreshold = *metric.Analysis.ConfidenceThreshold
		}
		if metric.Analysis.Alpha != nil {
			alphaValue = *metric.Analysis.Alpha
			alpha = metric.Analysis.Alpha
		}
		if metric.Analysis.ScoreThreshold != nil {
			scoreThreshold = *metric.Analysis.ScoreThreshold
		}
	}
	if comparator == "" {
		switch mode {
		case "sloBurnRate", "baseline", "score":
			comparator = "lte"
		default:
			comparator = "gt"
		}
	}
	threshold := metricThreshold(metric)
	if metric.Threshold == nil && mode == "baseline" {
		threshold = 1.0
	}
	return metricAnalysisConfig{
		Mode:                mode,
		comparator:          comparator,
		threshold:           threshold,
		baselineQuery:       baselineQuery,
		baselineHealthQuery: baselineHealthQuery,
		minSamples:          minSamples,
		maxSamples:          maxSamples,
		confidenceThreshold: confidenceThreshold,
		alpha:               alpha,
		alphaValue:          alphaValue,
		scoreThreshold:      scoreThreshold,
	}
}

func compare(value, threshold float64, comparator string) bool {
	switch comparator {
	case "gte":
		return value >= threshold
	case "lt":
		return value < threshold
	case "lte":
		return value <= threshold
	default:
		return value > threshold
	}
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

type matrixResult struct {
	Values [][]json.RawMessage `json:"values"` // [[timestamp, "value"], ...]
}

// queryInstant calls /api/v1/query and returns (hasSample, value, error).
// hasSample is false only when Prometheus returns no scalar/vector sample.
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
	defer func() { _ = resp.Body.Close() }()

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
		if len(results) > 1 {
			// Ambiguous: multiple series returned. The gate cannot make a safe decision
			// without knowing which series to evaluate. Callers should tighten their
			// PromQL selector so it returns exactly one series.
			return false, 0, fmt.Errorf("prometheus query returned %d series — use a more specific selector to return exactly one series", len(results))
		}
		if len(results[0].Value) < 2 {
			return false, 0, fmt.Errorf("unexpected value array length")
		}
		val, err := parsePromValue(results[0].Value[1])
		if err != nil {
			return false, 0, err
		}
		return true, val, nil

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
		return true, val, nil

	default:
		return false, 0, fmt.Errorf("unsupported resultType: %s", pr.Data.ResultType)
	}
}

func noInstantDataResult(metric kaprov1alpha1.MetricGate, query, baselineQuery, interval, reason string) Result {
	return Result{
		Phase:      kaprov1alpha1.GatePhaseInconclusive,
		Message:    reason,
		RetryAfter: interval,
		Evidence: []Evidence{metricEvidenceWithStats(metric, query, baselineQuery, 0, 0, 0, nil,
			reason, kaprov1alpha1.GatePhaseInconclusive, statEvidence{})},
	}
}

func (g *MetricsGate) queryRange(ctx context.Context, baseURL, query, window, interval string) ([]float64, error) {
	end := time.Now().UTC()
	windowDuration, err := time.ParseDuration(window)
	if err != nil {
		return nil, fmt.Errorf("parse window %q: %w", window, err)
	}
	step, err := time.ParseDuration(interval)
	if err != nil || step < minInterval {
		step = 30 * time.Second
	}
	endpoint := fmt.Sprintf("%s/api/v1/query_range", baseURL)
	params := url.Values{}
	params.Set("query", query)
	params.Set("start", strconv.FormatInt(end.Add(-windowDuration).Unix(), 10))
	params.Set("end", strconv.FormatInt(end.Unix(), 10))
	params.Set("step", strconv.FormatFloat(step.Seconds(), 'f', -1, 64))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"?"+params.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	resp, err := g.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("http: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("prometheus returned %d: %s", resp.StatusCode, body)
	}
	var pr prometheusInstantResponse
	if err := json.Unmarshal(body, &pr); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}
	if pr.Status != "success" {
		return nil, fmt.Errorf("prometheus status: %s", pr.Status)
	}
	if pr.Data.ResultType != "matrix" {
		return nil, fmt.Errorf("unsupported resultType for range query: %s", pr.Data.ResultType)
	}
	var results []matrixResult
	if err := json.Unmarshal(pr.Data.Result, &results); err != nil {
		return nil, fmt.Errorf("unmarshal matrix: %w", err)
	}
	if len(results) == 0 {
		return nil, nil
	}
	if len(results) > 1 {
		return nil, fmt.Errorf("prometheus query returned %d series — use a more specific selector to return exactly one series", len(results))
	}
	values := make([]float64, 0, len(results[0].Values))
	for _, pair := range results[0].Values {
		if len(pair) < 2 {
			return nil, fmt.Errorf("unexpected matrix value array length")
		}
		val, err := parsePromValue(pair[1])
		if err != nil {
			return nil, err
		}
		values = append(values, val)
	}
	return values, nil
}

func parsePromValue(raw json.RawMessage) (float64, error) {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return 0, fmt.Errorf("parse prom value: %w", err)
	}
	val, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, err
	}
	if math.IsNaN(val) || math.IsInf(val, 0) {
		return 0, fmt.Errorf("prometheus returned non-finite value: %v", val)
	}
	return val, nil
}

func metricEvidence(metric kaprov1alpha1.MetricGate, query, baselineQuery string, observed, baseline float64, reason string, _ kaprov1alpha1.GatePhase) Evidence {
	return metricEvidenceWithConfidence(metric, query, baselineQuery, observed, baseline, 1, nil, reason, "")
}

func metricEvidenceWithConfidence(metric kaprov1alpha1.MetricGate, query, baselineQuery string, observed, baseline float64, samples int, confidence *float64, reason string, _ kaprov1alpha1.GatePhase) Evidence {
	return metricEvidenceWithStats(metric, query, baselineQuery, observed, baseline, samples, confidence, reason, "", statEvidence{})
}

type statEvidence struct {
	BaselineHealthy *bool
	Alpha           *float64
	PValue          *float64
	EffectSize      float64
	Score           *float64
	DecisionRule    string
}

func metricEvidenceWithStats(metric kaprov1alpha1.MetricGate, query, baselineQuery string, observed, baseline float64, samples int, confidence *float64, reason string, _ kaprov1alpha1.GatePhase, stats statEvidence) Evidence {
	analysis := metricAnalysis(metric)
	e := Evidence{
		Type:                "metric",
		Provider:            metric.Provider,
		AnalysisMode:        analysis.Mode,
		Comparator:          analysis.comparator,
		Query:               query,
		BaselineQuery:       baselineQuery,
		BaselineHealthQuery: analysis.baselineHealthQuery,
		Window:              metricWindow(metric),
		Interval:            retryAfter(metric),
		ObservedValue:       formatFloat(observed),
		Threshold:           formatFloat(analysis.threshold),
		SampleCount:         int64(samples),
		Confidence:          confidence,
		BaselineHealthy:     stats.BaselineHealthy,
		Alpha:               stats.Alpha,
		PValue:              stats.PValue,
		Score:               stats.Score,
		DecisionRule:        stats.DecisionRule,
		Reason:              reason,
	}
	if baselineQuery != "" {
		e.BaselineValue = formatFloat(baseline)
	}
	if stats.EffectSize != 0 {
		e.EffectSize = formatFloat(stats.EffectSize)
	}
	return e
}

func formatFloat(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}

func (g *MetricsGate) baselineHealthy(ctx context.Context, baseURL, query string) (bool, error) {
	passed, value, err := g.queryInstant(ctx, baseURL, query)
	if err != nil {
		return false, err
	}
	return passed && value > 0, nil
}

func changeIsRegression(effectSize float64, comparator string) bool {
	switch comparator {
	case "lt", "lte":
		return effectSize > 0
	default:
		return effectSize < 0
	}
}

func ptrBool(v bool) *bool {
	return &v
}
