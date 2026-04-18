// Package mlflow implements a Gate that evaluates promotion readiness by
// querying MLflow Model Registry metrics.
//
// The gate fetches the registered model version from the MLflow tracking
// server, retrieves the associated training-run metrics, and compares a
// named metric against a configured threshold.
//
// Configuration — two ways to supply it:
//
//  1. JSON blob in MetricGate.Config:
//     {
//     "endpoint":     "http://mlflow.mlops.svc.cluster.local:5000",
//     "model_name":   "product-recommender",
//     "metric_key":   "accuracy",
//     "threshold":    0.94,
//     "operator":     "gte",
//     "model_stage":  "Staging"
//     }
//
//  2. MetricGate struct fields (convenience):
//     provider:   mlflow
//     endpoint:   http://mlflow...
//     query:      <metric_key>
//     threshold:  0.94
//
// operator "gte" (default): passes when metric_value >= threshold
// operator "lte":            passes when metric_value <= threshold
// operator "eq":             passes when metric_value == threshold
package mlflow

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/log"

	"kapro.io/kapro/pkg/gate"
)

// Config is the JSON-encoded gate configuration stored in MetricGate.Config.
type Config struct {
	// Endpoint is the MLflow tracking server URL.
	// e.g. "http://mlflow.mlops.svc.cluster.local:5000"
	Endpoint string `json:"endpoint"`

	// ModelName is the registered model name in MLflow Model Registry.
	ModelName string `json:"model_name"`

	// ModelVersion is the version to check. If empty, uses the artifact
	// tag/version from the Promotion being evaluated.
	ModelVersion string `json:"model_version,omitempty"`

	// MetricKey is the metric to evaluate (e.g. "accuracy", "f1_score", "auc").
	MetricKey string `json:"metric_key"`

	// Threshold is the minimum (or maximum for lte) acceptable value.
	Threshold float64 `json:"threshold"`

	// Operator controls the comparison: gte (default) | lte | eq.
	Operator string `json:"operator,omitempty"`

	// Token is an optional MLflow API token for authenticated servers.
	// Added as "Authorization: Bearer <token>" when set.
	Token string `json:"token,omitempty"`

	// ModelStage filters by model lifecycle stage: "Staging", "Production",
	// "Archived". When set, the gate also verifies that the model version is
	// in this stage.
	ModelStage string `json:"model_stage,omitempty"`
}

// Validate checks that all required fields are present.
func (c *Config) Validate() error {
	if c.Endpoint == "" {
		return fmt.Errorf("mlflow gate: endpoint is required")
	}
	if c.ModelName == "" {
		return fmt.Errorf("mlflow gate: model_name is required")
	}
	if c.ModelVersion == "" {
		return fmt.Errorf("mlflow gate: model_version is required")
	}
	if c.MetricKey == "" {
		return fmt.Errorf("mlflow gate: metric_key is required")
	}
	switch c.Operator {
	case "", "gte", "lte", "eq":
		// valid
	default:
		return fmt.Errorf("mlflow gate: operator must be gte | lte | eq, got %q", c.Operator)
	}
	return nil
}

func (c *Config) operator() string {
	if c.Operator == "" {
		return "gte"
	}
	return c.Operator
}

// Gate implements gate.Gate by querying MLflow Model Registry metrics.
type Gate struct{}

var _ gate.Gate = &Gate{}

// Evaluate fetches the MLflow model version, retrieves its run metrics, and
// returns pass/fail based on the configured threshold and operator.
func (g *Gate) Evaluate(ctx context.Context, req gate.Request) (gate.Result, error) {
	logger := log.FromContext(ctx)

	if req.Policy == nil {
		return gate.Result{Passed: true, Message: "no policy — skipping MLflow gate"}, nil
	}
	if req.MetricIndex >= len(req.Policy.Spec.Gate.Metrics) {
		return gate.Result{}, fmt.Errorf("mlflow gate: metric index %d out of range", req.MetricIndex)
	}

	metric := req.Policy.Spec.Gate.Metrics[req.MetricIndex]

	var cfg Config
	if len(metric.Config) > 0 {
		if err := json.Unmarshal(metric.Config, &cfg); err != nil {
			return gate.Result{}, fmt.Errorf("mlflow gate: parse config: %w", err)
		}
	} else {
		// Fall back to MetricGate struct fields.
		cfg = Config{
			Endpoint:  metric.Endpoint,
			MetricKey: metric.Query,
			Threshold: metric.Threshold,
		}
		// Derive model name and version from promotion when available.
		if req.Promotion != nil {
			cfg.ModelName = req.Promotion.Spec.ReleaseRef
			cfg.ModelVersion = req.Promotion.Spec.Version
		}
	}

	if err := cfg.Validate(); err != nil {
		return gate.Result{}, err
	}

	client := &http.Client{Timeout: 30 * time.Second}

	// Step 1: fetch model version details to get the run_id.
	modelVersion, err := getModelVersion(ctx, client, cfg)
	if err != nil {
		logger.Error(err, "MLflow gate: failed to fetch model version",
			"endpoint", cfg.Endpoint,
			"model", cfg.ModelName,
			"version", cfg.ModelVersion,
		)
		return gate.Result{
			Passed:     false,
			Message:    fmt.Sprintf("MLflow model version fetch failed: %v", err),
			RetryAfter: "30s",
		}, nil
	}

	// Step 2 (optional): verify model stage.
	if cfg.ModelStage != "" && modelVersion.CurrentStage != cfg.ModelStage {
		msg := fmt.Sprintf("MLflow model %q version %s is in stage %q, expected %q",
			cfg.ModelName, cfg.ModelVersion, modelVersion.CurrentStage, cfg.ModelStage)
		logger.Info("MLflow gate: stage mismatch",
			"model", cfg.ModelName,
			"version", cfg.ModelVersion,
			"current_stage", modelVersion.CurrentStage,
			"expected_stage", cfg.ModelStage,
		)
		return gate.Result{Passed: false, Message: msg, RetryAfter: "60s"}, nil
	}

	// Step 3: fetch the run to get metrics.
	metricValue, err := getRunMetric(ctx, client, cfg, modelVersion.RunID)
	if err != nil {
		// Metric key absent in the run → block with a descriptive message.
		var notFound *metricNotFoundError
		if asErr(err, &notFound) {
			logger.Info("MLflow gate: metric not found in run",
				"run_id", modelVersion.RunID,
				"metric_key", cfg.MetricKey,
			)
			return gate.Result{
				Passed:     false,
				Message:    fmt.Sprintf("metric %q not found in MLflow run %s", cfg.MetricKey, modelVersion.RunID),
				RetryAfter: "60s",
			}, nil
		}
		logger.Error(err, "MLflow gate: failed to fetch run metric",
			"run_id", modelVersion.RunID,
			"metric_key", cfg.MetricKey,
		)
		return gate.Result{
			Passed:     false,
			Message:    fmt.Sprintf("MLflow run metric fetch failed: %v", err),
			RetryAfter: "30s",
		}, nil
	}

	// Step 4: compare.
	passed := compare(metricValue, cfg.Threshold, cfg.operator())
	msg := fmt.Sprintf("MLflow metric %q = %.4f, threshold = %.4f (%s): %s",
		cfg.MetricKey, metricValue, cfg.Threshold, cfg.operator(), passStr(passed))

	logger.Info("MLflow gate evaluated",
		"model", cfg.ModelName,
		"version", cfg.ModelVersion,
		"run_id", modelVersion.RunID,
		"metric_key", cfg.MetricKey,
		"metric_value", metricValue,
		"threshold", cfg.Threshold,
		"operator", cfg.operator(),
		"passed", passed,
	)

	result := gate.Result{Passed: passed, Message: msg}
	if !passed {
		result.RetryAfter = "60s"
	}
	return result, nil
}

// ── MLflow API types ──────────────────────────────────────────────────────────

// modelVersionDetail holds the fields we need from the model-versions/get response.
type modelVersionDetail struct {
	RunID        string `json:"run_id"`
	CurrentStage string `json:"current_stage"`
}

// modelVersionResponse is the top-level envelope for GET /api/2.0/mlflow/model-versions/get.
type modelVersionResponse struct {
	ModelVersion modelVersionDetail `json:"model_version"`
}

// runMetric is a single entry in run.data.metrics[].
type runMetric struct {
	Key   string  `json:"key"`
	Value float64 `json:"value"`
	Step  int64   `json:"step"`
}

// runResponse is the subset of GET /api/2.0/mlflow/runs/get we parse.
type runResponse struct {
	Run struct {
		Data struct {
			Metrics []runMetric `json:"metrics"`
		} `json:"data"`
	} `json:"run"`
}

// ── API calls ─────────────────────────────────────────────────────────────────

// getModelVersion calls GET /api/2.0/mlflow/model-versions/get and returns
// the run_id and current_stage for the given model name + version.
func getModelVersion(ctx context.Context, client *http.Client, cfg Config) (modelVersionDetail, error) {
	endpoint := fmt.Sprintf("%s/api/2.0/mlflow/model-versions/get", cfg.Endpoint)

	params := url.Values{}
	params.Set("name", cfg.ModelName)
	params.Set("version", cfg.ModelVersion)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"?"+params.Encode(), nil)
	if err != nil {
		return modelVersionDetail{}, fmt.Errorf("mlflow: build model-versions/get request: %w", err)
	}
	if cfg.Token != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.Token)
	}

	resp, err := client.Do(req)
	if err != nil {
		return modelVersionDetail{}, fmt.Errorf("mlflow: model-versions/get: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return modelVersionDetail{}, fmt.Errorf("mlflow: read model-versions/get body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return modelVersionDetail{}, fmt.Errorf("mlflow: model-versions/get returned %d: %s", resp.StatusCode, body)
	}

	var mvr modelVersionResponse
	if err := json.Unmarshal(body, &mvr); err != nil {
		return modelVersionDetail{}, fmt.Errorf("mlflow: unmarshal model-versions/get: %w", err)
	}
	if mvr.ModelVersion.RunID == "" {
		return modelVersionDetail{}, fmt.Errorf("mlflow: model %q version %s has no run_id", cfg.ModelName, cfg.ModelVersion)
	}
	return mvr.ModelVersion, nil
}

// getRunMetric calls GET /api/2.0/mlflow/runs/get and returns the value for
// cfg.MetricKey.  Returns an error wrapping gate-specific context when the key
// is not present.
func getRunMetric(ctx context.Context, client *http.Client, cfg Config, runID string) (float64, error) {
	endpoint := fmt.Sprintf("%s/api/2.0/mlflow/runs/get", cfg.Endpoint)

	params := url.Values{}
	params.Set("run_id", runID)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"?"+params.Encode(), nil)
	if err != nil {
		return 0, fmt.Errorf("mlflow: build runs/get request: %w", err)
	}
	if cfg.Token != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.Token)
	}

	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("mlflow: runs/get: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, fmt.Errorf("mlflow: read runs/get body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("mlflow: runs/get returned %d: %s", resp.StatusCode, body)
	}

	var rr runResponse
	if err := json.Unmarshal(body, &rr); err != nil {
		return 0, fmt.Errorf("mlflow: unmarshal runs/get: %w", err)
	}

	for _, m := range rr.Run.Data.Metrics {
		if m.Key == cfg.MetricKey {
			return m.Value, nil
		}
	}
	// Metric not found — not an error from the caller's perspective; the gate
	// should block and retry rather than fail the promotion.
	return 0, &metricNotFoundError{key: cfg.MetricKey, runID: runID}
}

// ── error types ───────────────────────────────────────────────────────────────

// metricNotFoundError is returned when the requested metric key is absent from
// the MLflow run.  The gate converts it into a non-fatal Result.
type metricNotFoundError struct {
	key   string
	runID string
}

func (e *metricNotFoundError) Error() string {
	return fmt.Sprintf("metric %q not found in MLflow run %s", e.key, e.runID)
}

// asErr is a minimal errors.As helper for *metricNotFoundError to avoid an
// "errors" import just for one type check.
func asErr(err error, target **metricNotFoundError) bool {
	if err == nil {
		return false
	}
	v, ok := err.(*metricNotFoundError)
	if ok {
		*target = v
	}
	return ok
}

// ── comparison helpers ────────────────────────────────────────────────────────

func compare(value, threshold float64, operator string) bool {
	switch operator {
	case "gte":
		return value >= threshold
	case "lte":
		return value <= threshold
	case "eq":
		return value == threshold
	default:
		return value >= threshold
	}
}

func passStr(passed bool) string {
	if passed {
		return "PASS"
	}
	return "FAIL"
}
