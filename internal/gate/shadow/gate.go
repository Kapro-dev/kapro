// Package shadow implements a Gate that validates deployment safety by comparing
// shadow (mirrored) traffic responses against production responses.
//
// Shadow mode works by sending identical copies of production requests to the new
// version without serving its responses to end-users. The gate queries a shadow
// comparison service to assess:
//   - Total mirrored request count (sample size reached?)
//   - Divergence rate: % of responses that differ from production
//   - Shadow error rate: % of 5xx responses from the shadow
//
// The gate passes when:
//  1. TotalRequests >= config.MinSampleSize
//  2. DivergenceRate <= config.DivergenceThreshold
//  3. ErrorRate <= config.ErrorRateThreshold
//
// Compatible shadow comparison services:
//   - Diffy (https://github.com/opendiffy/diffy): responds on GET /summary.json
//   - Any HTTP service returning [ShadowStats] JSON at GET /v1/stats
//
// Example MetricGate configuration (JSON in MetricGate.Config):
//
//	{
//	  "endpoint":             "http://diffy.shadow.svc.cluster.local:31900",
//	  "min_sample_size":      1000,
//	  "divergence_threshold": 0.02,
//	  "error_rate_threshold": 0.01,
//	  "window":               "15m"
//	}
package shadow

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/log"

	"kapro.io/kapro/pkg/gate"
)

const (
	defaultMinSampleSize       = 500
	defaultDivergenceThreshold = 0.02
	defaultErrorRateThreshold  = 0.01
)

// Config is the JSON-encoded gate configuration stored in MetricGate.Config.
type Config struct {
	// Endpoint is the base URL of the shadow comparison service.
	// The gate probes GET <endpoint>/v1/stats and falls back to /summary.json.
	Endpoint string `json:"endpoint"`

	// MinSampleSize is the minimum mirrored request count before divergence
	// is evaluated. Prevents false positives on tiny traffic bursts.
	// Default: 500.
	MinSampleSize int `json:"min_sample_size,omitempty"`

	// DivergenceThreshold is the maximum acceptable response divergence as a
	// fraction (0.0–1.0). A response "diverges" when the shadow returns a
	// different status code, body structure, or field value from production.
	// Default: 0.02 (2%).
	DivergenceThreshold float64 `json:"divergence_threshold,omitempty"`

	// ErrorRateThreshold is the maximum acceptable 5xx error rate on the
	// shadow, expressed as a fraction. Default: 0.01 (1%).
	ErrorRateThreshold float64 `json:"error_rate_threshold,omitempty"`

	// Window restricts stats to the given time window (e.g. "15m", "1h").
	// Forwarded as ?window=<value> to the shadow service. When empty all
	// available stats are used.
	Window string `json:"window,omitempty"`

	// Token is an optional Bearer token for authenticated shadow services.
	Token string `json:"token,omitempty"`
}

func (c *Config) applyDefaults() {
	if c.MinSampleSize == 0 {
		c.MinSampleSize = defaultMinSampleSize
	}
	if c.DivergenceThreshold == 0 {
		c.DivergenceThreshold = defaultDivergenceThreshold
	}
	if c.ErrorRateThreshold == 0 {
		c.ErrorRateThreshold = defaultErrorRateThreshold
	}
}

func (c *Config) validate() error {
	if c.Endpoint == "" {
		return fmt.Errorf("shadow gate: endpoint is required")
	}
	return nil
}

// ShadowStats is the JSON shape returned by the shadow comparison service.
// Compatible with Diffy /summary.json (subset) and the /v1/stats contract.
type ShadowStats struct {
	// TotalRequests is the total mirrored requests processed.
	TotalRequests int64 `json:"total_requests"`
	// DivergentRequests is the count of responses that differed from production.
	DivergentRequests int64 `json:"divergent_requests"`
	// ErrorRequests is the count of 5xx responses from the shadow.
	ErrorRequests int64 `json:"error_requests"`

	// DivergenceRate is optionally pre-computed by the service (divergent/total).
	// When zero and TotalRequests > 0 the gate computes it from raw counts.
	DivergenceRate float64 `json:"divergence_rate,omitempty"`
	// ErrorRate is optionally pre-computed (errors/total).
	ErrorRate float64 `json:"error_rate,omitempty"`
}

// divergenceRate returns the effective divergence rate, computing from raw
// counts when the service does not supply a pre-computed value.
func (s *ShadowStats) divergenceRate() float64 {
	if s.DivergenceRate != 0 {
		return s.DivergenceRate
	}
	if s.TotalRequests == 0 {
		return 0
	}
	return float64(s.DivergentRequests) / float64(s.TotalRequests)
}

// errorRate returns the effective error rate.
func (s *ShadowStats) errorRate() float64 {
	if s.ErrorRate != 0 {
		return s.ErrorRate
	}
	if s.TotalRequests == 0 {
		return 0
	}
	return float64(s.ErrorRequests) / float64(s.TotalRequests)
}

// Gate implements KGI for shadow traffic comparison.
//
// It is safe for concurrent use. HTTPClient defaults to a 10-second timeout
// client when nil.
type Gate struct {
	HTTPClient *http.Client
}

func (g *Gate) httpClient() *http.Client {
	if g.HTTPClient != nil {
		return g.HTTPClient
	}
	return &http.Client{Timeout: 10 * time.Second}
}

// Evaluate queries the shadow comparison service and returns Passed=true
// when sample size, divergence, and error rate are within thresholds.
func (g *Gate) Evaluate(ctx context.Context, req gate.Request) (gate.Result, error) {
	if req.Policy == nil || req.MetricIndex >= len(req.Policy.Spec.Gate.Metrics) {
		return gate.Result{Passed: true, Message: "no shadow gate configured"}, nil
	}

	metric := req.Policy.Spec.Gate.Metrics[req.MetricIndex]

	var cfg Config
	if len(metric.Config) > 0 {
		if err := json.Unmarshal(metric.Config, &cfg); err != nil {
			return gate.Result{}, fmt.Errorf("shadow gate: invalid config JSON: %w", err)
		}
	} else {
		cfg.Endpoint = metric.Endpoint
	}

	cfg.applyDefaults()
	if err := cfg.validate(); err != nil {
		return gate.Result{}, err
	}

	stats, err := g.fetchStats(ctx, cfg)
	if err != nil {
		return gate.Result{}, fmt.Errorf("shadow gate: %w", err)
	}

	log.FromContext(ctx).Info("shadow gate stats",
		"total", stats.TotalRequests,
		"divergent", stats.DivergentRequests,
		"errors", stats.ErrorRequests,
	)

	if stats.TotalRequests < int64(cfg.MinSampleSize) {
		return gate.Result{
			Passed:     false,
			Message:    fmt.Sprintf("shadow: collecting sample (%d/%d requests mirrored)", stats.TotalRequests, cfg.MinSampleSize),
			RetryAfter: "30s",
		}, nil
	}

	errRate := stats.errorRate()
	if errRate > cfg.ErrorRateThreshold {
		return gate.Result{
			Passed:  false,
			Message: fmt.Sprintf("shadow: error rate %.2f%% exceeds threshold %.2f%%", errRate*100, cfg.ErrorRateThreshold*100),
		}, nil
	}

	divRate := stats.divergenceRate()
	if divRate > cfg.DivergenceThreshold {
		return gate.Result{
			Passed:  false,
			Message: fmt.Sprintf("shadow: divergence %.2f%% exceeds threshold %.2f%% (sample %d)", divRate*100, cfg.DivergenceThreshold*100, stats.TotalRequests),
		}, nil
	}

	return gate.Result{
		Passed:  true,
		Message: fmt.Sprintf("shadow: divergence %.2f%% error %.2f%% (sample %d)", divRate*100, errRate*100, stats.TotalRequests),
	}, nil
}

// fetchStats queries the shadow comparison service.
// Tries /v1/stats first (canonical Kapro contract), falls back to /summary.json
// for Diffy compatibility.
func (g *Gate) fetchStats(ctx context.Context, cfg Config) (*ShadowStats, error) {
	probes := []string{
		cfg.Endpoint + "/v1/stats",
		cfg.Endpoint + "/summary.json",
	}

	var lastErr error
	for _, u := range probes {
		if cfg.Window != "" {
			u += "?window=" + cfg.Window
		}
		stats, err := g.doGet(ctx, u, cfg.Token)
		if err == nil {
			return stats, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

func (g *Gate) doGet(ctx context.Context, u, token string) (*ShadowStats, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := g.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d from %s", resp.StatusCode, u)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	var stats ShadowStats
	if err := json.Unmarshal(body, &stats); err != nil {
		return nil, fmt.Errorf("decode stats response: %w", err)
	}
	return &stats, nil
}
